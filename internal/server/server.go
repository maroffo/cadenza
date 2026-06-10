// ABOUTME: HTTP surface of the single Cloud Run service: mux and route registration.
// ABOUTME: M1 exposes /healthz only; webhook and executor routes arrive in M2/M3.

package server

import "net/http"

// New builds the service handler. Routes are registered here and only here.
func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	return mux
}
