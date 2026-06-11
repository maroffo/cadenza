// ABOUTME: Tests for the hand-rolled tool loop: caps, tool_result discipline, error surfacing.
// ABOUTME: Driven entirely by the scripted fakeanthropic; no network, no real model.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/maroffo/cadenza/internal/fakes"
)

func newTestClient(url string) Client {
	return NewClient("test-key", url)
}

func TestRun_PlainTextCompletes(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "Buongiorno, giornata verde."})
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model:     "claude-haiku-4-5-20251001",
		System:    "sei un coach",
		UserText:  "dati di oggi: ...",
		MaxTokens: 512,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "Buongiorno, giornata verde." {
		t.Errorf("out = %q", res.Text)
	}
	if len(res.Transcript) != 2 {
		t.Errorf("transcript turns = %d, want 2 (user + assistant, the M5 seam)", len(res.Transcript))
	}
	if len(fake.Requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.Requests))
	}
	if fake.Requests[0].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q", fake.Requests[0].Model)
	}
}
func TestRun_ToolCallAnsweredThenCompletes(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.Call("tu_1", "get_recent_activities", `{"days":3}`),
		fakes.Text{S: "ieri hai corso facile, bene."},
	)
	defer fake.Close()

	called := 0
	tools := Tools{
		"get_recent_activities": Tool{
			Description: "ultime attività",
			Schema:      json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer"}}}`),
			Handler: func(ctx context.Context, _ string, input json.RawMessage) (string, error) {
				called++
				return `[{"date":"2026-06-10","type":"Run","load":35}]`, nil
			},
		},
	}
	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 256,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Fatalf("tool called %d times, want 1", called)
	}
	if res.Text != "ieri hai corso facile, bene." {
		t.Errorf("out = %q", res.Text)
	}
	// Second request must carry the tool_result for tu_1: a missing
	// tool_result for any tool_use id makes the real API reject the call.
	if len(fake.Requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(fake.Requests))
	}
	second, _ := json.Marshal(fake.Requests[1].Messages)
	if !strings.Contains(string(second), "tool_result") || !strings.Contains(string(second), "tu_1") {
		t.Errorf("second request missing tool_result for tu_1:\n%s", second)
	}
}

func TestRun_ToolFailureBecomesIsError(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.Call("tu_2", "boom", `{}`),
		fakes.Text{S: "ok, faccio senza"},
	)
	defer fake.Close()

	tools := Tools{
		"boom": Tool{
			Description: "fails",
			Schema:      json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, string, json.RawMessage) (string, error) {
				return "", errors.New("icu 502")
			},
		},
	}
	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 256,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v (tool failure must flow back as is_error, not abort)", err)
	}
	if res.Text == "" {
		t.Error("empty narrative after recoverable tool failure")
	}
	second, _ := json.Marshal(fake.Requests[1].Messages)
	if !strings.Contains(string(second), `"is_error":true`) {
		t.Errorf("tool failure not marked is_error:\n%s", second)
	}
}

func TestRun_UnknownToolAnsweredWithIsError(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.Call("tu_3", "not_registered", `{}`),
		fakes.Text{S: "ok"},
	)
	defer fake.Close()

	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, Tools{})
	if err != nil {
		t.Fatalf("Run: %v (unknown tool must not wedge the loop)", err)
	}
	second, _ := json.Marshal(fake.Requests[1].Messages)
	if !strings.Contains(string(second), `"is_error":true`) {
		t.Errorf("unknown tool not answered with is_error:\n%s", second)
	}
}

func TestRun_IterationCapStopsRunaway(t *testing.T) {
	script := make([]fakes.Scripted, 0, maxIterations+2)
	for range maxIterations + 2 {
		script = append(script, fakes.Call("tu_n", "ping", `{}`))
	}
	fake := fakes.NewAnthropic(script...)
	defer fake.Close()

	tools := Tools{"ping": Tool{
		Description: "loops forever",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, string, json.RawMessage) (string, error) {
			return "pong", nil
		},
	}}
	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, tools)
	if err == nil {
		t.Fatal("runaway loop did not error at the iteration cap")
	}
	if len(fake.Requests) > maxIterations {
		t.Fatalf("requests = %d, cap %d not enforced", len(fake.Requests), maxIterations)
	}
}

func TestRun_APIErrorSurfaces(t *testing.T) {
	// 400 is terminal (SDK retries only 408/429/5xx); must surface so the
	// morning job can fall back to the deterministic degraded message.
	fake := fakes.NewAnthropic(fakes.HTTPErr{Status: 400})
	defer fake.Close()

	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err == nil {
		t.Fatal("API error swallowed; degraded mode depends on it surfacing")
	}
}

