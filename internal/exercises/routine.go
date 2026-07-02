// ABOUTME: Deterministic daily prevention/strength routine: N exercises per muscle group.
// ABOUTME: Same day+equipment always yields the same picks; the day seed rotates them.

package exercises

import (
	"sort"
	"strings"
)

// norm lowercases and trims a vocabulary token so config CSV values, catalog
// fields, and group tokens all compare on the same footing.
func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// MuscleGroup is a display bucket over one or more catalog target tokens. The
// tokens are the dataset's own English vocabulary (see catalog.json targets).
type MuscleGroup struct {
	Label   string   // Italian label for the morning message
	Targets []string // catalog `target` values that belong to this group
}

// RoutineGroups is the fixed daily split surfaced every morning: arms, legs,
// back, core/mobility. Order is stable so the message reads the same way daily.
var RoutineGroups = []MuscleGroup{
	{Label: "Braccia", Targets: []string{"biceps", "triceps", "delts"}},
	{Label: "Gambe", Targets: []string{"quads", "hamstrings", "glutes", "calves"}},
	{Label: "Schiena", Targets: []string{"lats", "upper back"}},
	{Label: "Core/mobilità", Targets: []string{"abs", "spine"}},
}

// GroupPick is a resolved muscle group with the exercises chosen for a day.
type GroupPick struct {
	Label     string
	Exercises []Exercise
}

// DailyRoutine returns perGroup exercises for each RoutineGroup, chosen
// deterministically for the given day seed and honoring the available
// equipment. Body weight is always allowed; an empty equipment list means the
// full kit (no restriction). The seed rotates the picks so consecutive days
// differ, while the same (seed, equipment) always produces the same routine:
// the morning message is reproducible across restarts and retries.
func (c *Catalog) DailyRoutine(seed, perGroup int, equipment []string) []GroupPick {
	if perGroup < 1 {
		perGroup = 1
	}
	allowed := allowedEquipment(equipment)

	picks := make([]GroupPick, 0, len(RoutineGroups))
	for _, g := range RoutineGroups {
		candidates := c.groupCandidates(g, allowed)
		picks = append(picks, GroupPick{
			Label:     g.Label,
			Exercises: rotate(candidates, seed, perGroup),
		})
	}
	return picks
}

// allowedEquipment builds the lowercased set of usable equipment. A nil result
// means "no restriction" (full kit); otherwise body weight is always included
// so a bodyweight fallback is available whatever the athlete has today.
func allowedEquipment(equipment []string) map[string]bool {
	if len(equipment) == 0 {
		return nil
	}
	allowed := map[string]bool{"body weight": true}
	for _, e := range equipment {
		if e = norm(e); e != "" {
			allowed[e] = true
		}
	}
	return allowed
}

// groupCandidates returns the exercises whose primary target belongs to the
// group and whose equipment is allowed, sorted by ID for a stable rotation base.
func (c *Catalog) groupCandidates(g MuscleGroup, allowed map[string]bool) []Exercise {
	targets := make(map[string]bool, len(g.Targets))
	for _, t := range g.Targets {
		targets[norm(t)] = true
	}
	var out []Exercise
	for _, ex := range c.exercises {
		if !targets[norm(ex.Target)] {
			continue
		}
		if allowed != nil && !allowed[norm(ex.Equipment)] {
			continue
		}
		out = append(out, ex)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// rotate picks up to n exercises from candidates starting at a day-dependent
// offset, wrapping around. Consecutive-day windows advance by n so day-to-day
// overlap is minimized; a group with fewer than n candidates returns them all.
func rotate(candidates []Exercise, seed, n int) []Exercise {
	total := len(candidates)
	if total == 0 {
		return nil
	}
	if n > total {
		n = total
	}
	start := ((seed*n)%total + total) % total // non-negative even if seed < 0
	out := make([]Exercise, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, candidates[(start+i)%total])
	}
	return out
}
