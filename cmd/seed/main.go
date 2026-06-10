// ABOUTME: Profile seeder: computed baselines from intervals.icu history + YAML for the rest.
// ABOUTME: One-shot; the only writer of profile/current until M5 mutations land.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maroffo/cadenza/internal/config"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/store"
)

type seedFile struct {
	RampCap float64 `yaml:"ramp_cap"` // CTL ramp/week; clamped to Tier A (6.0) at read time
}

func main() {
	yamlPath := flag.String("f", "athlete.yaml", "athlete seed YAML")
	lookbackDays := flag.Int("lookback", 60, "wellness history window for baselines")
	dryRun := flag.Bool("dry-run", false, "compute and print, write nothing")
	flag.Parse()

	if err := run(context.Background(), *yamlPath, *lookbackDays, *dryRun); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, yamlPath string, lookbackDays int, dryRun bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.ICUAPIKey == "" {
		return fmt.Errorf("ICU_API_KEY is required to compute baselines")
	}

	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("seed file: %w", err)
	}
	var seed seedFile
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return fmt.Errorf("seed yaml: %w", err)
	}
	if seed.RampCap <= 0 {
		return fmt.Errorf("seed yaml: ramp_cap must be positive, got %v", seed.RampCap)
	}

	tz, err := time.LoadLocation(cfg.AthleteTZ)
	if err != nil {
		return fmt.Errorf("ATHLETE_TZ: %w", err)
	}
	newest := time.Now().In(tz).Format("2006-01-02")
	oldest := time.Now().In(tz).AddDate(0, 0, -lookbackDays).Format("2006-01-02")

	icuClient := icu.New("https://intervals.icu/api/v1", cfg.ICUAPIKey, cfg.ICUAthleteID)
	days, err := job.ICU{C: icuClient}.WellnessRange(ctx, oldest, newest)
	if err != nil {
		return fmt.Errorf("wellness history: %w", err)
	}

	baselines, err := job.ComputeBaselines(days)
	if err != nil {
		return err
	}
	slog.Info("computed baselines",
		"days_fetched", len(days),
		"hrv_mean", fmt.Sprintf("%.1f", baselines.HRVMean),
		"hrv_sd", fmt.Sprintf("%.1f", baselines.HRVSD),
		"resting_hr", fmt.Sprintf("%.1f", baselines.RestingHR),
		"ramp_cap", seed.RampCap,
	)
	if dryRun {
		slog.Info("dry run: nothing written")
		return nil
	}

	fsClient, err := store.NewClient(ctx, cfg.GCPProject)
	if err != nil {
		return fmt.Errorf("firestore: %w", err)
	}
	defer func() { _ = fsClient.Close() }()
	if err := store.NewProfiles(fsClient).Seed(ctx, baselines, seed.RampCap); err != nil {
		return fmt.Errorf("write profile/current: %w", err)
	}
	slog.Info("profile/current seeded")
	return nil
}
