// ABOUTME: Renders the verdict as a Telegram HTML block, appended to messages by code.
// ABOUTME: The model never assembles this text and cannot suppress or contradict it.

package verdict

import (
	"fmt"
	"strings"
)

// RenderBlock produces the fixed-format verdict footer. Telegram HTML mode
// supports only inline tags (b/i/code/pre/a); no tables, so bold-label lines.
func RenderBlock(v Verdict) string {
	var b strings.Builder

	emoji := map[Kind]string{Go: "🟢", Modify: "🟡", Skip: "🔴"}[v.Kind]
	fmt.Fprintf(&b, "%s <b>VERDETTO: %s</b>", emoji, v.Kind)

	for _, r := range v.Reasons {
		fmt.Fprintf(&b, "\n• %s\n  <i>%s, soglia %s</i>", r.Message, r.Observed, r.Threshold)
	}

	if v.Caps.MaxZone != 0 || v.Caps.MaxMinutes != 0 {
		b.WriteString("\n<b>Limiti oggi:</b>")
		if v.Caps.MaxZone != 0 {
			fmt.Fprintf(&b, " max Z%d", v.Caps.MaxZone)
		}
		if v.Caps.MaxMinutes != 0 {
			fmt.Fprintf(&b, " · max %d′", v.Caps.MaxMinutes)
		}
	}

	if len(v.DataGaps) > 0 {
		fmt.Fprintf(&b, "\n<i>Dati mancanti: %s</i>", strings.Join(v.DataGaps, ", "))
	}

	return b.String()
}
