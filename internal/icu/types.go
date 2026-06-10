// ABOUTME: Typed intervals.icu models with pointer fields: a missing value is nil, never zero.
// ABOUTME: Cadenza-owned additions on top of the copied client (which returns json.RawMessage).

package icu

import "encoding/json"

// Wellness is one day of wellness data. intervals.icu omits null fields
// entirely, so every optional metric is a pointer: nil means "not synced",
// which must never be read as 0 (a 0 HRV would trip every verdict rule).
type Wellness struct {
	ID         string   `json:"id"` // local ISO date, e.g. "2026-06-10"
	CTL        *float64 `json:"ctl"`
	ATL        *float64 `json:"atl"`
	RampRate   *float64 `json:"rampRate"`
	HRV        *float64 `json:"hrv"`
	RestingHR  *int     `json:"restingHR"`
	SleepSecs  *int     `json:"sleepSecs"`
	SleepScore *float64 `json:"sleepScore"`
	Weight     *float64 `json:"weight"`
	Soreness   *int     `json:"soreness"`
	Fatigue    *int     `json:"fatigue"`
	Stress     *int     `json:"stress"`
	Readiness  *float64 `json:"readiness"`
}

// DecodeWellnessRange parses the raw payload of ListWellness.
func DecodeWellnessRange(raw json.RawMessage) ([]Wellness, error) {
	var days []Wellness
	if err := json.Unmarshal(raw, &days); err != nil {
		return nil, err
	}
	return days, nil
}

// Event is a calendar event. Only the fields cadenza reads are modeled;
// workout_doc stays raw because its shape is unspecified upstream and is
// decoded defensively by the read-back verifier (M6).
type Event struct {
	ID             int64           `json:"id"`
	StartDateLocal string          `json:"start_date_local"`
	Category       string          `json:"category"`
	Name           *string         `json:"name"`
	Description    *string         `json:"description"`
	ExternalID     *string         `json:"external_id"`
	WorkoutDoc     json.RawMessage `json:"workout_doc"`
}

// DecodeEvents parses the raw payload of ListEvents.
func DecodeEvents(raw json.RawMessage) ([]Event, error) {
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, err
	}
	return events, nil
}
