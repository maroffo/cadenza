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
	"github.com/maroffo/cadenza/internal/telegram"
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
		// Cap = 8 (the full morning routine must fit in one reply); the 9th drops.
		{"dedup-and-cap", "X\n@demo: 1,1,2,3,4,5,6,7,8,9", "X", []string{"1", "2", "3", "4", "5", "6", "7", "8"}},
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
	// Contract: the coaching reply reaches the athlete, AND the partial delivery
	// is told to him (review finding: a silent half-routine looks like amnesia).
	if len(out.plain) != 2 {
		t.Fatalf("sends = %d, want 2 (reply + failure notice): %v", len(out.plain), out.plain)
	}
	if !strings.Contains(out.plain[1], "2 dimostrazioni su 2") {
		t.Errorf("failure notice malformed: %q", out.plain[1])
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

func TestRoutineSeed_DailyAndDegraded(t *testing.T) {
	a := routineSeed("2026-06-10", testTZ)
	b := routineSeed("2026-06-11", testTZ)
	if b != a+1 {
		t.Errorf("consecutive dates: %d then %d, want +1 (daily rotation)", a, b)
	}
	if got := routineSeed("garbage", testTZ); got != 0 {
		t.Errorf("parse error seed = %d, want 0", got)
	}
}

func TestConverse_RoutineWithIDsInContext(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm.Close()
	c, _, _, _, _, _ := newCoach(t, llm)
	cat := exercises.MustLoad()
	c.Catalog = cat

	if err := c.Converse(context.Background(), "mi fai vedere le gif degli esercizi?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	raw := string(llm.Requests[0].Raw)
	if !strings.Contains(raw, "Routine prevenzione/forza di oggi") {
		t.Fatal("routine block missing from the model context")
	}
	// The context must carry the SAME picks as the morning message for this date
	// (fixedNow = 2026-06-10), id + name, so @demo needs no search and no guessing.
	picks := cat.DailyRoutine(routineSeed("2026-06-10", testTZ), routinePerGroup, nil)
	for _, p := range picks {
		for _, ex := range p.Exercises {
			if !strings.Contains(raw, ex.ID+" "+ex.Name) {
				t.Errorf("context missing routine exercise %s %q", ex.ID, ex.Name)
			}
		}
	}
}

func TestConverse_NoCatalogNoRoutineContext(t *testing.T) {
	llm := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm.Close()
	c, _, _, _, _, _ := newCoach(t, llm) // Catalog nil

	if err := c.Converse(context.Background(), "ciao"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if strings.Contains(string(llm.Requests[0].Raw), "Routine prevenzione/forza") {
		t.Error("routine context present without a catalog")
	}
}

func TestRoutineContext_MatchesMorningBlock(t *testing.T) {
	// The athlete reads the morning block; the model reads routineContext. Same
	// date + same catalog + same equipment MUST mean the same exercises, or the
	// coach talks about a routine the athlete never received.
	cat := exercises.MustLoad()
	c := &Coach{Catalog: cat, Now: fixedNow, TZ: testTZ}
	m := Morning{Exercises: cat, Now: fixedNow, TZ: testTZ}

	coachCtx := c.routineContext(context.Background(), "2026-06-10")
	morning := m.routineBlock("2026-06-10")
	for _, p := range cat.DailyRoutine(routineSeed("2026-06-10", testTZ), routinePerGroup, nil) {
		for _, ex := range p.Exercises {
			if !strings.Contains(morning, telegram.Escape(ex.Name)) {
				t.Errorf("morning block missing %q", ex.Name)
			}
			if !strings.Contains(coachCtx, ex.ID) {
				t.Errorf("coach context missing id %s (%q)", ex.ID, ex.Name)
			}
		}
	}
}

type stubMorningRuns struct {
	done bool
	err  error
}

func (s stubMorningRuns) MorningCompleted(context.Context, string) (bool, error) {
	return s.done, s.err
}

func TestRoutineContext_PreSendWindowShowsYesterday(t *testing.T) {
	// Live-bug follow-up (review finding): between midnight and the morning
	// send, the routine the athlete LAST RECEIVED is yesterday's. The context
	// must say so and carry BOTH days, honestly labeled.
	cat := exercises.MustLoad()
	c := &Coach{Catalog: cat, MorningRuns: stubMorningRuns{done: false}, Now: fixedNow, TZ: testTZ}
	got := c.routineContext(context.Background(), "2026-06-10")

	if !strings.Contains(got, "NON e' ancora stato inviato") {
		t.Fatalf("pre-send window not labeled:\n%s", got)
	}
	if !strings.Contains(got, "ieri, 2026-06-09") {
		t.Errorf("yesterday's routine missing:\n%s", got)
	}
	for _, p := range cat.DailyRoutine(routineSeed("2026-06-09", testTZ), routinePerGroup, nil) {
		for _, ex := range p.Exercises {
			if !strings.Contains(got, ex.ID) {
				t.Errorf("context missing yesterday's exercise id %s (%q)", ex.ID, ex.Name)
			}
		}
	}

	// Once today's message is out, a single honestly-labeled block remains.
	c.MorningRuns = stubMorningRuns{done: true}
	sent := c.routineContext(context.Background(), "2026-06-10")
	if strings.Contains(sent, "NON e' ancora") || strings.Contains(sent, "ieri,") {
		t.Errorf("post-send context still shows the pre-send window:\n%s", sent)
	}

	// A Runs read error degrades to the common case (sent), never blocks.
	c.MorningRuns = stubMorningRuns{err: errors.New("firestore blip")}
	deg := c.routineContext(context.Background(), "2026-06-10")
	if !strings.Contains(deg, "del messaggio del mattino di oggi") {
		t.Errorf("degraded context malformed:\n%s", deg)
	}
}
