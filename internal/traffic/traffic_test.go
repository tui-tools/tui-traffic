package traffic

import (
	"math"
	"testing"
	"time"
)

// sample is a small builder for the tests below: a moment, and one interface
// per pair of byte counters.
func sample(at time.Time, counters map[string][2]uint64) Sample {
	s := Sample{At: at, Links: map[string]Link{}}
	for name, pair := range counters {
		s.Interfaces = append(s.Interfaces, Interface{
			Name: name,
			RX:   Counters{Bytes: pair[0], Packets: pair[0] / 1000},
			TX:   Counters{Bytes: pair[1], Packets: pair[1] / 1000},
		})
		s.Links[name] = Link{State: "up", Carrier: true}
	}
	return s
}

func TestRatesBetween(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		window time.Duration
		prev   map[string][2]uint64
		cur    map[string][2]uint64
		want   map[string][2]float64
	}{
		{
			name:   "one second is the counters' difference",
			window: time.Second,
			prev:   map[string][2]uint64{"eth0": {1_000_000, 500_000}},
			cur:    map[string][2]uint64{"eth0": {1_250_000, 600_000}},
			want:   map[string][2]float64{"eth0": {250_000, 100_000}},
		},
		{
			// The window is what a rate is divided by, so a sample that
			// arrived late reports the same throughput rather than double it.
			name:   "a longer window divides by the window",
			window: 4 * time.Second,
			prev:   map[string][2]uint64{"eth0": {1_000_000, 0}},
			cur:    map[string][2]uint64{"eth0": {1_400_000, 0}},
			want:   map[string][2]float64{"eth0": {100_000, 0}},
		},
		{
			name:   "an idle interface reports zero, not nothing",
			window: time.Second,
			prev:   map[string][2]uint64{"lo": {42, 42}},
			cur:    map[string][2]uint64{"lo": {42, 42}},
			want:   map[string][2]float64{"lo": {0, 0}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rates := RatesBetween(sample(start, tc.prev),
				sample(start.Add(tc.window), tc.cur))
			if len(rates) != len(tc.want) {
				t.Fatalf("got %d rates, want %d", len(rates), len(tc.want))
			}
			for _, rate := range rates {
				want, ok := tc.want[rate.Name]
				if !ok {
					t.Fatalf("unexpected interface %q", rate.Name)
				}
				if math.Abs(rate.RXBytesPerSecond-want[0]) > 0.001 ||
					math.Abs(rate.TXBytesPerSecond-want[1]) > 0.001 {
					t.Errorf("%s = %.1f/%.1f, want %.1f/%.1f", rate.Name,
						rate.RXBytesPerSecond, rate.TXBytesPerSecond,
						want[0], want[1])
				}
				if rate.Window != tc.window {
					t.Errorf("Window = %s, want %s", rate.Window, tc.window)
				}
			}
		})
	}
}

// A counter that went backwards was reset or wrapped. The subtraction would
// give an enormous number, and an interface that appears to have moved twenty
// exabytes in a second is worse than a gap in the graph.
func TestRatesBetweenHandlesAResetCounter(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	rates := RatesBetween(
		sample(start, map[string][2]uint64{"veth0": {9_000_000, 9_000_000}}),
		sample(start.Add(time.Second), map[string][2]uint64{"veth0": {12, 12}}))

	if len(rates) != 1 {
		t.Fatalf("got %d rates, want 1", len(rates))
	}
	if !rates[0].Reset {
		t.Error("a counter that went backwards is a reset and is flagged as one")
	}
	if rates[0].BytesPerSecond() != 0 {
		t.Errorf("rate = %.0f, want zero rather than the wrapped difference",
			rates[0].BytesPerSecond())
	}
}

// An interface that appeared between the samples has no rate yet. Dividing
// its lifetime total by the window would show a brand new bridge as the
// busiest thing on the machine.
func TestRatesBetweenSkipsANewInterface(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	rates := RatesBetween(
		sample(start, map[string][2]uint64{"eth0": {0, 0}}),
		sample(start.Add(time.Second), map[string][2]uint64{
			"eth0": {100, 100}, "docker0": {5_000_000_000, 0}}))

	if len(rates) != 1 || rates[0].Name != "eth0" {
		t.Fatalf("got %+v, want only the interface present in both samples", rates)
	}
}

func TestRatesBetweenRejectsAWindowThatIsNotOne(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	counters := map[string][2]uint64{"eth0": {1, 1}}
	for _, window := range []time.Duration{0, -time.Second} {
		if rates := RatesBetween(sample(at, counters),
			sample(at.Add(window), counters)); rates != nil {
			t.Errorf("window %s produced rates: %+v", window, rates)
		}
	}
}

// The busiest interface is first, because that is what the reader opened the
// screen to find. Ties fall back to the name so the order is stable while
// nothing is moving.
func TestSortRatesPutsTheBusiestFirst(t *testing.T) {
	rates := []Rate{
		{Name: "lo"},
		{Name: "eth0", RXBytesPerSecond: 1_000},
		{Name: "docker0"},
		{Name: "wlan0", TXBytesPerSecond: 5_000},
	}
	SortRates(rates)

	want := []string{"wlan0", "eth0", "docker0", "lo"}
	for i, name := range want {
		if rates[i].Name != name {
			t.Fatalf("order = %v, want %v", names(rates), want)
		}
	}
}

func names(rates []Rate) []string {
	out := make([]string, len(rates))
	for i, rate := range rates {
		out[i] = rate.Name
	}
	return out
}

func TestRateUpTreatsUnknownAsUp(t *testing.T) {
	tests := []struct {
		state string
		want  bool
	}{
		// Loopback and most tunnels have no carrier to report, and they are
		// up as far as anybody using them is concerned.
		{"unknown", true},
		{"up", true},
		{"down", false},
		{"dormant", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			rate := Rate{Link: Link{State: tc.state}}
			if got := rate.Up(); got != tc.want {
				t.Errorf("Up() = %v for %q, want %v", got, tc.state, tc.want)
			}
		})
	}
}
