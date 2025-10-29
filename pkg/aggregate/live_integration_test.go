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
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/minio/warp/pkg/bench"
)

// approx checks a and b are within rel of each other.
func approx(a, b, rel float64) bool {
	if a == 0 && b == 0 {
		return true
	}
	if a == 0 || b == 0 {
		return false
	}
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	if a > b {
		return d/a <= rel
	}
	return d/b <= rel
}

func genOpsSized(clientID, op string, size int64, n int, start time.Time, baseDur time.Duration) bench.Operations {
	ops := make(bench.Operations, 0, n)
	for i := 0; i < n; i++ {
		st := start.Add(time.Second * time.Duration(i))
		dur := baseDur + time.Duration((i%7))*10*time.Millisecond
		fb := st.Add(25*time.Millisecond + time.Duration(i%5)*5*time.Millisecond)
		opEnd := st.Add(dur)
		ops = append(ops, bench.Operation{
			Start:     st,
			End:       opEnd,
			FirstByte: &fb,
			OpType:    op,
			ClientID:  clientID,
			Endpoint:  "ep1",
			ObjPerOp:  1,
			Size:      size,
			Thread:    uint32(i % 8),
		})
	}
	return ops
}

func runLiveFromOps(t *testing.T, ops bench.Operations, clientID string) *Realtime {
	t.Helper()
	ch := make(chan bench.Operation, len(ops))
	go func() {
		for _, o := range ops {
			ch <- o
		}
		close(ch)
	}()
	return Live(ch, nil, clientID, nil)
}

