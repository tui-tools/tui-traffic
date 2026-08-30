package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// Layout constants: the rows the table cannot use.
const (
	headerLines = 2
	tabLines    = 1
	footerLines = 2
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// The widths at which each column earns its place. A 40-column pane keeps the
// interface and the two rates, which is the least that is still the tool;
// everything else is added back as there is room for it.
const (
	widthForSparkline = 58
	widthForPackets   = 76
	widthForLink      = 96
	widthForErrors    = 112
)

// sparkWidth is how many columns the last-minute picture is drawn into. It is
// not the sixty samples the history keeps: the line shows the most recent
// twenty-four of them, which at one second each is the shape of the last
// half-minute at a size that fits beside the numbers.
const sparkWidth = 24

// row is one line of any of the three tables, with what the filter matches.
type row struct {
	cells []string
	style *lipgloss.Style
	text  string
}

// tableHeight is the number of rows that fit on screen.
func (a *app) tableHeight() int {
	return max(a.height-headerLines-tabLines-footerLines-1, minTableHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeFilter:
		return a.input.View(a.theme, a.width, a.height)
	case modeHelp:
		return lipgloss.Place(a.width, a.height, lipgloss.Center, lipgloss.Center,
			ui.HelpScreen(a.theme, "tui-traffic — keys", helpKeys(), a.width))
	default:
		return a.browseView()
	}
}

// browseView renders a screen: header, tab bar, table, help bar, status. Every
// tool in the family draws these same bands.
func (a *app) browseView() string {
	columns, rows := a.table()

	var body string
	switch {
	case a.loading && len(rows) == 0:
		body = ui.EmptyState(a.theme, a.waitingMessage(), a.width, a.tableHeight()+1)
	case len(rows) == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme, "could not read — see the message below",
			a.width, a.tableHeight()+1)
	case len(rows) == 0 && a.filter != "":
		body = ui.EmptyState(a.theme, "nothing matches "+strconv.Quote(a.filter),
			a.width, a.tableHeight()+1)
	case len(rows) == 0:
		body = ui.EmptyState(a.theme, a.emptyMessage(), a.width, a.tableHeight()+1)
	default:
		body = a.renderTable(columns, rows)
	}

	help := ui.HelpBar(a.theme, shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(),
		a.width)
	return strings.Join([]string{a.header(), a.tabs(), body, help, status}, "\n")
}

// waitingMessage is what a screen says before it has anything. On the
// interfaces screen it is worth being specific: the first sample cannot show
// a rate, because a rate is the difference between two of them.
func (a *app) waitingMessage() string {
	if a.screen == screenInterfaces && a.samples == 1 {
		return "measuring — a rate needs a second reading, one interval from now"
	}
	return "reading…"
}

// emptyMessage is what a screen with no rows says, which is different on each.
func (a *app) emptyMessage() string {
	switch a.screen {
	case screenConnections:
		if a.connections.Total == 0 {
			return "no connections are being tracked right now"
		}
		return "nothing to list: the counts are in the header"
	case screenSockets:
		return "nothing is listening on this machine"
	default:
		return "no interface reported counters"
	}
}

// header renders the facts at the top of every screen.
func (a *app) header() string {
	t := a.theme
	facts := a.screenFacts()

	if a.paused {
		style := t.Warn
		facts = append(facts, ui.Fact{Label: "refresh", Value: "paused",
			Style: &style})
	} else {
		facts = append(facts, ui.Fact{Label: "every", Value: a.interval.String()})
	}
	// The conntrack version, when this machine has it. Most do not, and a
	// badge for a program that is not installed would be noise on nearly
	// every screen this tool draws.
	for _, result := range installed(a.backendCompat) {
		facts = append(facts, ui.CompatFact(t, result))
	}

	subtitle := a.backend.Describe()
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	return ui.Header{Title: "tui-traffic", Subtitle: subtitle, Facts: facts}.
		Render(t, a.width)
}

