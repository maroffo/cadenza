// ABOUTME: Tests for the lookup_food tool: deterministic portion scaling, units, tool gating.
// ABOUTME: Asserts Go owns the arithmetic (grams/units in, scaled macros out) and numbers are real.

package job

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/maroffo/cadenza/internal/fakes"
	"github.com/maroffo/cadenza/internal/foods"
)

// parseFoodReply pulls the JSON object out of the lookup_food tool reply.
func parseFoodReply(t *testing.T, reply string) map[string]any {
	t.Helper()
	i := strings.Index(reply, "{")
	if i < 0 {
		t.Fatalf("no JSON in reply: %q", reply)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(reply[i:]), &m); err != nil {
		t.Fatalf("bad JSON in reply: %v\n%s", err, reply)
	}
	return m
}

func portionVal(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	p, ok := m["valori_porzione"].(map[string]any)
	if !ok {
		t.Fatalf("no valori_porzione in %v", m)
	}
	v, ok := p[key].(float64)
	if !ok {
		t.Fatalf("no %s in portion %v", key, p)
	}
	return v
}

func TestLookupFood_ScalesByGrams(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	reply, err := c.lookupFood([]byte(`{"query":"banana","grams":236}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	m := parseFoodReply(t, reply)
	if m["alimento"] != "Banana" {
		t.Errorf("alimento = %v, want Banana", m["alimento"])
	}
	// Banana is ~22.8g carb/100g; 236g -> ~53.8g. Go did the math, not the model.
	if got := portionVal(t, m, "carbo_g"); math.Abs(got-53.8) > 1.0 {
		t.Errorf("carbo for 236g = %.1f, want ~53.8", got)
	}
}

func TestLookupFood_ScalesByUnits(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	reply, err := c.lookupFood([]byte(`{"query":"banana","units":2}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	m := parseFoodReply(t, reply)
	// 2 bananas ~ 236g (118g each); carbs ~53.8g.
	if g, ok := m["grammi_stimati"].(float64); !ok || math.Abs(g-236) > 1 {
		t.Errorf("grammi_stimati = %v, want ~236", m["grammi_stimati"])
	}
	if got := portionVal(t, m, "carbo_g"); math.Abs(got-53.8) > 1.5 {
		t.Errorf("carbo for 2 units = %.1f, want ~53.8", got)
	}
}

func TestLookupFood_UnitsRejectedForNonCountable(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	reply, err := c.lookupFood([]byte(`{"query":"olio","units":2}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	m := parseFoodReply(t, reply)
	if _, has := m["valori_porzione"]; has {
		t.Error("olive oil has no unit; should not return a per-unit portion")
	}
	if m["nota"] == nil {
		t.Error("expected a nota explaining units are not available")
	}
}

func TestLookupFood_NoMatch(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	reply, err := c.lookupFood([]byte(`{"query":"zzzznotafood"}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	if !strings.Contains(reply, "Nessun alimento") {
		t.Errorf("expected no-match message, got %q", reply)
	}
}

func TestLookupFood_SurfacesAllergens(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	// Mozzarella is high-lactose dairy: both tags must surface (safety-relevant
	// for the lactose-intolerant family member).
	reply, err := c.lookupFood([]byte(`{"query":"mozzarella"}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	m := parseFoodReply(t, reply)
	al, ok := m["allergeni"].([]any)
	if !ok {
		t.Fatalf("no allergeni for mozzarella: %v", m)
	}
	got := map[string]bool{}
	for _, a := range al {
		got[a.(string)] = true
	}
	if !got["milk"] || !got["lactose"] {
		t.Errorf("mozzarella allergeni = %v, want milk+lactose", al)
	}
	// A food with no allergens omits the key entirely.
	banana, _ := c.lookupFood([]byte(`{"query":"banana"}`))
	if _, has := parseFoodReply(t, banana)["allergeni"]; has {
		t.Error("banana should carry no allergeni key")
	}
}

func TestLookupFood_NoPortionReturnsPer100g(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	reply, err := c.lookupFood([]byte(`{"query":"banana"}`))
	if err != nil {
		t.Fatalf("lookupFood: %v", err)
	}
	m := parseFoodReply(t, reply)
	if _, has := m["valori_porzione"]; has {
		t.Error("no grams/units given: should not return valori_porzione")
	}
	p, ok := m["per_100g"].(map[string]any)
	if !ok {
		t.Fatalf("no per_100g: %v", m)
	}
	if cg, _ := p["carbo_g"].(float64); cg < 22 || cg > 24 {
		t.Errorf("per_100g carbo = %v, want ~22.8", p["carbo_g"])
	}
}

func TestLookupFood_ErrorPaths(t *testing.T) {
	c := &Coach{Foods: foods.MustLoad()}
	for _, in := range []string{`{"query":"   "}`, `not json`, `{}`} {
		reply, err := c.lookupFood([]byte(in))
		if err == nil {
			t.Errorf("input %q: expected error, got reply %q", in, reply)
		}
		if reply != "" {
			t.Errorf("input %q: expected empty reply on error, got %q", in, reply)
		}
	}
}

func TestLookupFoodToolGatedByCatalog(t *testing.T) {
	const marker = "USDA + curati" // appears only in the lookup_food tool description
	llm := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm.Close()
	c, _, _, _, _, _ := newCoach(t, llm)
	if err := c.Converse(context.Background(), "nutrizione?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if strings.Contains(string(llm.Requests[0].Raw), marker) {
		t.Error("lookup_food tool registered without a foods catalog")
	}

	llm2 := fakes.NewAnthropic(fakes.Text{S: "ok"})
	defer llm2.Close()
	c2, _, _, _, _, _ := newCoach(t, llm2)
	c2.Foods = foods.MustLoad()
	if err := c2.Converse(context.Background(), "nutrizione?"); err != nil {
		t.Fatalf("Converse: %v", err)
	}
	if !strings.Contains(string(llm2.Requests[0].Raw), marker) {
		t.Error("lookup_food tool missing with a foods catalog wired")
	}
}
