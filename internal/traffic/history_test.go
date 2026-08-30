package traffic

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHistoryKeepsAWindow(t *testing.T) {
	history := NewHistory(3)
	for i := range 5 {
		history.Record([]Rate{{Name: "eth0", RXBytesPerSecond: float64(i)}})
	}
	got := history.Series("eth0")
	want := []float64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("series = %v, want the last %d readings", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("series = %v, want %v", got, want)
		}
	}
}

// A machine that brings up a container network per build would otherwise
// accumulate a series per bridge for as long as the tool is open.
func TestHistoryForgetsAnInterfaceThatWentAway(t *testing.T) {
	history := NewHistory(10)
	history.Record([]Rate{{Name: "eth0"}, {Name: "veth1234"}})
	if history.Len() != 2 {
		t.Fatalf("Len = %d, want 2", history.Len())
	}
	history.Record([]Rate{{Name: "eth0"}})
	if history.Len() != 1 {
		t.Errorf("Len = %d, want the vanished interface forgotten", history.Len())
	}
	if history.Series("veth1234") != nil {
		t.Error("the vanished interface still has a series")
	}
}

func TestNewHistoryDefaultsToAMinute(t *testing.T) {
	for _, length := range []int{0, -1} {
		history := NewHistory(length)
		for range HistoryLength + 5 {
			history.Record([]Rate{{Name: "eth0"}})
		}
		if got := len(history.Series("eth0")); got != HistoryLength {
			t.Errorf("NewHistory(%d) kept %d readings, want %d", length, got,
				HistoryLength)
		}
	}
}

func TestSparkline(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		width  int
		want   string
	}{
		{"a rising series", []float64{0, 1, 2, 3, 4, 5, 6, 7}, 8, "▁▂▃▄▅▆▇█"},
		{"a flat series draws the baseline", []float64{5, 5, 5}, 3, "███"},
		{"an idle interface is a baseline, not a blank",
			[]float64{0, 0, 0}, 3, "▁▁▁"},
		{"a short series grows from the right",
			[]float64{4, 8}, 5, "   ▄█"},
		{"a long series shows the most recent window",
			[]float64{100, 0, 0, 0}, 3, "▁▁▁"},
		{"nothing to draw yet", nil, 4, "    "},
		{"no room to draw it", []float64{1, 2}, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Sparkline(tc.values, tc.width)
			if got != tc.want {
				t.Errorf("Sparkline(%v, %d) = %q, want %q", tc.values, tc.width,
					got, tc.want)
			}
		})
	}
}

// The line is drawn into a fixed column, so it has to be exactly as wide as
// it was asked for however odd the values are. A line one cell too long
// pushes every column after it off the screen.
func TestSparklineIsAlwaysTheWidthAskedFor(t *testing.T) {
	series := [][]float64{
		nil, {0}, {1, 2, 3}, {-5, 0, 5}, {1e18, 1, 0},
		make([]float64, 200),
	}
	for _, values := range series {
		for width := 1; width <= 12; width++ {
			line := Sparkline(values, width)
			if got := utf8.RuneCountInString(line); got != width {
				t.Fatalf("Sparkline(%v, %d) is %d cells wide: %q",
					values, width, got, line)
			}
			if strings.ContainsAny(line, "\n\t") {
				t.Fatalf("a sparkline broke its line: %q", line)
			}
		}
	}
}