// screenFacts are the header facts that belong to the open screen.
func (a *app) screenFacts() []ui.Fact {
	t := a.theme
	switch a.screen {
	case screenConnections:
		facts := []ui.Fact{
			{Label: "tracked", Value: strconv.Itoa(a.connections.Total)},
			{Label: "from", Value: string(a.connections.Source)},
		}
		// Whether the kernel is counting bytes is the fact that explains the
		// missing column, so it is in the header rather than in a footnote.
		style := t.Warn
		if a.connections.Accounting == traffic.AccountingOn {
			style = t.OK
		}
		facts = append(facts, ui.Fact{Label: "byte accounting",
			Value: string(a.connections.Accounting), Style: &style})
		return facts

	case screenSockets:
		return []ui.Fact{
			{Label: "sockets", Value: strconv.Itoa(a.sockets.Total)},
			{Label: "listening", Value: strconv.Itoa(len(a.sockets.Listening))},
			{Label: "established", Value: strconv.Itoa(a.sockets.Established)},
		}

	default:
		var rx, tx float64
		up := 0
		for _, rate := range a.rates {
			rx += rate.RXBytesPerSecond
			tx += rate.TXBytesPerSecond
			if rate.Up() {
				up++
			}
		}
		return []ui.Fact{
			{Label: "interfaces", Value: strconv.Itoa(len(a.rates))},
			{Label: "up", Value: strconv.Itoa(up)},
			{Label: "total in", Value: perSecond(rx)},
			{Label: "total out", Value: perSecond(tx)},
		}
	}
}

// tabs renders the three screens as one row, with the current one accented.
func (a *app) tabs() string {
	var parts []string
	for s := screen(0); s < screenCount; s++ {
		label := strconv.Itoa(int(s)+1) + " " + s.title()
		if s == a.screen {
			parts = append(parts, a.theme.Accent.Render("["+label+"]"))
			continue
		}
		parts = append(parts, a.theme.Muted.Render(" "+label+" "))
	}
	return a.theme.Footer.Width(a.width).Render(
		ui.Truncate(strings.Join(parts, " "), a.width-2))
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	switch a.screen {
	case screenConnections:
		if a.connections.Note != "" {
			return ui.Truncate(a.connections.Note, a.width-4)
		}
		return a.connections.Detail + "  ·  tab to move  ·  ? for help"
	case screenSockets:
		return a.sockets.Source + "  ·  tab to move  ·  ? for help"
	default:
		return strconv.Itoa(a.rowCount()) + " interfaces  ·  " +
			"the line is the last " + strconv.Itoa(sparkWidth) +
			" samples  ·  ? for help"
	}
}

// renderTable draws the current screen's rows.
func (a *app) renderTable(columns []ui.Column, rows []row) string {
	cells := make([][]string, len(rows))
	styles := make([]*lipgloss.Style, len(rows))
	for i, r := range rows {
		cells[i], styles[i] = r.cells, r.style
	}
	return ui.Table{
		Columns: columns, Rows: cells, Styles: styles,
		Selected: a.cursor[a.screen], Offset: a.offset[a.screen],
		Height: a.tableHeight(),
	}.Render(a.theme, a.width)
}

// rowCount is how many rows the open screen has after the filter, which is
// what the cursor is clamped against.
func (a *app) rowCount() int {
	_, rows := a.table()
	return len(rows)
}

// table builds the columns and the rows of the open screen.
func (a *app) table() ([]ui.Column, []row) {
	switch a.screen {
	case screenConnections:
		return a.connectionsTable()
	case screenSockets:
		return a.socketsTable()
	default:
		return a.interfacesTable()
	}
}

