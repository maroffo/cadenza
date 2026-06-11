// ABOUTME: Tests for baseline computation: mean/SD correctness, nil exclusion, sample floors.
// ABOUTME: Golden arithmetic checked against hand-computed values.

package job

import (
	"math"
	"testing"

	"github.com/maroffo/cadenza/internal/icu"
)

func wellnessWith(hrv []float64, rhr []int) []icu.Wellness {
	n := max(len(hrv), len(rhr))
	days := make([]icu.Wellness, n)
	for idx := range days {
		if idx < len(hrv) {
			v := hrv[idx]
			days[idx].HRV = &v
		}
		if idx < len(rhr) {
			v := rhr[idx]
			days[idx].RestingHR = &v
		}
	}
	return days
}

func repeatF(v float64, n int) []float64 {
	out := make([]float64, n)
	for idx := range out {
		out[idx] = v + float64(idx%5) // small spread so SD > 0
	}
	return out
}

func repeatI(v, n int) []int {
	out := make([]int, n)
	for idx := range out {
		out[idx] = v
	}
	return out
}

func TestComputeBaselines_HandComputed(t *testing.T) {
	// HRV samples 60,62,64,66,68 repeated 4x: mean 64, population SD sqrt(8).
	var hrv []float64
	for range 4 {
		hrv = append(hrv, 60, 62, 64, 66, 68)
	}
	days := wellnessWith(hrv, repeatI(47, 20))

	b, err := ComputeBaselines(days)
	if err != nil {
		t.Fatalf("ComputeBaselines: %v", err)
	}
	if math.Abs(b.HRVMean-64) > 1e-9 {
		t.Errorf("HRVMean = %v, want 64", b.HRVMean)
	}
	if math.Abs(b.HRVSD-math.Sqrt(8)) > 1e-9 {
		t.Errorf("HRVSD = %v, want sqrt(8)=%v", b.HRVSD, math.Sqrt(8))
	}
	if math.Abs(b.RestingHR-47) > 1e-9 {
		t.Errorf("RestingHR = %v, want 47", b.RestingHR)
	}
}

func TestComputeBaselines_NilsExcluded(t *testing.T) {
	days := wellnessWith(repeatF(60, 20), repeatI(47, 20))
	days = append(days, icu.Wellness{ID: "gap-day"}) // all nil, must not skew

	withGap, err := ComputeBaselines(days)
	if err != nil {
		t.Fatalf("ComputeBaselines: %v", err)
	}
	withoutGap, err := ComputeBaselines(days[:20])
	if err != nil {
		t.Fatalf("ComputeBaselines: %v", err)
	}
	if withGap != withoutGap {
		t.Errorf("nil day changed baselines: %+v vs %+v", withGap, withoutGap)
	}
}

func TestComputeBaselines_TooFewSamplesRejected(t *testing.T) {
	days := wellnessWith(repeatF(60, 5), repeatI(47, 20))
	if _, err := ComputeBaselines(days); err == nil {
		t.Fatal("5 HRV samples accepted; baseline on noise must be rejected")
	}
}

func TestComputeBaselines_ZeroSDRejected(t *testing.T) {
	flat := make([]float64, 20)
	for idx := range flat {
		flat[idx] = 60 // identical every day: device stuck
	}
	days := wellnessWith(flat, repeatI(47, 20))
	if _, err := ComputeBaselines(days); err == nil {
		t.Fatal("zero-SD HRV accepted; threshold math would divide meaning by zero")
	}
}
