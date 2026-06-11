// ABOUTME: The conversational coach flow: session continuity, tools, mutations with confirmation.
// ABOUTME: Free text becomes coaching; every reply still carries the code-owned verdict footer.

package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/verdict"
)

// maxSessionTurns rotates the conversation before the context (and the
// Firestore doc count) grows unbounded.
const maxSessionTurns = 40

// ActivitiesSource provides trimmed recent activities for the tools.
type ActivitiesSource interface {
	ActivitiesRange(ctx context.Context, oldest, newest string) ([]icu.Activity, error)
}

// RulesSource lists the confirmed coaching rules for the profile prefix.
type RulesSource interface {
	ActiveTexts(ctx context.Context) ([]string, error)
}

// MutationProposer appends a proposed profile change (idempotent by id).
type MutationProposer interface {
	Propose(ctx context.Context, id string, mut store.Mutation) error
}

// ConversationStore extends SessionStore with history loading.
type ConversationStore interface {
	SessionStore
	LoadTurns(ctx context.Context, sessionID string) ([]store.Turn, error)
}

// ChatState tracks the active conversation pointer.
type ChatState interface {
	ActiveSession(ctx context.Context) (string, error)
	SetActiveSession(ctx context.Context, sessionID string) error
}

// Confirmer sends the one-tap mutation confirmation.
type Confirmer interface {
	SendConfirm(ctx context.Context, text, yesData, noData string) error
}

type Coach struct {
	Agent      agent.Coach
	Wellness   WellnessSource
	Activities ActivitiesSource
	Profiles   ProfileSource
	Rules      RulesSource
	Muts       MutationProposer
	Sessions   ConversationStore
	Chats      ChatState
	Status     StatusComposer
	Out        Interactor
	Confirm    Confirmer
	Now        func() time.Time
	TZ         *time.Location
}

// Converse handles one free-text athlete message end to end.
func (c *Coach) Converse(ctx context.Context, text string) error {
	body, v, err := c.Status.Compose(ctx)
	if err != nil {
		// Honest degraded reply beats retry spam: the athlete asked NOW.
		slog.Warn("coach: today context unavailable", "err", err)
		return c.Out.Send(ctx,
			"⚠️ Non riesco a leggere i dati di oggi da intervals.icu in questo momento; riprova tra poco.")
	}

	sessionID, history := c.loadHistory(ctx)
	prefix, err := c.profilePrefix(ctx)
	if err != nil {
		return fmt.Errorf("coach: profile prefix: %w", err)
	}

	userText := fmt.Sprintf(
		"Contesto deterministico di oggi (gia' calcolato, non contraddirlo):\n%s\n%s\n\nMessaggio dell'atleta:\n%s",
		body, verdict.RenderBlock(v), text)

	res, err := c.Agent.Reply(ctx, agent.CoachInput{
		Profile: prefix, History: history, UserText: userText,
	}, c.tools(sessionID))
	if err != nil {
		slog.Warn("coach: reply failed, degraded", "err", err)
		return c.Out.Send(ctx,
			telegram.DegradedLLMDown()+"\n\nIl quadro deterministico di oggi:\n\n"+body+"\n\n"+verdict.RenderBlock(v))
	}

	reply := telegram.SanitizeNarrative(res.Text)
	if err := c.Out.SendWithVerdict(ctx, reply, v); err != nil {
		return fmt.Errorf("coach: send: %w", err)
	}
	c.persist(ctx, sessionID, text, reply)
	return nil
}

// loadHistory returns the active session id (possibly "") and its replayable
// history. ANY load problem degrades to a fresh session (decision 11).
func (c *Coach) loadHistory(ctx context.Context) (string, []anthropic.MessageParam) {
	sessionID, err := c.Chats.ActiveSession(ctx)
	if err != nil || sessionID == "" {
		return "", nil
	}
	turns, err := c.Sessions.LoadTurns(ctx, sessionID)
	if err != nil {
		slog.Warn("coach: session load failed, fresh session", "session", sessionID, "err", err)
		return "", nil
	}
	if len(turns) >= maxSessionTurns {
		slog.Info("coach: session rotated", "session", sessionID, "turns", len(turns))
		return "", nil
	}
	history := make([]anthropic.MessageParam, 0, len(turns))
	for _, t := range turns {
		block := anthropic.NewTextBlock(t.Content)
		if t.Role == "assistant" {
			history = append(history, anthropic.NewAssistantMessage(block))
		} else {
			history = append(history, anthropic.NewUserMessage(block))
		}
	}
	return sessionID, history
}

