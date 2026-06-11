// ABOUTME: Maps task envelopes to job runs; the executor's single dispatch table.
// ABOUTME: New task types register here as milestones add them.

package job

import (
	"context"
	"fmt"

	"github.com/maroffo/cadenza/internal/task"
)

type Deps struct {
	Morning  Morning
	Watchdog Watchdog
}

// Dispatch satisfies task.Dispatcher.
func (d Deps) Dispatch(ctx context.Context, env task.Envelope) error {
	switch env.Type {
	case task.TypeMorningCheck:
		return d.Morning.Run(ctx)
	case task.TypeWatchdog:
		return d.Watchdog.Run(ctx)
	default:
		// Unhandled type is poison: retrying cannot make a handler appear.
		return fmt.Errorf("dispatch: unhandled task type %q (id %s): %w", env.Type, env.ID, task.ErrPoison)
	}
}
