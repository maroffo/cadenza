// ABOUTME: HTTP surface of the single Cloud Run service: mux and route registration.
// ABOUTME: Routes register here and only here; /healthz stays dependency-free by design.

package server

import "net/http"

// Deps carries the route handlers. Nil handlers leave their route
// unregistered, so a dev binary can boot with only /healthz.
type Deps struct {
	Executor http.Handler // POST /internal/execute (M2)
	Webhook  http.Handler // POST /telegram/webhook (M3)
}

// New builds the service handler.
func New(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	if deps.Executor != nil {
		mux.Handle("POST /internal/execute", deps.Executor)
	}
	if deps.Webhook != nil {
		mux.Handle("POST /telegram/webhook", deps.Webhook)
	}
	return mux
}
