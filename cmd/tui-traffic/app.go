package main

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// screen is one of the three views the tool is made of. They are tabs rather
// than nested screens because they answer three separate questions about the
// same machine, and a reader arrives with one of them already in mind.
type screen int

const (
	// screenInterfaces is throughput, which is the reason the tool exists.
	screenInterfaces screen = iota
	// screenConnections is the conntrack table summarised.
	screenConnections
	// screenSockets is what is listening and what is established.
	screenSockets
	screenCount
)

// title names a screen for the tab bar.
func (s screen) title() string {
	switch s {
	case screenConnections:
		return "connections"
	case screenSockets:
		return "sockets"
	default:
		return "interfaces"
	}
}

// mode is the dialog the app currently has open. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	modeBrowse mode = iota
	modeFilter
	modeHelp
)

// readTimeout bounds one round of reads, so a machine whose conntrack table
// is enormous cannot wedge the refresh loop behind it.
const readTimeout = 10 * time.Second

// app is the tui-traffic Bubble Tea model.
type app struct {
	backend traffic.Backend
	theme   theme.Theme
	// backendCompat is what the version probe found, rendered in the header.
	backendCompat []compat.Result
	// interval is how often a sample is taken.
	interval time.Duration

	// history is the last minute of throughput per interface, which is the
	// only state this tool keeps and the only thing the sparkline is drawn
	// from. It never touches the disk.
	history *traffic.History
	// previous is the last sample, which the next one is subtracted from: a
	// rate is a difference over time and there is no rate without both.
	previous traffic.Sample
	// samples counts the reads so far, so the first screen can say it is
	// waiting for the second one rather than showing zeroes as if they were
	// measurements.
	samples int

	rates       []traffic.Rate
	connections traffic.Connections
	sockets     traffic.Sockets
	// haveConnections and haveSockets record whether those screens have ever
	// been read, so switching to one shows "reading…" rather than "nothing".
	haveConnections bool
	haveSockets     bool

	width, height int
	screen        screen
	// cursor and offset are per screen, so moving between tabs does not lose
	// the row the reader was on.
	cursor [screenCount]int
	offset [screenCount]int
	filter string

	mode  mode
	input ui.Input

	// paused stops the sampling without stopping the program, for reading a
	// row that keeps moving out from under the cursor.
	paused bool
	// generation guards the timer: a re-read on demand starts a new chain,
	// and a tick from the old one is ignored rather than doubling the rate.
	generation int

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last read failed, so the empty state does
	// not claim there is simply nothing to show.
	loadFailed bool
}

// readMsg carries one round of reads. Which of the three it contains depends
// on the screen that is open: the interface counters are always read, because
// the sparkline's history has to stay continuous while the reader is looking
// at something else, and the two heavier reads are done only for the screen
// that is showing them.
type readMsg struct {
	generation  int
	sample      traffic.Sample
	connections *traffic.Connections
	sockets     *traffic.Sockets
	err         error
}

// tickMsg wakes the next round of reads.
type tickMsg struct{ generation int }

