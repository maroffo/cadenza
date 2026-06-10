// ABOUTME: End-to-end boot test: the real handler serves /healthz over real HTTP.
// ABOUTME: Grows into full scenario tests (emulator + fakes) from M2 onward.

//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maroffo/cadenza/internal/server"
)

func TestBoot_HealthzOverHTTP(t *testing.T) {
	ts := httptest.NewServer(server.New(server.Deps{}))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}
