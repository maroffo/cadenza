// ABOUTME: Tests for the deterministic daily routine: shape, determinism, rotation, equipment filter.
// ABOUTME: Stdlib testing only; all data comes from the embedded catalog.json, no network.

package exercises

import (
	"strings"
	"testing"
)

// targetToGroup maps every catalog target used by RoutineGroups back to its
// group label, so a returned exercise can be checked against the right bucket.
func targetToGroup() map[string]string {
	m := make(map[string]string)
	for _, g := range RoutineGroups {
		for _, t := range g.Targets {
			m[strings.ToLower(t)] = g.Label
		}
	}
	return m
}

func TestDailyRoutine_ShapeAndGrouping(t *testing.T) {
	c := load(t)
	t2g := targetToGroup()

	picks := c.DailyRoutine(0, 2, nil) // full kit
	if len(picks) != len(RoutineGroups) {
		t.Fatalf("got %d groups, want %d", len(picks), len(RoutineGroups))
	}
	for i, p := range picks {
		if p.Label != RoutineGroups[i].Label {
			t.Errorf("group %d label = %q, want %q", i, p.Label, RoutineGroups[i].Label)
		}
		if len(p.Exercises) != 2 {
			t.Errorf("group %q returned %d exercises, want 2", p.Label, len(p.Exercises))
		}
		seen := make(map[string]bool)
		for _, ex := range p.Exercises {
			if got := t2g[strings.ToLower(ex.Target)]; got != p.Label {
				t.Errorf("exercise %s (target %q) placed in %q, belongs to %q",
					ex.ID, ex.Target, p.Label, got)
			}
			if seen[ex.ID] {
				t.Errorf("group %q returned duplicate exercise %s", p.Label, ex.ID)
			}
			seen[ex.ID] = true
		}
	}
}

func TestDailyRoutine_Deterministic(t *testing.T) {
	c := load(t)
	a := c.DailyRoutine(42, 2, []string{"band"})
	b := c.DailyRoutine(42, 2, []string{"band"})
	if !sameRoutine(a, b) {
		t.Error("same (seed, equipment) produced different routines")
	}
}

func TestDailyRoutine_RotatesByDay(t *testing.T) {
	c := load(t)
	// Arms has hundreds of candidates: consecutive day seeds must not repeat
	// the same pair (the whole point of the rotation).
	d0 := c.DailyRoutine(0, 2, nil)
	d1 := c.DailyRoutine(1, 2, nil)
	if sameRoutine(d0, d1) {
		t.Error("consecutive day seeds produced identical routines; rotation is broken")
	}
}

func TestDailyRoutine_RespectsEquipment(t *testing.T) {
	c := load(t)
	picks := c.DailyRoutine(7, 2, []string{"band"})
	for _, p := range picks {
		for _, ex := range p.Exercises {
			eq := strings.ToLower(ex.Equipment)
			if eq != "band" && eq != "body weight" {
				t.Errorf("group %q returned %s with equipment %q; only band + body weight allowed",
					p.Label, ex.ID, ex.Equipment)
			}
		}
	}
}

func TestDailyRoutine_FullKitAllowsAnyEquipment(t *testing.T) {
	c := load(t)
	// With no restriction, at least one pick should use gear beyond body weight
	// over a spread of seeds (otherwise the "empty = full kit" path is a no-op).
	sawGear := false
	for seed := 0; seed < 20 && !sawGear; seed++ {
		for _, p := range c.DailyRoutine(seed, 2, nil) {
			for _, ex := range p.Exercises {
				if strings.ToLower(ex.Equipment) != "body weight" {
					sawGear = true
				}
			}
		}
	}
	if !sawGear {
		t.Error("full-kit routine never used non-bodyweight equipment across 20 days")
	}
}

func sameRoutine(a, b []GroupPick) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i].Exercises) != len(b[i].Exercises) {
			return false
		}
		for j := range a[i].Exercises {
			if a[i].Exercises[j].ID != b[i].Exercises[j].ID {
				return false
			}
		}
	}
	return true
}
