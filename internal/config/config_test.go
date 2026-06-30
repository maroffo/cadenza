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
	if cfg.ModelCheap != "claude-haiku-4-5-20251001" {
		t.Errorf("ModelCheap default = %q", cfg.ModelCheap)
	}
}

func TestLoad_ModelCheapOverride(t *testing.T) {
	cfg, err := Load(env(map[string]string{"MODEL_CHEAP": "claude-haiku-9"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ModelCheap != "claude-haiku-9" {
		t.Errorf("ModelCheap = %q, want override", cfg.ModelCheap)
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

func TestLoad_DefaultEquipment(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"trims and splits", "dumbbell, band ,kettlebell", []string{"dumbbell", "band", "kettlebell"}},
		{"unset is nil", "", nil},
		{"only separators and blanks is nil", "  ,  ,", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			if tc.raw != "" {
				m["CADENZA_DEFAULT_EQUIPMENT"] = tc.raw
			}
			cfg, err := Load(env(m))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !equalStrings(cfg.DefaultEquipment, tc.want) {
				t.Errorf("DefaultEquipment = %#v, want %#v", cfg.DefaultEquipment, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func TestNormalizeAllergens(t *testing.T) {
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil keeps lactose baseline", nil, []string{"lactose"}},
		{"italian lattosio canonicalizes", []string{"lattosio"}, []string{"lactose"}},
		{"italian synonyms + baseline, sorted", []string{"glutine", "Soia"}, []string{"gluten", "lactose", "soy"}},
		{"unknown token kept, baseline still present", []string{"sconosciuto"}, []string{"lactose", "sconosciuto"}},
		{"lactose cannot be dropped", []string{"gluten"}, []string{"gluten", "lactose"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeAllergens(c.in); !eq(got, c.want) {
				t.Errorf("normalizeAllergens(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
