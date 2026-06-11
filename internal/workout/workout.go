// ABOUTME: Structured workout schema: the model fills it, Go validates and marshals it.
// ABOUTME: JSON-first per decision 26: traps are unrepresentable (no distance, no nesting, no DSL).

package workout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

// Sport values cadenza writes in v1.
const (
	SportRun  = "Run"
	SportRide = "Ride"
)

// ToolSchema is the single source of truth for the agent tool input: the
// same shape Validate enforces. No distance field exists in v1 (HR-only
// targets sync most reliably per the spec); a model literally cannot emit
// "400m" because no string carries units.
const ToolSchema = `{
  "type": "object",
  "required": ["date", "sport", "title", "items"],
  "additionalProperties": false,
  "properties": {
    "date":  {"type": "string", "description": "yyyy-mm-dd, data locale atleta"},
    "sport": {"type": "string", "enum": ["Run", "Ride"]},
    "title": {"type": "string", "maxLength": 80},
    "items": {
      "type": "array", "minItems": 1, "maxItems": 12,
      "items": {"oneOf": [
        {"$ref": "#/$defs/step"},
        {"$ref": "#/$defs/repeat"}
      ]}
    }
  },
  "$defs": {
    "hr": {"oneOf": [
      {"type": "object", "required": ["zone"], "additionalProperties": false,
       "properties": {"zone": {"type": "integer", "minimum": 1, "maximum": 5}}},
      {"type": "object", "required": ["zone_start", "zone_end"], "additionalProperties": false,
       "properties": {"zone_start": {"type": "integer", "minimum": 1, "maximum": 5},
                      "zone_end":   {"type": "integer", "minimum": 1, "maximum": 5}}}
    ]},
    "step": {
      "type": "object", "required": ["hr"], "additionalProperties": false,
      "properties": {
        "label":     {"type": "string", "maxLength": 40},
        "minutes":   {"type": "integer", "minimum": 0, "maximum": 180},
        "seconds":   {"type": "integer", "enum": [0, 15, 30, 45]},
        "hr":        {"$ref": "#/$defs/hr"},
        "intensity": {"type": "string", "enum": ["warmup", "cooldown", "recovery", "active"]}
      }
    },
    "repeat": {
      "type": "object", "required": ["repeat", "steps"], "additionalProperties": false,
      "properties": {
        "repeat": {"type": "integer", "minimum": 2, "maximum": 12},
        "steps":  {"type": "array", "minItems": 1, "maxItems": 4,
                   "items": {"$ref": "#/$defs/step"}}
      }
    }
  }
}`

// HRTarget is a zone or a zone range; exactly one form is set.
type HRTarget struct {
	Zone      int `json:"zone,omitempty"`
	ZoneStart int `json:"zone_start,omitempty"`
	ZoneEnd   int `json:"zone_end,omitempty"`
}

type Step struct {
	Label     string   `json:"label,omitempty"`
	Minutes   int      `json:"minutes,omitempty"`
	Seconds   int      `json:"seconds,omitempty"`
	HR        HRTarget `json:"hr"`
	Intensity string   `json:"intensity,omitempty"`
}

// Repeat holds plain steps only: nesting is a type error, not a validation
// failure.
type Repeat struct {
	Count int    `json:"repeat"`
	Steps []Step `json:"steps"`
}

// Item is a tagged union: exactly one of Step/Repeat.
type Item struct {
	Step   *Step
	Repeat *Repeat
}

