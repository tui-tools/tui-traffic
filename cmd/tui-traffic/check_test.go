package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// --check is the read path with no UI on it, and it is what a script or a lab
// run reads. What has to hold is that it is one sample of all three screens,
// that it says where each of them came from, and that the exit code is not a
// verdict about the network.
func TestRunCheckPrintsOneSampleOfEverything(t *testing.T) {
	var out strings.Builder
	interval := 20 * time.Millisecond
	err := runCheck(traffic.NewFake(),
		[]compat.Result{{Backend: "conntrack", Version: "1.4.8"}},
		interval, &out)
	if err != nil {
		t.Fatalf("runCheck: %v", err)
	}

	var report checkReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("the output is not JSON: %v\n%s", err, out.String())
	}

	if report.Tool != toolName {
		t.Errorf("tool = %q", report.Tool)
	}
	if report.Backend != "demo" {
		t.Errorf("backend = %q, want the demo one", report.Backend)
	}
	if report.Interval != interval.String() {
		t.Errorf("interval = %q, want %q", report.Interval, interval)
	}
	if len(report.Interfaces) == 0 {
		t.Error("no interface rates: --check takes two samples for a reason")
	}
	if report.Connections.Total == 0 {
		t.Error("no connections")
	}
	if report.Sockets.Total == 0 {
		t.Error("no sockets")
	}
	if len(report.Compat) != 1 {
		t.Errorf("compat = %+v, want the one probed backend", report.Compat)
	}

	// The sources block is the part a reader has to see: a connections total
	// counted from sockets is a different number from one read out of the
	// conntrack table, and the JSON says which it is.
	if report.Sources.Connections == "" || report.Sources.Interfaces == "" ||
		report.Sources.Sockets == "" {
		t.Errorf("the sources block is incomplete: %+v", report.Sources)
	}
	if report.Sources.Accounting == "" {
		t.Error("the JSON does not say whether the kernel counts bytes")
	}
}

// Every rate in the JSON was measured over a window, and the window is in
// there with it: a number whose window is not stated cannot be checked.
func TestRunCheckReportsTheWindowEveryRateWasMeasuredOver(t *testing.T) {
	var out strings.Builder
	if err := runCheck(traffic.NewFake(), nil, 20*time.Millisecond, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	var report checkReport
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, rate := range report.Interfaces {
		if rate.Window <= 0 {
			t.Fatalf("%s has no window: %+v", rate.Name, rate)
		}
		if rate.Name == "" {
			t.Fatalf("a rate with no interface: %+v", rate)
		}
	}
}