// profilePrefix renders the STABLE athlete block: same data, same bytes,
// or the prompt cache never hits.
func (c *Coach) profilePrefix(ctx context.Context) (string, error) {
	baselines, rampCap, err := c.Profiles.Profile(ctx)
	if err != nil {
		return "", err
	}
	rules, err := c.Rules.ActiveTexts(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("PROFILO ATLETA (calcolato dal sistema):\n")
	fmt.Fprintf(&b, "- Baseline HRV: %.1f (SD %.1f)\n", baselines.HRVMean, baselines.HRVSD)
	fmt.Fprintf(&b, "- Baseline FC riposo: %.1f bpm\n", baselines.RestingHR)
	fmt.Fprintf(&b, "- Tetto rampa CTL: %.1f/settimana\n", rampCap)
	if len(rules) > 0 {
		b.WriteString("Regole personali confermate:\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	return b.String(), nil
}

func (c *Coach) persist(ctx context.Context, sessionID, userText, reply string) {
	if sessionID == "" {
		id, err := c.Sessions.Create(ctx, "chat", c.Now())
		if err != nil {
			slog.Warn("coach: session create failed", "err", err)
			return
		}
		if err := c.Chats.SetActiveSession(ctx, id); err != nil {
			slog.Warn("coach: set active session failed", "err", err)
		}
		sessionID = id
	}
	turns, err := c.Sessions.LoadTurns(ctx, sessionID)
	seq := 0
	if err == nil {
		seq = len(turns)
	}
	if err := c.Sessions.AppendTurn(ctx, sessionID, seq+1, "user", userText, ""); err != nil {
		slog.Warn("coach: persist user turn failed", "err", err)
		return
	}
	if err := c.Sessions.AppendTurn(ctx, sessionID, seq+2, "assistant", reply, c.Agent.Model); err != nil {
		slog.Warn("coach: persist assistant turn failed", "err", err)
	}
}

const dateOnly = "2006-01-02"

// tools builds the read registry plus the mutation proposer, bound to the
// current session for deterministic proposal ids.
func (c *Coach) tools(sessionID string) agent.Tools {
	if sessionID == "" {
		sessionID = "s-pending"
	}
	return agent.Tools{
		"get_recent_activities": {
			Description: "Ultime attività dell'atleta (già filtrate). days: 1-14.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":1,"maximum":14}},"required":["days"]}`),
			Handler: func(ctx context.Context, _ string, input json.RawMessage) (string, error) {
				var in struct {
					Days int `json:"days"`
				}
				if err := json.Unmarshal(input, &in); err != nil || in.Days < 1 || in.Days > 14 {
					return "", fmt.Errorf("days deve essere 1-14")
				}
				now := c.Now().In(c.TZ)
				acts, err := c.Activities.ActivitiesRange(ctx,
					now.AddDate(0, 0, -in.Days).Format(dateOnly), now.Format(dateOnly))
				if err != nil {
					return "", err
				}
				out, _ := json.Marshal(acts)
				return string(out), nil
			},
		},
		"get_wellness": {
			Description: "Serie wellness recente (HRV, FC riposo, sonno, CTL/ATL/rampa). days: 1-30.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":1,"maximum":30}},"required":["days"]}`),
			Handler: func(ctx context.Context, _ string, input json.RawMessage) (string, error) {
				var in struct {
					Days int `json:"days"`
				}
				if err := json.Unmarshal(input, &in); err != nil || in.Days < 1 || in.Days > 30 {
					return "", fmt.Errorf("days deve essere 1-30")
				}
				now := c.Now().In(c.TZ)
				days, err := c.Wellness.WellnessRange(ctx,
					now.AddDate(0, 0, -in.Days).Format(dateOnly), now.Format(dateOnly))
				if err != nil {
					return "", err
				}
				out, _ := json.Marshal(days)
				return string(out), nil
			},
		},
		"propose_profile_update": {
			Description: "Proponi una modifica al profilo (regola personale o tetto rampa). " +
				"NON attiva finché l'atleta non conferma col bottone.",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"kind":{"type":"string","enum":["ramp_cap","rule"]},
				"new_value":{"type":"string"},
				"rationale":{"type":"string"},
				"source_quote":{"type":"string"}},
				"required":["kind","new_value","rationale","source_quote"]}`),
			Handler: func(ctx context.Context, toolUseID string, input json.RawMessage) (string, error) {
				return c.propose(ctx, sessionID, toolUseID, input)
			},
		},
	}
}

func (c *Coach) propose(ctx context.Context, sessionID, toolUseID string, input json.RawMessage) (string, error) {
	var in struct {
		Kind        string `json:"kind"`
		NewValue    string `json:"new_value"`
		Rationale   string `json:"rationale"`
		SourceQuote string `json:"source_quote"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("input non valido: %w", err)
	}
	var label, old string
	switch in.Kind {
	case store.MutationRampCap:
		var capVal float64
		if _, err := fmt.Sscanf(in.NewValue, "%f", &capVal); err != nil || capVal <= 0 || capVal > 6 {
			return "", fmt.Errorf("ramp_cap deve essere un numero in (0, 6], ricevuto %q", in.NewValue)
		}
		_, current, err := c.Profiles.Profile(ctx)
		if err == nil {
			old = fmt.Sprintf("%.1f", current)
		}
		label = fmt.Sprintf("tetto rampa CTL: %s → %s/settimana", old, in.NewValue)
	case store.MutationRule:
		if l := len(strings.TrimSpace(in.NewValue)); l < 5 || l > 200 {
			return "", fmt.Errorf("la regola deve essere 5-200 caratteri")
		}
		label = fmt.Sprintf("nuova regola: %q", in.NewValue)
	default:
		return "", fmt.Errorf("kind %q non supportato (ramp_cap|rule)", in.Kind)
	}

	id := store.MutationID(sessionID, toolUseID)
	if err := c.Muts.Propose(ctx, id, store.Mutation{
		Kind: in.Kind, NewValue: in.NewValue, OldValue: old,
		Rationale: in.Rationale, SourceQuote: in.SourceQuote,
		SessionID: sessionID, ToolUseID: toolUseID,
	}); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Salvo nel profilo?\n<b>%s</b>\n<i>«%s»</i>",
		telegram.Escape(label), telegram.Escape(in.SourceQuote))
	if err := c.Confirm.SendConfirm(ctx, msg, "pm:"+id+":y", "pm:"+id+":n"); err != nil {
		return "", err
	}
	return "Proposta registrata e inviata all'atleta per conferma. NON è attiva: " +
		"dillo chiaramente e non darla per acquisita.", nil
}
