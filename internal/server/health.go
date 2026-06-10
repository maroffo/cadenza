// ABOUTME: Health endpoint: static 200, zero dependency checks by design.
// ABOUTME: Must succeed during cold start, so it touches nothing.

package server

import "net/http"

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
