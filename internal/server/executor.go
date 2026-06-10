// ABOUTME: The single executor endpoint: in-app OIDC validation + task-type dispatch.
// ABOUTME: Called by Cloud Tasks and Cloud Scheduler; the service is public, so auth lives here.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/maroffo/cadenza/internal/task"
)

// TokenValidator verifies a bearer token for an audience and returns the
// authenticated principal's email. Prod wires Google's idtoken validator;
// tests stub it.
type TokenValidator interface {
	Validate(ctx context.Context, token, audience string) (email string, err error)
}

type Executor struct {
	Validator    TokenValidator
	Audience     string // the service URL, no query params
	InvokerEmail string // cadenza-invoker@... service account
	Dispatch     task.Dispatcher
}

// maxEnvelopeBytes bounds the request body; envelopes are small by design.
const maxEnvelopeBytes = 1 << 20

func (e *Executor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Fail closed: idtoken.Validate skips the audience check entirely when
	// audience is empty, so a misconfigured deploy must refuse to serve
	// rather than silently accept tokens minted for any audience.
	if e.Audience == "" || e.InvokerEmail == "" {
		slog.Error("executor: refusing to serve without audience/invoker configuration")
		http.Error(w, "executor not configured", http.StatusServiceUnavailable)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	email, err := e.Validator.Validate(r.Context(), token, e.Audience)
	if err != nil {
		slog.Warn("executor: token rejected", "err", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if email != e.InvokerEmail {
		slog.Warn("executor: wrong principal", "email", email)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var env task.Envelope
	body := http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		// Poison message: retrying cannot fix a malformed body. Log loudly,
		// return 200 so Cloud Tasks does not burn its attempts on it.
		slog.Error("executor: malformed envelope dropped", "err", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := env.Validate(); err != nil {
		slog.Error("executor: invalid envelope dropped", "err", err, "type", env.Type, "id", env.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := e.Dispatch(r.Context(), env); err != nil {
		if errors.Is(err, task.ErrPoison) {
			// Permanent failure: drop with 200 so retries are not burned.
			slog.Error("executor: poison task dropped", "type", env.Type, "id", env.ID, "err", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Transient failure: non-2xx makes Cloud Tasks retry within its
		// bounded policy (max-attempts caps the blast radius).
		slog.Error("executor: dispatch failed", "type", env.Type, "id", env.ID, "err", err)
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
		return
	}
	slog.Info("executor: done", "type", env.Type, "id", env.ID)
	w.WriteHeader(http.StatusOK)
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) || len(h) == len(prefix) {
		return "", false
	}
	return strings.TrimPrefix(h, prefix), true
}
