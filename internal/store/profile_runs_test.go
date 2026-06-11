// ABOUTME: Emulator tests for the profile and runs repositories.
// ABOUTME: Skips without FIRESTORE_EMULATOR_HOST; REQUIRE_EMULATOR=1 turns skips fatal.

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maroffo/cadenza/internal/verdict"
)

func TestProfiles_SeedAndGetRoundTrip(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	want := verdict.Baselines{HRVMean: 68, HRVSD: 6, RestingHR: 47}
	if err := p.Seed(ctx, want, 4.0); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, rampCap, err := p.Profile(ctx)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if got != want {
		t.Errorf("baselines = %+v, want %+v", got, want)
	}
	if rampCap != 4.0 {
		t.Errorf("rampCap = %v, want 4.0", rampCap)
	}
}

func TestProfiles_ImplausibleBaselinesRejected(t *testing.T) {
	client := emulatorClient(t)
	p := NewProfiles(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := p.Seed(ctx, verdict.Baselines{}, 4.0); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, _, err := p.Profile(ctx); err == nil {
		t.Fatal("zero baselines accepted; coaching on invented numbers must fail loudly")
	}
}

func TestRuns_MorningLifecycle(t *testing.T) {
	client := emulatorClient(t)
	r := NewRuns(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Opaque unique id: the repo treats dates as strings, and uniqueness
	// keeps reruns against a long-lived emulator instance independent.
	date := fmt.Sprintf("2099-test-%d", time.Now().UnixNano())

	done, err := r.MorningCompleted(ctx, date)
	if err != nil {
		t.Fatalf("MorningCompleted: %v", err)
	}
	if done {
		t.Fatal("fresh date reports completed")
	}
	if err := r.MarkMorningCompleted(ctx, date, "GO"); err != nil {
		t.Fatalf("MarkMorningCompleted: %v", err)
	}
	done, err = r.MorningCompleted(ctx, date)
	if err != nil {
		t.Fatalf("MorningCompleted after mark: %v", err)
	}
	if !done {
		t.Fatal("marked date reports not completed")
	}
}

func TestChats_SaveAndGet(t *testing.T) {
	client := emulatorClient(t)
	c := NewChats(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := c.Save(ctx, 424242, 424242); err != nil {
		t.Fatalf("Save: %v", err)
	}
	chatID, userID, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if chatID != 424242 || userID != 424242 {
		t.Errorf("got chat=%d user=%d, want 424242/424242", chatID, userID)
	}
}

func TestChats_GetBeforeStartReturnsZeros(t *testing.T) {
	client := emulatorClient(t)
	c := NewChats(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Force the not-found branch even on a long-lived emulator.
	_, _ = client.Collection("state").Doc("chat").Delete(ctx)
	chatID, userID, err := c.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if chatID != 0 || userID != 0 {
		t.Errorf("got chat=%d user=%d, want zeros (/start never happened)", chatID, userID)
	}
}

func TestRuns_DeferredLifecycle(t *testing.T) {
	client := emulatorClient(t)
	r := NewRuns(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	date := fmt.Sprintf("2099-defer-%d", time.Now().UnixNano())

	if err := r.MarkMorningDeferred(ctx, date, 1); err != nil {
		t.Fatalf("MarkMorningDeferred: %v", err)
	}
	done, err := r.MorningCompleted(ctx, date)
	if err != nil || done {
		t.Fatalf("deferred reports completed=%v err=%v, want false", done, err)
	}
	alive, err := r.MorningAlive(ctx, date)
	if err != nil || !alive {
		t.Fatalf("deferred reports alive=%v err=%v, want true (watchdog quiet)", alive, err)
	}

	if err := r.MarkMorningCompleted(ctx, date, "GO"); err != nil {
		t.Fatalf("MarkMorningCompleted: %v", err)
	}
	done, err = r.MorningCompleted(ctx, date)
	if err != nil || !done {
		t.Fatalf("completed after deferral reports done=%v err=%v, want true", done, err)
	}
}