// interfacesTable is throughput per interface, busiest first. The columns
// drop from the right as the terminal narrows, so a 40-column pane still
// carries the interface and its two rates — which is the least that is still
// this tool.
func (a *app) interfacesTable() ([]ui.Column, []row) {
	columns := []ui.Column{
		{Title: "INTERFACE", Width: 12, Flex: true},
		{Title: "IN/s", Width: 11},
		{Title: "OUT/s", Width: 11},
	}
	spark := a.width >= widthForSparkline
	packets := a.width >= widthForPackets
	link := a.width >= widthForLink
	errors := a.width >= widthForErrors
	if spark {
		columns = append(columns, ui.Column{Title: "LAST " +
			strconv.Itoa(sparkWidth) + "s", Width: sparkWidth})
	}
	if packets {
		columns = append(columns,
			ui.Column{Title: "IN p/s", Width: 9},
			ui.Column{Title: "OUT p/s", Width: 9})
	}
	if link {
		columns = append(columns, ui.Column{Title: "LINK", Width: 10})
	}
	if errors {
		columns = append(columns, ui.Column{Title: "ERR/DROP", Width: 13})
	}

	rows := make([]row, 0, len(a.rates))
	for _, rate := range a.rates {
		if !a.matches(rate.Name + " " + rate.Link.State) {
			continue
		}
		cells := []string{rate.Name, perSecond(rate.RXBytesPerSecond),
			perSecond(rate.TXBytesPerSecond)}
		if spark {
			cells = append(cells,
				traffic.Sparkline(a.history.Series(rate.Name), sparkWidth))
		}
		if packets {
			cells = append(cells, packetsPerSecond(rate.RXPacketsPerSecond),
				packetsPerSecond(rate.TXPacketsPerSecond))
		}
		if link {
			cells = append(cells, linkLabel(rate))
		}
		if errors {
			cells = append(cells, fmt.Sprintf("%s/%s",
				count(rate.Total.RX.Errors+rate.Total.TX.Errors),
				count(rate.Total.RX.Dropped+rate.Total.TX.Dropped)))
		}
		rows = append(rows, row{cells: cells, style: a.interfaceStyle(rate),
			text: rate.Name})
	}
	return columns, rows
}