func TestRun_PauseTurnResumesAndCountsAgainstCap(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.Stop{Reason: "pause_turn", S: "sto pensando"},
		fakes.Text{S: "fatto"},
	)
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "fatto" {
		t.Errorf("out = %q", res.Text)
	}
	if len(fake.Requests) != 2 {
		t.Fatalf("requests = %d, want 2 (pause then resume)", len(fake.Requests))
	}
	// The resume must re-send the paused assistant content as-is.
	second, _ := json.Marshal(fake.Requests[1].Messages)
	if !strings.Contains(string(second), "sto pensando") {
		t.Errorf("paused content not re-sent:\n%s", second)
	}
}

func TestRun_RefusalErrorsWithoutRetry(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Stop{Reason: "refusal", S: "no"})
	defer fake.Close()

	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err == nil {
		t.Fatal("refusal must surface as error (degraded fallback)")
	}
	if len(fake.Requests) != 1 {
		t.Fatalf("requests = %d, want exactly 1 (never retry a refusal)", len(fake.Requests))
	}
}

func TestRun_MaxTokensReturnsPartialText(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Stop{Reason: "max_tokens", S: "narrativa tronca"})
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 16,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v (truncated beats silent)", err)
	}
	if res.Text != "narrativa tronca" || res.StopReason != "max_tokens" {
		t.Errorf("res = %+v", res)
	}
}

func TestRun_EmptyEndTurnErrors(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: ""})
	defer fake.Close()

	if _, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, nil); err == nil {
		t.Fatal("empty end_turn accepted; degraded mode depends on the error")
	}
}

func TestRun_TransientOverloadRecoversViaSDKRetries(t *testing.T) {
	// 529s are retryable: the SDK's built-in retries (2) must absorb a
	// transient overload so 07:00 does not degrade unnecessarily.
	fake := fakes.NewAnthropic(
		fakes.HTTPErr{Status: 529},
		fakes.HTTPErr{Status: 529},
		fakes.Text{S: "ripreso"},
	)
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v (SDK retries must absorb transient 529s)", err)
	}
	if res.Text != "ripreso" {
		t.Errorf("out = %q", res.Text)
	}
}

func TestRun_ParallelToolCallsAllAnswered(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.ToolUse{Calls: []fakes.ToolCall{
			{ID: "tu_a", Name: "ok_tool", Input: json.RawMessage(`{}`)},
			{ID: "tu_b", Name: "bad_tool", Input: json.RawMessage(`{}`)},
		}},
		fakes.Text{S: "fatto con entrambi"},
	)
	defer fake.Close()

	tools := Tools{
		"ok_tool": Tool{Description: "ok", Schema: json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, string, json.RawMessage) (string, error) { return "dati", nil }},
		"bad_tool": Tool{Description: "bad", Schema: json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, string, json.RawMessage) (string, error) { return "", errors.New("boom") }},
	}
	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text == "" {
		t.Error("empty text")
	}
	second, _ := json.Marshal(fake.Requests[1].Messages)
	for _, want := range []string{"tu_a", "tu_b", `"is_error":true`} {
		if !strings.Contains(string(second), want) {
			t.Errorf("parallel tool_results missing %q:\n%s", want, second)
		}
	}
}

func TestRun_MalformedToolSchemaFailsLoudly(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "never reached"})
	defer fake.Close()

	tools := Tools{"broken": Tool{Description: "x", Schema: json.RawMessage(`{not json`),
		Handler: func(context.Context, string, json.RawMessage) (string, error) { return "", nil }}}
	if _, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 128,
	}, tools); err == nil {
		t.Fatal("malformed developer schema registered silently")
	}
	if len(fake.Requests) != 0 {
		t.Fatal("request sent despite invalid tool schema")
	}
}

func TestRun_ToolsSerializedInSortedOrder(t *testing.T) {
	// Tools sit at position zero of the M5 cache prefix: map ordering would
	// silently invalidate the cache on every call.
	fake := fakes.NewAnthropic(fakes.Text{S: "ok"}, fakes.Text{S: "ok"})
	defer fake.Close()

	tools := Tools{}
	for _, n := range []string{"zeta", "alpha", "mid"} {
		tools[n] = Tool{Description: n, Schema: json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, string, json.RawMessage) (string, error) { return "", nil }}
	}
	for range 2 {
		if _, err := Run(context.Background(), newTestClient(fake.URL()), Request{
			Model: "m", System: "s", UserText: "u", MaxTokens: 64,
		}, tools); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	a, _ := json.Marshal(fake.Requests[0].Tools)
	b, _ := json.Marshal(fake.Requests[1].Tools)
	if string(a) != string(b) {
		t.Fatal("tool serialization not deterministic across calls")
	}
	ia, im, iz := strings.Index(string(a), "alpha"), strings.Index(string(a), "mid"), strings.Index(string(a), "zeta")
	if ia >= im || im >= iz {
		t.Fatalf("tools not sorted: alpha@%d mid@%d zeta@%d", ia, im, iz)
	}
}

