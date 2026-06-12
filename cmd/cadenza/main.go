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

	"github.com/maroffo/cadenza/internal/agent"
	"github.com/maroffo/cadenza/internal/config"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/icuwrite"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/server"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/task"
	"github.com/maroffo/cadenza/internal/telegram"
	"github.com/maroffo/cadenza/internal/web"
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

	deps, webServer, err := buildJobs(ctx, cfg, retry)
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

	rootMux := server.New(server.Deps{Executor: executor, Webhook: webhook})
	if webServer != nil {
		webServer.Register(rootMux)
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           rootMux,
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

// webHistory adapts the chat page to the chats+sessions stores.
type webHistory struct {
	chats    *store.Chats
	sessions *store.Sessions
}

func (h webHistory) ActiveTurns(ctx context.Context, limit int) ([]store.Turn, error) {
	return store.ActiveTurns(ctx, h.chats, h.sessions, limit)
}

// webAudit aggregates the transparency sources for the audit page.
type webAudit struct {
	ledger *store.Ledger
	muts   *store.Mutations
	budget *store.Budget
}

func (a webAudit) RecentWrites(ctx context.Context, limit int) ([]store.WriteRecord, error) {
	return a.ledger.RecentWrites(ctx, limit)
}
func (a webAudit) RecentMutations(ctx context.Context, limit int) ([]store.MutationWithID, error) {
	return a.muts.RecentMutations(ctx, limit)
}
func (a webAudit) SpentToday(ctx context.Context, date string) (int, error) {
	return a.budget.SpentToday(ctx, date)
}
func (a webAudit) RecordWebChange(ctx context.Context, kind, oldValue, newValue string) error {
	return a.muts.RecordWebChange(ctx, kind, oldValue, newValue)
}

func buildJobs(ctx context.Context, cfg *config.Config, retry task.DelayedEnqueuer) (job.Deps, *web.Server, error) {
	tz, err := time.LoadLocation(cfg.AthleteTZ)
	if err != nil {
		return job.Deps{}, nil, fmt.Errorf("ATHLETE_TZ: %w", err)
	}

	fsClient, err := store.NewClient(ctx, cfg.GCPProject)
	if err != nil {
		return job.Deps{}, nil, fmt.Errorf("firestore: %w", err)
	}

	// Burst floor of 1: a configured rate in (0,1) would otherwise truncate
	// to burst 0, which blocks every request forever.
	icuClient := icu.New(icu.DefaultBaseURL, cfg.ICUAPIKey, cfg.ICUAthleteID,
		icu.WithRateLimit(cfg.ICURatePerSec, max(1, int(cfg.ICURatePerSec))))

	tgBot, err := bot.New(cfg.TelegramBotToken, bot.WithSkipGetMe())
	if err != nil {
		return job.Deps{}, nil, fmt.Errorf("telegram bot: %w", err)
	}
	sender := telegram.NewSender(tgBot, cfg.TelegramChatID)

	runs := store.NewRuns(fsClient)
	injuries := store.NewInjuries(fsClient)
	injuryJob := job.InjuryJob{
		Injuries: injuries, Out: sender, Keyboard: sender, Retry: retry,
		Now: time.Now, TZ: tz,
	}
	debrief := job.Debrief{
		Activities: job.ICU{C: icuClient},
		Events:     job.ICU{C: icuClient},
		Plans:      store.NewLedger(fsClient),
		Marks:      store.NewDebriefs(fsClient),
		Out:        sender,
		Now:        time.Now,
		TZ:         tz,
	}
	morning := job.Morning{
		Wellness: job.ICU{C: icuClient},
		Profiles: store.NewProfiles(fsClient),
		Out:      sender,
		Runs:     runs,
		Injuries: injuries,
		Events:   job.ICU{C: icuClient},
		Checkins: store.NewCheckins(fsClient),
		Keyboard: sender,
		Debrief:  &debrief,
		Retry:    retry,
		Now:      time.Now,
		TZ:       tz,
	}
	message := job.Message{
		AllowedUserID: cfg.TelegramChatID,
		Dedup:         store.NewDedup(fsClient),
		Chats:         store.NewChats(fsClient),
		Out:           sender,
		Status:        morning,
	}
	// M4/M5: the coach voice. No key (dev without LLM) = skeleton mode.
	if cfg.AnthropicAPIKey != "" {
		llm := agent.NewClient(cfg.AnthropicAPIKey, cfg.AnthropicBaseURL)
		morning.Narrator = agent.Narrator{Client: llm, Model: cfg.ModelCheap}
		debrief.Narrator = agent.Debriefer{Client: llm, Model: cfg.ModelCheap}
		morning.Sessions = store.NewSessions(fsClient)
		morning.ModelName = cfg.ModelCheap

		chats := store.NewChats(fsClient)
		message.Coach = &job.Coach{
			Agent:      agent.Coach{Client: llm, Model: cfg.ModelDeep},
			Wellness:   job.ICU{C: icuClient},
			Activities: job.ICU{C: icuClient},
			Profiles:   store.NewProfiles(fsClient),
			Rules:      store.NewRules(fsClient),
			RuleCount:  store.NewRules(fsClient),
			Muts:       store.NewMutations(fsClient),
			Budget:     store.NewBudget(fsClient),
			Sessions:   store.NewSessions(fsClient),
			Chats:      chats,
			Status:     morning,
			Out:        sender,
			Confirm:    sender,
			Writer:     &icuwrite.Writer{C: icuClient},
			Ledger:     store.NewLedger(fsClient),
			Events:     job.ICU{C: icuClient},
			Plans:      store.NewLedger(fsClient),
			Summary:    agent.Summarizer{Client: llm, Model: cfg.ModelCheap},
			Now:        time.Now,
			TZ:         tz,
		}
		message.Coach.Injuries = injuries
		message.Coach.InjurySched = injuryJob
		message.Muts = store.NewMutations(fsClient)
	}
	message.InjuryFlow = &injuryJob
	message.Checkins = store.NewCheckins(fsClient)
	message.Keyboard = sender
	// M8 dashboard: enabled when the web secret exists.
	var webServer *web.Server
	if cfg.WebSessionSecret != "" && cfg.ExecutorAudience != "" {
		// The HMAC key IS the auth boundary: refuse weak ones outright.
		if len(cfg.WebSessionSecret) < 32 {
			return job.Deps{}, nil, fmt.Errorf("WEB_SESSION_SECRET troppo corto (%d): minimo 32 byte (openssl rand -hex 32)", len(cfg.WebSessionSecret))
		}
		webAuth := web.Auth{
			Secret:   []byte(cfg.WebSessionSecret),
			Sessions: store.NewWebSessions(fsClient),
			BaseURL:  cfg.ExecutorAudience,
			Now:      time.Now,
		}
		message.WebLink = webAuth.MintLink
		webChats := store.NewChats(fsClient)
		webSessions := store.NewSessions(fsClient)
		webServer = &web.Server{
			Auth:     webAuth,
			ICU:      icuClient,
			Status:   morning,
			History:  webHistory{chats: webChats, sessions: webSessions},
			Injuries: injuries,
			Rules:    store.NewRules(fsClient),
			Profiles: store.NewProfiles(fsClient),
			Audit: webAudit{
				ledger: store.NewLedger(fsClient),
				muts:   store.NewMutations(fsClient),
				budget: store.NewBudget(fsClient),
			},
			Now: time.Now,
			TZ:  tz,
		}
		// Typed-nil trap: a nil *job.Coach in a non-nil interface panics on
		// first use. Skeleton mode (no LLM) keeps Chat nil and the handler
		// answers honestly.
		if message.Coach != nil {
			webServer.Chat = message.Coach
		}
	}
	watchdog := job.Watchdog{Runs: runs, Out: sender, Now: time.Now, TZ: tz}
	return job.Deps{Morning: morning, Watchdog: watchdog, Message: message, Injury: injuryJob, Debrief: debrief}, webServer, nil
}
