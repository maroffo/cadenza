// ABOUTME: Tests for the executor: OIDC gate, principal check, poison drops, retryable failures.
// ABOUTME: Validator is stubbed; no Google dependency in tests.

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/task"
)

type stubValidator struct {
	email string
	err   error
	// captured
	gotToken    string
	gotAudience string
}

func (s *stubValidator) Validate(_ context.Context, token, audience string) (string, error) {
	s.gotToken = token
	s.gotAudience = audience
	return s.email, s.err
}

func newExecutor(v TokenValidator, dispatch task.Dispatcher) *Executor {
	return &Executor{
		Validator:    v,
		Audience:     "https://cadenza.example.run.app",
		InvokerEmail: "cadenza-invoker@proj.iam.gserviceaccount.com",
		Dispatch:     dispatch,
	}
}

func post(t *testing.T, e *Executor, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/execute", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"v":1,"type":"morning_check","id":"morning-2026-06-10"}`

func TestExecutor_NoTokenIs401(t *testing.T) {
	e := newExecutor(&stubValidator{}, nil)
	if rec := post(t, e, "", validBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestExecutor_BadTokenIs401(t *testing.T) {
	e := newExecutor(&stubValidator{err: errors.New("expired")}, nil)
	if rec := post(t, e, "Bearer bad", validBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestExecutor_WrongPrincipalIs403(t *testing.T) {
	e := newExecutor(&stubValidator{email: "evil@example.com"}, nil)
	if rec := post(t, e, "Bearer ok", validBody); rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestExecutor_ValidRequestDispatches(t *testing.T) {
	v := &stubValidator{email: "cadenza-invoker@proj.iam.gserviceaccount.com"}
	var got task.Envelope
	e := newExecutor(v, func(_ context.Context, env task.Envelope) error {
		got = env
		return nil
	})
	rec := post(t, e, "Bearer tok", validBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got.ID != "morning-2026-06-10" || got.Type != task.TypeMorningCheck {
		t.Errorf("dispatched %+v", got)
	}
	if v.gotToken != "tok" || v.gotAudience != "https://cadenza.example.run.app" {
		t.Errorf("validator got token=%q audience=%q", v.gotToken, v.gotAudience)
	}
}

func TestExecutor_PoisonBodiesDroppedWith200(t *testing.T) {
	v := &stubValidator{email: "cadenza-invoker@proj.iam.gserviceaccount.com"}
	called := false
	e := newExecutor(v, func(context.Context, task.Envelope) error {
		called = true
		return nil
	})
	for name, body := range map[string]string{
		"malformed json":   `{not json`,
		"invalid envelope": `{"v":1,"type":"","id":""}`,
		"wrong version":    `{"v":99,"type":"morning_check","id":"x"}`,
	} {
		rec := post(t, e, "Bearer tok", body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200 (poison drop, no retry burn)", name, rec.Code)
		}
	}
	if called {
		t.Error("dispatch called for poison body")
	}
}

func TestExecutor_DispatchErrorIs500ForRetry(t *testing.T) {
	v := &stubValidator{email: "cadenza-invoker@proj.iam.gserviceaccount.com"}
	e := newExecutor(v, func(context.Context, task.Envelope) error {
		return errors.New("firestore unavailable")
	})
	if rec := post(t, e, "Bearer tok", validBody); rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 (Cloud Tasks must retry)", rec.Code)
	}
}
