// ABOUTME: Dashboard handlers: read pages, clamped actions, web chat on the shared session.
// ABOUTME: Server-rendered html/template + HTMX; same binary, same stores, same gates as the bot.

package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/store"
	"github.com/maroffo/cadenza/internal/verdict"
)

//go:embed templates/*.html
var templateFS embed.FS

var tmpl = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// StatusComposer mirrors the job-layer seam (deterministic daily picture).
type StatusComposer interface {
	Compose(ctx context.Context) (string, verdict.Verdict, error)
}

// Converser is the web chat entry: same coach, reply returned for rendering.
type Converser interface {
	ConverseReply(ctx context.Context, text string) (string, verdict.Verdict, error)
}

// InjuryAdmin covers the dashboard's injury reads and the resolve action.
type InjuryAdmin interface {
	ListOpen(ctx context.Context) ([]store.Injury, error)
	Resolve(ctx context.Context, id string) error
}

// RuleAdmin lists and deactivates confirmed rules.
type RuleAdmin interface {
	ListActive(ctx context.Context) ([]store.Rule, error)
	Deactivate(ctx context.Context, id string) error
}

// ProfileAdmin reads and tightens the profile (ramp cap, Tier A clamped).
type ProfileAdmin interface {
	Profile(ctx context.Context) (verdict.Baselines, float64, error)
	SetRampCap(ctx context.Context, cap float64) error
}

// AuditSource reads the transparency collections.
type AuditSource interface {
	RecentWrites(ctx context.Context, limit int) ([]store.WriteRecord, error)
	RecentMutations(ctx context.Context, limit int) ([]store.MutationWithID, error)
	SpentToday(ctx context.Context, date string) (int, error)
}

// ChatHistory exposes the active conversation for the chat page.
type ChatHistory interface {
	ActiveTurns(ctx context.Context, limit int) ([]store.Turn, error)
}

type Server struct {
	Auth     Auth
	ICU      *icu.Client
	Status   StatusComposer
	Chat     Converser
	History  ChatHistory
	Injuries InjuryAdmin
	Rules    RuleAdmin
	Profiles ProfileAdmin
	Audit    AuditSource
	Now      func() time.Time
	TZ       *time.Location
}

// Register mounts all dashboard routes on the mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/login", s.Auth.HandleLogin)
	mux.HandleFunc("GET /app", s.Auth.Require(s.overview))
	mux.HandleFunc("GET /app/trends", s.Auth.Require(s.trends))
	mux.HandleFunc("GET /app/trends.json", s.Auth.Require(s.trendsJSON))
	mux.HandleFunc("GET /app/verdicts", s.Auth.Require(s.verdicts))
	mux.HandleFunc("GET /app/calendar", s.Auth.Require(s.calendar))
	mux.HandleFunc("GET /app/audit", s.Auth.Require(s.audit))
	mux.HandleFunc("GET /app/injuries", s.Auth.Require(s.injuries))
	mux.HandleFunc("POST /app/injuries/resolve", s.Auth.Require(s.resolveInjury))
	mux.HandleFunc("POST /app/rules/deactivate", s.Auth.Require(s.deactivateRule))
	mux.HandleFunc("POST /app/profile/rampcap", s.Auth.Require(s.setRampCap))
	mux.HandleFunc("GET /app/chat", s.Auth.Require(s.chat))
	mux.HandleFunc("POST /app/chat", s.Auth.Require(s.chatSend))
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, page, data); err != nil {
		slog.Error("web: render", "page", page, "err", err)
	}
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	body, v, err := s.Status.Compose(r.Context())
	data := map[string]any{"Page": "overview", "Err": err != nil}
	if err == nil {
		data["Body"] = body
		data["Verdict"] = v
		data["Block"] = verdict.RenderBlock(v)
	}
	s.render(w, "overview.html", data)
}

type trendPoint struct {
	Date      string   `json:"date"`
	HRV       *float64 `json:"hrv,omitempty"`
	RestingHR *int     `json:"rhr,omitempty"`
	Readiness *float64 `json:"readiness,omitempty"`
	SleepH    *float64 `json:"sleep_h,omitempty"`
	CTL       *float64 `json:"ctl,omitempty"`
	ATL       *float64 `json:"atl,omitempty"`
	Ramp      *float64 `json:"ramp,omitempty"`
}

func (s *Server) wellnessRange(ctx context.Context, days int) ([]icu.Wellness, error) {
	now := s.Now().In(s.TZ)
	raw, err := s.ICU.ListWellness(ctx, icu.GetWellnessRangeParams{
		Oldest: now.AddDate(0, 0, -days).Format("2006-01-02"),
		Newest: now.Format("2006-01-02"),
	})
	if err != nil {
		return nil, err
	}
	return icu.DecodeWellnessRange(raw)
}

func (s *Server) trends(w http.ResponseWriter, r *http.Request) {
	s.render(w, "trends.html", map[string]any{"Page": "trends"})
}

func (s *Server) trendsJSON(w http.ResponseWriter, r *http.Request) {
	days, err := s.wellnessRange(r.Context(), 90)
	if err != nil {
		http.Error(w, "intervals.icu non disponibile", http.StatusBadGateway)
		return
	}
	points := make([]trendPoint, 0, len(days))
	for _, d := range days {
		p := trendPoint{Date: d.ID, HRV: d.HRV, RestingHR: d.RestingHR,
			Readiness: d.Readiness, CTL: d.CTL, ATL: d.ATL, Ramp: d.RampRate}
		if d.SleepSecs != nil {
			h := float64(*d.SleepSecs) / 3600
			p.SleepH = &h
		}
		points = append(points, p)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Date < points[j].Date })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(points)
}

