// ABOUTME: Tests for the hand-rolled tool loop: caps, tool_result discipline, error surfacing.
// ABOUTME: Driven entirely by the scripted fakeanthropic; no network, no real model.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/fakes"
)

func newTestClient(url string) Client {
	return NewClient("test-key", url)
}

func TestRun_PlainTextCompletes(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "Buongiorno, giornata verde."})
	defer fake.Close()

	out, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model:     "claude-haiku-4-5-20251001",
		System:    "sei un coach",
		UserText:  "dati di oggi: ...",
		MaxTokens: 512,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "Buongiorno, giornata verde." {
		t.Errorf("out = %q", out)
	}
	if len(fake.Requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(fake.Requests))
	}
	if fake.Requests[0].Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q", fake.Requests[0].Model)
	}
}

func TestRun_NoCacheControlOnCheapTier(t *testing.T) {
	// Decision 10: no caching on Haiku; nothing reads the cache within the
	// TTL of a single-shot 07:00 run.
	fake := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer fake.Close()

	_, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "claude-haiku-4-5-20251001", System: "s", UserText: "u", MaxTokens: 128,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(string(fake.Requests[0].System), "cache_control") {
		t.Error("cache_control present on the cheap tier")
	}
}

func TestRun_ToolCallAnsweredThenCompletes(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.ToolUse{ID: "tu_1", Name: "get_recent_activities", Input: json.RawMessage(`{"days":3}`)},
		fakes.Text{S: "ieri hai corso facile, bene."},
	)
	defer fake.Close()

	called := 0
	tools := Tools{
		"get_recent_activities": Tool{
			Description: "ultime attività",
			Schema:      json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer"}}}`),
			Handler: func(ctx context.Context, input json.RawMessage) (string, error) {
				called++
				return `[{"date":"2026-06-10","type":"Run","load":35}]`, nil
			},
		},
	}
	out, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 256,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called != 1 {
		t.Fatalf("tool called %d times, want 1", called)
	}
	if out != "ieri hai corso facile, bene." {
		t.Errorf("out = %q", out)
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
		fakes.ToolUse{ID: "tu_2", Name: "boom", Input: json.RawMessage(`{}`)},
		fakes.Text{S: "ok, faccio senza"},
	)
	defer fake.Close()

	tools := Tools{
		"boom": Tool{
			Description: "fails",
			Schema:      json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, json.RawMessage) (string, error) {
				return "", errors.New("icu 502")
			},
		},
	}
	out, err := Run(context.Background(), newTestClient(fake.URL()), Request{
		Model: "m", System: "s", UserText: "u", MaxTokens: 256,
	}, tools)
	if err != nil {
		t.Fatalf("Run: %v (tool failure must flow back as is_error, not abort)", err)
	}
	if out == "" {
		t.Error("empty narrative after recoverable tool failure")
	}
	second, _ := json.Marshal(fake.Requests[1].Messages)
	if !strings.Contains(string(second), `"is_error":true`) {
		t.Errorf("tool failure not marked is_error:\n%s", second)
	}
}

func TestRun_UnknownToolAnsweredWithIsError(t *testing.T) {
	fake := fakes.NewAnthropic(
		fakes.ToolUse{ID: "tu_3", Name: "not_registered", Input: json.RawMessage(`{}`)},
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
		script = append(script, fakes.ToolUse{ID: "tu_n", Name: "ping", Input: json.RawMessage(`{}`)})
	}
	fake := fakes.NewAnthropic(script...)
	defer fake.Close()

	tools := Tools{"ping": Tool{
		Description: "loops forever",
		Schema:      json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, json.RawMessage) (string, error) {
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
