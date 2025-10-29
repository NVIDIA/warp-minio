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
	"encoding/json"
	"testing"
	"time"
)

// TestLatencyHistogram_MergeIntoZero ensures merging into a zero-value destination
// histogram copies layout and counts rather than being a no-op.
func TestLatencyHistogram_MergeIntoZero(t *testing.T) {
	var dst LatencyHistogram // zero value
	src := NewLatencyHistogram()
	src.AddDuration(10 * time.Millisecond)
	src.AddDuration(20 * time.Millisecond)
	if src.Samples() == 0 {
		t.Fatal("source histogram should have samples")
	}

	// Merge into zero-value dst
	dst.Merge(src)
	if dst.Samples() != src.Samples() {
		t.Fatalf("merge into zero failed: got %d samples, want %d", dst.Samples(), src.Samples())
	}
	// Percentiles should match
	if dst.Quantile(0.5) != src.Quantile(0.5) {
		t.Fatalf("median mismatch after merge into zero: got %v want %v", dst.Quantile(0.5), src.Quantile(0.5))
	}
}

// TestBpsHistogram_MergeIntoZero ensures merging into a zero-value destination
// also works for BpsHistogram.
func TestBpsHistogram_MergeIntoZero(t *testing.T) {
	var dst BpsHistogram // zero value
	src := NewBpsHistogram()
	src.AddBps(1 << 20) // 1 MiB/s
	src.AddBps(1 << 21) // 2 MiB/s
	if src.Samples() == 0 {
		t.Fatal("source bps histogram should have samples")
	}

	dst.Merge(src)
	if dst.Samples() != src.Samples() {
		t.Fatalf("merge into zero failed: got %d samples, want %d", dst.Samples(), src.Samples())
	}
	if dst.Quantile(0.5) != src.Quantile(0.5) {
		t.Fatalf("median mismatch after merge into zero: got %v want %v", dst.Quantile(0.5), src.Quantile(0.5))
	}
}

// TestHistogram_JSONSerialization verifies that histograms can be marshaled to JSON
// and unmarshaled correctly, preserving sample counts and percentiles.
func TestHistogram_JSONSerialization(t *testing.T) {
	t.Run("LatencyHistogram", func(t *testing.T) {
		// Create a latency histogram with some data
		h := NewLatencyHistogram()
		h.AddDuration(100 * time.Millisecond)
		h.AddDuration(200 * time.Millisecond)
		h.AddDuration(150 * time.Millisecond)
		h.AddDuration(50 * time.Millisecond)

		// Marshal to JSON
		data, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("failed to marshal latency histogram: %v", err)
		}

		// Verify JSON contains both idx and millis fields
		var rawJSON []map[string]interface{}
		if err := json.Unmarshal(data, &rawJSON); err != nil {
			t.Fatalf("failed to unmarshal to raw JSON: %v", err)
		}
		if len(rawJSON) == 0 {
			t.Fatal("expected non-empty JSON array")
		}
		// Check first bucket has expected fields
		firstBucket := rawJSON[0]
		if _, ok := firstBucket["idx"]; !ok {
			t.Error("JSON should contain 'idx' field")
		}
		if _, ok := firstBucket["millis"]; !ok {
			t.Error("JSON should contain 'millis' field")
		}
		if _, ok := firstBucket["n"]; !ok {
			t.Error("JSON should contain 'n' field")
		}

		// Unmarshal and verify
		var h2 LatencyHistogram
		if err := json.Unmarshal(data, &h2); err != nil {
			t.Fatalf("failed to unmarshal latency histogram: %v", err)
		}

		// Verify samples preserved
		if h2.Samples() != h.Samples() {
			t.Errorf("samples mismatch: original=%d, unmarshaled=%d", h.Samples(), h2.Samples())
		}

		// Verify percentiles match (should be exact since we're using bucket indices)
		percentiles := []float64{0.5, 0.9, 0.99}
		for _, p := range percentiles {
			orig := h.Quantile(p)
			unmarshaled := h2.Quantile(p)
			if orig != unmarshaled {
				t.Errorf("p%v mismatch: original=%v, unmarshaled=%v", p*100, orig, unmarshaled)
			}
		}
	})

	t.Run("BpsHistogram", func(t *testing.T) {
		// Create a BPS histogram
		bps := NewBpsHistogram()
		bps.AddBps(1024 * 1024)      // 1 MB/s
		bps.AddBps(10 * 1024 * 1024) // 10 MB/s
		bps.AddBps(5 * 1024 * 1024)  // 5 MB/s

		// Marshal to JSON
		data, err := json.Marshal(bps)
		if err != nil {
			t.Fatalf("failed to marshal BPS histogram: %v", err)
		}

		// Verify JSON contains idx and n fields (but not millis)
		var rawJSON []map[string]interface{}
		if err := json.Unmarshal(data, &rawJSON); err != nil {
			t.Fatalf("failed to unmarshal to raw JSON: %v", err)
		}
		if len(rawJSON) == 0 {
			t.Fatal("expected non-empty JSON array")
		}
		// Check first bucket has expected fields
		firstBucket := rawJSON[0]
		if _, ok := firstBucket["idx"]; !ok {
			t.Error("BPS JSON should contain 'idx' field")
		}
		if _, ok := firstBucket["bps"]; !ok {
			t.Error("BPS JSON should contain 'bps' field")
		}
		if _, ok := firstBucket["n"]; !ok {
			t.Error("BPS JSON should contain 'n' field")
		}
		if _, ok := firstBucket["millis"]; ok {
			t.Error("BPS JSON should NOT contain 'millis' field")
		}

		// Unmarshal and verify
		var bps2 BpsHistogram
		if err := json.Unmarshal(data, &bps2); err != nil {
			t.Fatalf("failed to unmarshal BPS histogram: %v", err)
		}

		// Verify samples preserved
		if bps2.Samples() != bps.Samples() {
			t.Errorf("samples mismatch: original=%d, unmarshaled=%d", bps.Samples(), bps2.Samples())
		}

		// Verify percentiles match
		percentiles := []float64{0.5, 0.9, 0.99}
		for _, p := range percentiles {
			orig := bps.Quantile(p)
			unmarshaled := bps2.Quantile(p)
			if orig != unmarshaled {
				t.Errorf("p%v mismatch: original=%v, unmarshaled=%v", p*100, orig, unmarshaled)
			}
		}
	})

	t.Run("EmptyHistograms", func(t *testing.T) {
		// Test empty histograms serialize to empty arrays
		emptyLatency := NewLatencyHistogram()
		data, err := json.Marshal(emptyLatency)
		if err != nil {
			t.Fatalf("failed to marshal empty latency histogram: %v", err)
		}
		if string(data) != "[]" && string(data) != "null" {
			t.Errorf("empty latency histogram should serialize to [] or null, got: %s", string(data))
		}

		emptyBps := NewBpsHistogram()
		data, err = json.Marshal(emptyBps)
		if err != nil {
			t.Fatalf("failed to marshal empty BPS histogram: %v", err)
		}
		if string(data) != "[]" && string(data) != "null" {
			t.Errorf("empty BPS histogram should serialize to [] or null, got: %s", string(data))
		}
	})
}
