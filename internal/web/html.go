// ABOUTME: Telegram-HTML to safe web HTML: escape everything, re-enable only b/i.
// ABOUTME: Coach text carries <b>/<i> by contract; everything else must stay inert.

package web

import (
	"html"
	"html/template"
	"strings"
)

// coachHTML renders coach/system text (Telegram HTML subset) safely on the
// web: full escape first, then ONLY the allowlisted tags come back to life.
// A <script> typed or generated anywhere stays visible text, never code.
func coachHTML(s string) template.HTML {
	escaped := html.EscapeString(s)
	r := strings.NewReplacer(
		"&lt;b&gt;", "<b>", "&lt;/b&gt;", "</b>",
		"&lt;i&gt;", "<i>", "&lt;/i&gt;", "</i>",
	)
	return template.HTML(r.Replace(escaped))
}
