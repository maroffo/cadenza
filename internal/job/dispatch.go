// ABOUTME: Maps task envelopes to job runs; the executor's single dispatch table.
// ABOUTME: New task types register here as milestones add them.

package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/maroffo/cadenza/internal/task"
)

type Deps struct {
	Morning  Morning
	Watchdog Watchdog
	Message  Message
	Injury   InjuryJob
	Debrief  Debrief
}

// Dispatch satisfies task.Dispatcher.
func (d Deps) Dispatch(ctx context.Context, env task.Envelope) error {
	switch env.Type {
	case task.TypeMorningCheck:
		// The Scheduler's static body carries no payload (attempt 0); the
		// HRV self-retry envelopes carry their attempt number.
		var p morningPayload
		if len(env.Payload) > 0 {
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return fmt.Errorf("dispatch: bad morning payload (id %s): %w", env.ID, task.ErrPoison)
			}
		}
		return d.Morning.RunAttempt(ctx, p.Attempt)
	case task.TypeWatchdog:
		return d.Watchdog.Run(ctx)
	case task.TypeTelegramUpdate:
		return d.Message.Run(ctx, env)
	case task.TypeInjuryWakeup:
		return d.Injury.Wakeup(ctx, env)
	case task.TypeDailyReconcile:
		return d.Injury.Reconcile(ctx)
	case task.TypeDailyDebrief:
		return d.Debrief.Sweep(ctx)
	default:
		// Unhandled type is poison: retrying cannot make a handler appear.
		return fmt.Errorf("dispatch: unhandled task type %q (id %s): %w", env.Type, env.ID, task.ErrPoison)
	}
}
