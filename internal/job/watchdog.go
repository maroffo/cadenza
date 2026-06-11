// ABOUTME: The in-system dead-man layer: if today's morning run is missing, say so loudly.
// ABOUTME: The ERROR log feeds the Monitoring email alert; the bot message covers the athlete.

package job

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/maroffo/cadenza/internal/telegram"
)

type Watchdog struct {
	Runs RunStore
	Out  Messenger
	Now  func() time.Time
	TZ   *time.Location
}

func (w Watchdog) Run(ctx context.Context) error {
	today := w.Now().In(w.TZ).Format(dateLayout)

	done, err := w.Runs.MorningCompleted(ctx, today)
	if err != nil {
		return fmt.Errorf("watchdog: completion check: %w", err)
	}
	if done {
		slog.Info("watchdog: morning completed, all good", "date", today)
		return nil
	}

	// severity=ERROR is load-bearing: the Monitoring alert matches on it and
	// emails out-of-band (the bot must not be its own monitor).
	slog.Error("watchdog: morning check missing", "date", today)
	if err := w.Out.Send(ctx, telegram.WatchdogMissedMorning()); err != nil {
		return fmt.Errorf("watchdog: notify: %w", err)
	}
	return nil
}
