// ABOUTME: Typed intervals.icu models with pointer fields: a missing value is nil, never zero.
// ABOUTME: Cadenza-owned additions on top of the copied client (which returns json.RawMessage).

package icu

import "encoding/json"

// DefaultBaseURL is the production API root; lives here (cadenza-owned file)
// so the copied client files stay pristine.
const DefaultBaseURL = "https://intervals.icu/api/v1"

// Wellness is one day of wellness data. intervals.icu omits null fields
// entirely, so every optional metric is a pointer: nil means "not synced",
// which must never be read as 0 (a 0 HRV would trip every verdict rule).
// Wellness carries the FULL intervals.icu wellness surface (D31): the
// verdict still consumes only its four ruled metrics; everything else is
// model context. omitempty keeps the tool payloads compact: most fields are
// null on most days and null is not 0 (and not worth tokens either).
type Wellness struct {
	ID         string   `json:"id"` // local ISO date, e.g. "2026-06-10"
	CTL        *float64 `json:"ctl,omitempty"`
	ATL        *float64 `json:"atl,omitempty"`
	RampRate   *float64 `json:"rampRate,omitempty"`
	HRV        *float64 `json:"hrv,omitempty"`
	HRVSDNN    *float64 `json:"hrvSDNN,omitempty"`
	RestingHR  *int     `json:"restingHR,omitempty"`
	AvgSleepHR *float64 `json:"avgSleepingHR,omitempty"`
	SleepSecs  *int     `json:"sleepSecs,omitempty"`
	SleepScore *float64 `json:"sleepScore,omitempty"`
	SleepQual  *int     `json:"sleepQuality,omitempty"`
	Weight     *float64 `json:"weight,omitempty"`
	BodyFat    *float64 `json:"bodyFat,omitempty"`
	Abdomen    *float64 `json:"abdomen,omitempty"`
	Soreness   *int     `json:"soreness,omitempty"`
	Fatigue    *int     `json:"fatigue,omitempty"`
	Stress     *int     `json:"stress,omitempty"`
	Mood       *int     `json:"mood,omitempty"`
	Motivation *int     `json:"motivation,omitempty"`
	Injury     *int     `json:"injury,omitempty"`
	Readiness  *float64 `json:"readiness,omitempty"`
	SpO2       *float64 `json:"spO2,omitempty"`
	Systolic   *int     `json:"systolic,omitempty"`
	Diastolic  *int     `json:"diastolic,omitempty"`
	// Nutrition and fueling context (D31): model judgment only, no rules.
	KcalConsumed   *int     `json:"kcalConsumed,omitempty"`
	Protein        *float64 `json:"protein,omitempty"`       // grams
	Carbohydrates  *float64 `json:"carbohydrates,omitempty"` // grams
	FatTotal       *float64 `json:"fatTotal,omitempty"`      // grams
	Hydration      *float64 `json:"hydration,omitempty"`
	HydrationVol   *float64 `json:"hydrationVolume,omitempty"`
	BloodGlucose   *float64 `json:"bloodGlucose,omitempty"`
	Lactate        *float64 `json:"lactate,omitempty"`
	Steps          *int     `json:"steps,omitempty"`
	VO2Max         *float64 `json:"vo2max,omitempty"`
	BaevskySI      *float64 `json:"baevskySI,omitempty"`
	MenstrualPhase *string  `json:"menstrualPhase,omitempty"`
	// Sleep phases and energy (watch sync; capitalized keys are the API's).
	REMSleep      *float64 `json:"REMSleep,omitempty"`
	DeepSleep     *float64 `json:"DeepSleep,omitempty"`
	LightSleep    *float64 `json:"LightSleep,omitempty"`
	Respiration   *float64 `json:"respiration,omitempty"`
	ActiveEnergy  *float64 `json:"ActiveEnergy,omitempty"`
	RestingEnergy *float64 `json:"RestingEnergy,omitempty"`
	// Comments are the athlete's own diary notes: data, never instructions.
	Comments *string `json:"comments,omitempty"`
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

// Activity is the trimmed view cadenza exposes to tools: full payloads are
// huge and would poison the model context (mvilanova lesson).
type Activity struct {
	// ID is a STRING in the intervals.icu API ("i86384921"), unlike event
	// ids which are numeric. Decoding it as int64 breaks the whole list.
	ID             string   `json:"id"`
	StartDateLocal string   `json:"start_date_local"`
	Type           string   `json:"type"`
	Name           *string  `json:"name,omitempty"`
	MovingTime     *int     `json:"moving_time,omitempty"` // seconds
	Distance       *float64 `json:"distance,omitempty"`    // meters
	TrainingLoad   *int     `json:"icu_training_load,omitempty"`
	AverageHR      *int     `json:"average_heartrate,omitempty"`
	MaxHR          *int     `json:"max_heartrate,omitempty"`
	AverageSpeed   *float64 `json:"average_speed,omitempty"`
	Cadence        *float64 `json:"average_cadence,omitempty"`
	ElevationGain  *float64 `json:"total_elevation_gain,omitempty"`
	// Analytics for polarization and intensity-creep talk (spec PRINCIPLE):
	// intensity, aerobic efficiency, HR drift, seconds per HR zone.
	Intensity        *float64 `json:"icu_intensity,omitempty"`
	EfficiencyFactor *float64 `json:"icu_efficiency_factor,omitempty"`
	Decoupling       *float64 `json:"decoupling,omitempty"`
	HRZoneTimes      []int    `json:"icu_hr_zone_times,omitempty"`
	Kcal             *int     `json:"calories,omitempty"`
	// Subjective post-activity signals (debrief: percepito vs oggettivo).
	RPE  *int `json:"icu_rpe,omitempty"` // 1-10 athlete-reported
	Feel *int `json:"feel,omitempty"`    // 1-5, 1 = great
}

// DecodeActivities parses the raw payload of ListActivities.
func DecodeActivities(raw json.RawMessage) ([]Activity, error) {
	var out []Activity
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DecodeEvents parses the raw payload of ListEvents.
func DecodeEvents(raw json.RawMessage) ([]Event, error) {
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, err
	}
	return events, nil
}
