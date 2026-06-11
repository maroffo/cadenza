// ABOUTME: HTTP surface of the single Cloud Run service: mux and route registration.
// ABOUTME: Routes register here and only here; /health stays dependency-free by design.

package server

import "net/http"

// Deps carries the route handlers. Nil handlers leave their route
// unregistered, so a dev binary can boot with only /health.
type Deps struct {
	Executor http.Handler // POST /internal/execute (M2)
	Webhook  http.Handler // POST /telegram/webhook (M3)
}

// New builds the service mux; returned concrete so the composition root can
// mount optional surfaces (dashboard) without type assertions.
func New(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)
	if deps.Executor != nil {
		mux.Handle("POST /internal/execute", deps.Executor)
	}
	if deps.Webhook != nil {
		mux.Handle("POST /telegram/webhook", deps.Webhook)
	}
	return mux
}
