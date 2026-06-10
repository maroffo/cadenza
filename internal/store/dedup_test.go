// ABOUTME: Tests for side-effect dedup: key validation (unit) and atomicity (emulator).
// ABOUTME: Emulator cases skip unless FIRESTORE_EMULATOR_HOST is set; REQUIRE_EMULATOR=1 makes skips fatal.

package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
)

func emulatorClient(t *testing.T) *firestore.Client {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		// CI sets REQUIRE_EMULATOR=1 so the gate cannot go hollow by accident.
		if os.Getenv("REQUIRE_EMULATOR") == "1" {
			t.Fatal("REQUIRE_EMULATOR=1 but FIRESTORE_EMULATOR_HOST is not set")
		}
		t.Skip("FIRESTORE_EMULATOR_HOST not set, skipping emulator test")
	}
	client, err := firestore.NewClient(context.Background(), "cadenza-test")
	if err != nil {
		t.Fatalf("firestore client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// Validation failures must not touch the network, so these run without emulator.
func TestDedupReserve_RejectsInvalidKeys(t *testing.T) {
	d := NewDedup(nil) // client must never be reached
	ctx := context.Background()
	for _, key := range []string{
		"",                             // empty
		"a/b",                          // path separator would change the document path
		"key with spaces",              // not in allowlist
		".dotfirst",                    // must start alphanumeric
		"x" + strings.Repeat("a", 200), // over length cap (201 chars)
	} {
		ok, err := d.Reserve(ctx, key, time.Hour)
		if err == nil || ok {
			t.Errorf("Reserve(%q) = %v, %v; want false, validation error", key, ok, err)
		}
	}
}

func TestDedupReserve_RejectsNonPositiveTTL(t *testing.T) {
	d := NewDedup(nil)
	ctx := context.Background()
	for _, ttl := range []time.Duration{0, -time.Hour} {
		ok, err := d.Reserve(ctx, "valid-key", ttl)
		if err == nil || ok {
			t.Errorf("Reserve(ttl=%v) = %v, %v; want false, validation error", ttl, ok, err)
		}
	}
}

func TestDedupReserve_AcceptsRealKeyShapes(t *testing.T) {
	// The key shapes the executor will actually use must pass validation.
	for _, key := range []string{
		"tg-update-987654321",
		"morning-2026-06-10",
		"reconcile-2026-06-10",
		"injury-inj-20260610-knee-day5-r1",
		"send-morning:2026-06-10",
		"icuwrite-evt-2026-06-12-a1b2c3d4",
	} {
		if !validDedupKey.MatchString(key) {
			t.Errorf("real key shape %q rejected by validation", key)
		}
	}
}

func TestDedupReserve_FirstWinsReplayNoops(t *testing.T) {
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("tg-update-%d", time.Now().UnixNano())

	first, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if !first {
		t.Fatal("first Reserve = false, want true")
	}

	second, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if second {
		t.Fatal("second Reserve = true, want false (duplicate must no-op)")
	}
}

func TestDedupReserve_ConcurrentSingleWinner(t *testing.T) {
	// The component exists for exactly this property: N concurrent deliveries
	// of the same update, exactly one owner. Sequential tests would also pass
	// with a racy read-then-write implementation.
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := fmt.Sprintf("race-%d", time.Now().UnixNano())

	const n = 10
	var wg sync.WaitGroup
	wins := make(chan bool, n)
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			ok, err := d.Reserve(ctx, key, time.Hour)
			if err != nil {
				errs <- err
				return
			}
			wins <- ok
		})
	}
	wg.Wait()
	close(wins)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Reserve error: %v", err)
	}
	winners := 0
	for ok := range wins {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

func TestDedupReserve_ErrorIsNotADuplicate(t *testing.T) {
	// false+error means UNKNOWN: callers must be able to distinguish it from
	// false+nil (duplicate). A canceled context must surface as an error.
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, err := d.Reserve(ctx, fmt.Sprintf("dead-%d", time.Now().UnixNano()), time.Hour)
	if ok {
		t.Fatal("Reserve on canceled context = true, want false")
	}
	if err == nil {
		t.Fatal("Reserve on canceled context returned nil error; callers cannot distinguish duplicate from failure")
	}
}

func TestDedupReserve_DistinctKeysIndependent(t *testing.T) {
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, err := d.Reserve(ctx, fmt.Sprintf("a-%d", time.Now().UnixNano()), time.Hour)
	if err != nil || !a {
		t.Fatalf("Reserve a = %v, %v; want true, nil", a, err)
	}
	b, err := d.Reserve(ctx, fmt.Sprintf("b-%d", time.Now().UnixNano()), time.Hour)
	if err != nil || !b {
		t.Fatalf("Reserve b = %v, %v; want true, nil", b, err)
	}
}
