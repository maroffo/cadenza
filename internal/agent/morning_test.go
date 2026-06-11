// ABOUTME: Tests for the Narrator: the prompt CONTENT is the decision-15 enforcement surface.
// ABOUTME: Every verdict element must reach the model; dropping a line must fail a test.

package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/verdict"
)

func TestMorningNarrative_PromptCarriesEverything(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "Giornata da gestire con prudenza."})
	defer fake.Close()

	in := NarrativeInput{
		Date: "2026-06-12",
		Body: "HRV: 30\nFC riposo: 61 bpm",
		Verdict: verdict.Verdict{
			Kind: verdict.Modify,
			Reasons: []verdict.Reason{{
				RuleID: "hrv_low", Message: "HRV sotto il range personale",
				Observed: "30", Threshold: "29.3",
			}},
			Checks: []verdict.Check{
				{Label: "FC riposo", Observed: "+6.5", Limit: "max +5", Passed: false},
				{Label: "sonno", Observed: "7.1h", Limit: "min 6.0h", Passed: true},
			},
			Caps:     verdict.Caps{MaxZone: 2, MaxMinutes: 60},
			DataGaps: []string{"ramp rate non disponibile"},
		},
	}
	n := Narrator{Client: newTestClient(fake.URL()), Model: "claude-haiku-test"}
	out, err := n.MorningNarrative(context.Background(), in)
	if err != nil {
		t.Fatalf("MorningNarrative: %v", err)
	}
	if out == "" {
		t.Fatal("empty narrative")
	}

	req := fake.Requests[0]
	if req.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
	}
	user, _ := json.Marshal(req.Messages)
	prompt := string(user)
	for _, want := range []string{
		"2026-06-12",
		"Verdetto deterministico: MODIFY",
		"hrv_low", "HRV sotto il range personale", "soglia 29.3",
		"FC riposo", "fuori range",
		"sonno", "[ok]",
		"max Z2, max 60 minuti",
		"Dati mancanti: ramp rate non disponibile",
		"HRV: 30",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q (decision 15: the model narrates ALL pre-computed facts)", want)
		}
	}
	system := string(req.System)
	for _, want := range []string{"italiano", "non fare MAI aritmetica", "Non contraddire mai il verdetto"} {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestMorningNarrative_PartialCapsRenderOnlySetParts(t *testing.T) {
	fake := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer fake.Close()

	in := NarrativeInput{
		Date: "2026-06-12", Body: "x",
		Verdict: verdict.Verdict{Kind: verdict.Skip, Caps: verdict.Caps{MaxZone: 1}},
	}
	n := Narrator{Client: newTestClient(fake.URL()), Model: "m"}
	if _, err := n.MorningNarrative(context.Background(), in); err != nil {
		t.Fatalf("MorningNarrative: %v", err)
	}
	prompt, _ := json.Marshal(fake.Requests[0].Messages)
	if strings.Contains(string(prompt), "max 0 minuti") {
		t.Error("partial caps rendered a zero limit (the model would parrot 'max 0 minuti')")
	}
	if !strings.Contains(string(prompt), "max Z1") {
		t.Error("set cap missing from prompt")
	}
}

func TestMorningNarrative_NoModelConfigured(t *testing.T) {
	n := Narrator{}
	if _, err := n.MorningNarrative(context.Background(), NarrativeInput{}); err == nil {
		t.Fatal("missing model accepted")
	}
}
