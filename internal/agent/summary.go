// ABOUTME: Session-rotation summarizer: cheap-tier bridge so a fresh session keeps the thread.
// ABOUTME: The summary is DATA seeded into the new session, clearly framed, never instructions.

package agent

import (
	"context"
	"fmt"
)

const summarySystem = `Riassumi questa conversazione di coaching sportivo in 5-8 punti
fattuali, in italiano, ciascuno su una riga che inizia con "- ".
Includi SOLO ciò che è stato detto: infortuni o dolori menzionati,
decisioni prese, allenamenti pianificati o scritti, preferenze espresse,
domande rimaste aperte. NON inventare nulla, NON aggiungere consigli o
istruzioni, NON riportare numeri di wellness (arrivano freschi dal
sistema a ogni conversazione).`

// Summarizer bridges session rotations on the cheap tier.
type Summarizer struct {
	Client Client
	Model  string
}

func (s Summarizer) Summarize(ctx context.Context, transcript string) (string, error) {
	if s.Model == "" {
		return "", fmt.Errorf("summarizer: model not configured")
	}
	res, err := Run(ctx, s.Client, Request{
		Model:     s.Model,
		System:    summarySystem,
		UserText:  transcript,
		MaxTokens: 512,
	}, nil)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
