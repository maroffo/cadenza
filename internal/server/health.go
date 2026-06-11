// ABOUTME: Health endpoint (/health: /healthz is reserved by the run.app frontend).
// ABOUTME: Must succeed during cold start, so it touches nothing.

package server

import "net/http"

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
