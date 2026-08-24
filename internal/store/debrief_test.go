// ABOUTME: Emulator tests for fenced lease-backed debrief delivery claims.
// ABOUTME: Failed sends release immediately; crashes recover after expiry; stale owners cannot mutate replacement claims.

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDebriefs_ClaimReleaseCompleteLifecycle(t *testing.T) {
	client := emulatorClient(t)
	d := NewDebriefs(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("act-lease-%d", time.Now().UnixNano())

	first, err := d.Claim(ctx, key)
	if err != nil || first == "" {
		t.Fatalf("first Claim = %q,%v", first, err)
	}
	if token, err := d.Claim(ctx, key); err != nil || token != "" {
		t.Fatalf("live Claim = %q,%v; want empty,nil", token, err)
	}
	if err := d.Release(ctx, key, first); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := d.Claim(ctx, key)
	if err != nil || second == "" {
		t.Fatalf("Claim after release = %q,%v", second, err)
	}
	if err := d.Complete(ctx, key, second); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := d.Release(ctx, key, second); err != nil {
		t.Fatalf("late Release: %v", err)
	}
	if token, err := d.Claim(ctx, key); err != nil || token != "" {
		t.Fatalf("completed Claim = %q,%v; want empty,nil", token, err)
	}
}

func TestDebriefs_ExpiredClaimIsReclaimedAndStaleOwnerIsFenced(t *testing.T) {
	client := emulatorClient(t)
	d := NewDebriefs(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := fmt.Sprintf("act-expired-%d", time.Now().UnixNano())
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	first, err := d.Claim(ctx, key)
	if err != nil || first == "" {
		t.Fatalf("first Claim = %q,%v", first, err)
	}
	d.now = func() time.Time { return base.Add(debriefLease + time.Second) }
	second, err := d.Claim(ctx, key)
	if err != nil || second == "" || second == first {
		t.Fatalf("expired Claim = %q,%v; want fresh token", second, err)
	}
	if err := d.Release(ctx, key, first); err != nil {
		t.Fatalf("stale Release: %v", err)
	}
	if token, err := d.Claim(ctx, key); err != nil || token != "" {
		t.Fatalf("stale release erased replacement: Claim=%q,%v", token, err)
	}
	if err := d.Complete(ctx, key, first); !errors.Is(err, ErrDebriefLeaseLost) {
		t.Fatalf("stale Complete = %v, want ErrDebriefLeaseLost", err)
	}
	if err := d.Complete(ctx, key, second); err != nil {
		t.Fatalf("replacement Complete: %v", err)
	}
}
