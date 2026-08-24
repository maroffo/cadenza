// ABOUTME: Tests for side-effect dedup: key validation (unit) and atomicity (emulator).
// ABOUTME: Emulator cases skip unless FIRESTORE_EMULATOR_HOST is set; REQUIRE_EMULATOR=1 makes skips fatal.

package store

import (
	"context"
	"errors"
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
		leaseID, err := d.Reserve(ctx, key, time.Hour)
		if err == nil || leaseID != "" {
			t.Errorf("Reserve(%q) = %q, %v; want empty token, validation error", key, leaseID, err)
		}
	}
}

func TestDedupReserve_RejectsNonPositiveTTL(t *testing.T) {
	d := NewDedup(nil)
	ctx := context.Background()
	for _, ttl := range []time.Duration{0, -time.Hour} {
		leaseID, err := d.Reserve(ctx, "valid-key", ttl)
		if err == nil || leaseID != "" {
			t.Errorf("Reserve(ttl=%v) = %q, %v; want empty token, validation error", ttl, leaseID, err)
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

func TestDedupReserve_ProcessingThenCompleted(t *testing.T) {
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("tg-update-%d", time.Now().UnixNano())

	first, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if err != nil || first == "" {
		t.Fatalf("first Reserve = %q, %v; want token,nil", first, err)
	}
	second, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if second != "" || !errors.Is(err, ErrDedupInProgress) {
		t.Fatalf("live processing Reserve = %q,%v; want empty,ErrDedupInProgress", second, err)
	}
	if err := d.Complete(ctx, key, first); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	third, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if err != nil || third != "" {
		t.Fatalf("completed Reserve = %q,%v; want empty,nil", third, err)
	}
	if err := d.Release(ctx, key, first); err != nil {
		t.Fatalf("late Release: %v", err)
	}
	fourth, err := d.Reserve(ctx, key, 7*24*time.Hour)
	if err != nil || fourth != "" {
		t.Fatalf("release erased completion: Reserve=%q,%v", fourth, err)
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
			leaseID, err := d.Reserve(ctx, key, time.Hour)
			if err != nil {
				errs <- err
				return
			}
			wins <- leaseID != ""
		})
	}
	wg.Wait()
	close(wins)
	close(errs)

	for err := range errs {
		if !errors.Is(err, ErrDedupInProgress) {
			t.Fatalf("concurrent Reserve error: %v", err)
		}
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

func TestDedupReserve_ExpiredLeaseIsReclaimedAndStaleOwnerIsFenced(t *testing.T) {
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("lease-%d", time.Now().UnixNano())
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	first, err := d.Reserve(ctx, key, time.Hour)
	if err != nil || first == "" {
		t.Fatalf("initial Reserve = %q,%v", first, err)
	}
	d.now = func() time.Time { return base.Add(dedupLease + time.Second) }
	second, err := d.Reserve(ctx, key, time.Hour)
	if err != nil || second == "" || second == first {
		t.Fatalf("expired lease Reserve = %q,%v; want fresh token", second, err)
	}
	if err := d.Release(ctx, key, first); err != nil {
		t.Fatalf("stale Release: %v", err)
	}
	if token, err := d.Reserve(ctx, key, time.Hour); token != "" || !errors.Is(err, ErrDedupInProgress) {
		t.Fatalf("stale release erased replacement: Reserve=%q,%v", token, err)
	}
	if err := d.Complete(ctx, key, first); !errors.Is(err, ErrDedupLeaseLost) {
		t.Fatalf("stale Complete = %v, want ErrDedupLeaseLost", err)
	}
	if err := d.Complete(ctx, key, second); err != nil {
		t.Fatalf("replacement Complete: %v", err)
	}
}

func TestDedupReserve_ErrorIsNotADuplicate(t *testing.T) {
	// false+error means UNKNOWN: callers must be able to distinguish it from
	// false+nil (duplicate). A canceled context must surface as an error.
	client := emulatorClient(t)
	d := NewDedup(client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	leaseID, err := d.Reserve(ctx, fmt.Sprintf("dead-%d", time.Now().UnixNano()), time.Hour)
	if leaseID != "" {
		t.Fatal("Reserve on canceled context returned a token")
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
	if err != nil || a == "" {
		t.Fatalf("Reserve a = %q, %v; want token, nil", a, err)
	}
	b, err := d.Reserve(ctx, fmt.Sprintf("b-%d", time.Now().UnixNano()), time.Hour)
	if err != nil || b == "" {
		t.Fatalf("Reserve b = %q, %v; want token, nil", b, err)
	}
}
