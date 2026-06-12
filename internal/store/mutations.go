// ABOUTME: Append-only profile mutations with one-tap confirmation (decision 14).
// ABOUTME: The model proposes, the athlete confirms, a transaction applies: never silently.

package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrMutationInvalid marks terminal mutation failures: retrying a tap cannot
// fix them, so callers map this to a user-facing notice, never a retry.
var ErrMutationInvalid = fmt.Errorf("mutation invalid")

const (
	mutationsCollection = "profile_events"
	rulesCollection     = "rules"

	MutationRampCap = "ramp_cap"
	MutationRule    = "rule"

	// tierARampCapStore mirrors verdict's Tier A ceiling: the data layer can
	// only tighten safety bounds (decision 13); enforced again at apply time.
	tierARampCapStore = 6.0

	// maxActiveRules bounds the prefix injection surface and its cache cost:
	// the prefix must not grow without limit (security review M5).
	maxActiveRules = 15

	// proposalTTL: a stale confirm button months later confirms a context
	// that no longer exists. Older proposals resolve as expired.
	proposalTTL = 48 * time.Hour

	// mutationRetention mirrors turn retention: rationale and source_quote
	// are verbatim athlete words, health-adjacent by construction.
	mutationRetention = 18 * 30 * 24 * time.Hour
)

// SanitizeRuleText collapses control characters and newlines: a confirmed
// rule becomes persistent prompt material, and multi-line text can mimic
// system-prompt sections (security review M5).
func SanitizeRuleText(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(cleaned), " ")
}

type Mutations struct {
	client *firestore.Client
}

func NewMutations(client *firestore.Client) *Mutations {
	return &Mutations{client: client}
}

// Mutation is one proposed profile change, with full provenance.
type Mutation struct {
	Kind        string    `firestore:"kind"` // ramp_cap | rule
	NewValue    string    `firestore:"new_value"`
	OldValue    string    `firestore:"old_value,omitempty"`
	Rationale   string    `firestore:"rationale"`
	SourceQuote string    `firestore:"source_quote"`
	SessionID   string    `firestore:"session_id"`
	ToolUseID   string    `firestore:"tool_use_id"`
	Status      string    `firestore:"status"` // proposed | confirmed | rejected | expired
	CreatedAt   time.Time `firestore:"created_at"`
	ResolvedAt  time.Time `firestore:"resolved_at,omitempty"`
	// ExpiresAt drives the Firestore TTL policy (18 months): athlete quotes
	// do not live forever by accident.
	ExpiresAt time.Time `firestore:"expires_at"`
}

// MutationID is deterministic on (session, tool_use): an agent-loop retry
// can never create a duplicate proposal.
func MutationID(sessionID, toolUseID string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + toolUseID))
	return fmt.Sprintf("mut-%x", sum[:8])
}

// Propose appends the event; AlreadyExists means a retry already proposed
// it, which is success.
func (m *Mutations) Propose(ctx context.Context, id string, mut Mutation) error {
	mut.Status = "proposed"
	mut.CreatedAt = time.Now().UTC()
	mut.ExpiresAt = mut.CreatedAt.Add(mutationRetention)
	_, err := m.client.Collection(mutationsCollection).Doc(id).Create(ctx, mut)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mutation propose: %w", err)
	}
	return nil
}

// Discard removes a still-proposed mutation: the compensation when the
// confirm prompt could not reach the athlete (no orphan proposals).
func (m *Mutations) Discard(ctx context.Context, id string) error {
	ref := m.client.Collection(mutationsCollection).Doc(id)
	return m.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var mut Mutation
		if err := snap.DataTo(&mut); err != nil || mut.Status != "proposed" {
			return nil // resolved meanwhile: leave the audit trail intact
		}
		return tx.Delete(ref)
	})
}

// Resolve flips a proposed mutation and, on approval, applies it to the
// materialized profile in the SAME transaction. Double taps and replays
// no-op (status guard); stale proposals (>48h) expire instead of applying.
// Terminal problems (unknown id, invalid stored value) return
// ErrMutationInvalid: a retry cannot fix them.
func (m *Mutations) Resolve(ctx context.Context, id string, approve bool) (*Mutation, error) {
	ref := m.client.Collection(mutationsCollection).Doc(id)
	var out Mutation
	err := m.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return fmt.Errorf("%w: id %s sconosciuto", ErrMutationInvalid, id)
		}
		if err != nil {
			return err
		}
		if err := snap.DataTo(&out); err != nil {
			return fmt.Errorf("%w: decode: %v", ErrMutationInvalid, err)
		}
		if out.Status != "proposed" {
			return nil // already resolved: idempotent
		}
		newStatus := "rejected"
		switch {
		case time.Since(out.CreatedAt) > proposalTTL:
			newStatus = "expired"
		case approve:
			newStatus = "confirmed"
			if err := m.apply(tx, id, &out); err != nil {
				return err
			}
		}
		out.Status = newStatus
		out.ResolvedAt = time.Now().UTC()
		return tx.Set(ref, map[string]any{
			"status": newStatus, "resolved_at": out.ResolvedAt,
		}, firestore.MergeAll)
	})
	if err != nil {
		return nil, fmt.Errorf("mutation resolve: %w", err)
	}
	return &out, nil
}