type verdictRow struct {
	Date    string
	Kind    verdict.Kind
	Reasons []verdict.Reason
}

// verdicts recomputes history from wellness + the CURRENT profile: pure and
// honest about it (the banner says thresholds are today's).
func (s *Server) verdicts(w http.ResponseWriter, r *http.Request) {
	baselines, rampCap, err := s.Profiles.Profile(r.Context())
	if err != nil {
		s.render(w, "error.html", map[string]any{"Msg": "profilo non disponibile"})
		return
	}
	days, err := s.wellnessRange(r.Context(), 30)
	if err != nil {
		s.render(w, "error.html", map[string]any{"Msg": "intervals.icu non disponibile"})
		return
	}
	sort.Slice(days, func(i, j int) bool { return days[i].ID < days[j].ID })
	var rows []verdictRow
	for i, d := range days {
		var window []verdict.Day
		for j := max(0, i-7); j < i; j++ {
			window = append(window, dayFromWellness(days[j]))
		}
		v := verdict.Compute(verdict.Input{
			Today: dayFromWellness(d), Window: window,
			Baselines: baselines, RampCap: rampCap,
		}, verdict.DefaultRules())
		rows = append(rows, verdictRow{Date: d.ID, Kind: v.Kind, Reasons: v.Reasons})
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	s.render(w, "verdicts.html", map[string]any{"Page": "verdicts", "Rows": rows})
}

func dayFromWellness(w icu.Wellness) verdict.Day {
	return verdict.Day{
		Date: w.ID, HRV: w.HRV, RestingHR: w.RestingHR, SleepSecs: w.SleepSecs,
		RampRate: w.RampRate, CTL: w.CTL, ATL: w.ATL,
		Readiness: w.Readiness, SleepScore: w.SleepScore, SpO2: w.SpO2,
		Soreness: w.Soreness, Fatigue: w.Fatigue, InjuryFeel: w.Injury,
	}
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request) {
	writes, err := s.Audit.RecentWrites(r.Context(), 20)
	if err != nil {
		slog.Warn("web: ledger", "err", err)
	}
	s.render(w, "calendar.html", map[string]any{"Page": "calendar", "Writes": writes})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	muts, _ := s.Audit.RecentMutations(ctx, 20)
	rules, _ := s.Rules.ListActive(ctx)
	spent, _ := s.Audit.SpentToday(ctx, s.Now().In(s.TZ).Format("2006-01-02"))
	baselines, rampCap, _ := s.Profiles.Profile(ctx)
	s.render(w, "audit.html", map[string]any{
		"Page": "audit", "Mutations": muts, "Rules": rules,
		"Spent": spent, "Baselines": baselines, "RampCap": rampCap,
	})
}

func (s *Server) injuries(w http.ResponseWriter, r *http.Request) {
	open, err := s.Injuries.ListOpen(r.Context())
	if err != nil {
		slog.Warn("web: injuries", "err", err)
	}
	s.render(w, "injuries.html", map[string]any{"Page": "injuries", "Open": open})
}

func (s *Server) resolveInjury(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "id mancante", http.StatusBadRequest)
		return
	}
	if err := s.Injuries.Resolve(r.Context(), id); err != nil {
		http.Error(w, "risoluzione non riuscita", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/injuries", http.StatusSeeOther)
}

func (s *Server) deactivateRule(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	if id == "" {
		http.Error(w, "id mancante", http.StatusBadRequest)
		return
	}
	if err := s.Rules.Deactivate(r.Context(), id); err != nil {
		http.Error(w, "disattivazione non riuscita", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/audit", http.StatusSeeOther)
}

// setRampCap accepts only values the safety design allows: (0, 6] Tier A,
// the same ceiling the mutation path enforces.
func (s *Server) setRampCap(w http.ResponseWriter, r *http.Request) {
	capVal, err := strconv.ParseFloat(r.FormValue("ramp_cap"), 64)
	if err != nil || capVal <= 0 || capVal > 6 {
		http.Error(w, "ramp_cap deve essere in (0, 6]", http.StatusBadRequest)
		return
	}
	if err := s.Profiles.SetRampCap(r.Context(), capVal); err != nil {
		http.Error(w, "aggiornamento non riuscito", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/app/audit", http.StatusSeeOther)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	turns, err := s.History.ActiveTurns(r.Context(), 40)
	if err != nil {
		slog.Warn("web: chat history", "err", err)
	}
	s.render(w, "chat.html", map[string]any{"Page": "chat", "Turns": turns})
}

// chatSend posts one message through the SAME coach pipeline as Telegram
// (budget, tools, gate, session) and renders the reply fragment for HTMX.
func (s *Server) chatSend(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")
	if text == "" || len(text) > 2000 {
		http.Error(w, "messaggio vuoto o troppo lungo", http.StatusBadRequest)
		return
	}
	reply, v, err := s.Chat.ConverseReply(r.Context(), text)
	if err != nil {
		s.render(w, "chat_reply.html", map[string]any{
			"User": text, "Reply": "Coach non disponibile in questo momento, riprova.",
		})
		return
	}
	s.render(w, "chat_reply.html", map[string]any{
		"User": text, "Reply": reply, "Block": verdict.RenderBlock(v),
	})
}

var _ = fmt.Sprintf // keep fmt for template helpers below