func TestRun_DeepTierWireShape(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "risposta"})
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model:     "claude-opus-4-8",
		System:    "sei il coach",
		Profile:   "PROFILO: baseline HRV 35",
		UserText:  "come sto?",
		MaxTokens: 2048,
		Cache:     true,
		Thinking:  true,
		Effort:    "high",
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "risposta" {
		t.Errorf("text = %q", res.Text)
	}
	raw := string(fake.Requests[0].Raw)

	// Exactly ONE cache breakpoint, on the profile block (the stable prefix).
	if n := strings.Count(raw, "cache_control"); n != 1 {
		t.Errorf("cache_control occurrences = %d, want exactly 1:\n%s", n, raw)
	}
	if !strings.Contains(raw, "PROFILO: baseline HRV 35") {
		t.Error("profile block missing from system")
	}
	if !strings.Contains(raw, `"thinking":{`) || !strings.Contains(raw, `"adaptive"`) {
		t.Errorf("adaptive thinking missing:\n%s", raw)
	}
	if !strings.Contains(raw, `"effort":"high"`) {
		t.Errorf("output_config effort missing:\n%s", raw)
	}
}

func TestRun_CheapTierStaysClean(t *testing.T) {
	// The Haiku request must NOT grow thinking/effort/cache by accident:
	// Haiku 4.5 rejects adaptive thinking and the cache would never be read.
	fake := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer fake.Close()

	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "claude-haiku-4-5-20251001", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw := string(fake.Requests[0].Raw)
	for _, forbidden := range []string{`"cache_control"`, `"thinking":{`, `"output_config":`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("cheap tier request contains %q:\n%s", forbidden, raw)
		}
	}
}

func TestRun_HistoryPrecedesUserTurn(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "continuo"})
	defer fake.Close()

	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("ieri ti ho chiesto del lungo")),
		anthropic.NewAssistantMessage(anthropic.NewTextBlock("e ti ho risposto di tenerlo facile")),
	}
	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", History: history, UserText: "e oggi?", MaxTokens: 256,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.Requests[0].Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (2 history + 1 new)", len(fake.Requests[0].Messages))
	}
	raw := string(fake.Requests[0].Raw)
	iH := strings.Index(raw, "ieri ti ho chiesto")
	iU := strings.Index(raw, "e oggi?")
	if iH == -1 || iU == -1 || iH > iU {
		t.Errorf("history not before user turn: hist@%d user@%d", iH, iU)
	}
	if len(res.Transcript) != 4 {
		t.Errorf("transcript = %d, want 4 (history + user + assistant)", len(res.Transcript))
	}
}

func TestRun_UsageCaptured(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer fake.Close()

	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 64,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 50 {
		t.Errorf("usage = %+v, want fake's 100/50", res.Usage)
	}
}

func TestRun_ThinkingPreservedAcrossToolContinuation(t *testing.T) {
	// Production config is Thinking+tools together: the API REQUIRES the
	// thinking block replayed verbatim when the loop continues after tools.
	fake := fakes.NewAnthropic(
		fakes.ToolUse{
			Thinking: "ragionamento-interno-da-replicare",
			Calls:    []fakes.ToolCall{{ID: "tu_t", Name: "ping", Input: json.RawMessage(`{}`)}},
		},
		fakes.Text{S: "fatto"},
	)
	defer fake.Close()

	tools := Tools{"ping": Tool{Description: "p", Schema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, string, json.RawMessage) (string, error) { return "pong", nil }}}
	res, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 256, Thinking: true,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "fatto" {
		t.Errorf("text = %q", res.Text)
	}
	second := string(fake.Requests[1].Raw)
	if !strings.Contains(second, "ragionamento-interno-da-replicare") {
		t.Errorf("thinking block not replayed on continuation:\n%s", second)
	}
	if !strings.Contains(second, "fake-sig") {
		t.Errorf("thinking signature not replayed (API rejects without it)")
	}
}
