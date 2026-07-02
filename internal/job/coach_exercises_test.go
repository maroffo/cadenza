// ABOUTME: Tests for the exercise library: search_exercises tool, @demo extraction, GIF delivery.
// ABOUTME: Asserts text-before-GIF ordering and the URL-then-cached-file_id delivery path.

package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/exercises"
	"github.com/maroffo/cadenza/internal/fakes"
)

// stubAnimator records every animation send and returns a fixed file_id; it also
// captures how many text bodies had already been delivered, to prove the GIF is
// pushed AFTER the coaching text.
type stubAnimator struct {
	out      *stubInteractor
	fileID   string
	err      error
	sources  []string
	captions []string
	bodiesAt []int
}

func (a *stubAnimator) SendAnimation(_ context.Context, source, caption string) (string, error) {
	a.sources = append(a.sources, source)
	a.captions = append(a.captions, caption)
	if a.out != nil {
		a.bodiesAt = append(a.bodiesAt, len(a.out.plain))
	}
	return a.fileID, a.err
}

// stubMediaCache is a map-backed file_id cache that records writes.
type stubMediaCache struct {
	store  map[string]string
	getErr error
	sets   map[string]string
	setCnt int
	getCnt int
}

func newStubMediaCache() *stubMediaCache {
	return &stubMediaCache{store: map[string]string{}, sets: map[string]string{}}
}

func (m *stubMediaCache) Get(_ context.Context, id string) (string, bool, error) {
	m.getCnt++
	if m.getErr != nil {
		return "", false, m.getErr
	}
	v, ok := m.store[id]
	return v, ok, nil
}

func (m *stubMediaCache) Set(_ context.Context, id, fileID string) error {
	m.setCnt++
	m.sets[id] = fileID
	m.store[id] = fileID
	return nil
}

