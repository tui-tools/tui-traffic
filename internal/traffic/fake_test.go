package traffic

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The demo exists so a reviewer can judge the tool without a machine to point
// it at, and on a read-only tool that is most of what there is to judge. What
// has to hold is that the numbers move: a demo whose sparkline is a flat line
// shows none of what this screen is for.
func TestFakeCountersMove(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()

	first, err := fake.Sample(ctx)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	second, err := fake.Sample(ctx)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}

	rates := RatesBetween(first, second)
	if len(rates) == 0 {
		t.Fatal("no rates between two demo samples")
	}
	var moving int
	for _, rate := range rates {
		if rate.Reset {
			t.Errorf("the demo counters went backwards on %s", rate.Name)
		}
		if rate.BytesPerSecond() > 0 {
			moving++
		}
	}
	if moving < 4 {
		t.Errorf("%d of %d demo interfaces are moving data, want most of them",
			moving, len(rates))
	}
	// The busiest is first, which is the sort the real screen uses.
	if rates[0].Name != "enp3s0" {
		t.Errorf("busiest = %q, want the wired interface", rates[0].Name)
	}
	// And the one that is down carries nothing at all.
	for _, rate := range rates {
		if rate.Name == "enp4s0" && rate.BytesPerSecond() != 0 {
			t.Errorf("an interface that is down is moving data: %+v", rate)
		}
	}
}

// The demo machine's lifetime totals should look like a machine that has been
// up for a while, not one that started when the program did.
func TestFakeHasRunInCounters(t *testing.T) {
	sample, err := NewFake().Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	for _, item := range sample.Interfaces {
		if item.Name == "enp3s0" {
			if item.RX.Bytes < 100_000_000_000 {
				t.Errorf("the busy interface has moved only %d bytes since boot",
					item.RX.Bytes)
			}
			if item.RX.Errors == 0 {
				t.Error("a long-running interface has accumulated some errors")
			}
			return
		}
	}
	t.Fatal("the demo machine has no enp3s0")
}

func TestFakeConnectionsAndSockets(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()

	connections, err := fake.Connections(ctx)
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if connections.Accounting != AccountingOn {
		t.Errorf("Accounting = %q: the demo shows the screen with the "+
			"talkers on it", connections.Accounting)
	}
	if len(connections.Talkers) == 0 {
		t.Error("accounting is on and there are no talkers")
	}
	if connections.Total != len(demoFlows()) {
		t.Errorf("Total = %d, want %d", connections.Total, len(demoFlows()))
	}

	sockets, err := fake.Sockets(ctx)
	if err != nil {
		t.Fatalf("Sockets: %v", err)
	}
	if len(sockets.Listening) == 0 {
		t.Error("the demo machine listens on nothing")
	}
}

// The demo says it is a demo on every screen. A demo mistaken for a reading
// of the machine in front of you is worse than no demo at all.
func TestFakeSaysItIsOne(t *testing.T) {
	fake := NewFake()
	if fake.Name() != "demo" {
		t.Errorf("Name() = %q", fake.Name())
	}
	for _, want := range []string{"demo", "does not exist"} {
		if !strings.Contains(fake.Describe(), want) {
			t.Errorf("Describe() = %q, want it to carry %q", fake.Describe(), want)
		}
	}
}
