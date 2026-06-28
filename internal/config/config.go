// ABOUTME: Env-based configuration with fail-fast validation; getenv injected for tests.
// ABOUTME: Grows per milestone; only fields the current code actually reads belong here.

package config

import (
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	Port       string
	Env        string // "dev" or "prod"
	GCPProject string

	ICUAPIKey     string
	ICUAthleteID  string  // "0" = the athlete owning the API key (intervals.icu convention)
	ICURatePerSec float64 // overrides the client default only when ICU_RATE_PER_SEC is set
	AthleteTZ     string

	TelegramBotToken      string
	TelegramChatID        int64  // also the allowlisted user id (private chat: equal)
	TelegramWebhookSecret string // X-Telegram-Bot-Api-Secret-Token

	// Executor OIDC: audience is the service URL without query params,
	// invoker is the cadenza-invoker@ service account email.
	ExecutorAudience string
	InvokerEmail     string

	// Cloud Tasks queue path: projects/<p>/locations/<l>/queues/<q>.
	TasksQueuePath string

	// Anthropic: key required in prod from M4; base URL overridable for
	// tests and e2e (empty = real API).
	AnthropicAPIKey  string
	AnthropicBaseURL string
	ModelCheap       string
	ModelDeep        string

	// WebSessionSecret signs dashboard magic links and cookies (M8).
	// Empty = dashboard disabled.
	WebSessionSecret string

	// DefaultEquipment is the athlete's standing home kit (decision 9), surfaced
	// to the coach so it need not be restated every conversation. Empty = full
	// kit assumed. Per-day conversational overrides still take precedence.
	DefaultEquipment []string
}

// Load reads configuration via getenv (os.Getenv in main, a map in tests).
// Dev boots with no env at all; prod fails fast on anything missing.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Port:          orDefault(getenv("PORT"), "8080"),
		Env:           orDefault(getenv("ENV"), "dev"),
		GCPProject:    getenv("GCP_PROJECT"),
		ICUAPIKey:     getenv("ICU_API_KEY"),
		ICUAthleteID:  orDefault(getenv("ICU_ATHLETE_ID"), "0"),
		ICURatePerSec: 3,
		AthleteTZ:     orDefault(getenv("ATHLETE_TZ"), "Europe/Rome"),
	}

	if raw := getenv("ICU_RATE_PER_SEC"); raw != "" {
		rate, err := strconv.ParseFloat(raw, 64)
		if err != nil || rate <= 0 {
			return nil, fmt.Errorf("ICU_RATE_PER_SEC: invalid value %q", raw)
		}
		cfg.ICURatePerSec = rate
	}

	if raw := getenv("TELEGRAM_CHAT_ID"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID: invalid value %q", raw)
		}
		cfg.TelegramChatID = id
	}
	cfg.TelegramBotToken = getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramWebhookSecret = getenv("TELEGRAM_WEBHOOK_SECRET")
	cfg.ExecutorAudience = getenv("EXECUTOR_AUDIENCE")
	cfg.InvokerEmail = getenv("INVOKER_EMAIL")
	cfg.TasksQueuePath = getenv("TASKS_QUEUE_PATH")
	cfg.AnthropicAPIKey = getenv("ANTHROPIC_API_KEY")
	cfg.AnthropicBaseURL = getenv("ANTHROPIC_BASE_URL")
	cfg.ModelCheap = orDefault(getenv("MODEL_CHEAP"), "claude-haiku-4-5-20251001")
	cfg.ModelDeep = orDefault(getenv("MODEL_DEEP"), "claude-opus-4-8")
	cfg.WebSessionSecret = getenv("WEB_SESSION_SECRET")
	cfg.DefaultEquipment = splitCSV(getenv("CADENZA_DEFAULT_EQUIPMENT"))

	if cfg.Env != "dev" && cfg.Env != "prod" {
		return nil, fmt.Errorf("ENV must be dev or prod, got %q", cfg.Env)
	}
	if cfg.Env == "prod" {
		required := map[string]string{
			"GCP_PROJECT":             cfg.GCPProject,
			"ICU_API_KEY":             cfg.ICUAPIKey,
			"TELEGRAM_BOT_TOKEN":      cfg.TelegramBotToken,
			"TELEGRAM_WEBHOOK_SECRET": cfg.TelegramWebhookSecret,
			"EXECUTOR_AUDIENCE":       cfg.ExecutorAudience,
			"INVOKER_EMAIL":           cfg.InvokerEmail,
			"TASKS_QUEUE_PATH":        cfg.TasksQueuePath,
			"ANTHROPIC_API_KEY":       cfg.AnthropicAPIKey,
		}
		for name, v := range required {
			if v == "" {
				return nil, fmt.Errorf("%s is required in prod", name)
			}
		}
		if cfg.TelegramChatID == 0 {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID is required in prod")
		}
	}
	return cfg, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// splitCSV parses a comma-separated env value into a trimmed slice, dropping
// empty entries. Empty or all-blank input yields nil (field stays unset).
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
