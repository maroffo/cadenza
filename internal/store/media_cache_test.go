// ABOUTME: Tests for the exercise media cache: id validation (unit) and round-trip (emulator).
// ABOUTME: Emulator cases skip unless FIRESTORE_EMULATOR_HOST is set; REQUIRE_EMULATOR=1 makes skips fatal.

package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Validation failures must not touch the network, so these run without emulator.
func TestMediaCacheGet_RejectsInvalidKeys(t *testing.T) {
	m := NewMediaCache(nil) // client must never be reached
	ctx := context.Background()
	for _, id := range []string{
		"",                             // empty
		"a/b",                          // path separator would change the document path
		"id with spaces",               // not in allowlist
		".dotfirst",                    // must start alphanumeric
		"x" + strings.Repeat("a", 200), // over length cap (201 chars)
	} {
		fileID, ok, err := m.Get(ctx, id)
		if err == nil || ok || fileID != "" {
			t.Errorf("Get(%q) = %q, %v, %v; want \"\", false, validation error", id, fileID, ok, err)
		}
	}
}

func TestMediaCacheSet_RejectsInvalidKeys(t *testing.T) {
	m := NewMediaCache(nil)
	ctx := context.Background()
	for _, id := range []string{"", "a/b", ".dotfirst"} {
		if err := m.Set(ctx, id, "FILE123"); err == nil {
			t.Errorf("Set(%q) = nil; want validation error", id)
		}
	}
}

func TestMediaCacheSet_RejectsEmptyFileID(t *testing.T) {
	m := NewMediaCache(nil) // empty file_id rejected before any network call
	ctx := context.Background()
	if err := m.Set(ctx, "squat", ""); err == nil {
		t.Error("Set with empty file_id = nil; want error")
	}
}

func TestMediaCacheGet_MissingKeyIsNoError(t *testing.T) {
	client := emulatorClient(t)
	m := NewMediaCache(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fileID, ok, err := m.Get(ctx, fmt.Sprintf("missing-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("Get on missing key: %v", err)
	}
	if ok {
		t.Fatal("Get on missing key = true, want false")
	}
	if fileID != "" {
		t.Errorf("Get on missing key fileID = %q, want empty", fileID)
	}
}

func TestMediaCacheSetGet_RoundTrips(t *testing.T) {
	client := emulatorClient(t)
	m := NewMediaCache(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := fmt.Sprintf("ex-%d", time.Now().UnixNano())

	if err := m.Set(ctx, id, "CACHED123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fileID, ok, err := m.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !ok {
		t.Fatal("Get after Set = false, want true")
	}
	if fileID != "CACHED123" {
		t.Errorf("Get after Set fileID = %q, want CACHED123", fileID)
	}
}

func TestMediaCacheSet_Overwrites(t *testing.T) {
	// A re-uploaded GIF gets a fresh file_id; the latest write must win so
	// callers never re-send a stale (possibly expired) reference.
	client := emulatorClient(t)
	m := NewMediaCache(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := fmt.Sprintf("ex-overwrite-%d", time.Now().UnixNano())

	if err := m.Set(ctx, id, "OLD"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := m.Set(ctx, id, "NEW"); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	fileID, ok, err := m.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get after overwrite = %q, %v, %v; want NEW, true, nil", fileID, ok, err)
	}
	if fileID != "NEW" {
		t.Errorf("Get after overwrite fileID = %q, want NEW", fileID)
	}
}
