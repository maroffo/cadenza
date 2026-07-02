// ABOUTME: Tests for message composition: morning template, nil-safe rendering, 4096 splitting.
// ABOUTME: A split must never land inside an HTML tag (Telegram rejects the whole message).

package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func TestMorningBody_AllData(t *testing.T) {
	body := MorningBody(MorningData{
		Date:      "2026-06-10",
		HRV:       fp(68),
		RestingHR: ip(47),
		SleepSecs: ip(26100), // 7.25h
		CTL:       fp(41.3),
		ATL:       fp(47.9),
		RampRate:  fp(3.2),
	})
	for _, want := range []string{"68", "47", "7.2", "41.3", "47.9", "3.2"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "<b>") {
		t.Errorf("body must use HTML bold labels:\n%s", body)
	}
}

func TestMorningBody_MissingDataRendersAsUnavailable(t *testing.T) {
	body := MorningBody(MorningData{Date: "2026-06-10"})
	if strings.Contains(body, "0.0") || strings.Contains(body, "<nil>") {
		t.Errorf("missing data must never render as zero or <nil>:\n%s", body)
	}
	if !strings.Contains(body, "n/d") {
		t.Errorf("missing metrics should render as n/d:\n%s", body)
	}
}

func TestMorningBody_StaleDataIsLabeled(t *testing.T) {
	body := MorningBody(MorningData{
		Date: "2026-06-10", HRV: fp(65), Stale: true, StaleAsOf: "2026-06-09",
	})
	if !strings.Contains(body, "2026-06-09") {
		t.Errorf("stale data must carry its date:\n%s", body)
	}
}

func TestMorningRoutine_RendersGroupsAndSkipsEmpty(t *testing.T) {
	out := MorningRoutine([]RoutineGroup{
		{Label: "Braccia", Exercises: []RoutineExercise{
			{Name: "Band Curl", Equipment: "band"},
			{Name: "Push-up", Equipment: "body weight"},
		}},
		{Label: "Gambe", Exercises: nil}, // empty group must be skipped
	})
	if !strings.Contains(out, "Braccia") || !strings.Contains(out, "Band Curl") {
		t.Errorf("routine missing content:\n%s", out)
	}
	if strings.Contains(out, "Gambe") {
		t.Errorf("empty group should be skipped:\n%s", out)
	}
	if strings.Contains(out, "<table") {
		t.Errorf("routine uses unsupported HTML:\n%s", out)
	}
}

func TestMorningRoutine_EmptyInputRendersEmpty(t *testing.T) {
	if got := MorningRoutine(nil); got != "" {
		t.Errorf("nil groups = %q, want empty", got)
	}
	if got := MorningRoutine([]RoutineGroup{{Label: "Braccia"}}); got != "" {
		t.Errorf("all-empty groups = %q, want empty", got)
	}
}

func TestMorningRoutine_EscapesNames(t *testing.T) {
	out := MorningRoutine([]RoutineGroup{
		{Label: "Core", Exercises: []RoutineExercise{{Name: "A<b>&x", Equipment: "b<a"}}},
	})
	if strings.Contains(out, "A<b>&x") || strings.Contains(out, "b<a") {
		t.Errorf("dynamic exercise text not HTML-escaped:\n%s", out)
	}
}

func TestDegradedTemplates_NeverEmpty(t *testing.T) {
	cases := map[string]string{
		"icu_down_no_data": DegradedNoData(),
		"llm_down":         DegradedLLMDown(),
	}
	for name, msg := range cases {
		if strings.TrimSpace(msg) == "" {
			t.Errorf("%s template is empty", name)
		}
		if strings.Contains(msg, "<table") {
			t.Errorf("%s uses unsupported HTML:\n%s", name, msg)
		}
	}
}

func TestSplitMessage_ShortPassthrough(t *testing.T) {
	chunks := SplitMessage("hello <b>world</b>")
	if len(chunks) != 1 || chunks[0] != "hello <b>world</b>" {
		t.Fatalf("chunks = %q, want single passthrough", chunks)
	}
}

func TestSplitMessage_SplitsOnParagraphs(t *testing.T) {
	para := strings.Repeat("a", 1500)
	msg := para + "\n\n" + para + "\n\n" + para // 4504 chars total
	chunks := SplitMessage(msg)
	if len(chunks) < 2 {
		t.Fatalf("expected split, got %d chunk(s)", len(chunks))
	}
	for n, c := range chunks {
		if len(c) > telegramLimit {
			t.Errorf("chunk %d is %d chars, over limit", n, len(c))
		}
	}
	if got := strings.Join(chunks, "\n\n"); got != msg {
		t.Error("paragraph split must be lossless when rejoined")
	}
}

func TestSplitMessage_NeverSplitsInsideTag(t *testing.T) {
	// Pathological: one giant paragraph of bold fragments with no \n\n.
	frag := "<b>0123456789</b> "
	msg := strings.Repeat(frag, 300) // ~5400 chars, no paragraph breaks
	chunks := SplitMessage(msg)
	if len(chunks) < 2 {
		t.Fatalf("expected split, got %d chunk(s)", len(chunks))
	}
	for n, c := range chunks {
		if len(c) > telegramLimit {
			t.Errorf("chunk %d over limit: %d", n, len(c))
		}
		if strings.Count(c, "<") != strings.Count(c, ">") {
			t.Errorf("chunk %d has unbalanced angle brackets (split inside a tag?):\n%.80s...", n, c)
		}
		if strings.Count(c, "<b>") != strings.Count(c, "</b>") {
			t.Errorf("chunk %d has unbalanced <b> tags", n)
		}
	}
}

func TestEscape(t *testing.T) {
	got := Escape(`pace 4'45"/km & <stryd>`)
	if strings.Contains(got, "<stryd>") {
		t.Errorf("Escape left raw angle brackets: %q", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("Escape missed ampersand: %q", got)
	}
}

func TestSplitMessage_MultibyteSafe(t *testing.T) {
	// A giant no-space emoji paragraph must never split mid-rune: an
	// invalid-UTF-8 chunk makes Telegram reject the whole message.
	msg := strings.Repeat("📈", 2000) // 8000 bytes, no spaces or newlines
	for n, c := range SplitMessage(msg) {
		if len(c) > telegramLimit {
			t.Errorf("chunk %d over limit: %d", n, len(c))
		}
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is invalid UTF-8 (split mid-rune)", n)
		}
	}
}

func TestSanitizeNarrative_AllowlistOnly(t *testing.T) {
	in := `Oggi <b>spingi</b> con <i>criterio</i>. <a href="https://evil.example">link</a> <script>x</script><u>sotto</u>`
	out := SanitizeNarrative(in)
	if !strings.Contains(out, "<b>spingi</b>") || !strings.Contains(out, "<i>criterio</i>") {
		t.Errorf("allowed tags stripped: %q", out)
	}
	for _, forbidden := range []string{"<a", "<script", "<u>"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("forbidden tag survived: %q in %q", forbidden, out)
		}
	}
	if !strings.Contains(out, "link") {
		t.Errorf("inner text lost: %q", out)
	}
}
