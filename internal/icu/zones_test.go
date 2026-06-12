// ABOUTME: Zone extraction tests on the live payload shape (probed 2026-06-12).
// ABOUTME: Sport matching is by the types list, not the group name.

package icu

import (
	"encoding/json"
	"testing"
)

const athleteFixture = `{"id":"i1","sportSettings":[
	{"types":["Ride","VirtualRide","GravelRide"],"lthr":162,"max_hr":179,
	 "hr_zones":[130,144,151,161,165,170,179],
	 "hr_zone_names":["Recovery","Aerobic","Tempo","SubThreshold","SuperThreshold","Aerobic Capacity","Anaerobic"]},
	{"types":["Run","TrailRun"],"lthr":162,"max_hr":179,
	 "hr_zones":[136,144,152,161,165,170,179],
	 "hr_zone_names":["Recovery","Aerobic","Tempo","SubThreshold","SuperThreshold","Aerobic Capacity","Anaerobic"]}
]}`

func TestExtractZones_MatchesByTypeList(t *testing.T) {
	sets, err := ExtractZones(json.RawMessage(athleteFixture), []string{"Ride", "Run", "Swim"})
	if err != nil {
		t.Fatalf("ExtractZones: %v", err)
	}
	if len(sets) != 2 {
		t.Fatalf("sets = %d, want 2 (Swim has no group in fixture)", len(sets))
	}
	if sets[0].Sport != "Ride" || sets[0].LTHR != 162 || sets[0].Zones[0] != 130 {
		t.Errorf("ride = %+v", sets[0])
	}
	if sets[1].Sport != "Run" || sets[1].Zones[0] != 136 {
		t.Errorf("run zone1 differs from ride (136 vs 130): %+v", sets[1])
	}
}

func TestExtractZones_MalformedPayloadErrors(t *testing.T) {
	if _, err := ExtractZones(json.RawMessage(`{broken`), []string{"Ride"}); err == nil {
		t.Fatal("malformed athlete payload accepted")
	}
}
