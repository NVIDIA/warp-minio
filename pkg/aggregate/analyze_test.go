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

package aggregate

import (
	"encoding/json"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/minio/warp/pkg/bench"
)

func genOps(durs, ttfbs []time.Duration) bench.Operations {
	now := time.Now()
	ops := make(bench.Operations, 0, len(durs))
	for i, d := range durs {
		start := now.Add(time.Duration(i) * time.Second)
		end := start.Add(d)
		var fb *time.Time
		if i < len(ttfbs) && ttfbs[i] > 0 {
			t := start.Add(ttfbs[i])
			fb = &t
		}
		ops = append(ops, bench.Operation{
			Start:     start,
			End:       end,
			FirstByte: fb,
			OpType:    "GET",
			Endpoint:  "host1",
			ClientID:  "c1",
			ObjPerOp:  1,
			Size:      1024,
		})
	}
	return ops
}

func approxEq(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestSingleSizedRequests_MergeAccurate(t *testing.T) {
	// Two clients with different latency distributions
	opsA := genOps([]time.Duration{
		10 * time.Millisecond,
		12 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
	}, nil)
	opsB := genOps([]time.Duration{
		15 * time.Millisecond,
		18 * time.Millisecond,
		22 * time.Millisecond,
		28 * time.Millisecond,
		35 * time.Millisecond,
	}, nil)

	a := SingleSizedRequests{}
	a.fill(opsA)
	b := SingleSizedRequests{}
	b.fill(opsB)

	merged := a
	merged.add(b)

	// Ground truth from combined ops
	all := append(bench.Operations{}, opsA...)
	all = append(all, opsB...)

	// Avg
	var sum float64
	var minV, maxV float64
	for i, op := range all {
		ms := float64(op.Duration()) / float64(time.Millisecond)
		sum += ms
		if i == 0 || ms < minV {
			minV = ms
		}
		if ms > maxV {
			maxV = ms
		}
	}
	avg := sum / float64(len(all))

	// Stddev
	var ss float64
	for _, op := range all {
		ms := float64(op.Duration()) / float64(time.Millisecond)
		ss += (ms - avg) * (ms - avg)
	}
	std := math.Sqrt(ss / float64(len(all)-1))

	if !approxEq(merged.DurAvgMillis, avg, 0.05) {
		t.Fatalf("avg mismatch: got=%.3f want=%.3f", merged.DurAvgMillis, avg)
	}
	if !approxEq(merged.StdDev, std, 0.1) {
		t.Fatalf("stddev mismatch: got=%.3f want=%.3f", merged.StdDev, std)
	}
	if !approxEq(merged.FastestMillis, minV, 0.01) {
		t.Fatalf("min mismatch: got=%.3f want=%.3f", merged.FastestMillis, minV)
	}
	if !approxEq(merged.SlowestMillis, maxV, 0.01) {
		t.Fatalf("max mismatch: got=%.3f want=%.3f", merged.SlowestMillis, maxV)
	}

	// Percentiles via histogram should be close to actuals upper-bounded
	// Median should be around 22-28ms given inputs
	if merged.DurMedianMillis < 20 || merged.DurMedianMillis > 30 {
		t.Fatalf("median out of expected range: got=%.3fms", merged.DurMedianMillis)
	}
}

func TestTTFB_MergeAccurate_AndJSONOmit(t *testing.T) {
	ops := genOps([]time.Duration{
		20 * time.Millisecond,
		24 * time.Millisecond,
		30 * time.Millisecond,
	}, []time.Duration{
		5 * time.Millisecond,
		10 * time.Millisecond,
		8 * time.Millisecond,
	})
	start, end := ops.TimeRange()
	ttfb := bench.TtfbFromOps(ops, start, end)
	if ttfb == nil {
		t.Fatal("expected TTFB")
	}
	// Validate scalars populated
	if ttfb.AverageMillis <= 0 || ttfb.MedianMillis <= 0 || ttfb.P90Millis <= 0 {
		t.Fatal("expected scalar TTFB fields populated")
	}
	// Ensure we do not serialize PercentilesMillis
	b, err := json.Marshal(ttfb)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || string(b) == "null" {
		t.Fatal("unexpected empty JSON")
	}
	if jsonContainsKey(b, "percentiles_millis") {
		t.Fatal("PercentilesMillis must be omitted in JSON")
	}
}

func TestRequestSizeRange_BpsHistogramMergeAccurate(t *testing.T) {
	// Two sets with different B/s distributions
	opsA := genOps([]time.Duration{20 * time.Millisecond, 25 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond}, nil)
	// Scale sizes to create distinct bps: mix of sizes
	for i := range opsA {
		opsA[i].Size = int64(1<<20) * int64(i+1) // 1MiB, 2MiB, 3MiB, 4MiB
	}
	opsB := genOps([]time.Duration{15 * time.Millisecond, 18 * time.Millisecond, 22 * time.Millisecond, 28 * time.Millisecond}, nil)
	for i := range opsB {
		opsB[i].Size = int64(1<<20) * int64(i+2) // 2MiB, 3MiB, 4MiB, 5MiB
	}

	// Build ranges
	segA := bench.SizeSegment{Ops: opsA}
	segB := bench.SizeSegment{Ops: opsB}
	var rA, rB RequestSizeRange
	rA.fill(segA)
	rB.fill(segB)

	// Merge and compute ground truth from union (per-op bps)
	rM := rA
	rM.add(rB)
	// Ground truth percentiles and stddev
	all := append(bench.Operations{}, opsA...)
	all = append(all, opsB...)
	// Compute per-op bps and sort
	bpsVals := make([]float64, 0, len(all))
	var sumBytes int64
	var sumDur time.Duration
	var sumBps, sumSqBps float64
	for _, op := range all {
		if op.Duration() <= 0 || op.Size <= 0 {
			continue
		}
		sumBytes += op.Size
		sumDur += op.Duration()
		bps := float64(op.Size) * float64(time.Second) / float64(op.Duration())
		bpsVals = append(bpsVals, bps)
		sumBps += bps
		sumSqBps += bps * bps
	}
	sort.Float64s(bpsVals)
	getPct := func(p float64) float64 {
		if len(bpsVals) == 0 {
			return 0
		}
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		idx := int(math.Ceil(float64(len(bpsVals))*p)) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(bpsVals) {
			idx = len(bpsVals) - 1
		}
		return bpsVals[idx]
	}
	gt50 := getPct(0.5)
	gt90 := getPct(0.9)
	gt99 := getPct(0.99)
	// Average computed from total bytes / total duration
	var gtAvg float64
	if sumDur > 0 {
		gtAvg = float64(sumBytes) * float64(time.Second) / float64(sumDur)
	}

	// Assert merged values are close (within bucket resolution; be generous)
	// These are bucket-edge approximations; allow +20% tolerance
	tol := 0.20
	if math.Abs(rM.BpsMedian-gt50)/gt50 > tol {
		t.Fatalf("median bps mismatch: got=%.2f want≈%.2f", rM.BpsMedian, gt50)
	}
	if math.Abs(rM.Bps90-gt90)/gt90 > tol {
		t.Fatalf("p90 bps mismatch: got=%.2f want≈%.2f", rM.Bps90, gt90)
	}
	if math.Abs(rM.Bps99-gt99)/gt99 > tol {
		t.Fatalf("p99 bps mismatch: got=%.2f want≈%.2f", rM.Bps99, gt99)
	}
	if gtAvg > 0 && math.Abs(rM.BpsAverage-gtAvg)/gtAvg > tol {
		t.Fatalf("avg bps mismatch: got=%.2f want≈%.2f", rM.BpsAverage, gtAvg)
	}

	// Stddev from per-op bps
	if len(bpsVals) > 1 {
		n := float64(len(bpsVals))
		varVar := (sumSqBps - (sumBps*sumBps)/n) / (n - 1)
		if varVar < 0 {
			varVar = 0
		}
		gtStd := math.Sqrt(varVar)
		// Stddev can be more sensitive to bucket edges; allow 30% tolerance
		if gtStd > 0 && math.Abs(rM.BpsStdDev-gtStd)/gtStd > 0.30 {
			t.Fatalf("stddev bps mismatch: got=%.2f want≈%.2f", rM.BpsStdDev, gtStd)
		}
	}

	// JSON should include bps_std_dev
	b, err := json.Marshal(rM)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonContainsKey(b, "bps_std_dev") {
		t.Fatal("bps_std_dev missing in JSON")
	}
}

func jsonContainsKey(b []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// TestJSONRoundTrip_HistMergesByIndex validates that histograms are automatically
// serialized/deserialized and can be merged across processes.
func TestJSONRoundTrip_HistMergesByIndex(t *testing.T) {
	// Build two artificial SingleSizedRequests with simple distributions
	a := SingleSizedRequests{}
	a.DurHist = bench.NewLatencyHistogram()
	a.DurHist.AddDuration(200 * time.Millisecond)
	a.DurHist.AddDuration(250 * time.Millisecond)
	a.Requests = 2
	a.DurSumMillis = durToMillisF(200*time.Millisecond) + durToMillisF(250*time.Millisecond)
	a.DurSumSqMillis = durToMillisF(200*time.Millisecond)*durToMillisF(200*time.Millisecond) + durToMillisF(250*time.Millisecond)*durToMillisF(250*time.Millisecond)
	// TTFB for A
	ta := &bench.TTFB{}
	ta.Hist = bench.NewLatencyHistogram()
	ta.Hist.AddDuration(50 * time.Millisecond)
	ta.Hist.AddDuration(75 * time.Millisecond)
	ta.SumMillis = durToMillisF(50*time.Millisecond) + durToMillisF(75*time.Millisecond)
	ta.SumSqMillis = durToMillisF(50*time.Millisecond)*durToMillisF(50*time.Millisecond) + durToMillisF(75*time.Millisecond)*durToMillisF(75*time.Millisecond)
	a.FirstByte = ta

	b := SingleSizedRequests{}
	b.DurHist = bench.NewLatencyHistogram()
	b.DurHist.AddDuration(300 * time.Millisecond)
	b.DurHist.AddDuration(350 * time.Millisecond)
	b.Requests = 2
	b.DurSumMillis = durToMillisF(300*time.Millisecond) + durToMillisF(350*time.Millisecond)
	b.DurSumSqMillis = durToMillisF(300*time.Millisecond)*durToMillisF(300*time.Millisecond) + durToMillisF(350*time.Millisecond)*durToMillisF(350*time.Millisecond)
	// TTFB for B
	tb := &bench.TTFB{}
	tb.Hist = bench.NewLatencyHistogram()
	tb.Hist.AddDuration(60 * time.Millisecond)
	tb.Hist.AddDuration(90 * time.Millisecond)
	tb.SumMillis = durToMillisF(60*time.Millisecond) + durToMillisF(90*time.Millisecond)
	tb.SumSqMillis = durToMillisF(60*time.Millisecond)*durToMillisF(60*time.Millisecond) + durToMillisF(90*time.Millisecond)*durToMillisF(90*time.Millisecond)
	b.FirstByte = tb

	// Simulate client JSON encode → coordinator decode
	// Histograms are now automatically serialized/deserialized via MarshalJSON/UnmarshalJSON
	encA, _ := json.Marshal(a)
	encB, _ := json.Marshal(b)
	var decA, decB SingleSizedRequests
	_ = json.Unmarshal(encA, &decA)
	_ = json.Unmarshal(encB, &decB)

	// Merge and validate non-zero samples and percentile sanity
	merged := decA
	merged.add(decB)
	if merged.DurHist.Samples() == 0 {
		t.Fatal("merged duration histogram has zero samples")
	}
	if merged.FirstByte == nil || merged.FirstByte.Hist.Samples() == 0 {
		t.Fatal("merged TTFB histogram has zero samples")
	}
	// Check medians are within expected ranges
	medDur := merged.DurMedianMillis
	if medDur < 200 || medDur > 350 {
		t.Fatalf("unexpected merged duration median: %.3fms", medDur)
	}
	medTTFB := merged.FirstByte.MedianMillis
	if medTTFB < 50 || medTTFB > 90 {
		t.Fatalf("unexpected merged TTFB median: %.3fms", medTTFB)
	}
}
