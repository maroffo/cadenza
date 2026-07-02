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
	"github.com/maroffo/cadenza/internal/foods"
	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/job"
	"github.com/maroffo/cadenza/internal/recipes"
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
	seedRecipes := flag.Bool("recipes", false, "seed the family recipe book (recipes+meals) from the embedded YAML into Firestore, then exit")
	seedFamily := flag.Bool("family", false, "seed the family energy profile from a YAML (-f, default family.yaml) into Firestore, then exit")
	flag.Parse()

	if *seedRecipes {
		if err := runRecipes(context.Background(), *dryRun); err != nil {
			slog.Error("seed recipes", "err", err)
			os.Exit(1)
		}
		return
	}

	if *seedFamily {
		path := *yamlPath
		if path == "athlete.yaml" { // default belongs to the profile seed; family has its own
			path = "family.yaml"
		}
		if err := runFamily(context.Background(), path, *dryRun); err != nil {
			slog.Error("seed family", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(context.Background(), *yamlPath, *lookbackDays, *dryRun); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
}

// runRecipes writes the curated embedded recipe book into Firestore, the one-off
// migration behind the runtime-mutable recipe dashboard. It strict-loads the
// embed first (a dirty book aborts before any write), so a broken YAML never
// half-seeds. Idempotent: re-running overwrites each doc by id.
func runRecipes(ctx context.Context, dryRun bool) error {
	book := recipes.MustLoad(foods.MustLoad()) // strict: embed must be clean
	rs, ms := book.Recipes(), book.Meals()
	slog.Info("recipe seed prepared", "recipes", len(rs), "meals", len(ms), "dry_run", dryRun)
	if dryRun {
		return nil
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	fsClient, err := store.NewClient(ctx, cfg.GCPProject)
	if err != nil {
		return fmt.Errorf("firestore: %w", err)
	}
	defer func() { _ = fsClient.Close() }()

	rstore := store.NewRecipes(fsClient)
	for _, r := range rs {
		if err := rstore.SaveRecipe(ctx, r); err != nil {
			return fmt.Errorf("seed recipe %q: %w", r.ID, err)
		}
	}
	for _, m := range ms {
		if err := rstore.SaveMeal(ctx, m); err != nil {
			return fmt.Errorf("seed meal %q: %w", m.ID, err)
		}
	}
	slog.Info("recipe book seeded to firestore", "recipes", len(rs), "meals", len(ms))
	return nil
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

// runFamily reads the family energy profile from YAML and writes it to
// Firestore (profile/family), the one-off migration behind the meal_targets
// tool. It validates the distribution sums ~100 and every member has positive
// kcal before writing, so a broken YAML never half-seeds. Idempotent.
func runFamily(ctx context.Context, yamlPath string, dryRun bool) error {
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("family file: %w", err)
	}
	var fam store.Family
	if err := yaml.Unmarshal(raw, &fam); err != nil {
		return fmt.Errorf("family yaml: %w", err)
	}
	if len(fam.Membri) == 0 {
		return fmt.Errorf("family yaml: nessun membro")
	}
	var sum float64
	for _, v := range fam.Distribuzione {
		sum += v
	}
	if sum < 95 || sum > 105 {
		return fmt.Errorf("family yaml: la distribuzione somma %.0f%%, deve essere ~100", sum)
	}
	for _, m := range fam.Membri {
		if m.KcalCaldo <= 0 || m.KcalFreddo <= 0 {
			return fmt.Errorf("family yaml: %q ha kcal non valide (caldo %.0f, freddo %.0f)", m.Nome, m.KcalCaldo, m.KcalFreddo)
		}
	}
	slog.Info("family seed prepared", "membri", len(fam.Membri), "pasti", len(fam.Distribuzione), "dry_run", dryRun)
	if dryRun {
		return nil
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	fsClient, err := store.NewClient(ctx, cfg.GCPProject)
	if err != nil {
		return fmt.Errorf("firestore: %w", err)
	}
	defer func() { _ = fsClient.Close() }()
	if err := store.NewProfiles(fsClient).SeedFamily(ctx, fam); err != nil {
		return fmt.Errorf("write profile/family: %w", err)
	}
	slog.Info("family profile seeded to firestore", "membri", len(fam.Membri))
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
