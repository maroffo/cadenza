// ABOUTME: Env-based configuration with fail-fast validation; getenv injected for tests.
// ABOUTME: Grows per milestone; only fields the current code actually reads belong here.

package config

import (
	"fmt"
	"strconv"
)

type Config struct {
	Port       string
	Env        string // "dev" or "prod"
	GCPProject string

	// intervals.icu fields are validated here but wired to the client in M2.
	ICUAPIKey     string
	ICUAthleteID  string  // "0" = the athlete owning the API key (intervals.icu convention)
	ICURatePerSec float64 // overrides the client default only when ICU_RATE_PER_SEC is set
	AthleteTZ     string
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

	if cfg.Env != "dev" && cfg.Env != "prod" {
		return nil, fmt.Errorf("ENV must be dev or prod, got %q", cfg.Env)
	}
	if cfg.Env == "prod" {
		if cfg.GCPProject == "" {
			return nil, fmt.Errorf("GCP_PROJECT is required in prod")
		}
		if cfg.ICUAPIKey == "" {
			return nil, fmt.Errorf("ICU_API_KEY is required in prod")
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
