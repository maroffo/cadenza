// ABOUTME: Adapts the copied icu client (raw JSON) to the typed WellnessSource interface.
// ABOUTME: Keeps the pristine client untouched; decoding lives with the consumer.

package job

import (
	"context"

	"github.com/maroffo/cadenza/internal/icu"
)

type ICU struct {
	C *icu.Client
}

func (a ICU) WellnessRange(ctx context.Context, oldest, newest string) ([]icu.Wellness, error) {
	raw, err := a.C.ListWellness(ctx, icu.GetWellnessRangeParams{Oldest: oldest, Newest: newest})
	if err != nil {
		return nil, err
	}
	return icu.DecodeWellnessRange(raw)
}

func (a ICU) ActivitiesRange(ctx context.Context, oldest, newest string) ([]icu.Activity, error) {
	raw, err := a.C.ListActivities(ctx, icu.ListActivitiesParams{Oldest: oldest, Newest: newest})
	if err != nil {
		return nil, err
	}
	return icu.DecodeActivities(raw)
}