func TestExtractDemos(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantText string
		wantIDs  []string
	}{
		{"none", "Prova 3x12.", "Prova 3x12.", nil},
		{"single", "Prova il goblet squat.\n@demo: 0001", "Prova il goblet squat.", []string{"0001"}},
		{"multi-and-spaces", "Schiena:\n@demo: 0001, 0419 ,0007", "Schiena:", []string{"0001", "0419", "0007"}},
		{"case-insensitive", "Ecco.\n@DEMO: 0002", "Ecco.", []string{"0002"}},
		{"dedup-and-cap", "X\n@demo: 1,1,2,3,4,5,6", "X", []string{"1", "2", "3", "4"}},
		{"mid-text-line", "Riga uno\n@demo: 9\nRiga due", "Riga uno\nRiga due", []string{"9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotIDs := extractDemos(tc.in)
			if gotText != tc.wantText {
				t.Errorf("text = %q, want %q", gotText, tc.wantText)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tc.wantIDs, ",") {
				t.Errorf("ids = %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestSearchExercisesToolExposedOnlyWithCatalog(t *testing.T) {
	// The static system prompt always names search_exercises (like it names
	// write_workout); the conditional part is the TOOL itself, whose description
	// inlines the catalog vocabulary ("Muscoli target:"). That string is the
	// discriminator: present only when the tool is registered.
	const toolMarker = "Muscoli target:"

	// Without a catalog the tool is hidden.
	llm := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm.Close()
	c, _, _, _, _, _ := newCoach(t, llm)
	if err := c.Converse(context.Background(), "esercizi?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if strings.Contains(string(llm.Requests[0].Raw), toolMarker) {
		t.Error("search_exercises tool registered without a catalog")
	}

	// With a catalog the tool and its vocabulary are advertised.
	llm2 := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm2.Close()
	c2, _, _, _, _, _ := newCoach(t, llm2)
	c2.Catalog = exercises.MustLoad()
	if err := c2.Converse(context.Background(), "esercizi?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	raw := string(llm2.Requests[0].Raw)
	for _, want := range []string{"search_exercises", "body weight", toolMarker} {
		if !strings.Contains(raw, want) {
			t.Errorf("request missing %q with a catalog wired", want)
		}
	}
}

func TestDemoDelivery_TextBeforeGIF_FirstSendUsesURLAndCaches(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Prova il <b>3/4 sit-up</b>.\n@demo: 0001"})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	cat := exercises.MustLoad()
	c.Catalog = cat
	cache := newStubMediaCache()
	c.MediaCache = cache
	anim := &stubAnimator{out: out, fileID: "FILEID_NEW"}
	c.Animator = anim

	if err := c.Converse(context.Background(), "cosa per gli addominali?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}

	// The @demo line is stripped from the delivered text.
	if len(out.plain) != 1 {
		t.Fatalf("bodies = %d, want 1", len(out.plain))
	}
	if strings.Contains(out.plain[0], "@demo") {
		t.Errorf("annotation leaked into reply: %q", out.plain[0])
	}
	if !strings.Contains(out.plain[0], "sit-up") {
		t.Errorf("reply lost its content: %q", out.plain[0])
	}

	// Exactly one animation, sent AFTER the text (one body already delivered).
	if len(anim.sources) != 1 {
		t.Fatalf("animations = %d, want 1", len(anim.sources))
	}
	if anim.bodiesAt[0] != 1 {
		t.Errorf("GIF sent before text: bodies-at-send = %d, want 1", anim.bodiesAt[0])
	}
	// First send (cache miss) uses the upstream GitHub URL.
	ex, _ := cat.ByID("0001")
	if anim.sources[0] != cat.GIFSourceURL(ex) {
		t.Errorf("source = %q, want GIF URL %q", anim.sources[0], cat.GIFSourceURL(ex))
	}
	if anim.captions[0] != ex.Name {
		t.Errorf("caption = %q, want %q", anim.captions[0], ex.Name)
	}
	// The returned file_id is cached for next time.
	if cache.sets["0001"] != "FILEID_NEW" {
		t.Errorf("file_id not cached: %v", cache.sets)
	}
}

func TestDemoDelivery_CachedFileIDSkipsURLAndDoesNotRecache(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Ecco.\n@demo: 0001"})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	c.Catalog = exercises.MustLoad()
	cache := newStubMediaCache()
	cache.store["0001"] = "FILEID_CACHED"
	c.MediaCache = cache
	anim := &stubAnimator{out: out, fileID: "SHOULD_NOT_BE_USED"}
	c.Animator = anim

	if err := c.Converse(context.Background(), "fammi vedere"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(anim.sources) != 1 || anim.sources[0] != "FILEID_CACHED" {
		t.Fatalf("source = %v, want [FILEID_CACHED]", anim.sources)
	}
	if cache.setCnt != 0 {
		t.Errorf("re-cached on a cache hit: setCnt = %d", cache.setCnt)
	}
}

func TestDemoDelivery_CacheReadErrorFallsBackToURL(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Ecco.\n@demo: 0001"})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	cat := exercises.MustLoad()
	c.Catalog = cat
	cache := newStubMediaCache()
	cache.getErr = errors.New("firestore down") // read blip
	c.MediaCache = cache
	anim := &stubAnimator{out: out, fileID: "FILEID_NEW"}
	c.Animator = anim

	if err := c.Converse(context.Background(), "fammi vedere"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	// A cache READ error degrades to the source URL, never fails the reply.
	if len(out.plain) != 1 {
		t.Fatalf("reply not delivered: bodies = %d", len(out.plain))
	}
	ex, _ := cat.ByID("0001")
	if len(anim.sources) != 1 || anim.sources[0] != cat.GIFSourceURL(ex) {
		t.Fatalf("source = %v, want [%s] (URL fallback on read error)", anim.sources, cat.GIFSourceURL(ex))
	}
	// Documented behavior: a read miss/error is treated as "not cached", so a
	// successful send still writes the file_id (idempotent, same id->same gif).
	if cache.sets["0001"] != "FILEID_NEW" {
		t.Errorf("expected write-after-read-error to cache the file_id, got sets=%v", cache.sets)
	}
}

func TestDemoDelivery_SendFailureSkipsAndDoesNotCache(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "Due esercizi.\n@demo: 0001,0002"})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	c.Catalog = exercises.MustLoad()
	cache := newStubMediaCache()
	c.MediaCache = cache
	anim := &stubAnimator{out: out, err: errors.New("telegram 400")} // every send fails
	c.Animator = anim

	if err := c.Converse(context.Background(), "mostrami"); err != nil {
		t.Fatalf("Converse must stay nil on demo send failure: %v", err)
	}
	// Contract: the coaching reply still reaches the athlete.
	if len(out.plain) != 1 {
		t.Fatalf("reply not delivered despite demo failure: bodies = %d", len(out.plain))
	}
	// Both ids are still attempted (one failure does not abort the rest)...
	if len(anim.sources) != 2 {
		t.Errorf("attempts = %d, want 2 (a failed send must not abort the next id)", len(anim.sources))
	}
	// ...and a failed send never caches a file_id.
	if cache.setCnt != 0 {
		t.Errorf("cached after a failed send: setCnt = %d", cache.setCnt)
	}
}

func TestDemoDelivery_UnknownIDAndNoAnimatorAreSafe(t *testing.T) {
	// Unknown id: no animation, no panic.
	llm := fakes.NewAnthropic(fakes.Text{S: "Testo.\n@demo: ZZZZ"})
	defer llm.Close()
	c, out, _, _, _, _ := newCoach(t, llm)
	c.Catalog = exercises.MustLoad()
	anim := &stubAnimator{out: out, fileID: "X"}
	c.Animator = anim
	if err := c.Converse(context.Background(), "x"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if len(anim.sources) != 0 {
		t.Errorf("sent animation for unknown id: %v", anim.sources)
	}
	if len(out.plain) != 1 || strings.Contains(out.plain[0], "@demo") {
		t.Errorf("reply mishandled: %v", out.plain)
	}
}
