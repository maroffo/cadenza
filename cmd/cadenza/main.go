// ABOUTME: Service entrypoint: load config, wire dependencies, serve with graceful shutdown.
// ABOUTME: --job runs one job in-process; --poll long-polls Telegram: the GCP-free dev loops.

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

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
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
	poll := flag.Bool("poll", false, "dev mode: long-poll Telegram instead of serving the webhook")
	flag.Parse()

	if err := run(context.Background(), *runOnce, *poll); err != nil {
		slog.Error("cadenza", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, runOnce string, poll bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.Env == "prod" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	// Local dispatch closes over deps, assigned below: dispatch only runs at
	// request time, after wiring completes.
	var deps job.Deps
	local := task.Local{Dispatch: func(ctx context.Context, e task.Envelope) error {
		return deps.Dispatch(ctx, e)
	}}

	// Durable path in prod (webhook enqueue + HRV retry); in-process in dev.
	var enq task.Enqueuer = local
	var retry task.DelayedEnqueuer = local
	if cfg.Env == "prod" {
		tasksClient, err := cloudtasks.NewClient(ctx)
		if err != nil {
			return fmt.Errorf("cloudtasks client: %w", err)
		}
		defer func() { _ = tasksClient.Close() }()
		ct := &task.CloudTasks{
			Client:    tasksClient,
			QueuePath: cfg.TasksQueuePath,
			TargetURL: cfg.ExecutorAudience + "/internal/execute",
			Audience:  cfg.ExecutorAudience,
			InvokerSA: cfg.InvokerEmail,
		}
		enq = ct
		retry = ct
	}

	deps, err = buildJobs(ctx, cfg, retry)
	if err != nil {
		return err
	}

	if runOnce != "" {
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

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if poll {
		if cfg.Env == "prod" {
			return fmt.Errorf("--poll is dev-only; prod uses the webhook")
		}
		return telegram.Poll(ctx, cfg.TelegramBotToken, local)
	}

	executor := &server.Executor{
		Validator:    server.GoogleValidator{},
		Audience:     cfg.ExecutorAudience,
		InvokerEmail: cfg.InvokerEmail,
		Dispatch:     deps.Dispatch,
	}
	webhook := &server.Webhook{
		Secret:        cfg.TelegramWebhookSecret,
		AllowedUserID: cfg.TelegramChatID,
		Enqueue:       enq,
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.New(server.Deps{Executor: executor, Webhook: webhook}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Bounds slow readers without killing legitimate executor runs,
		// which hold the response open for the whole morning job.
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

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

func buildJobs(ctx context.Context, cfg *config.Config, retry task.DelayedEnqueuer) (job.Deps, error) {
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
		Retry:    retry,
		Now:      time.Now,
		TZ:       tz,
	}
	watchdog := job.Watchdog{Runs: runs, Out: sender, Now: time.Now, TZ: tz}
	message := job.Message{
		AllowedUserID: cfg.TelegramChatID,
		Dedup:         store.NewDedup(fsClient),
		Chats:         store.NewChats(fsClient),
		Out:           sender,
		Status:        morning,
	}
	return job.Deps{Morning: morning, Watchdog: watchdog, Message: message}, nil
}