// newApp builds the model around a backend.
func newApp(backend traffic.Backend, th theme.Theme,
	backendCompat []compat.Result, interval time.Duration) *app {
	a := &app{
		backend: backend, theme: th, backendCompat: backendCompat,
		interval: interval, history: traffic.NewHistory(traffic.HistoryLength),
		width: 80, height: 24, loading: true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.read() }

// read takes one round of readings in the background.
func (a *app) read() tea.Cmd {
	backend := a.backend
	generation := a.generation
	wantConnections := a.screen == screenConnections || !a.haveConnections
	wantSockets := a.screen == screenSockets || !a.haveSockets

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()

		msg := readMsg{generation: generation}
		sample, err := backend.Sample(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.sample = sample

		// A failure on one of the two heavier reads is reported without
		// losing the sample: the interfaces screen keeps working on a machine
		// whose conntrack table cannot be read, which is most of them.
		if wantConnections {
			if connections, err := backend.Connections(ctx); err == nil {
				msg.connections = &connections
			} else if msg.err == nil {
				msg.err = err
			}
		}
		if wantSockets {
			if sockets, err := backend.Sockets(ctx); err == nil {
				msg.sockets = &sockets
			} else if msg.err == nil {
				msg.err = err
			}
		}
		return msg
	}
}

// tick schedules the next round.
func (a *app) tick() tea.Cmd {
	generation := a.generation
	return tea.Tick(a.interval, func(time.Time) tea.Msg {
		return tickMsg{generation: generation}
	})
}

// refresh abandons the running timer chain and starts a new one immediately.
// It is what `r` does, and what resuming from a pause does.
func (a *app) refresh() tea.Cmd {
	a.generation++
	a.loading = true
	return a.read()
}

// setStatus records a message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case readMsg:
		if msg.generation != a.generation {
			// A read from an abandoned chain. Dropping it keeps the sample
			// spacing honest, which is what the rates are divided by.
			return a, nil
		}
		return a, a.applyRead(msg)

	case tickMsg:
		if msg.generation != a.generation {
			return a, nil
		}
		if a.paused {
			return a, a.tick()
		}
		return a, a.read()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// applyRead folds one round of readings into the model and schedules the next.
func (a *app) applyRead(msg readMsg) tea.Cmd {
	a.loading = false
	if msg.err != nil && msg.sample.At.IsZero() {
		a.loadFailed = true
		a.setStatus(ui.StatusError, msg.err.Error())
		return a.tick()
	}
	a.loadFailed = false
	if msg.err != nil {
		// The sample arrived and something else did not. The screen that
		// wanted it says so, and the interfaces keep updating.
		a.setStatus(ui.StatusWarn, msg.err.Error())
	} else if a.statusKind == ui.StatusError || a.statusKind == ui.StatusWarn {
		a.setStatus(ui.StatusInfo, "")
	}

	if a.samples > 0 {
		if rates := traffic.RatesBetween(a.previous, msg.sample); rates != nil {
			a.rates = rates
			a.history.Record(rates)
		}
	}
	a.previous = msg.sample
	a.samples++

	if msg.connections != nil {
		a.connections, a.haveConnections = *msg.connections, true
	}
	if msg.sockets != nil {
		a.sockets, a.haveSockets = *msg.sockets, true
	}
	a.clampCursor()
	return a.tick()
}

// handleKey routes a key press to the open screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	switch a.mode {
	case modeFilter:
		return a.handleFilter(msg)
	case modeHelp:
		a.mode = modeBrowse
		return a, nil
	default:
		return a.handleBrowseKey(msg)
	}
}

// handleFilter resolves the filter prompt.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		// Filter as the user types.
		a.filter = a.input.Value()
		a.clampCursor()
		return a, cmd
	}
	if a.input.Accepted {
		a.filter = a.input.Value()
	} else {
		a.filter = ""
	}
	a.clampCursor()
	a.mode = modeBrowse
	return a, nil
}

// handleBrowseKey handles the three main screens, which share every key.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "1":
		return a, a.show(screenInterfaces)
	case "2":
		return a, a.show(screenConnections)
	case "3":
		return a, a.show(screenSockets)
	case "tab", "l", "right":
		return a, a.show((a.screen + 1) % screenCount)
	case "shift+tab", "h", "left":
		return a, a.show((a.screen + screenCount - 1) % screenCount)
	case "p", " ":
		a.paused = !a.paused
		if a.paused {
			a.setStatus(ui.StatusInfo, "paused — p resumes")
			return a, nil
		}
		a.setStatus(ui.StatusInfo, "")
		return a, a.refresh()
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
	case "G", "end":
		a.cursor[a.screen] = max(a.rowCount()-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "/":
		a.input = ui.NewInput("Filter", "name, address, state…", a.filter)
		a.input.Help = "Empty clears the filter."
		a.mode = modeFilter
	case "r", "ctrl+r":
		return a, a.refresh()
	}
	return a, nil
}

// show switches to a screen, reading it at once when it has never been read.
// Waiting a whole interval to find out what is on a tab you just opened is
// the kind of delay that reads as a broken tool.
func (a *app) show(s screen) tea.Cmd {
	if a.screen == s {
		return nil
	}
	a.screen = s
	a.clampCursor()
	switch s {
	case screenConnections:
		if !a.haveConnections {
			return a.refresh()
		}
	case screenSockets:
		if !a.haveSockets {
			return a.refresh()
		}
	}
	return nil
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor[a.screen] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset within range. It runs
// after every read as well as after every key, because the rows under the
// cursor are re-sorted every second: an interface that went quiet moves down
// the list, and the viewport has to follow it rather than scroll off the end.
func (a *app) clampCursor() {
	count := a.rowCount()
	if count == 0 {
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
		return
	}
	a.cursor[a.screen] = min(max(a.cursor[a.screen], 0), count-1)

	height := a.tableHeight()
	if a.cursor[a.screen] < a.offset[a.screen] {
		a.offset[a.screen] = a.cursor[a.screen]
	}
	if a.cursor[a.screen] >= a.offset[a.screen]+height {
		a.offset[a.screen] = a.cursor[a.screen] - height + 1
	}
	a.offset[a.screen] = max(min(a.offset[a.screen], max(count-height, 0)), 0)
}

// matches reports whether a row survives the filter, which is a plain
// case-insensitive substring over everything the row would show.
func (a *app) matches(text string) bool {
	if a.filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(a.filter))
}
