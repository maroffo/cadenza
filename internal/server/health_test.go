// ABOUTME: Tests for the health endpoint: static 200 with no dependency checks.
// ABOUTME: Must stay dependency-free so it passes during cold start.

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	h := New(Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h := New(Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthWrongMethodIs405(t *testing.T) {
	// Pins the method-scoped route contract before POST routes arrive (M2/M3).
	h := New(Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
