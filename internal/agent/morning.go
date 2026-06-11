// ABOUTME: The Haiku morning narrator: short Italian coaching prose over pre-computed numbers.
// ABOUTME: The model narrates; it never computes, never contradicts the Go verdict (decision 15).

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/maroffo/cadenza/internal/verdict"
)

// morningSystem is the narrative contract: distilled from the coaching spec
// (tone, principles) and the safety decisions. The deterministic verdict
// block is appended by code AFTER this text; the model cannot touch it.
const morningSystem = `Sei il coach di resistenza personale dell'atleta: diretto, caldo, niente
riempitivi aziendali ne' cheerleading. Rispondi SEMPRE in italiano.

Il tuo compito ogni mattina: 2-4 frasi di lettura della giornata sopra i
numeri che ricevi. Spiega il PERCHE' in termini fisiologici semplici
(autonomico vs strutturale, freschezza vs carico) citando i valori che
ricevi, mai ricalcolandoli.

Regole non negoziabili:
- I numeri e il verdetto (GO/MODIFY/SKIP) arrivano gia' calcolati dal
  sistema: non fare MAI aritmetica tua, non inventare valori mancanti.
- Non contraddire mai il verdetto: puoi spiegarlo e contestualizzarlo.
- Un'HRV record NON autorizza a superare il piano di oggi: i tendini non
  hanno HRV (atleta master: il recupero strutturale e' piu' lento).
- Se ci sono dati mancanti, dillo con franchezza e resta conservativo.
- Niente tabelle, niente markdown: prosa breve, al massimo un grassetto
  HTML <b> per il punto chiave.`

// NarrativeInput is everything the narrator may talk about; all of it is
// computed in Go before the model sees it.
type NarrativeInput struct {
	Date    string
	Body    string // the deterministic numbers block (already rendered)
	Verdict verdict.Verdict
}

// Narrator produces the morning prose on the cheap tier.
type Narrator struct {
	Client Client
	Model  string
}

func (n Narrator) MorningNarrative(ctx context.Context, in NarrativeInput) (string, error) {
	if n.Model == "" {
		return "", fmt.Errorf("narrator: model not configured")
	}
	var u strings.Builder
	fmt.Fprintf(&u, "Data: %s\n\nNumeri di oggi (gia' calcolati):\n%s\n\n", in.Date, in.Body)
	fmt.Fprintf(&u, "Verdetto deterministico: %s\n", in.Verdict.Kind)
	for _, r := range in.Verdict.Reasons {
		fmt.Fprintf(&u, "- regola %s: %s (osservato %s, soglia %s)\n", r.RuleID, r.Message, r.Observed, r.Threshold)
	}
	for _, c := range in.Verdict.Checks {
		state := "ok"
		if !c.Passed {
			state = "fuori range"
		}
		fmt.Fprintf(&u, "- margine %s: %s (%s) [%s]\n", c.Label, c.Observed, c.Limit, state)
	}
	if v := in.Verdict.Caps; v.MaxZone != 0 || v.MaxMinutes != 0 {
		fmt.Fprintf(&u, "Limiti di oggi: max Z%d, max %d minuti\n", v.MaxZone, v.MaxMinutes)
	}
	if len(in.Verdict.DataGaps) > 0 {
		fmt.Fprintf(&u, "Dati mancanti: %s\n", strings.Join(in.Verdict.DataGaps, ", "))
	}
	u.WriteString("\nScrivi la lettura della giornata (2-4 frasi).")

	// Decision 10: NO cache_control on this tier; a single-shot 07:00 call
	// has nothing reading the cache within the TTL.
	return Run(ctx, n.Client, Request{
		Model:     n.Model,
		System:    morningSystem,
		UserText:  u.String(),
		MaxTokens: 1024,
	}, nil)
}
