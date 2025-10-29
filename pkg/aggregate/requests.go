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
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/minio/warp/pkg/bench"
)

// SingleSizedRequests contains statistics when all objects have the same size.
type SingleSizedRequests struct {
	// Request times by host.
	ByHost map[string]SingleSizedRequests `json:"by_host,omitempty"`

	// FirstAccess is filled if the same object is accessed multiple times.
	// This records the first access of the object.
	FirstAccess *SingleSizedRequests `json:"first_access,omitempty"`

	// FirstAccess is filled if the same object is accessed multiple times.
	// This records the last access of the object.
	LastAccess *SingleSizedRequests `json:"last_access,omitempty"`

	// Time to first byte if applicable.
	FirstByte *bench.TTFB `json:"first_byte,omitempty"`

	// Host names, sorted.
	HostNames MapAsSlice `json:"host_names,omitempty"`

	// DurHist holds the latency histogram; automatically serialized to sparse format.
	DurHist bench.LatencyHistogram `json:"dur_hist,omitempty"`

	// Internal precise running sums to derive avg/stddev on merges.
	DurSumMillis   float64 `json:"dur_sum_millis,omitempty"`
	DurSumSqMillis float64 `json:"dur_sum_sq_millis,omitempty"`

	// Median request duration.
	DurMedianMillis float64 `json:"dur_median_millis"`

	// Fastest request time.
	FastestMillis float64 `json:"fastest_millis"`

	// Slowest request time.
	SlowestMillis float64 `json:"slowest_millis"`

	// StdDev is the standard deviation of requests.
	StdDev float64 `json:"std_dev_millis"`

	// 99% request time.
	Dur99Millis float64 `json:"dur_99_millis"`

	// 90% request time.
	Dur90Millis float64 `json:"dur_90_millis"`

	// Average request duration.
	DurAvgMillis float64 `json:"dur_avg_millis"`

	// Total number of requests.
	Requests int `json:"requests"`

	// Object size per operation. Can be 0.
	ObjSize int64 `json:"obj_size"`

	// Skipped if too little data.
	Skipped bool `json:"skipped,omitempty"`

	// MergedEntries is a counter for the number of merged entries contained in this result.
	MergedEntries int `json:"merged_entries"`
}

func (a SingleSizedRequests) String() string {
	if a.Requests == 0 {
		return ""
	}
	fmtMillis := func(v float64) string {
		if v > float64(100*time.Millisecond) {
			dur := time.Duration(v * float64(time.Millisecond)).Round(time.Millisecond)
			return dur.String()
		}
		return fmt.Sprintf("%.1fms", v)
	}
	return fmt.Sprint(
		"Avg: ", fmtMillis(a.DurAvgMillis),
		", 50%: ", fmtMillis(a.DurMedianMillis),
		", 90%: ", fmtMillis(a.Dur90Millis),
		", 99%: ", fmtMillis(a.Dur99Millis),
		", Fastest: ", fmtMillis(a.FastestMillis),
		", Slowest: ", fmtMillis(a.SlowestMillis),
		", StdDev: ", fmtMillis(a.StdDev),
	)
}

// updateDerivedDurationStats recomputes all derived duration statistics from source fields
// (DurSumMillis, DurSumSqMillis, DurHist).
func (a *SingleSizedRequests) updateDerivedDurationStats() {
	n := float64(a.DurHist.Samples())
	if n > 0 {
		a.DurAvgMillis = a.DurSumMillis / n
		if n > 1 {
			variance := (a.DurSumSqMillis - (a.DurSumMillis*a.DurSumMillis)/n) / (n - 1)
			if variance < 0 {
				variance = 0
			}
			a.StdDev = math.Sqrt(variance)
		} else {
			a.StdDev = 0
		}
		a.DurMedianMillis = durToMillisF(a.DurHist.Quantile(0.5))
		a.Dur90Millis = durToMillisF(a.DurHist.Quantile(0.9))
		a.Dur99Millis = durToMillisF(a.DurHist.Quantile(0.99))
	}
}

