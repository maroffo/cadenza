// ABOUTME: Post-workout debrief narrator: cheap tier, evidence-based, two short paragraphs.
// ABOUTME: The numbers arrive computed; the model comments, it never recalculates.

package agent

import (
	"context"
	"fmt"
)

const debriefSystem = `Sei il coach di resistenza di MAx. Hai davanti il
confronto GIA' CALCOLATO tra allenamento prescritto ed eseguito (più i
segnali soggettivi se presenti). Scrivi un debrief in italiano, massimo due
paragrafi brevi: cosa è andato come previsto, cosa no e perché può essere
successo, un'indicazione per il recupero o per domani. Tono asciutto e
concreto, mai trionfalistico. NON ricalcolare i numeri: commenta quelli che
ricevi. Se percepito (RPE) e oggettivo divergono, dillo: è il segnale più
utile. Formato: testo semplice, al massimo <b>...</b> per il grassetto.`

// Debriefer wraps the cheap tier for post-workout commentary.
type Debriefer struct {
	Client Client
	Model  string
}

func (d Debriefer) Narrate(ctx context.Context, dataBlock string) (string, error) {
	if d.Model == "" {
		return "", fmt.Errorf("debriefer: model not configured")
	}
	res, err := Run(ctx, d.Client, Request{
		Model:     d.Model,
		System:    debriefSystem,
		UserText:  dataBlock,
		MaxTokens: 700,
	}, nil)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
