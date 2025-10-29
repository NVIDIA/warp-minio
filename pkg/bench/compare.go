/*
 * Warp (C) 2019 MinIO, Inc.
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
	"errors"
	"fmt"
	"math"
	"time"
)

// Comparison is a comparison between two benchmarks.
type Comparison struct {
	TTFB *TTFBCmp
	Op   string

	Average CmpSegment
	Fastest CmpSegment
	Median  CmpSegment
	Slowest CmpSegment
	Reqs    CmpReqs
}

// CmpSegment is s comparisons between two segments.
type CmpSegment struct {
	Before, After    *Segment
	ThroughputPerSec float64
	ObjPerSec        float64
	OpsEndedPerSec   float64
}

type CmpReqs struct {
	CmpRequests
	Before, After CmpRequests
}

func (c *CmpReqs) Compare(before, after Operations) {
	c.Before.fill(before)
	c.After.fill(after)
	a := c.After
	b := c.Before
	c.CmpRequests = CmpRequests{
		Average: a.Average - b.Average,
		Worst:   a.Worst - b.Worst,
		Best:    a.Best - b.Best,
		Median:  a.Median - b.Median,
		P90:     a.P90 - b.P90,
		P99:     a.P99 - b.P99,
		StdDev:  a.StdDev - b.StdDev,
	}
}

type CmpRequests struct {
	AvgObjSize int64
	Requests   int
	Average    time.Duration
	Best       time.Duration
	Median     time.Duration
	P90        time.Duration
	P99        time.Duration
	Worst      time.Duration
	StdDev     time.Duration
}

func (c *CmpRequests) fill(ops Operations) {
	ops.SortByDuration()
	c.Requests = len(ops)
	c.AvgObjSize = ops.AvgSize()
	c.Average = ops.AvgDuration()
	c.Best = ops.Median(0).Duration()
	c.Median = ops.Median(0.5).Duration()
	c.P90 = ops.Median(0.9).Duration()
	c.P99 = ops.Median(0.99).Duration()
	c.Worst = ops.Median(1).Duration()
	c.StdDev = ops.StdDev()
}

// String returns a human readable representation of the TTFB comparison.
func (c *CmpReqs) String() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("Avg: %s%v (%s%.f%%), P50: %s%v (%s%.f%%), P99: %s%v (%s%.f%%), Best: %s%v (%s%.f%%), Worst: %s%v (%s%.f%%) StdDev: %s%v (%s%.f%%)",
		plusPositiveD(c.Average),
		c.Average.Round(time.Millisecond/20),
		plusPositiveD(c.Average),
		100*(float64(c.After.Average)-float64(c.Before.Average))/float64(c.Before.Average),
		plusPositiveD(c.Median),
		c.Median,
		plusPositiveD(c.Median),
		100*(float64(c.After.Median)-float64(c.Before.Median))/float64(c.Before.Median),
		plusPositiveD(c.P99),
		c.P99,
		plusPositiveD(c.P99),
		100*(float64(c.After.P99)-float64(c.Before.P99))/float64(c.Before.P99),
		plusPositiveD(c.Best),
		c.Best,
		plusPositiveD(c.Best),
		100*(float64(c.After.Best)-float64(c.Before.Best))/float64(c.Before.Best),
		plusPositiveD(c.Worst),
		c.Worst,
		plusPositiveD(c.Worst),
		100*(float64(c.After.Worst)-float64(c.Before.Worst))/float64(c.Before.Worst),
		plusPositiveD(c.StdDev),
		c.StdDev,
		plusPositiveD(c.StdDev),
		100*(float64(c.After.StdDev)-float64(c.Before.StdDev))/float64(c.Before.StdDev),
	)
}

// Compare sets c to a comparison between before and after.
func (c *CmpSegment) Compare(before, after Segment) {
	c.Before = &before
	c.After = &after
	mbB, opsB, objsB := before.SpeedPerSec()
	mbA, opsA, objsA := after.SpeedPerSec()
	c.ObjPerSec = 100 * (objsA - objsB) / objsB
	c.OpsEndedPerSec = 100 * (opsA - opsB) / opsB
	if mbB > 0 {
		c.ThroughputPerSec = 100 * (mbA - mbB) / mbB
	} else {
		c.ThroughputPerSec = 0
	}
}

// String returns a string representation of the segment comparison.
func (c CmpSegment) String() string {
	speed := ""
	mibB, _, objsB := c.Before.SpeedPerSec()
	mibA, _, objsA := c.After.SpeedPerSec()

	if c.ThroughputPerSec != 0 {
		speed = fmt.Sprintf("%s%.02f%% (%s%.1f MiB/s) throughput, ",
			plusPositiveF(c.ThroughputPerSec), c.ThroughputPerSec,
			plusPositiveF(c.ThroughputPerSec), mibA-mibB,
		)
	}
	return fmt.Sprintf("%s%s%.02f%% (%s%.1f) obj/s",
		speed, plusPositiveF(c.ObjPerSec), c.ObjPerSec,
		plusPositiveF(objsA-objsB), objsA-objsB,
	)
}

func plusPositiveF(f float64) string {
	switch {
	case f > 0 && !math.IsInf(f, 1):
		return "+"
	default:
		return ""
	}
}

func plusPositiveD(d time.Duration) string {
	switch {
	case d > 0:
		return "+"
	default:
		return ""
	}
}

func Compare(before, after Operations, analysis time.Duration, allThreads bool) (*Comparison, error) {
	var res Comparison
	if before.FirstOpType() != after.FirstOpType() {
		return nil, fmt.Errorf("different operation types. before: %v, after %v", before.FirstOpType(), after.FirstOpType())
	}
	if analysis <= 0 {
		return nil, fmt.Errorf("invalid analysis duration: %v", analysis)
	}
	if len(before.Errors()) > 0 || len(after.Errors()) > 0 {
		return nil, fmt.Errorf("errors recorded in benchmark run. before: %v, after %d", len(before.Errors()), len(after.Errors()))
	}
	res.Op = before.FirstOpType()
	segment := func(ops Operations) (Segments, error) {
		ops.SortByStartTime()
		segs := ops.Segment(SegmentOptions{
			From:           time.Time{},
			PerSegDuration: analysis,
			AllThreads:     allThreads,
		})
		if len(segs) <= 1 {
			return nil, errors.New("too few samples")
		}
		totals := ops.Total(allThreads)
		if totals.TotalBytes > 0 {
			segs.SortByThroughput()
		} else {
			segs.SortByObjsPerSec()
		}
		return segs, nil
	}
	bs, err := segment(before)
	if err != nil {
		return nil, fmt.Errorf("segmenting before: %w", err)
	}
	as, err := segment(after)
	if err != nil {
		return nil, fmt.Errorf("segmenting after: %w", err)
	}

	res.Median.Compare(bs.Median(0.5), as.Median(0.5))
	res.Slowest.Compare(bs.Median(0.0), as.Median(0.0))
	res.Fastest.Compare(bs.Median(1), as.Median(1))

	beforeTotals := before.Total(allThreads)
	afterTotals := after.Total(allThreads)
	res.Reqs.Compare(before, after)

	res.Average.Compare(beforeTotals, afterTotals)

	// Compare TTFB
	beforeStart, beforeEnd := before.TimeRange()
	afterStart, afterEnd := after.TimeRange()
	beforeTTFB := TtfbFromOps(before, beforeStart, beforeEnd)
	afterTTFB := TtfbFromOps(after, afterStart, afterEnd)
	if beforeTTFB != nil && afterTTFB != nil {
		res.TTFB = beforeTTFB.Compare(*afterTTFB)
	}

	return &res, nil
}
