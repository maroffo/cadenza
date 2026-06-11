// ABOUTME: Dashboard handlers: read pages, clamped actions, web chat on the shared session.
// ABOUTME: Server-rendered html/template + HTMX; same binary, same stores, same gates as the bot.

package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"math"
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

var tmpl = template.Must(template.New("").
	Funcs(template.FuncMap{"coachhtml": coachHTML}).
	ParseFS(templateFS, "templates/*.html"))

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

// AuditSource reads the transparency collections and records web-originated
// profile changes (no profile change without an event, decision 14).
type AuditSource interface {
	RecentWrites(ctx context.Context, limit int) ([]store.WriteRecord, error)
	RecentMutations(ctx context.Context, limit int) ([]store.MutationWithID, error)
	SpentToday(ctx context.Context, date string) (int, error)
	RecordWebChange(ctx context.Context, kind, oldValue, newValue string) error
}

// ChatHistory exposes the active conversation for the chat page.
type ChatHistory interface {
	ActiveTurns(ctx context.Context, limit int) ([]store.Turn, error)
}

// WellnessSource is the read seam for trends/verdicts: same shape the job
// layer uses, so handlers stay testable without a live intervals.icu.
type WellnessSource interface {
	ListWellness(ctx context.Context, p icu.GetWellnessRangeParams) (json.RawMessage, error)
}

type Server struct {
	Auth     Auth
	ICU      WellnessSource
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
	mux.HandleFunc("GET /app/login", s.Auth.HandleLoginPage)
	mux.HandleFunc("POST /app/login", s.Auth.HandleLogin)
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
	mux.HandleFunc("POST /app/logout", s.Auth.HandleLogout)
}

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	// Buffer first: a mid-render error must become a 500, not a silent
	// truncated 200 with headers already gone.
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, page, data); err != nil {
		slog.Error("web: render", "page", page, "err", err)
		http.Error(w, "errore di rendering", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// CSP: self + the two pinned CDNs; inline script/style are part of the
	// served pages (trends bootstrap, layout styles), so they stay allowed:
	// SRI pins the external files byte-for-byte.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com; "+
			"style-src 'self' 'unsafe-inline' https://unpkg.com; connect-src 'self'; "+
			"img-src 'self' data:; frame-ancestors 'none'")
	_, _ = buf.WriteTo(w)
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
	days, err := s.wellnessRange(r.Context(), 37)
	if err != nil {
		s.render(w, "error.html", map[string]any{"Msg": "intervals.icu non disponibile"})
		return
	}
	sort.Slice(days, func(i, j int) bool { return days[i].ID < days[j].ID })
	var rows []verdictRow
	for i, d := range days {
		if len(days) > 30 && i < len(days)-30 {
			continue // the first week only seeds windows, never renders
		}
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
	muts, err := s.Audit.RecentMutations(ctx, 20)
	if err != nil {
		slog.Warn("web: mutations", "err", err)
	}
	rules, err := s.Rules.ListActive(ctx)
	if err != nil {
		slog.Warn("web: rules", "err", err)
	}
	spent, err := s.Audit.SpentToday(ctx, s.Now().In(s.TZ).Format("2006-01-02"))
	if err != nil {
		slog.Warn("web: budget", "err", err)
	}
	baselines, rampCap, err := s.Profiles.Profile(ctx)
	if err != nil {
		slog.Warn("web: profile", "err", err)
	}
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
	// Positive-form guard: NaN fails BOTH capVal<=0 and capVal>6, so the
	// negative form would wave it through (live finding from review).
	if err != nil || math.IsNaN(capVal) || !(capVal > 0 && capVal <= 6) {
		http.Error(w, "ramp_cap deve essere in (0, 6]", http.StatusBadRequest)
		return
	}
	_, oldCap, _ := s.Profiles.Profile(r.Context())
	if err := s.Profiles.SetRampCap(r.Context(), capVal); err != nil {
		http.Error(w, "aggiornamento non riuscito", http.StatusInternalServerError)
		return
	}
	// Same invariant as the bot path: no profile change without an event.
	if err := s.Audit.RecordWebChange(r.Context(), "ramp_cap",
		strconv.FormatFloat(oldCap, 'f', 1, 64), strconv.FormatFloat(capVal, 'f', 1, 64)); err != nil {
		slog.Warn("web: ramp cap audit event failed", "err", err)
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
	if s.Chat == nil {
		// Skeleton mode (no LLM configured): honest, never a typed-nil panic.
		s.render(w, "chat_reply.html", map[string]any{
			"User": r.FormValue("text"), "Reply": "Coach non configurato su questo ambiente.",
		})
		return
	}
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
