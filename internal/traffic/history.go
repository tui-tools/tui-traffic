package traffic

import "strings"

// HistoryLength is how many samples the sparkline remembers: sixty, which at
// the default one-second interval is the last minute. It is the only state
// this tool keeps, it lives in memory, and it is gone when the program exits.
const HistoryLength = 60

// sparkRunes are the eight block heights a sparkline is drawn from, lowest
// first. A terminal that cannot draw them draws nothing legible either way,
// and every terminal this family targets can.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// History is the per-interface ring of recent throughput readings the
// sparkline is drawn from.
//
// It is a ring rather than a growing slice because this program runs for
// hours on a screen nobody is watching: a minute of history is what the
// sparkline shows, so a minute is what is kept.
type History struct {
	length int
	series map[string][]float64
}

// NewHistory returns a history keeping the last length readings per
// interface. A length of zero or less uses HistoryLength.
func NewHistory(length int) *History {
	if length <= 0 {
		length = HistoryLength
	}
	return &History{length: length, series: map[string][]float64{}}
}

// Record appends one reading per interface and forgets the interfaces that
// were not in this round of rates. Forgetting matters: a machine that brings
// up a container network per build would otherwise accumulate a series per
// bridge for as long as the tool is open.
func (h *History) Record(rates []Rate) {
	seen := make(map[string]bool, len(rates))
	for _, rate := range rates {
		seen[rate.Name] = true
		series := append(h.series[rate.Name], rate.BytesPerSecond())
		if len(series) > h.length {
			series = series[len(series)-h.length:]
		}
		h.series[rate.Name] = series
	}
	for name := range h.series {
		if !seen[name] {
			delete(h.series, name)
		}
	}
}

// Series returns the readings kept for an interface, oldest first.
func (h *History) Series(name string) []float64 { return h.series[name] }

// Len is how many interfaces are being remembered.
func (h *History) Len() int { return len(h.series) }

// Sparkline draws a series as one line of block characters, width columns
// wide, showing the most recent width readings.
//
// The scale is the maximum of the window shown, not a fixed one: the shape of
// the last minute is what the line is for, and an interface whose peak is a
// kilobyte deserves the same shape as one whose peak is a gigabit. That makes
// the line unreadable as an absolute quantity, which is why the numbers are
// in the columns beside it and never only in the picture.
//
// A window with nothing in it yet is padded on the left, so the line grows
// from the right as samples arrive rather than stretching.
func Sparkline(values []float64, width int) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}

	peak := 0.0
	for _, value := range values {
		if value > peak {
			peak = value
		}
	}

	var line strings.Builder
	line.Grow(width * 3)
	for range width - len(values) {
		line.WriteByte(' ')
	}
	for _, value := range values {
		switch {
		case peak <= 0 || value <= 0:
			// Nothing moved. The floor rune draws the baseline, which reads
			// as a quiet interface; a blank would read as no data at all,
			// and those are different things.
			line.WriteRune(sparkRunes[0])
		default:
			index := int(value / peak * float64(len(sparkRunes)-1))
			if index >= len(sparkRunes) {
				index = len(sparkRunes) - 1
			}
			line.WriteRune(sparkRunes[index])
		}
	}
	return line.String()
}