func (it *Item) UnmarshalJSON(raw []byte) error {
	var probe struct {
		RepeatCount *int            `json:"repeat"`
		Steps       json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	if probe.RepeatCount != nil || probe.Steps != nil {
		var r Repeat
		if err := strictUnmarshal(raw, &r); err != nil {
			return fmt.Errorf("repeat malformato: %w", err)
		}
		it.Repeat = &r
		return nil
	}
	var s Step
	if err := strictUnmarshal(raw, &s); err != nil {
		return fmt.Errorf("step malformato: %w", err)
	}
	it.Step = &s
	return nil
}

// strictUnmarshal rejects unknown fields: the API does not enforce tool
// schemas, and a silently-dropped "distance_m" would let the calendar
// diverge from what the model told the athlete.
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

type Plan struct {
	Date  string `json:"date"`
	Sport string `json:"sport"`
	Title string `json:"title"`
	Items []Item `json:"items"`
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Validate enforces the cross-field rules the JSON schema cannot express.
// Errors are model-readable: precise tool_result errors make regeneration
// targeted instead of random.
func (p Plan) Validate() error {
	if !dateRe.MatchString(p.Date) {
		return fmt.Errorf("date %q non è yyyy-mm-dd", p.Date)
	}
	if p.Sport != SportRun && p.Sport != SportRide {
		return fmt.Errorf("sport %q non supportato (Run|Ride)", p.Sport)
	}
	if p.Title == "" || len(p.Title) > 80 {
		return fmt.Errorf("title obbligatorio, max 80 caratteri")
	}
	if len(p.Items) == 0 || len(p.Items) > 12 {
		return fmt.Errorf("items deve avere 1-12 elementi, ne ha %d", len(p.Items))
	}
	for i, it := range p.Items {
		switch {
		case it.Step != nil && it.Repeat != nil:
			return fmt.Errorf("items[%d]: step e repeat insieme", i)
		case it.Step == nil && it.Repeat == nil:
			return fmt.Errorf("items[%d]: né step né repeat", i)
		case it.Step != nil:
			if err := validateStep(*it.Step, i, len(p.Items)); err != nil {
				return err
			}
		default:
			r := it.Repeat
			if r.Count < 2 || r.Count > 12 {
				return fmt.Errorf("items[%d]: repeat %d fuori da [2,12]", i, r.Count)
			}
			if len(r.Steps) == 0 || len(r.Steps) > 4 {
				return fmt.Errorf("items[%d]: repeat con %d step, range [1,4]", i, len(r.Steps))
			}
			for j, s := range r.Steps {
				if s.Intensity == "warmup" || s.Intensity == "cooldown" {
					return fmt.Errorf("items[%d].steps[%d]: warmup/cooldown dentro un repeat", i, j)
				}
				if err := validateStep(s, i, len(p.Items)); err != nil {
					return fmt.Errorf("items[%d].steps[%d]: %w", i, j, err)
				}
			}
		}
	}
	return nil
}

func validateStep(s Step, pos, total int) error {
	if len(s.Label) > 40 {
		return fmt.Errorf("label oltre 40 caratteri")
	}
	if s.Minutes < 0 || s.Minutes > 180 {
		return fmt.Errorf("minutes %d fuori da [0,180]", s.Minutes)
	}
	switch s.Seconds {
	case 0, 15, 30, 45:
	default:
		return fmt.Errorf("seconds %d non in {0,15,30,45}", s.Seconds)
	}
	if s.Minutes == 0 && s.Seconds == 0 {
		return fmt.Errorf("step a durata zero")
	}
	switch {
	case s.HR.Zone != 0:
		if s.HR.Zone < 1 || s.HR.Zone > 5 || s.HR.ZoneStart != 0 || s.HR.ZoneEnd != 0 {
			return fmt.Errorf("hr.zone %d non valida", s.HR.Zone)
		}
	case s.HR.ZoneStart != 0 || s.HR.ZoneEnd != 0:
		if s.HR.ZoneStart < 1 || s.HR.ZoneEnd > 5 || s.HR.ZoneStart >= s.HR.ZoneEnd {
			return fmt.Errorf("hr range %d-%d non valido", s.HR.ZoneStart, s.HR.ZoneEnd)
		}
	default:
		return fmt.Errorf("step senza target hr")
	}
	switch s.Intensity {
	case "", "active", "recovery":
	case "warmup":
		if pos != 0 {
			return fmt.Errorf("warmup solo come primo item")
		}
	case "cooldown":
		if pos != total-1 {
			return fmt.Errorf("cooldown solo come ultimo item")
		}
	default:
		return fmt.Errorf("intensity %q non valida", s.Intensity)
	}
	return nil
}

// Seconds returns the step duration in seconds.
func (s Step) DurationSeconds() int { return s.Minutes*60 + s.Seconds }

// Flatten expands repeats into the executed step sequence.
func (p Plan) Flatten() []Step {
	var out []Step
	for _, it := range p.Items {
		switch {
		case it.Step != nil:
			out = append(out, *it.Step)
		case it.Repeat != nil:
			for range it.Repeat.Count {
				out = append(out, it.Repeat.Steps...)
			}
		}
	}
	return out
}

// TotalSeconds is the planned duration.
func (p Plan) TotalSeconds() int {
	total := 0
	for _, s := range p.Flatten() {
		total += s.DurationSeconds()
	}
	return total
}

// BuildDoc marshals the plan into the intervals.icu workout_doc shape the
// live spike verified (steps with duration seconds + hr_zone targets; the
// server resolves _hr itself). No description text is EVER sent alongside:
// the parser would re-derive steps from it and clobber the doc.
func (p Plan) BuildDoc() (json.RawMessage, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	steps := make([]map[string]any, 0, len(p.Items))
	for i, it := range p.Items {
		switch {
		case it.Step != nil:
			steps = append(steps, stepDoc(*it.Step, i == 0, i == len(p.Items)-1))
		case it.Repeat != nil:
			sub := make([]map[string]any, 0, len(it.Repeat.Steps))
			for _, s := range it.Repeat.Steps {
				sub = append(sub, stepDoc(s, false, false))
			}
			steps = append(steps, map[string]any{
				"reps": it.Repeat.Count, "text": fmt.Sprintf("%dx", it.Repeat.Count), "steps": sub,
			})
		}
	}
	doc, err := json.Marshal(map[string]any{"steps": steps})
	if err != nil {
		return nil, fmt.Errorf("workout doc marshal: %w", err)
	}
	return doc, nil
}

func stepDoc(s Step, first, last bool) map[string]any {
	doc := map[string]any{"duration": s.DurationSeconds()}
	if s.HR.Zone != 0 {
		doc["hr"] = map[string]any{"units": "hr_zone", "value": s.HR.Zone}
	} else {
		doc["hr"] = map[string]any{"units": "hr_zone", "start": s.HR.ZoneStart, "end": s.HR.ZoneEnd}
	}
	if s.Label != "" {
		doc["text"] = s.Label
	}
	switch s.Intensity {
	case "warmup":
		if first {
			doc["warmup"] = true
		}
	case "cooldown":
		if last {
			doc["cooldown"] = true
		}
	}
	return doc
}
