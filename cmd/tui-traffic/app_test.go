package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// newTestApp builds the model over the demo machine, sized like an ordinary
// terminal.
func newTestApp(t *testing.T) *app {
	t.Helper()
	a := newApp(traffic.NewFake(), theme.FromPalette(theme.TokyoNight()), nil,
		10*time.Millisecond)
	a.width, a.height = 120, 30
	return a
}

// step runs a command and feeds its message back into the model, which is
// what the Bubble Tea runtime does. A tick is not run: waiting for a timer in
// a test buys nothing, and every read the model does is reachable directly.
func step(t *testing.T, a *app, cmd tea.Cmd) {
	t.Helper()
	for range 4 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if _, ok := msg.(tickMsg); ok {
			// The chain would wait here; the test drives the next read itself.
			return
		}
		_, cmd = a.Update(msg)
	}
}

// sample drives one full round of reads.
func sample(t *testing.T, a *app) {
	t.Helper()
	step(t, a, a.read())
}

// A rate is the difference between two samples, so the first one cannot show
// one — and the screen says it is measuring rather than drawing zeroes as if
// they were readings.
func TestFirstSampleHasNoRatesYet(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())

	if a.samples != 1 {
		t.Fatalf("samples = %d, want 1", a.samples)
	}
	if len(a.rates) != 0 {
		t.Errorf("the first sample produced rates: %+v", a.rates)
	}
	if !strings.Contains(a.waitingMessage(), "second reading") {
		t.Errorf("waiting message = %q", a.waitingMessage())
	}

	time.Sleep(15 * time.Millisecond)
	sample(t, a)
	if len(a.rates) == 0 {
		t.Fatal("the second sample produced no rates")
	}
	if a.history.Len() == 0 {
		t.Error("the sparkline history was not recorded")
	}
	if got := a.View(); !strings.Contains(got, "INTERFACE") {
		t.Errorf("the interfaces table is not on screen:\n%s", got)
	}
}

// The three screens are tabs, and each one keeps its own cursor: moving
// between them must not lose the row the reader was on.
func TestScreensAreTabsWithTheirOwnCursor(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())
	time.Sleep(15 * time.Millisecond)
	sample(t, a)

	press(t, a, "j")
	press(t, a, "j")
	if a.cursor[screenInterfaces] != 2 {
		t.Fatalf("cursor = %d, want 2", a.cursor[screenInterfaces])
	}

	press(t, a, "2")
	if a.screen != screenConnections {
		t.Fatalf("screen = %v, want connections", a.screen)
	}
	if a.cursor[screenConnections] != 0 {
		t.Errorf("the new screen inherited a cursor: %d", a.cursor[screenConnections])
	}
	press(t, a, "1")
	if a.cursor[screenInterfaces] != 2 {
		t.Errorf("the cursor was lost switching screens: %d",
			a.cursor[screenInterfaces])
	}

	press(t, a, "tab")
	press(t, a, "tab")
	if a.screen != screenSockets {
		t.Errorf("two tabs from interfaces should be sockets, got %v", a.screen)
	}
	press(t, a, "shift+tab")
	if a.screen != screenConnections {
		t.Errorf("shift+tab goes back, got %v", a.screen)
	}
}

// Pausing is what makes a moving screen readable. It stops the sampling
// without stopping the program, and resuming samples at once rather than
// waiting out the interval.
func TestPauseStopsTheSampling(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())

	press(t, a, "p")
	if !a.paused {
		t.Fatal("p did not pause")
	}
	before := a.samples
	// A tick from the running chain arrives and does nothing but re-arm.
	if _, cmd := a.Update(tickMsg{generation: a.generation}); cmd == nil {
		t.Error("a paused tick should still re-arm the timer")
	}
	if a.samples != before {
		t.Errorf("samples went from %d to %d while paused", before, a.samples)
	}

	press(t, a, "p")
	if a.paused {
		t.Fatal("p did not resume")
	}
}

// A read from an abandoned chain is dropped rather than applied. Two timers
// running at once would halve the interval the rates are divided by, which
// would show every number as half what it is.
func TestAReadFromAnOldChainIsIgnored(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())
	before := a.samples

	a.Update(readMsg{generation: a.generation - 1})
	if a.samples != before {
		t.Errorf("a stale read was applied: samples %d -> %d", before, a.samples)
	}
}