func (m *Mutations) apply(tx *firestore.Transaction, id string, mut *Mutation) error {
	switch mut.Kind {
	case MutationRampCap:
		cap, err := strconv.ParseFloat(strings.TrimSpace(mut.NewValue), 64)
		if err != nil {
			return fmt.Errorf("%w: ramp_cap %q non numerico", ErrMutationInvalid, mut.NewValue)
		}
		// Tier A gate at apply time: even a confirmed hostile value cannot
		// loosen past the code ceiling.
		if cap <= 0 || cap > tierARampCapStore {
			return fmt.Errorf("%w: ramp_cap %v fuori da (0, %v]", ErrMutationInvalid, cap, tierARampCapStore)
		}
		return tx.Set(m.client.Collection(profileCollection).Doc(profileDocID),
			map[string]any{"ramp_cap": cap}, firestore.MergeAll)
	case MutationRule:
		// Re-validate at apply time (defense in depth, mirrors ramp_cap):
		// whatever wrote the proposed doc, the prefix only gets clean text.
		text := SanitizeRuleText(mut.NewValue)
		if l := len(text); l < 5 || l > 200 {
			return fmt.Errorf("%w: regola di %d caratteri fuori da [5,200]", ErrMutationInvalid, l)
		}
		// Doc id from CONTENT, not mutation id: a duplicate proposal chain
		// (redelivery with fresh tool_use ids) converges on one rule doc.
		sum := sha256.Sum256([]byte(text))
		return tx.Set(m.client.Collection(rulesCollection).Doc(fmt.Sprintf("rule-%x", sum[:8])),
			map[string]any{
				"text": text, "active": true,
				"source_mutation": id, "created_at": time.Now().UTC(),
			})
	default:
		return fmt.Errorf("%w: kind %q sconosciuto", ErrMutationInvalid, mut.Kind)
	}
}

// CountActive returns the number of active rules (prefix cap enforcement).
func (r *Rules) CountActive(ctx context.Context) (int, error) {
	rules, err := r.ListActive(ctx)
	if err != nil {
		return 0, err
	}
	return len(rules), nil
}

// MaxActiveRules is the propose-time cap exported for the job layer.
const MaxActiveRules = maxActiveRules

// Rule is one confirmed coaching rule, injected into the Opus prefix.
type Rule struct {
	ID   string `firestore:"-"`
	Text string `firestore:"text"`
}

type Rules struct {
	client *firestore.Client
}

func NewRules(client *firestore.Client) *Rules {
	return &Rules{client: client}
}

func (r *Rules) ListActive(ctx context.Context) ([]Rule, error) {
	// Explicit OrderBy: prefix byte-stability is a cache invariant, not an
	// accident of Firestore's implicit __name__ ordering.
	docs, err := r.client.Collection(rulesCollection).
		Where("active", "==", true).
		OrderBy(firestore.DocumentID, firestore.Asc).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("rules list: %w", err)
	}
	out := make([]Rule, 0, len(docs))
	for _, d := range docs {
		var rule Rule
		if err := d.DataTo(&rule); err != nil {
			return nil, fmt.Errorf("rule decode %s: %w", d.Ref.ID, err)
		}
		rule.ID = d.Ref.ID
		out = append(out, rule)
	}
	return out, nil
}

// ActiveTexts returns just the rule texts, sorted by doc id for prefix
// stability (same rules, same bytes, or the prompt cache never hits).
func (r *Rules) ActiveTexts(ctx context.Context) ([]string, error) {
	rules, err := r.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.Text)
	}
	return out, nil
}

// MutationWithID pairs a mutation with its doc id for the dashboard.
type MutationWithID struct {
	ID string
	Mutation
}

// RecentMutations lists the latest profile events for the audit page.
func (m *Mutations) RecentMutations(ctx context.Context, limit int) ([]MutationWithID, error) {
	docs, err := m.client.Collection(mutationsCollection).
		OrderBy("created_at", firestore.Desc).Limit(limit).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("mutations recent: %w", err)
	}
	out := make([]MutationWithID, 0, len(docs))
	for _, d := range docs {
		var mut Mutation
		if err := d.DataTo(&mut); err != nil {
			return nil, fmt.Errorf("mutation decode %s: %w", d.Ref.ID, err)
		}
		out = append(out, MutationWithID{ID: d.Ref.ID, Mutation: mut})
	}
	return out, nil
}

// Deactivate turns a confirmed rule off (dashboard action: the athlete's
// explicit choice, the symmetric counterpart of the one-tap confirm).
func (r *Rules) Deactivate(ctx context.Context, id string) error {
	_, err := r.client.Collection(rulesCollection).Doc(id).Set(ctx, map[string]any{
		"active": false, "deactivated_at": time.Now().UTC(),
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("rule deactivate: %w", err)
	}
	return nil
}

// RecordWebChange writes an already-applied profile event originating from
// the dashboard: the athlete acted directly, so there is nothing to confirm,
// but the no-change-without-event invariant (decision 14) still holds.
func (m *Mutations) RecordWebChange(ctx context.Context, kind, oldValue, newValue string) error {
	id := fmt.Sprintf("web-%s-%d", kind, time.Now().UnixNano())
	_, err := m.client.Collection(mutationsCollection).Doc(id).Create(ctx, Mutation{
		Kind: kind, NewValue: newValue, Rationale: "modifica diretta da dashboard (vecchio valore: " + oldValue + ")",
		SourceQuote: "azione web", Status: "applied",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(mutationRetention),
	})
	if err != nil {
		return fmt.Errorf("web change record: %w", err)
	}
	return nil
}
