// ABOUTME: Computes personal baselines (HRV mean/SD, resting HR) from wellness history.
// ABOUTME: Pure arithmetic in Go per the spec's failure-mode table; used by cmd/seed.

package job

import (
	"fmt"
	"math"

	"github.com/maroffo/cadenza/internal/icu"
	"github.com/maroffo/cadenza/internal/verdict"
)

// minBaselineSamples guards against coaching on a baseline made of noise.
const minBaselineSamples = 14

// ComputeBaselines derives baselines from a wellness window (typically 30-60
// days). Nil metrics are excluded, never counted as zero.
func ComputeBaselines(days []icu.Wellness) (verdict.Baselines, error) {
	var hrv, rhr []float64
	for _, d := range days {
		if d.HRV != nil {
			hrv = append(hrv, *d.HRV)
		}
		if d.RestingHR != nil {
			rhr = append(rhr, float64(*d.RestingHR))
		}
	}
	if len(hrv) < minBaselineSamples {
		return verdict.Baselines{}, fmt.Errorf("baselines: only %d HRV samples, need %d", len(hrv), minBaselineSamples)
	}
	if len(rhr) < minBaselineSamples {
		return verdict.Baselines{}, fmt.Errorf("baselines: only %d resting HR samples, need %d", len(rhr), minBaselineSamples)
	}

	hrvMean, hrvSD := meanSD(hrv)
	rhrMean, _ := meanSD(rhr)
	if hrvSD == 0 {
		return verdict.Baselines{}, fmt.Errorf("baselines: HRV standard deviation is zero across %d samples (device stuck?)", len(hrv))
	}
	return verdict.Baselines{HRVMean: hrvMean, HRVSD: hrvSD, RestingHR: rhrMean}, nil
}

func meanSD(xs []float64) (mean, sd float64) {
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	sd = math.Sqrt(ss / float64(len(xs)))
	return mean, sd
}