func (a *SingleSizedRequests) add(b SingleSizedRequests) {
	if b.Skipped {
		return
	}
	a.Requests += b.Requests
	a.ObjSize += b.ObjSize
	// Precise sums for avg/stddev
	a.DurSumMillis += b.DurSumMillis
	a.DurSumSqMillis += b.DurSumSqMillis
	if a.MergedEntries == 0 {
		a.FastestMillis = b.FastestMillis
	} else {
		a.FastestMillis = min(a.FastestMillis, b.FastestMillis)
	}
	a.SlowestMillis = max(a.SlowestMillis, b.SlowestMillis)
	a.MergedEntries += b.MergedEntries
	a.HostNames.AddMap(b.HostNames)
	// Merge histograms losslessly
	a.DurHist.Merge(b.DurHist)
	if a.ByHost == nil && len(b.ByHost) > 0 {
		a.ByHost = make(map[string]SingleSizedRequests, len(b.ByHost))
	}
	for k, v := range b.ByHost {
		x := a.ByHost[k]
		x.add(v)
		a.ByHost[k] = x
	}
	// Recompute all derived duration stats from merged sums and histogram
	a.updateDerivedDurationStats()
	if b.FirstAccess != nil {
		if a.FirstAccess == nil {
			a.FirstAccess = &SingleSizedRequests{}
		}
		a.FirstAccess.add(*b.FirstAccess)
	}
	if b.LastAccess != nil {
		if a.LastAccess == nil {
			a.LastAccess = &SingleSizedRequests{}
		}
		a.LastAccess.add(*b.LastAccess)
	}
	if b.FirstByte != nil {
		if a.FirstByte == nil {
			a.FirstByte = &bench.TTFB{}
		}
		a.FirstByte.Merge(*b.FirstByte)
	}
}

func (a *SingleSizedRequests) fill(ops bench.Operations) {
	start, end := ops.TimeRange()
	a.Requests = len(ops)
	a.ObjSize = ops.FirstObjSize()
	// Build latency histogram from operations
	a.DurHist = bench.NewLatencyHistogram()
	a.DurSumMillis = 0
	a.DurSumSqMillis = 0
	for _, op := range ops {
		if op.Err != "" {
			continue
		}
		d := op.Duration()
		a.DurHist.AddDuration(d)
		ms := durToMillisF(d)
		a.DurSumMillis += ms
		a.DurSumSqMillis += ms * ms
		if a.FastestMillis == 0 || ms < a.FastestMillis {
			a.FastestMillis = ms
		}
		if ms > a.SlowestMillis {
			a.SlowestMillis = ms
		}
	}
	// Compute derived duration stats from sums and histogram
	a.updateDerivedDurationStats()
	// Build TTFB including histogram from operations
	a.FirstByte = bench.TtfbFromOps(ops, start, end)
	a.MergedEntries = 1
}

func (a *SingleSizedRequests) fillFirstLast(ops bench.Operations) {
	if !ops.IsMultiTouch() {
		return
	}
	var first, last SingleSizedRequests
	o := ops.FilterFirst()
	first.fill(o)
	a.FirstAccess = &first
	o = ops.FilterLast()
	last.fill(o)
	a.LastAccess = &last
}

