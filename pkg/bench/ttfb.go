/*
 * Warp (C) 2019-2020 MinIO, Inc.
 * Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package bench

import (
	"fmt"
	"math"
	"time"
)

// TTFB contains times to first byte if applicable.
type TTFB struct {
	AverageMillis float64 `json:"average_millis"`
	FastestMillis float64 `json:"fastest_millis"`
	P25Millis     float64 `json:"p25_millis"`
	MedianMillis  float64 `json:"median_millis"`
	P75Millis     float64 `json:"p75_millis"`
	P90Millis     float64 `json:"p90_millis"`
	P99Millis     float64 `json:"p99_millis"`
	SlowestMillis float64 `json:"slowest_millis"`
	StdDevMillis  float64 `json:"std_dev_millis"`
	// Histogram of TTFB values; automatically serialized to sparse format.
	Hist LatencyHistogram `json:"ttfb_hist,omitempty"`
	// Running sums for precise avg/stddev across merges.
	SumMillis   float64 `json:"sum_millis,omitempty"`
	SumSqMillis float64 `json:"sum_sq_millis,omitempty"`
}

// String returns a human printable version of the time to first byte.
func (t TTFB) String() string {
	if t.AverageMillis == 0 {
		return ""
	}
	fMilli := float64(time.Millisecond)
	return fmt.Sprintf("Avg: %v, Best: %v, 25th: %v, Median: %v, 75th: %v, 90th: %v, 99th: %v, Worst: %v, StdDev: %v",
		time.Duration(t.AverageMillis*fMilli).Round(time.Millisecond),
		time.Duration(t.FastestMillis*fMilli).Round(time.Millisecond),
		time.Duration(t.P25Millis*fMilli).Round(time.Millisecond),
		time.Duration(t.MedianMillis*fMilli).Round(time.Millisecond),
		time.Duration(t.P75Millis*fMilli).Round(time.Millisecond),
		time.Duration(t.P90Millis*fMilli).Round(time.Millisecond),
		time.Duration(t.P99Millis*fMilli).Round(time.Millisecond),
		time.Duration(t.SlowestMillis*fMilli).Round(time.Millisecond),
		time.Duration(t.StdDevMillis*fMilli).Round(time.Millisecond))
}

// updateDerivedStats recomputes all derived TTFB statistics from source fields
// (SumMillis, SumSqMillis, Hist).
func (t *TTFB) updateDerivedStats() {
	n := float64(t.Hist.Samples())
	if n > 0 {
		t.AverageMillis = t.SumMillis / n
		if n > 1 {
			variance := (t.SumSqMillis - (t.SumMillis*t.SumMillis)/n) / (n - 1)
			if variance < 0 {
				variance = 0
			}
			t.StdDevMillis = math.Sqrt(variance)
		} else {
			t.StdDevMillis = 0
		}
		t.P25Millis = durToMillisF(t.Hist.Quantile(0.25))
		t.MedianMillis = durToMillisF(t.Hist.Quantile(0.5))
		t.P75Millis = durToMillisF(t.Hist.Quantile(0.75))
		t.P90Millis = durToMillisF(t.Hist.Quantile(0.9))
		t.P99Millis = durToMillisF(t.Hist.Quantile(0.99))
	}
}

// Merge merges another TTFB into this one.
func (t *TTFB) Merge(other TTFB) {
	// Merge precise sums
	t.SumMillis += other.SumMillis
	t.SumSqMillis += other.SumSqMillis
	if other.FastestMillis != 0 {
		// Deal with 0 value being the best always.
		t.FastestMillis = min(t.FastestMillis, other.FastestMillis)
		if t.FastestMillis == 0 {
			t.FastestMillis = other.FastestMillis
		}
	}
	t.SlowestMillis = max(t.SlowestMillis, other.SlowestMillis)

	// Merge histograms
	t.Hist.Merge(other.Hist)
	// Recompute all derived stats from merged sums and histogram
	t.updateDerivedStats()
}

// TtfbFromOps builds a TTFB structure from operations for a given time range,
// computing both summary fields and the histogram. This is preferred for live
// paths where we want lossless merging via histogram buckets.
func TtfbFromOps(ops Operations, start, end time.Time) *TTFB {
	if start.After(end) || start.Equal(end) {
		return nil
	}
	filtered := ops.FilterByHasTTFB(true).FilterInsideRange(start, end)
	if len(filtered) == 0 {
		return nil
	}
	// Initialize TTFB and histogram
	t := &TTFB{Hist: NewLatencyHistogram()}
	for _, op := range filtered {
		d := op.TTFB()
		t.Hist.AddDuration(d)
		ms := durToMillisF(d)
		t.SumMillis += ms
		t.SumSqMillis += ms * ms
		if t.FastestMillis == 0 || ms < t.FastestMillis {
			t.FastestMillis = ms
		}
		if ms > t.SlowestMillis {
			t.SlowestMillis = ms
		}
	}
	// Compute all derived stats from sums and histogram
	t.updateDerivedStats()
	return t
}

// TTFBCmp is a comparison between two TTFB runs.
type TTFBCmp struct {
	TTFB
	Before, After TTFB
}

// Compare compares this TTFB with another and returns comparison stats.
func (t TTFB) Compare(after TTFB) *TTFBCmp {
	if t.AverageMillis == 0 {
		return nil
	}
	return &TTFBCmp{
		TTFB: TTFB{
			AverageMillis: after.AverageMillis - t.AverageMillis,
			SlowestMillis: after.SlowestMillis - t.SlowestMillis,
			FastestMillis: after.FastestMillis - t.FastestMillis,
			MedianMillis:  after.MedianMillis - t.MedianMillis,
			P25Millis:     after.P25Millis - t.P25Millis,
			P75Millis:     after.P75Millis - t.P75Millis,
			P90Millis:     after.P90Millis - t.P90Millis,
			P99Millis:     after.P99Millis - t.P99Millis,
			StdDevMillis:  after.StdDevMillis - t.StdDevMillis,
		},
		Before: t,
		After:  after,
	}
}

// String returns a human readable representation of the TTFB comparison.
func (t *TTFBCmp) String() string {
	if t == nil {
		return ""
	}
	plusPositiveF := func(f float64) string {
		if f > 0 {
			return "+"
		}
		return ""
	}
	return fmt.Sprintf("Avg: %s%.1fms (%s%.f%%), P50: %s%.1fms (%s%.f%%), P99: %s%.1fms (%s%.f%%), Best: %s%.1fms (%s%.f%%), Worst: %s%.1fms (%s%.f%%) StdDev: %s%.1fms (%s%.f%%)",
		plusPositiveF(t.AverageMillis),
		t.AverageMillis,
		plusPositiveF(t.AverageMillis),
		100*(t.After.AverageMillis-t.Before.AverageMillis)/t.Before.AverageMillis,
		plusPositiveF(t.MedianMillis),
		t.MedianMillis,
		plusPositiveF(t.MedianMillis),
		100*(t.After.MedianMillis-t.Before.MedianMillis)/t.Before.MedianMillis,
		plusPositiveF(t.P99Millis),
		t.P99Millis,
		plusPositiveF(t.P99Millis),
		100*(t.After.P99Millis-t.Before.P99Millis)/t.Before.P99Millis,
		plusPositiveF(t.FastestMillis),
		t.FastestMillis,
		plusPositiveF(t.FastestMillis),
		100*(t.After.FastestMillis-t.Before.FastestMillis)/t.Before.FastestMillis,
		plusPositiveF(t.SlowestMillis),
		t.SlowestMillis,
		plusPositiveF(t.SlowestMillis),
		100*(t.After.SlowestMillis-t.Before.SlowestMillis)/t.Before.SlowestMillis,
		plusPositiveF(t.StdDevMillis),
		t.StdDevMillis,
		plusPositiveF(t.StdDevMillis),
		100*(t.After.StdDevMillis-t.Before.StdDevMillis)/t.Before.StdDevMillis,
	)
}

func durToMillisF(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
