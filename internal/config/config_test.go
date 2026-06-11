// ABOUTME: Tests for env-based configuration: defaults, validation, fail-fast in prod.
// ABOUTME: getenv is injected so tests never touch the real environment.

package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_DevDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.ICUAthleteID != "0" {
		t.Errorf("ICUAthleteID = %q, want 0", cfg.ICUAthleteID)
	}
	if cfg.ICURatePerSec != 3 {
		t.Errorf("ICURatePerSec = %v, want 3", cfg.ICURatePerSec)
	}
	if cfg.AthleteTZ != "Europe/Rome" {
		t.Errorf("AthleteTZ = %q, want Europe/Rome", cfg.AthleteTZ)
	}
}

func TestLoad_ExplicitOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PORT":             "9000",
		"ATHLETE_TZ":       "Europe/Madrid",
		"ICU_ATHLETE_ID":   "i12345",
		"ICU_RATE_PER_SEC": "5",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9000" {
		t.Errorf("Port = %q, want 9000", cfg.Port)
	}
	if cfg.AthleteTZ != "Europe/Madrid" {
		t.Errorf("AthleteTZ = %q, want Europe/Madrid", cfg.AthleteTZ)
	}
	if cfg.ICUAthleteID != "i12345" {
		t.Errorf("ICUAthleteID = %q, want i12345", cfg.ICUAthleteID)
	}
	if cfg.ICURatePerSec != 5 {
		t.Errorf("ICURatePerSec = %v, want 5", cfg.ICURatePerSec)
	}
}

func TestLoad_InvalidEnvRejected(t *testing.T) {
	_, err := Load(env(map[string]string{"ENV": "staging"}))
	if err == nil || !strings.Contains(err.Error(), "ENV") {
		t.Fatalf("err = %v, want ENV validation error", err)
	}
}

func completeProdEnv() map[string]string {
	return map[string]string{
		"ENV":                     "prod",
		"GCP_PROJECT":             "p",
		"ICU_API_KEY":             "k",
		"TELEGRAM_BOT_TOKEN":      "t",
		"TELEGRAM_CHAT_ID":        "424242",
		"TELEGRAM_WEBHOOK_SECRET": "s",
		"EXECUTOR_AUDIENCE":       "https://cadenza.example.run.app",
		"INVOKER_EMAIL":           "cadenza-invoker@p.iam.gserviceaccount.com",
		"TASKS_QUEUE_PATH":        "projects/p/locations/europe-west1/queues/cadenza-exec",
		"ANTHROPIC_API_KEY":       "a",
	}
}

func TestLoad_ProdRequirements(t *testing.T) {
	for _, missing := range []string{
		"GCP_PROJECT", "ICU_API_KEY", "TELEGRAM_BOT_TOKEN",
		"TELEGRAM_CHAT_ID", "TELEGRAM_WEBHOOK_SECRET",
		"EXECUTOR_AUDIENCE", "INVOKER_EMAIL", "TASKS_QUEUE_PATH",
		"ANTHROPIC_API_KEY",
	} {
		t.Run("missing "+missing+" rejected", func(t *testing.T) {
			m := completeProdEnv()
			delete(m, missing)
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("err = %v, want %s required error", err, missing)
			}
		})
	}
	t.Run("complete prod config accepted", func(t *testing.T) {
		cfg, err := Load(env(completeProdEnv()))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.GCPProject != "p" || cfg.ICUAPIKey != "k" {
			t.Errorf("cfg = %+v, want project p and key k", cfg)
		}
		if cfg.TelegramChatID != 424242 {
			t.Errorf("TelegramChatID = %d, want 424242", cfg.TelegramChatID)
		}
	})
	t.Run("bad chat id rejected", func(t *testing.T) {
		m := completeProdEnv()
		m["TELEGRAM_CHAT_ID"] = "not-a-number"
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "TELEGRAM_CHAT_ID") {
			t.Fatalf("err = %v, want TELEGRAM_CHAT_ID parse error", err)
		}
	})
}

func TestLoad_BadRateRejected(t *testing.T) {
	for _, raw := range []string{"fast", "0", "-1"} {
		t.Run(raw, func(t *testing.T) {
			_, err := Load(env(map[string]string{"ICU_RATE_PER_SEC": raw}))
			if err == nil || !strings.Contains(err.Error(), "ICU_RATE_PER_SEC") {
				t.Fatalf("err = %v, want ICU_RATE_PER_SEC rejection for %q", err, raw)
			}
		})
	}
}
