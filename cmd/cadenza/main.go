// ABOUTME: Service entrypoint: load config, build the handler, serve with graceful shutdown.
// ABOUTME: Wiring only; all behavior lives in internal packages.

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maroffo/cadenza/internal/config"
	"github.com/maroffo/cadenza/internal/server"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.Env == "prod" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.New(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("cadenza listening", "port", cfg.Port, "env", cfg.Env)

	select {
	case err := <-errCh:
		slog.Error("server", "err", err)
		os.Exit(1)
	case <-ctx.Done():
		// Cloud Run sends SIGTERM before instance shutdown; drain in-flight work.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			slog.Error("shutdown", "err", err)
		}
		slog.Info("cadenza stopped")
	}
}