// interfaceStyle colours a row, so the eye finds what matters without
// reading: an interface that is down is muted, one that is moving data is
// accented, and one that is up and idle is ordinary.
func (a *app) interfaceStyle(rate traffic.Rate) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case !rate.Up():
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	case rate.Reset:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case rate.BytesPerSecond() > 0:
		style = a.theme.Row.Foreground(a.theme.Info.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// linkLabel renders the link state, with the negotiated speed where the
// kernel knows one.
func linkLabel(rate traffic.Rate) string {
	if rate.Link.Speed > 0 {
		return fmt.Sprintf("%s %dM", rate.Link.State, rate.Link.Speed)
	}
	return rate.Link.State
}

// connectionsTable is the tracked connections: the counts by protocol and
// state, and then the busiest flows where the kernel counted any.
//
// The two are one table rather than two panes because they are two readings
// of the same set of connections, and a reader scrolls from one into the
// other. The talkers section carries its own heading row, which is also what
// says the section is missing when accounting is off.
func (a *app) connectionsTable() ([]ui.Column, []row) {
	wide := a.width >= widthForPackets
	columns := []ui.Column{
		{Title: "PROTO", Width: 7},
		{Title: "WHAT", Width: 30, Flex: true},
		{Title: "COUNT", Width: 12},
	}
	if wide {
		columns = append(columns, ui.Column{Title: "PACKETS", Width: 10})
	}

	pad := func(cells []string) []string {
		if wide && len(cells) == 3 {
			return append(cells, "")
		}
		return cells
	}

	rows := make([]row, 0, len(a.connections.States)+len(a.connections.Talkers)+1)
	for _, state := range a.connections.States {
		text := state.Protocol + " " + state.State
		if !a.matches(text) {
			continue
		}
		rows = append(rows, row{
			cells: pad([]string{state.Protocol, state.State,
				strconv.Itoa(state.Count)}),
			text: text,
		})
	}

	heading := a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	if len(a.connections.Talkers) > 0 {
		rows = append(rows, row{
			cells: pad([]string{"", "— busiest flows by bytes —", ""}),
			style: &heading, text: "",
		})
		for _, talker := range a.connections.Talkers {
			text := talker.Protocol + " " + talker.From + " " + talker.To
			if !a.matches(text) {
				continue
			}
			cells := []string{talker.Protocol, talker.From + " → " + talker.To,
				bytes(talker.Bytes)}
			if wide {
				cells = append(cells, count(talker.Packets))
			}
			rows = append(rows, row{cells: cells, text: text})
		}
		return columns, rows
	}

	// No talkers, and the reason is worth a row of its own: an absent column
	// with no explanation reads as a broken tool.
	if a.connections.Total > 0 && a.filter == "" {
		rows = append(rows, row{
			cells: pad([]string{"", noTalkersReason(a.connections), ""}),
			style: &heading, text: "",
		})
	}
	return columns, rows
}

// noTalkersReason says why there is no list of busiest flows.
func noTalkersReason(connections traffic.Connections) string {
	switch {
	case connections.Source == traffic.SourceSockets:
		return "— no byte figures: these are sockets, and nothing counted bytes —"
	case connections.Accounting == traffic.AccountingOn:
		return "— nothing has moved a byte yet —"
	default:
		return "— no byte figures: net.netfilter.nf_conntrack_acct is off —"
	}
}

// socketsTable is what this machine is listening on, by port. It is the
// answer to "what is exposed here", which is why it is sorted by port and not
// by anything else.
func (a *app) socketsTable() ([]ui.Column, []row) {
	wide := a.width >= widthForPackets
	columns := []ui.Column{
		{Title: "PROTO", Width: 7},
		{Title: "LISTENING ON", Width: 24, Flex: true},
		{Title: "STATE", Width: 13},
	}
	if wide {
		columns = append(columns, ui.Column{Title: "UID", Width: 7})
	}

	rows := make([]row, 0, len(a.sockets.Listening))
	for _, socket := range a.sockets.Listening {
		text := socket.Protocol + " " + socket.LocalAddr() + " " + socket.State
		if !a.matches(text) {
			continue
		}
		cells := []string{socket.Protocol, socket.LocalAddr(), socket.State}
		if wide {
			cells = append(cells, strconv.FormatUint(uint64(socket.UID), 10))
		}
		rows = append(rows, row{cells: cells, text: text})
	}
	return columns, rows
}

// perSecond renders a byte rate in the largest unit that keeps it short.
// Rates are decimal — a megabit is a million bits everywhere in networking —
// which is why this is not the binary scale a file size uses.
func perSecond(rate float64) string {
	if rate <= 0 {
		return "-"
	}
	return bytesDecimal(rate) + "/s"
}

// bytes renders a byte total.
func bytes(total uint64) string {
	if total == 0 {
		return "-"
	}
	return bytesDecimal(float64(total))
}

// bytesDecimal is the shared scale: three significant figures at most, so the
// column stays the same width whatever is in it.
func bytesDecimal(value float64) string {
	const unit = 1000.0
	if value < unit {
		return fmt.Sprintf("%.0f B", value)
	}
	scaled, exponent := value/unit, 0
	for scaled >= unit && exponent < 4 {
		scaled /= unit
		exponent++
	}
	suffix := [...]string{"kB", "MB", "GB", "TB", "PB"}[exponent]
	if scaled < 10 {
		return fmt.Sprintf("%.2f %s", scaled, suffix)
	}
	if scaled < 100 {
		return fmt.Sprintf("%.1f %s", scaled, suffix)
	}
	return fmt.Sprintf("%.0f %s", scaled, suffix)
}

// packetsPerSecond renders a packet rate.
func packetsPerSecond(rate float64) string {
	if rate <= 0 {
		return "-"
	}
	if rate < 10 {
		return fmt.Sprintf("%.1f", rate)
	}
	return count(uint64(rate))
}

// count renders a plain number short enough for a narrow column.
func count(value uint64) string {
	switch {
	case value < 1_000:
		return strconv.FormatUint(value, 10)
	case value < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	case value < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	default:
		return fmt.Sprintf("%.1fG", float64(value)/1_000_000_000)
	}
}

// shortHelpKeys is the single-line hint bar.
func shortHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "tab", Desc: "screen"},
		{Key: "p", Desc: "pause"},
		{Key: "/", Desc: "filter"},
		{Key: "r", Desc: "sample now"},
		{Key: "?", Desc: "help"},
		{Key: "q", Desc: "quit"},
	}
}

// helpKeys is the full key list.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "1 / 2 / 3", Desc: "interfaces, connections, sockets"},
		{Key: "tab / shift+tab", Desc: "next / previous screen"},
		{Key: "", Desc: ""},
		{Key: "↑/k, ↓/j", Desc: "move the selection"},
		{Key: "g / G", Desc: "first / last row"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "/", Desc: "filter the rows (esc clears)"},
		{Key: "", Desc: ""},
		{Key: "p / space", Desc: "pause and resume the refresh"},
		{Key: "r", Desc: "sample now, without waiting for the tick"},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "this tool only reads: there is nothing here that changes anything"},
	}
}
