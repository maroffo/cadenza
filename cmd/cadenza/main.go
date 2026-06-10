// ABOUTME: Service entrypoint: load config, wire dependencies, serve with graceful shutdown.
// ABOUTME: --job morning|watchdog runs one job in-process and exits: the GCP-free dev loop.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"

	"github.com/maroffo/cadenza/internal/config"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/server"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
)

func main() {
	runOnce := flag.String("job", "", "run one job in-process and exit: morning|watchdog")
	flag.Parse()

	if err := run(context.Background(), *runOnce); err != nil {
		slog.Error("cadenza", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, runOnce string) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.Env == "prod" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	deps, err := buildJobs(ctx, cfg)
	if err != nil {
		return err
	}

	if runOnce != "" {
		local := task.Local{Dispatch: deps.Dispatch}
		today := time.Now().In(deps.Morning.TZ).Format("2006-01-02")
		envelope := task.Envelope{V: task.EnvelopeVersion, ID: runOnce + "-" + today}
		switch runOnce {
		case "morning":
			envelope.Type = task.TypeMorningCheck
		case "watchdog":
			envelope.Type = task.TypeWatchdog
		default:
			return fmt.Errorf("unknown --job %q (morning|watchdog)", runOnce)
		}
		return local.Enqueue(ctx, envelope)
	}

	executor := &server.Executor{
		Validator:    server.GoogleValidator{},
		Audience:     cfg.ExecutorAudience,
		InvokerEmail: cfg.InvokerEmail,
		Dispatch:     deps.Dispatch,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.New(server.Deps{Executor: executor}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Bounds slow readers without killing legitimate executor runs,
		// which hold the response open for the whole morning job.
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("cadenza listening", "port", cfg.Port, "env", cfg.Env)

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		// Cloud Run sends SIGTERM before instance shutdown; drain in-flight work.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			slog.Error("shutdown", "err", err)
		}
		slog.Info("cadenza stopped")
		return nil
	}
}

func buildJobs(ctx context.Context, cfg *config.Config) (job.Deps, error) {
	tz, err := time.LoadLocation(cfg.AthleteTZ)
	if err != nil {
		return job.Deps{}, fmt.Errorf("ATHLETE_TZ: %w", err)
	}

	fsClient, err := store.NewClient(ctx, cfg.GCPProject)
	if err != nil {
		return job.Deps{}, fmt.Errorf("firestore: %w", err)
	}

	// Burst floor of 1: a configured rate in (0,1) would otherwise truncate
	// to burst 0, which blocks every request forever.
	icuClient := icu.New(icu.DefaultBaseURL, cfg.ICUAPIKey, cfg.ICUAthleteID,
		icu.WithRateLimit(cfg.ICURatePerSec, max(1, int(cfg.ICURatePerSec))))

	tgBot, err := bot.New(cfg.TelegramBotToken, bot.WithSkipGetMe())
	if err != nil {
		return job.Deps{}, fmt.Errorf("telegram bot: %w", err)
	}
	sender := telegram.NewSender(tgBot, cfg.TelegramChatID)

	runs := store.NewRuns(fsClient)
	morning := job.Morning{
		Wellness: job.ICU{C: icuClient},
		Profiles: store.NewProfiles(fsClient),
		Out:      sender,
		Runs:     runs,
		Now:      time.Now,
		TZ:       tz,
	}
	watchdog := job.Watchdog{Runs: runs, Out: sender, Now: time.Now, TZ: tz}
	return job.Deps{Morning: morning, Watchdog: watchdog}, nil
}
