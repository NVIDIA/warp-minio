/*
 * Warp (C) 2019-2025 MinIO, Inc.
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
	"encoding/json"
	"math"
	"time"
)

// Generic, unit-agnostic histogram core and configuration.

// Section describes a contiguous log2 section with a specific bucket density.
type Section struct {
	log2Range       int // number of log2 units covered by this section
	perLog2Fraction int // number of buckets per log2 unit (granularity)
}

// BucketConfig configures the bucket layout.
type BucketConfig struct {
	log2Start float64
	sections  []Section
}

func (c BucketConfig) TotalBuckets() int {
	b := 0
	for _, s := range c.sections {
		b += s.log2Range * s.perLog2Fraction
	}
	return b
}

// ResolveIndex maps a positive value in core units into a bucket index.
// Value x should already be scaled to the core's base unit for this config.
func (c BucketConfig) ResolveIndex(x float64) int {
	if x <= 0 {
		return 0
	}
	log2Val := math.Log2(x)
	idxBase := 0
	bound := c.log2Start
	for _, s := range c.sections {
		next := bound + float64(s.log2Range)
		if log2Val <= next {
			rel := log2Val - bound
			idx := idxBase + int(rel*float64(s.perLog2Fraction))
			// clamp inside section
			maxIdx := idxBase + s.log2Range*s.perLog2Fraction - 1
			if idx < idxBase {
				idx = idxBase
			}
			if idx > maxIdx {
				idx = maxIdx
			}
			return idx
		}
		// move to next section
		idxBase += s.log2Range * s.perLog2Fraction
		bound = next
	}
	// beyond last section: clamp to last bucket
	total := c.TotalBuckets()
	if total == 0 {
		return 0
	}
	return total - 1
}

// UpperBound returns the numeric upper bound for the given bucket index in core units.
func (c BucketConfig) UpperBound(idx int) float64 {
	if idx < 0 {
		idx = 0
	}
	total := c.TotalBuckets()
	if total == 0 {
		return 0
	}
	if idx >= total {
		idx = total - 1
	}
	// walk sections to find local index
	idxBase := 0
	bound := c.log2Start
	for _, s := range c.sections {
		count := s.log2Range * s.perLog2Fraction
		if idx < idxBase+count {
			step := 1.0 / float64(s.perLog2Fraction)
			rel := idx - idxBase
			log2 := bound + float64(rel+1)*step
			return math.Pow(2, log2)
		}
		idxBase += count
		bound += float64(s.log2Range)
	}
	// last bucket upper bound
	last := c.sections[len(c.sections)-1]
	step := 1.0 / float64(last.perLog2Fraction)
	log2 := bound + float64(last.log2Range)*step
	return math.Pow(2, log2)
}

// HistogramCore stores bucket counts per BucketConfig.
type HistogramCore struct {
	counts []uint64
	total  uint64
	cfg    BucketConfig
}

func NewHistogramCore(cfg BucketConfig) HistogramCore {
	return HistogramCore{counts: make([]uint64, cfg.TotalBuckets()), cfg: cfg}
}

func (h *HistogramCore) AddIndex(idx int) {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(h.counts) {
		idx = len(h.counts) - 1
	}
	h.counts[idx]++
	h.total++
}

func (h *HistogramCore) Merge(other HistogramCore) {
	// If destination is empty, clone other's layout and counts.
	if len(h.counts) == 0 && len(other.counts) > 0 {
		h.cfg = other.cfg
		h.counts = make([]uint64, len(other.counts))
		copy(h.counts, other.counts)
		h.total = other.total
		return
	}
	if len(h.counts) != len(other.counts) {
		// incompatible configs; do nothing
		return
	}
	for i := range h.counts {
		h.counts[i] += other.counts[i]
	}
	h.total += other.total
}

func (h HistogramCore) QuantileIndex(p float64) int {
	if h.total == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	target := uint64(math.Ceil(float64(h.total) * p))
	var c uint64
	for i, v := range h.counts {
		c += v
		if c >= target {
			return i
		}
	}
	return len(h.counts) - 1
}

func (h HistogramCore) ToSparse() []SparseBucket {
	out := make([]SparseBucket, 0, 8)
	for i, v := range h.counts {
		if v != 0 {
			out = append(out, SparseBucket{Idx: i, Count: v})
		}
	}
	return out
}

func (h *HistogramCore) FromSparse(s []SparseBucket) {
	for _, b := range s {
		if b.Idx >= 0 && b.Idx < len(h.counts) && b.Count > 0 {
			h.counts[b.Idx] += b.Count
			h.total += b.Count
		}
	}
}

// Latency histogram wrapper (microseconds scaled by baseUnitMultiplier)
//
// Base unit is time.Microsecond scaled by baseUnitMultiplier. Buckets
// are laid out in three contiguous log2 sections with different granularity
// to provide higher resolution in the most interesting ranges.

const baseUnitMultiplier = 10 // Scale factor for the microsecond base unit

// SparseBucket is a compact representation of a non-empty bucket.
type SparseBucket struct {
	Idx   int    `json:"idx"`
	Count uint64 `json:"n"`
}

// LatencySparseBucket is a sparse bucket for latency histograms with both index and milliseconds.
// The Idx field is used for accurate reconstruction; Millis is calculated for human readability.
type LatencySparseBucket struct {
	Idx    int     `json:"idx"`
	Millis float64 `json:"millis"`
	Count  uint64  `json:"n"`
}

// BpsSparseBucket is a sparse bucket for BPS histograms with both index and bytes-per-second.
// The Idx field is used for accurate reconstruction; Bps is calculated for human readability.
type BpsSparseBucket struct {
	Idx   int     `json:"idx"`
	Bps   float64 `json:"bps"`
	Count uint64  `json:"n"`
}

// LatencyHistogram wrapper
type LatencyHistogram struct {
	core HistogramCore
}

func NewLatencyHistogram() LatencyHistogram {
	// 3-Section Variable Precision Histogram
	// ====================================
	// Low section:  7 log2 units × 1 buckets/unit = 7 buckets
	// Med section:  10 log2 units × 7 buckets/unit = 70 buckets
	// High section:  6 log2 units × 6 buckets/unit = 36 buckets
	// Total buckets: 113
	// Base unit: tens of microseconds (10μs)
	//
	// Section log2 ranges:
	// Low:  1.0 to 8.0 (precision: 1/1 = 1.0)
	// Med:  8.0 to 18.0 (precision: 1/7 = 0.14285714285714285)
	// High:  18.0 to 24.0 (precision: 1/6 = 0.16666666666666666)
	//
	// KEY MILESTONES
	// ============================================================
	// First bucket >= 0.100 ms: bucket 2 Low[2] (0.160 ms)
	// First bucket >= 1.000 ms: bucket 5 Low[5] (1.280 ms)
	// First bucket >= 10.000 ms: bucket 20 Med[13] (10.240 ms)
	// First bucket >= 100.000 ms: bucket 44 Med[37] (110.256 ms)
	// First bucket >= 1000.000 ms: bucket 67 Med[60] (1075.230 ms)
	// First bucket >= 10000.000 ms: bucket 88 High[11] (10485.760 ms)
	// First bucket >= 100000.000 ms: bucket 108 High[31] (105689.838 ms)
	//
	// Section ranges:
	// Low:  0.040 ms to 2.560 ms (7 buckets)
	// Med:  2.826 ms to 2621.440 ms (70 buckets)
	// High:  2942.467 ms to 167772.160 ms (36 buckets)
	cfg := BucketConfig{
		log2Start: 1.0,
		sections: []Section{
			{log2Range: 7, perLog2Fraction: 1},
			{log2Range: 10, perLog2Fraction: 7},
			{log2Range: 6, perLog2Fraction: 6},
		},
	}
	return LatencyHistogram{core: NewHistogramCore(cfg)}
}

func (h *LatencyHistogram) AddDuration(d time.Duration) {
	usUnits := (float64(d) / float64(time.Microsecond)) / baseUnitMultiplier
	idx := h.core.cfg.ResolveIndex(usUnits)
	h.core.AddIndex(idx)
}

func (h *LatencyHistogram) Merge(other LatencyHistogram) {
	h.core.Merge(other.core)
}

func (h LatencyHistogram) Quantile(p float64) time.Duration {
	idx := h.core.QuantileIndex(p)
	upper := h.core.cfg.UpperBound(idx) * baseUnitMultiplier
	return time.Duration(upper) * time.Microsecond
}

func (h LatencyHistogram) Samples() uint64 { return h.core.total }

// MarshalJSON implements json.Marshaler, serializing to sparse format with both index and millis.
func (h LatencyHistogram) MarshalJSON() ([]byte, error) {
	sparse := h.core.ToSparse()
	if len(sparse) == 0 {
		return json.Marshal([]LatencySparseBucket{})
	}
	out := make([]LatencySparseBucket, len(sparse))
	for i, b := range sparse {
		upper := h.core.cfg.UpperBound(b.Idx) * baseUnitMultiplier // in microseconds
		dur := time.Duration(upper) * time.Microsecond
		out[i] = LatencySparseBucket{
			Idx:    b.Idx,
			Millis: float64(dur) / float64(time.Millisecond),
			Count:  b.Count,
		}
	}
	return json.Marshal(out)
}

// UnmarshalJSON implements json.Unmarshaler, deserializing from sparse format.
// Only uses Idx field for reconstruction; Millis is ignored.
func (h *LatencyHistogram) UnmarshalJSON(data []byte) error {
	// Initialize with default config if empty
	if len(h.core.counts) == 0 {
		*h = NewLatencyHistogram()
	}
	var sparse []LatencySparseBucket
	if err := json.Unmarshal(data, &sparse); err != nil {
		return err
	}
	// Convert to SparseBucket (using only Idx and Count)
	buckets := make([]SparseBucket, len(sparse))
	for i, b := range sparse {
		buckets[i] = SparseBucket{Idx: b.Idx, Count: b.Count}
	}
	h.core.FromSparse(buckets)
	return nil
}

// BpsHistogram for per-operation throughput (bytes/second)
type BpsHistogram struct {
	core HistogramCore
}

func NewBpsHistogram() BpsHistogram {
	// Coverage ~ 1 B/s -> ~ 2^(37) B/s (~137 GB/s). Tunable later.
	// 3-Section Variable Precision Throughput Histogram
	// ====================================
	// Low section:  13 log2 units × 1 buckets/unit = 13 buckets
	// Med section:  18 log2 units × 5 buckets/unit = 90 buckets
	// High section:  5 log2 units × 2 buckets/unit = 10 buckets
	// Total buckets: 113
	//
	// Section log2 ranges:
	// Low:  0.0 to 13.0 (precision: 1/1 = 1.0)
	// Med:  13.0 to 31.0 (precision: 1/5 = 0.2)
	// High:  31.0 to 36.0 (precision: 1/2 = 0.5)
	//
	// KEY MILESTONES
	// ============================================================
	// First bucket >= 1 kB/s: bucket 9 Low[9] (1.024 kB/s)
	// First bucket >= 1 MB/s: bucket 47 Med[34] (1.049 MB/s)
	// First bucket >= 100 MB/s: bucket 80 Med[67] (101.7 MB/s)
	// First bucket >= 1 GB/s: bucket 97 Med[84] (1.074 GB/s)
	// First bucket >= 10 GB/s: bucket 107 High[4] (12.15 GB/s)
	// First bucket >= 40 GB/s: bucket 111 High[8] (48.59 GB/s)
	//
	// Section ranges:
	// Low:  2 B/s to 8.192 kB/s (13 buckets)
	// Med:  9.41 kB/s to 2.147 GB/s (90 buckets)
	// High:  3.037 GB/s to 68.72 GB/s (10 buckets)
	cfg := BucketConfig{
		log2Start: 0,
		sections: []Section{
			{log2Range: 13, perLog2Fraction: 1},
			{log2Range: 18, perLog2Fraction: 5},
			{log2Range: 5, perLog2Fraction: 2},
		},
	}
	return BpsHistogram{core: NewHistogramCore(cfg)}
}

func (h *BpsHistogram) AddBps(bps float64) {
	if bps <= 0 {
		return
	}
	idx := h.core.cfg.ResolveIndex(bps)
	h.core.AddIndex(idx)
}

func (h *BpsHistogram) Merge(other BpsHistogram) { h.core.Merge(other.core) }
func (h BpsHistogram) Quantile(p float64) float64 {
	idx := h.core.QuantileIndex(p)
	return h.core.cfg.UpperBound(idx)
}
func (h BpsHistogram) Samples() uint64 { return h.core.total }

// MarshalJSON implements json.Marshaler, serializing to sparse format with both index and bps.
func (h BpsHistogram) MarshalJSON() ([]byte, error) {
	sparse := h.core.ToSparse()
	if len(sparse) == 0 {
		return json.Marshal([]BpsSparseBucket{})
	}
	out := make([]BpsSparseBucket, len(sparse))
	for i, b := range sparse {
		upper := h.core.cfg.UpperBound(b.Idx)
		out[i] = BpsSparseBucket{
			Idx:   b.Idx,
			Bps:   upper,
			Count: b.Count,
		}
	}
	return json.Marshal(out)
}

// UnmarshalJSON implements json.Unmarshaler, deserializing from sparse format.
// Only uses Idx field for reconstruction; Bps is ignored.
func (h *BpsHistogram) UnmarshalJSON(data []byte) error {
	// Initialize with default config if empty
	if len(h.core.counts) == 0 {
		*h = NewBpsHistogram()
	}
	var sparse []BpsSparseBucket
	if err := json.Unmarshal(data, &sparse); err != nil {
		return err
	}
	// Convert to SparseBucket (using only Idx and Count)
	buckets := make([]SparseBucket, len(sparse))
	for i, b := range sparse {
		buckets[i] = SparseBucket{Idx: b.Idx, Count: b.Count}
	}
	h.core.FromSparse(buckets)
	return nil
}