type RequestSizeRange struct {
	// Time to first byte if applicable.
	FirstByte *bench.TTFB `json:"first_byte,omitempty"`

	// FirstAccess is filled if the same object is accessed multiple times.
	// This records the first touch of the object.
	FirstAccess *RequestSizeRange `json:"first_access,omitempty"`

	MinSizeString string `json:"min_size_string"`
	MaxSizeString string `json:"max_size_string"`

	BpsMedian         float64 `json:"bps_median"`
	AvgDurationMillis float64 `json:"avg_duration_millis"`

	// Stats:
	BpsAverage float64 `json:"bps_average"`
	// Number of requests in this range.
	Requests   int     `json:"requests"`
	Bps90      float64 `json:"bps_90"`
	Bps99      float64 `json:"bps_99"`
	BpsFastest float64 `json:"bps_fastest"`
	BpsSlowest float64 `json:"bps_slowest"`
	BpsStdDev  float64 `json:"bps_std_dev"`

	// Average payload size of requests in bytes.
	AvgObjSize int `json:"avg_obj_size"`
	// Maximum size in request size range (not included).
	MaxSize int `json:"max_size"`
	// Minimum size in request size range.
	MinSize int `json:"min_size"`

	// MergedEntries is a counter for the number of merged entries contained in this result.
	MergedEntries int `json:"merged_entries"`

	// BpsHist holds the per-operation throughput histogram; automatically serialized to sparse format.
	BpsHist bench.BpsHistogram `json:"bps_hist,omitempty"`

	// Internal sums for accurate merged percentiles/average
	sumBytes int64         `json:"-"`
	sumDur   time.Duration `json:"-"`
	sumBps   float64       `json:"-"`
	sumSqBps float64       `json:"-"`
	bpsCount int           `json:"-"`
}

// updateDerivedBpsStats recomputes all derived BPS statistics from source fields
// (sumBytes, sumDur, sumBps, sumSqBps, bpsCount, BpsHist).
func (s *RequestSizeRange) updateDerivedBpsStats() {
	// Compute average from total bytes and duration
	if s.sumDur > 0 {
		s.BpsAverage = float64(s.sumBytes) * float64(time.Second) / float64(s.sumDur)
	}
	// Compute standard deviation from per-op bps sums
	if s.bpsCount > 1 {
		n := float64(s.bpsCount)
		varVar := (s.sumSqBps - (s.sumBps*s.sumBps)/n) / (n - 1)
		if varVar < 0 {
			varVar = 0
		}
		s.BpsStdDev = math.Sqrt(varVar)
	}
	// Compute percentiles from histogram
	s.BpsMedian = s.BpsHist.Quantile(0.5)
	s.Bps90 = s.BpsHist.Quantile(0.9)
	s.Bps99 = s.BpsHist.Quantile(0.99)
	s.BpsFastest = s.BpsHist.Quantile(0.0)
	s.BpsSlowest = s.BpsHist.Quantile(1.0)
}

func (s RequestSizeRange) String() string {
	if s.MergedEntries <= 0 || s.Requests == 0 {
		return ""
	}
	return fmt.Sprint("Average: ", bench.Throughput(s.BpsAverage),
		", 50%: ", bench.Throughput(s.BpsMedian),
		", 90%: ", bench.Throughput(s.Bps90),
		", 99%: ", bench.Throughput(s.Bps99),
		", Fastest: ", bench.Throughput(s.BpsFastest),
		", Slowest: ", bench.Throughput(s.BpsSlowest),
		", StdDev: ", bench.Throughput(s.BpsStdDev),
	)
}

func (s *RequestSizeRange) fill(ss bench.SizeSegment) {
	ops := ss.Ops.SortByThroughputNonZero()
	if len(ops) == 0 {
		return
	}
	start, end := ops.TimeRange()
	s.Requests = len(ops)
	s.MinSize = int(ss.Smallest)
	s.MaxSize = int(ss.Biggest)
	s.MinSizeString, s.MaxSizeString = ss.SizesString()
	s.AvgObjSize = int(ops.AvgSize())
	s.AvgDurationMillis = durToMillisF(ops.AvgDuration())
	// Build per-operation Bps histogram and sums
	s.BpsHist = bench.NewBpsHistogram()
	s.sumBytes = 0
	s.sumDur = 0
	s.sumBps = 0
	s.sumSqBps = 0
	s.bpsCount = 0
	for _, op := range ops {
		if op.Duration() <= 0 || op.Size <= 0 {
			continue
		}
		s.sumBytes += op.Size
		s.sumDur += op.Duration()
		bps := float64(op.Size) * float64(time.Second) / float64(op.Duration())
		s.BpsHist.AddBps(bps)
		s.sumBps += bps
		s.sumSqBps += bps * bps
		s.bpsCount++
	}
	// Compute derived BPS stats from sums and histogram
	s.updateDerivedBpsStats()
	// Build TTFB from operations
	s.FirstByte = bench.TtfbFromOps(ops, start, end)
	s.MergedEntries = 1
}