func TestFilterNarrowsTheRows(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())
	time.Sleep(15 * time.Millisecond)
	sample(t, a)

	all := a.rowCount()
	if all < 3 {
		t.Fatalf("the demo machine has %d interfaces, want several", all)
	}
	a.filter = "wlan"
	if got := a.rowCount(); got != 1 {
		t.Errorf("filtered rows = %d, want 1", got)
	}
	a.filter = "no-such-interface"
	if got := a.rowCount(); got != 0 {
		t.Errorf("filtered rows = %d, want none", got)
	}
	if view := a.View(); !strings.Contains(view, "nothing matches") {
		t.Errorf("an empty filter result should say so:\n%s", view)
	}
}

// The layout has to survive a narrow pane. Columns drop from the right, and
// nothing may be drawn wider than the terminal.
func TestTheLayoutSurvivesANarrowTerminal(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())
	time.Sleep(15 * time.Millisecond)
	sample(t, a)

	for _, width := range []int{40, 58, 80, 120, 200} {
		a.width = width
		a.clampCursor()
		for s := screen(0); s < screenCount; s++ {
			a.screen = s
			for _, line := range strings.Split(a.View(), "\n") {
				if got := len([]rune(line)); got > width {
					t.Fatalf("at width %d the %s screen drew a %d-column line: %q",
						width, s.title(), got, line)
				}
			}
		}
	}
}

// The connections screen exists to be trusted, and the two things that make
// it trustworthy are on it: where the numbers came from, and whether the
// kernel counted any bytes.
func TestConnectionsScreenNamesItsSource(t *testing.T) {
	a := newTestApp(t)
	step(t, a, a.Init())
	a.screen = screenConnections

	view := a.View()
	for _, want := range []string{"byte accounting", "conntrack"} {
		if !strings.Contains(view, want) {
			t.Errorf("the connections screen does not show %q:\n%s", want, view)
		}
	}
}

// With accounting off there is no list of busiest flows, and the screen says
// why rather than leaving a hole where a column was.
func TestConnectionsScreenExplainsAMissingByteColumn(t *testing.T) {
	a := newTestApp(t)
	a.screen = screenConnections
	a.connections = traffic.Summarise(nil, traffic.SourceConntrack,
		"conntrack -L -o extended", "", traffic.AccountingOff)
	a.connections.Total = 12
	a.haveConnections = true

	_, rows := a.connectionsTable()
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the one that explains the missing column", len(rows))
	}
	if !strings.Contains(strings.Join(rows[0].cells, " "), "nf_conntrack_acct") {
		t.Errorf("the row does not name the setting: %+v", rows[0].cells)
	}
}

// The whole tool is a read. There is no key that changes anything, and this
// is the assertion that says so: every key on the keyboard is pressed, and
// the backend records nothing but reads.
func TestNoKeyChangesAnything(t *testing.T) {
	counter := &countingBackend{Backend: traffic.NewFake()}
	a := newApp(counter, theme.FromPalette(theme.TokyoNight()), nil,
		10*time.Millisecond)
	a.width, a.height = 120, 30
	step(t, a, a.Init())

	keys := []string{"a", "b", "c", "d", "e", "f", "i", "n", "o", "s", "t", "u",
		"v", "w", "x", "y", "z", "A", "D", "R", "X", "enter", "delete",
		"backspace", "1", "2", "3", "tab", "p", "r", "j", "k", "/", "?"}
	for _, key := range keys {
		if a.mode == modeFilter || a.mode == modeHelp {
			// Leave whatever the previous key opened, so the next key is
			// pressed against the browse screen.
			press(t, a, "esc")
			a.mode = modeBrowse
		}
		press(t, a, key)
	}

	if counter.reads == 0 {
		t.Fatal("the keys drove no reads at all, so nothing was exercised")
	}
	// There is nothing else to assert against, and that is the point: the
	// backend interface has no method that writes, so a key that changed
	// something could not compile.
}

// countingBackend counts the reads a session makes.
type countingBackend struct {
	traffic.Backend
	reads int
}

func (c *countingBackend) Sample(ctx context.Context) (traffic.Sample, error) {
	c.reads++
	return c.Backend.Sample(ctx)
}

func (c *countingBackend) Connections(ctx context.Context) (traffic.Connections, error) {
	c.reads++
	return c.Backend.Connections(ctx)
}

func (c *countingBackend) Sockets(ctx context.Context) (traffic.Sockets, error) {
	c.reads++
	return c.Backend.Sockets(ctx)
}

// press sends one key to the model and runs whatever it asked for.
func press(t *testing.T, a *app, key string) {
	t.Helper()
	_, cmd := a.Update(keyMsg(key))
	step(t, a, cmd)
}

// keyMsg builds the key message the runtime would send.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "delete":
		return tea.KeyMsg{Type: tea.KeyDelete}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}
