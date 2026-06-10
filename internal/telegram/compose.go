// ABOUTME: Pure message composition: morning template, degraded templates, HTML-safe 4096 splitting.
// ABOUTME: Telegram HTML mode only (MarkdownV2 escaping is fatal for pace-heavy coaching text).

package telegram

import (
	"fmt"
	"html"
	"strings"
)

// telegramLimit is the Bot API hard cap per message.
const telegramLimit = 4096

// Escape sanitizes dynamic text for HTML parse mode. Only & < > need it.
func Escape(s string) string {
	return html.EscapeString(s)
}

// MorningData carries the deterministic morning numbers. Pointers preserve
// the null-vs-zero distinction: a missing metric renders as "n/d", never 0.
type MorningData struct {
	Date      string
	HRV       *float64
	RestingHR *int
	SleepSecs *int
	CTL       *float64
	ATL       *float64
	RampRate  *float64
	Stale     bool
	StaleAsOf string // date of the data when Stale
}

func fmtF(v *float64, decimals int) string {
	if v == nil {
		return "n/d"
	}
	return fmt.Sprintf("%.*f", decimals, *v)
}

func fmtI(v *int) string {
	if v == nil {
		return "n/d"
	}
	return fmt.Sprintf("%d", *v)
}

func fmtSleep(secs *int) string {
	if secs == nil {
		return "n/d"
	}
	return fmt.Sprintf("%.1fh", float64(*secs)/3600)
}

// MorningBody renders the deterministic morning block. The verdict footer is
// appended by the sender, not here, so no caller can build a verdict-less
// morning message by accident.
func MorningBody(d MorningData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "☀️ <b>Check mattutino · %s</b>\n", d.Date)
	if d.Stale {
		fmt.Fprintf(&b, "<i>⚠️ dati del %s (non aggiornati a stamattina)</i>\n", d.StaleAsOf)
	}
	fmt.Fprintf(&b, "\n<b>HRV:</b> %s\n", fmtF(d.HRV, 0))
	fmt.Fprintf(&b, "<b>FC riposo:</b> %s bpm\n", fmtI(d.RestingHR))
	fmt.Fprintf(&b, "<b>Sonno:</b> %s\n", fmtSleep(d.SleepSecs))
	fmt.Fprintf(&b, "<b>CTL/ATL:</b> %s / %s\n", fmtF(d.CTL, 1), fmtF(d.ATL, 1))
	fmt.Fprintf(&b, "<b>Rampa:</b> %s/settimana", fmtF(d.RampRate, 1))
	return b.String()
}

// DegradedNoData: intervals.icu unreachable and no usable cache. No verdict
// is possible; the instruction errs toward less load (fail-safe direction).
func DegradedNoData() string {
	return "⚠️ <b>Cadenza degradato</b>\n" +
		"Non riesco a leggere i dati da intervals.icu e non ho dati recenti in cache.\n" +
		"Nessun verdetto possibile oggi: vai a sensazione, resta facile (Z2), " +
		"niente lavoro di qualità finché i dati non tornano. Riprovo automaticamente."
}

// DegradedLLMDown: the coach narrative is unavailable but data and verdict
// are not. Used from M4 on; defined now because it IS the M2 message shape.
func DegradedLLMDown() string {
	return "ℹ️ <i>Coach offline (errore API, riproverò): qui sotto i numeri e il verdetto deterministico.</i>"
}

// WatchdogMissedMorning: the 07:00 check never completed. Sent by the
// watchdog job alongside the ERROR log that triggers the email alert.
func WatchdogMissedMorning() string {
	return "⚠️ <b>Check mattutino mancato</b>\n" +
		"Il controllo delle 07:00 non è andato a buon fine (problema tecnico, " +
		"sto già suonando l'allarme via email).\n" +
		"Nel dubbio: vai a sensazione e resta facile, Z2, niente qualità."
}

// SplitMessage breaks a message into chunks within the Telegram limit.
// It prefers paragraph boundaries (\n\n), then line boundaries, then spaces,
// and as a last resort backtracks so a cut never lands inside an HTML tag:
// a severed tag makes Telegram reject the entire message with a 400.
func SplitMessage(s string) []string {
	if len(s) <= telegramLimit {
		return []string{s}
	}

	var chunks []string
	paragraphs := strings.Split(s, "\n\n")
	var cur strings.Builder

	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}

	for _, p := range paragraphs {
		extra := p
		if cur.Len() > 0 {
			extra = "\n\n" + p
		}
		if cur.Len()+len(extra) <= telegramLimit {
			cur.WriteString(extra)
			continue
		}
		flush()
		// Paragraph alone fits: start the next chunk with it.
		if len(p) <= telegramLimit {
			cur.WriteString(p)
			continue
		}
		// Single paragraph over the limit: hard-split, tag-safe.
		for len(p) > telegramLimit {
			cut := safeCut(p, telegramLimit)
			chunks = append(chunks, strings.TrimRight(p[:cut], " \n"))
			p = strings.TrimLeft(p[cut:], " \n")
		}
		if p != "" {
			cur.WriteString(p)
		}
	}
	flush()
	return chunks
}

// safeCut finds a cut point at or before limit that does not sever an HTML
// tag, preferring newline, then space.
func safeCut(s string, limit int) int {
	cut := limit
	if idx := strings.LastIndexByte(s[:limit], '\n'); idx > 0 {
		cut = idx
	} else if idx := strings.LastIndexByte(s[:limit], ' '); idx > 0 {
		cut = idx
	}
	// If an unclosed '<' precedes the cut, back off to just before it.
	if open := strings.LastIndexByte(s[:cut], '<'); open >= 0 {
		if strings.IndexByte(s[open:cut], '>') == -1 {
			cut = open
		}
	}
	if cut == 0 {
		cut = limit // degenerate input (single giant tag); cap rather than loop
	}
	return cut
}
