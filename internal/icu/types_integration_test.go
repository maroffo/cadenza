// ABOUTME: Live typed-decode smoke: real wellness range decodes into pointer-field structs.
// ABOUTME: Opt-in: go test -tags=integration ./internal/icu/ -run Integration

//go:build integration

package icu

import (
	"context"
	"testing"
	"time"
)

func TestIntegration_WellnessTypedDecode(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newest := time.Now().Format("2006-01-02")
	oldest := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	raw, err := c.ListWellness(ctx, GetWellnessRangeParams{Oldest: oldest, Newest: newest})
	if err != nil {
		t.Fatalf("ListWellness: %v", err)
	}
	days, err := DecodeWellnessRange(raw)
	if err != nil {
		t.Fatalf("DecodeWellnessRange: %v", err)
	}
	if len(days) == 0 {
		// An empty week is a legitimate athlete state (vacation, new account),
		// not a decode failure; the decode above already succeeded.
		t.Skip("no wellness days in the last week; typed decode succeeded on empty payload")
	}
	for _, d := range days {
		if d.ID == "" {
			t.Errorf("wellness day with empty id: %+v", d)
		}
		t.Logf("%s hrv=%v restingHR=%v ctl=%v atl=%v ramp=%v",
			d.ID, fmtPtr(d.HRV), fmtIntPtr(d.RestingHR), fmtPtr(d.CTL), fmtPtr(d.ATL), fmtPtr(d.RampRate))
	}
}

func fmtPtr(f *float64) any {
	if f == nil {
		return "nil"
	}
	return *f
}

func fmtIntPtr(i *int) any {
	if i == nil {
		return "nil"
	}
	return *i
}
