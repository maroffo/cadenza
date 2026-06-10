// ABOUTME: The work envelope every executor entry point speaks: versioned type + dedup id + payload.
// ABOUTME: Everything after M2 rides this shape; changes must stay additive (v bump).

package task

import (
	"context"
	"encoding/json"
	"fmt"
)

const EnvelopeVersion = 1

// Task types dispatched by the executor.
const (
	TypeTelegramUpdate = "telegram_update"
	TypeMorningCheck   = "morning_check"
	TypeInjuryWakeup   = "injury_wakeup"
	TypeDailyReconcile = "daily_reconcile"
	TypeWatchdog       = "watchdog"
)

type Envelope struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"` // deterministic; doubles as the dedup key
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (e Envelope) Validate() error {
	if e.V != EnvelopeVersion {
		return fmt.Errorf("envelope: unsupported version %d", e.V)
	}
	if e.Type == "" {
		return fmt.Errorf("envelope: empty type")
	}
	if e.ID == "" {
		return fmt.Errorf("envelope: empty id")
	}
	return nil
}

// Enqueuer abstracts Cloud Tasks so dev mode can run work in-process.
type Enqueuer interface {
	Enqueue(ctx context.Context, e Envelope) error
}

// Dispatcher executes an envelope; implemented by the job layer.
type Dispatcher func(ctx context.Context, e Envelope) error

// Local runs envelopes synchronously in-process: the dev-mode Enqueuer.
// Off Cloud Run there is no post-response CPU throttling to work around.
type Local struct {
	Dispatch Dispatcher
}

func (l Local) Enqueue(ctx context.Context, e Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	return l.Dispatch(ctx, e)
}