func (s *RequestSizeRange) add(b RequestSizeRange) {
	s.Requests += b.Requests
	if b.FirstByte != nil {
		if s.FirstByte == nil {
			s.FirstByte = &bench.TTFB{}
		}
		s.FirstByte.Merge(*b.FirstByte)
	}
	// Min/Max should be set
	s.AvgObjSize += b.AvgObjSize
	s.AvgDurationMillis += b.AvgDurationMillis
	// Merge sums for average
	s.sumBytes += b.sumBytes
	s.sumDur += b.sumDur
	s.sumBps += b.sumBps
	s.sumSqBps += b.sumSqBps
	s.bpsCount += b.bpsCount
	// Merge histogram
	s.BpsHist.Merge(b.BpsHist)
	// Recompute all derived BPS stats from merged sums and histogram
	s.updateDerivedBpsStats()
	s.MergedEntries += b.MergedEntries
}

func (s *RequestSizeRange) fillFirstAccess(ss bench.SizeSegment) {
	if !ss.Ops.IsMultiTouch() {
		return
	}
	ss.Ops = ss.Ops.FilterFirst()
	a := RequestSizeRange{}
	a.fill(ss)
	s.FirstAccess = &a
}

// RequestSizeRanges is an array of RequestSizeRange
type RequestSizeRanges []RequestSizeRange

// SortbySize will sort the ranges by size.
func (r RequestSizeRanges) SortbySize() {
	sort.Slice(r, func(i, j int) bool {
		return r[i].MinSize < r[j].MinSize
	})
}

// FindMatching will find a matching range, or create a new range.
// New entries will not me added to r.
func (r RequestSizeRanges) FindMatching(want RequestSizeRange) (v *RequestSizeRange, found bool) {
	for i := range r {
		if want.MinSize >= r[i].MinSize && want.MaxSize <= r[i].MaxSize {
			return &r[i], true
		}
	}
	return &RequestSizeRange{
		MinSizeString: want.MinSizeString,
		MaxSizeString: want.MaxSizeString,
		MaxSize:       want.MaxSize,
		MinSize:       want.MinSize,
	}, false
}

// MultiSizedRequests contains statistics when objects have the same different size.
type MultiSizedRequests struct {
	// ByHost contains request information by host.
	// This data is not segmented.
	ByHost map[string]RequestSizeRange `json:"by_host,omitempty"`

	// BySize contains request times separated by sizes
	BySize RequestSizeRanges `json:"by_size"`

	// Total number of requests.
	Requests int `json:"requests"`

	// Average object size
	AvgObjSize int64 `json:"avg_obj_size"`

	// Skipped if too little data.
	Skipped bool `json:"skipped,omitempty"`

	// MergedEntries is a counter for the number of merged entries contained in this result.
	MergedEntries int `json:"merged_entries"`
}

func (a *MultiSizedRequests) add(b MultiSizedRequests) {
	if b.Skipped {
		return
	}
	if a.ByHost == nil {
		a.ByHost = make(map[string]RequestSizeRange)
	}
	for ep, v := range b.ByHost {
		av := a.ByHost[ep]
		av.add(v)
		a.ByHost[ep] = av
	}
	a.Requests += b.Requests
	a.AvgObjSize += b.AvgObjSize
	for _, toMerge := range b.BySize {
		dst, found := a.BySize.FindMatching(toMerge)
		dst.add(toMerge)
		if !found {
			a.BySize = append(a.BySize, toMerge)
			a.BySize.SortbySize()
		}
	}
	a.MergedEntries += b.MergedEntries
}

