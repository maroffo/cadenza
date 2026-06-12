// ABOUTME: Sport-settings zone extraction: the athlete's real HR scheme per sport.
// ABOUTME: Cadenza-owned; the copied client stays pristine.

package icu

import (
	"encoding/json"
	"fmt"
	"slices"
)

// ZoneSet mirrors store.SportZones without importing store (icu stays leaf).
type ZoneSet struct {
	Sport    string
	LTHR     int
	MaxHR    int
	Zones    []int
	ZoneName []string
}

// ExtractZones pulls the HR zone scheme for each requested sport from the
// raw athlete payload (sportSettings groups carry a types list).
func ExtractZones(rawAthlete json.RawMessage, sports []string) ([]ZoneSet, error) {
	var athlete struct {
		SportSettings []struct {
			Types       []string `json:"types"`
			LTHR        *int     `json:"lthr"`
			MaxHR       *int     `json:"max_hr"`
			HRZones     []int    `json:"hr_zones"`
			HRZoneNames []string `json:"hr_zone_names"`
		} `json:"sportSettings"`
	}
	if err := json.Unmarshal(rawAthlete, &athlete); err != nil {
		return nil, fmt.Errorf("athlete decode: %w", err)
	}
	var out []ZoneSet
	for _, sport := range sports {
		for _, ss := range athlete.SportSettings {
			if !slices.Contains(ss.Types, sport) {
				continue
			}
			z := ZoneSet{Sport: sport, Zones: ss.HRZones, ZoneName: ss.HRZoneNames}
			if ss.LTHR != nil {
				z.LTHR = *ss.LTHR
			}
			if ss.MaxHR != nil {
				z.MaxHR = *ss.MaxHR
			}
			out = append(out, z)
			break
		}
	}
	return out, nil
}
