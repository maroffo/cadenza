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
	// Identity: the slow-changing half (D23 split, M9). Week-to-week state
	// (current goal tweaks, niggles) flows through confirmed memory instead.
	Sports        []string     `yaml:"sports"`
	Races         []store.Race `yaml:"races"`
	Availability  string       `yaml:"availability"`
	InjuryHistory string       `yaml:"injury_history"`
	Preferences   string       `yaml:"preferences"`
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

	icuClient := icu.New(icu.DefaultBaseURL, cfg.ICUAPIKey, cfg.ICUAthleteID)
	days, err := job.ICU{C: icuClient}.WellnessRange(ctx, oldest, newest)
	if err != nil {
		return fmt.Errorf("wellness history: %w", err)
	}

	baselines, err := job.ComputeBaselines(days)
	if err != nil {
		return err
	}
	zones, err := fetchZones(ctx, icuClient, seed.Sports)
	if err != nil {
		// Zones enrich the prefix; their absence must not block seeding.
		slog.Warn("sport zones unavailable, seeding without", "err", err)
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
	profiles := store.NewProfiles(fsClient)
	if err := profiles.Seed(ctx, baselines, seed.RampCap); err != nil {
		return fmt.Errorf("write profile/current: %w", err)
	}
	if err := profiles.SeedIdentity(ctx, store.Identity{
		Sports: seed.Sports, Races: seed.Races, Availability: seed.Availability,
		InjuryHistory: seed.InjuryHistory, Preferences: seed.Preferences, Zones: zones,
	}); err != nil {
		return fmt.Errorf("write profile/identity: %w", err)
	}
	slog.Info("profile seeded", "sports", seed.Sports, "races", len(seed.Races), "zones", len(zones))
	return nil
}

// fetchZones reads the athlete's HR scheme per sport from icu sport settings.
func fetchZones(ctx context.Context, c *icu.Client, sports []string) ([]store.SportZones, error) {
	raw, err := c.GetAthlete(ctx)
	if err != nil {
		return nil, err
	}
	sets, err := icu.ExtractZones(raw, sports)
	if err != nil {
		return nil, err
	}
	out := make([]store.SportZones, 0, len(sets))
	for _, z := range sets {
		out = append(out, store.SportZones{
			Sport: z.Sport, LTHR: z.LTHR, MaxHR: z.MaxHR,
			Zones: z.Zones, ZoneName: z.ZoneName,
		})
	}
	return out, nil
}