func (a *MultiSizedRequests) fill(ops bench.Operations, fillFirstAccess bool) {
	start, end := ops.TimeRange()
	a.Requests = len(ops)
	if len(ops) == 0 || end.Sub(start) < 100*time.Millisecond {
		a.Skipped = true
		return
	}
	a.AvgObjSize = ops.AvgSize()
	sizes := ops.SplitSizes(0.05)
	a.BySize = make([]RequestSizeRange, len(sizes))
	a.MergedEntries = 1
	var wg sync.WaitGroup
	wg.Add(len(sizes))
	for i := range sizes {
		go func(i int) {
			defer wg.Done()
			s := sizes[i]
			var r RequestSizeRange
			r.fill(s)
			if fillFirstAccess {
				r.fillFirstAccess(s)
			}
			// Store
			a.BySize[i] = r
		}(i)
	}
	wg.Wait()
}

// RequestAnalysisSingleSized performs analysis where all objects have equal size.
func RequestAnalysisSingleSized(o bench.Operations, allThreads bool) *SingleSizedRequests {
	var res SingleSizedRequests

	// Single type, require one operation per thread.
	start, end := o.ActiveTimeRange(allThreads)
	active := o.FilterInsideRange(start, end)

	if len(active) == 0 {
		res.Skipped = true
		return &res
	}
	res.MergedEntries = 1
	res.fill(active)
	res.fillFirstLast(o)
	res.HostNames.SetSlice(o.Endpoints())
	res.ByHost = RequestAnalysisHostsSingleSized(o)
	if len(res.HostNames) != len(res.ByHost) {
		res.HostNames.SetSlice(o.ClientIDs(clientAsHostPrefix))
	}
	return &res
}

// RequestAnalysisHostsSingleSized performs host analysis where all objects have equal size.
func RequestAnalysisHostsSingleSized(o bench.Operations) map[string]SingleSizedRequests {
	eps := o.SortSplitByEndpoint()
	if len(eps) == 1 {
		cl := o.SortSplitByClient(clientAsHostPrefix)
		if len(cl) > 1 {
			eps = cl
		}
		if len(eps) == 1 {
			return nil
		}
	}
	res := make(map[string]SingleSizedRequests, len(eps))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(len(eps))
	for ep, ops := range eps {
		go func(ep string, ops bench.Operations) {
			defer wg.Done()
			if len(ops) <= 1 {
				return
			}
			a := SingleSizedRequests{}
			a.fill(ops)
			mu.Lock()
			res[ep] = a
			mu.Unlock()
		}(ep, ops)
	}
	wg.Wait()
	return res
}

// RequestAnalysisMultiSized performs analysis where objects have different sizes.
func RequestAnalysisMultiSized(o bench.Operations, allThreads bool) *MultiSizedRequests {
	var res MultiSizedRequests
	// Single type, require one operation per thread.
	start, end := o.ActiveTimeRange(allThreads)
	active := o.FilterInsideRange(start, end)

	res.Requests = len(active)
	if len(active) == 0 {
		res.Skipped = true
		return &res
	}
	res.fill(active, true)
	res.ByHost = RequestAnalysisHostsMultiSized(active)
	res.MergedEntries = 1
	return &res
}

// RequestAnalysisHostsMultiSized performs host analysis where objects have different sizes.
func RequestAnalysisHostsMultiSized(o bench.Operations) map[string]RequestSizeRange {
	eps := o.SortSplitByEndpoint()
	if len(eps) == 1 {
		cl := o.SortSplitByClient(clientAsHostPrefix)
		if len(cl) > 1 {
			eps = cl
		}
		if len(eps) == 1 {
			return nil
		}
	}

	res := make(map[string]RequestSizeRange, len(eps))
	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(len(eps))
	for ep, ops := range eps {
		go func(ep string, ops bench.Operations) {
			defer wg.Done()
			if len(ops) <= 1 {
				return
			}
			a := RequestSizeRange{}
			a.fill(ops.SingleSizeSegment())
			mu.Lock()
			res[ep] = a
			mu.Unlock()
		}(ep, ops)
	}
	wg.Wait()
	return res
}

// durToMillis converts a duration to milliseconds.
// Rounded to nearest.
func durToMillis(d time.Duration) int {
	return int(d.Round(time.Millisecond) / time.Millisecond)
}

func durToMillisF(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