func roundTripJSON(t *testing.T, r Realtime) Realtime {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(r); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Realtime
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestLiveIntegration_SingleSizeTwoClients_JSONRoundtrip(t *testing.T) {
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	ops1 := genOpsSized("c1", "GET", 4<<20, 75, base, 420*time.Millisecond)
	ops2 := genOpsSized("c2", "GET", 4<<20, 72, base.Add(500*time.Millisecond), 430*time.Millisecond)

	r1 := runLiveFromOps(t, ops1, "c1")
	r2 := runLiveFromOps(t, ops2, "c2")

	// Merge on coordinator
	coord := newRealTime()
	coord.Merge(r1)
	coord.Merge(r2)
	coord.Finalize()

	// Live coordinator assertions
	if coord.Total.Throughput.BytesPS() <= 0 || coord.Total.Throughput.ObjectsPS() <= 0 {
		t.Fatalf("live throughput should be > 0: %+v", coord.Total.Throughput)
	}
	// Printed average should include MiB/s number close to BytesPS
	wantMiB := float64(coord.Total.Throughput.BytesPS()) / (1 << 20)
	_ = wantMiB
	avgLine := coord.Total.Throughput.StringDetails(true)
	if !strings.Contains(avgLine, "MiB/s") {
		t.Fatalf("average line missing MiB/s: %q", avgLine)
	}

	// Whole-run request totals present and sane
	if coord.Total.RequestsTotal == nil || coord.Total.RequestsTotal.Single == nil {
		t.Fatalf("expected single-sized totals present")
	}
	ss := coord.Total.RequestsTotal.Single
	if ss.DurAvgMillis <= 0 || ss.DurMedianMillis <= 0 || ss.Dur90Millis <= 0 || ss.Dur99Millis <= 0 {
		t.Fatalf("expected non-zero duration stats: %+v", *ss)
	}
	if ss.DurHist.Samples() == 0 {
		t.Fatalf("expected non-empty duration histogram")
	}
	// TTFB
	if ss.FirstByte == nil || ss.FirstByte.AverageMillis <= 0 || ss.FirstByte.MedianMillis <= 0 {
		t.Fatalf("expected non-zero TTFB stats: %+v", ss.FirstByte)
	}

	// Simulate warp analyze JSON path and reprint
	dec := roundTripJSON(t, coord)
	rep := dec.Report(ReportOptions{Details: true, Color: false})
	if !strings.Contains(rep.String(), " * Reqs: Avg:") || strings.Contains(rep.String(), " * Reqs: Avg: 0.0ms") {
		t.Fatalf("expected non-zero req stats in report, got:\n%s", rep.String())
	}
	if !strings.Contains(rep.String(), " * TTFB:") {
		t.Fatalf("expected TTFB line present in report")
	}
	// Per-client segments must retain mergeable histograms in JSON
	if len(dec.Total.Requests) == 0 {
		t.Fatalf("expected per-client request segments present")
	}
	for cl, segs := range dec.Total.Requests {
		_ = cl
		if len(segs) == 0 {
			t.Fatalf("client has no segments")
		}
		// Find a single-sized segment and ensure histogram is present
		found := false
		for i := range segs {
			if segs[i].Single != nil && segs[i].Single.DurHist.Samples() > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected at least one single-sized segment with non-empty duration histogram")
		}
	}
	// Check printed average still matches bytes/s approximately
	printed := dec.Total.Throughput.BytesPS()
	if printed <= 0 || !approx(float64(printed), float64(coord.Total.Throughput.BytesPS()), 0.05) {
		t.Fatalf("throughput mismatch; live=%v json=%v", coord.Total.Throughput.BytesPS(), printed)
	}
}

func TestLiveIntegration_MultiSizeTwoClients_JSONRoundtrip(t *testing.T) {
	base := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	// Interleave sizes within the same 10s segments to trigger multi-sized analysis
	makeInterleaved := func(client string, n int, start time.Time) bench.Operations {
		ops := make(bench.Operations, 0, n)
		for i := 0; i < n; i++ {
			sz := int64(1 << 20)
			if i%2 == 1 {
				sz = 8 << 20
			}
			st := start.Add(time.Second * time.Duration(i))
			dur := 350*time.Millisecond + time.Duration((i%5))*15*time.Millisecond
			fb := st.Add(20*time.Millisecond + time.Duration(i%3)*5*time.Millisecond)
			ops = append(ops, bench.Operation{
				Start:     st,
				End:       st.Add(dur),
				FirstByte: &fb,
				OpType:    "GET",
				ClientID:  client,
				Endpoint:  "ep1",
				ObjPerOp:  1,
				Size:      sz,
				Thread:    uint32(i % 8),
			})
		}
		return ops
	}
	ops1 := makeInterleaved("c1", 40, base)
	ops2 := makeInterleaved("c2", 38, base.Add(500*time.Millisecond))

	r1 := runLiveFromOps(t, ops1, "c1")
	r2 := runLiveFromOps(t, ops2, "c2")

	coord := newRealTime()
	coord.Merge(r1)
	coord.Merge(r2)
	coord.Finalize()

	if coord.Total.Throughput.BytesPS() <= 0 {
		t.Fatalf("live throughput should be > 0")
	}

	// In multi-size, totals should have Multi with BySize populated.
	if coord.Total.RequestsTotal == nil || coord.Total.RequestsTotal.Multi == nil || len(coord.Total.RequestsTotal.Multi.BySize) == 0 {
		t.Fatalf("expected multi-size totals present")
	}
	for _, rs := range coord.Total.RequestsTotal.Multi.BySize {
		if rs.Requests == 0 {
			continue
		}
		if rs.BpsAverage <= 0 || rs.BpsMedian <= 0 || rs.Bps90 <= 0 || rs.Bps99 <= 0 {
			t.Fatalf("expected non-zero per-size bps stats: %+v", rs)
		}
		if rs.BpsHist.Samples() == 0 {
			t.Fatalf("expected non-empty per-size bps histogram")
		}
		if rs.FirstByte != nil && rs.FirstByte.AverageMillis <= 0 {
			t.Fatalf("expected non-zero TTFB per-size if present: %+v", rs.FirstByte)
		}
	}

	// Compute ground-truth per-range stats and validate against BySize entries
	all := append(bench.Operations{}, ops1...)
	all = append(all, ops2...)
	computeGTForRange := func(minSize, maxSize int) (avg, std, p50, p90, p99 float64) {
		// Collect per-op bps for sizes within [minSize, maxSize)
		var sumBytes int64
		var sumDur time.Duration
		var vals []float64
		var sum, sumSq float64
		for _, op := range all {
			if op.Duration() <= 0 {
				continue
			}
			if int(op.Size) < minSize || int(op.Size) >= maxSize {
				continue
			}
			bps := float64(op.Size) * float64(time.Second) / float64(op.Duration())
			vals = append(vals, bps)
			sum += bps
			sumSq += bps * bps
			sumBytes += op.Size
			sumDur += op.Duration()
		}
		if sumDur > 0 {
			avg = float64(sumBytes) * float64(time.Second) / float64(sumDur)
		}
		if len(vals) > 1 {
			n := float64(len(vals))
			varVar := (sumSq - (sum*sum)/n) / (n - 1)
			if varVar < 0 {
				varVar = 0
			}
			std = math.Sqrt(varVar)
		}
		sort.Float64s(vals)
		pct := func(p float64) float64 {
			if len(vals) == 0 {
				return 0
			}
			idx := int(math.Ceil(float64(len(vals))*p)) - 1
			if idx < 0 {
				idx = 0
			}
			if idx >= len(vals) {
				idx = len(vals) - 1
			}
			return vals[idx]
		}
		p50 = pct(0.5)
		p90 = pct(0.9)
		p99 = pct(0.99)
		return avg, std, p50, p90, p99
	}

	// Validate each JSON size range using the ops that fall within it
	for _, r := range coord.Total.RequestsTotal.Multi.BySize {
		rs := r // copy loop var
		gtAvg, gtStd, gt50, gt90, gt99 := computeGTForRange(rs.MinSize, rs.MaxSize)
		// Tight tolerance for average (bytes/dur)
		if gtAvg > 0 && !approx(rs.BpsAverage, gtAvg, 0.05) {
			t.Fatalf("avg mismatch for range %s->%s: got=%.2f want≈%.2f", rs.MinSizeString, rs.MaxSizeString, rs.BpsAverage, gtAvg)
		}
		// Moderate tolerance for stddev (bucket effects minor)
		if gtStd > 0 && !approx(rs.BpsStdDev, gtStd, 0.20) {
			t.Fatalf("stddev mismatch for range %s->%s: got=%.2f want≈%.2f", rs.MinSizeString, rs.MaxSizeString, rs.BpsStdDev, gtStd)
		}
		// Looser tolerance for histogram-derived percentiles
		if gt50 > 0 && !approx(rs.BpsMedian, gt50, 0.20) {
			t.Fatalf("p50 mismatch for range %s->%s: got=%.2f want≈%.2f", rs.MinSizeString, rs.MaxSizeString, rs.BpsMedian, gt50)
		}
		if gt90 > 0 && !approx(rs.Bps90, gt90, 0.20) {
			t.Fatalf("p90 mismatch for range %s->%s: got=%.2f want≈%.2f", rs.MinSizeString, rs.MaxSizeString, rs.Bps90, gt90)
		}
		if gt99 > 0 && !approx(rs.Bps99, gt99, 0.20) {
			t.Fatalf("p99 mismatch for range %s->%s: got=%.2f want≈%.2f", rs.MinSizeString, rs.MaxSizeString, rs.Bps99, gt99)
		}
	}

	// JSON roundtrip should preserve ability to print
	dec := roundTripJSON(t, coord)
	rep := dec.Report(ReportOptions{Details: true, Color: false})
	if !strings.Contains(rep.String(), "Throughput, split into") {
		t.Fatalf("expected segmented throughput section in report")
	}
}
