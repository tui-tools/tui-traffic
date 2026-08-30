package traffic

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// ConnSource names where a connections summary was read from. It is part of
// the summary rather than a detail of the backend, because a count of
// conntrack flows and a count of open sockets are different numbers and a
// reader has to be able to tell which one is on screen.
type ConnSource string

const (
	// SourceConntrack is `conntrack -L -o extended`, the full table.
	SourceConntrack ConnSource = "conntrack"
	// SourceProcConntrack is /proc/net/nf_conntrack, the same table in the
	// same layout, readable without the conntrack tools but only by root.
	SourceProcConntrack ConnSource = "proc-nf-conntrack"
	// SourceSockets is the fallback: the socket tables counted as
	// connections, on a machine that tracks none.
	SourceSockets ConnSource = "sockets"
)

// Accounting reports whether the kernel counts packets and bytes per tracked
// connection. It is off by default on most distributions, which is why the
// connections screen so often has no byte figures to show.
type Accounting string

const (
	// AccountingUnknown is what a machine with no conntrack at all reports:
	// the question does not apply rather than having a no for an answer.
	AccountingUnknown Accounting = "unknown"
	// AccountingOff is net.netfilter.nf_conntrack_acct = 0.
	AccountingOff Accounting = "off"
	// AccountingOn is net.netfilter.nf_conntrack_acct = 1.
	AccountingOn Accounting = "on"
)

// Flow is one tracked connection, as conntrack describes it: the original
// direction's endpoints, the protocol's state where the protocol has states,
// and the traffic over it in both directions where the kernel counted it.
type Flow struct {
	Protocol string     `json:"protocol"`
	State    string     `json:"state,omitempty"`
	Source   netip.Addr `json:"-"`
	Dest     netip.Addr `json:"-"`
	// SourcePort and DestPort are zero for a protocol that has no ports,
	// which is every ICMP flow.
	SourcePort uint16 `json:"-"`
	DestPort   uint16 `json:"-"`
	// Packets and Bytes are both directions summed, and are meaningful only
	// when Accounted is true: a kernel that is not counting reports nothing
	// at all rather than zero, and the two must not be confused.
	Packets   uint64 `json:"packets,omitempty"`
	Bytes     uint64 `json:"bytes,omitempty"`
	Accounted bool   `json:"accounted"`
	// Assured and Unreplied are conntrack's own flags: a flow it has seen
	// traffic on in both directions, and one it has not.
	Assured   bool `json:"assured,omitempty"`
	Unreplied bool `json:"unreplied,omitempty"`
}

// From renders the original direction's source as the user reads it: an
// address, with a port when the protocol has one.
func (f Flow) From() string { return endpoint(f.Source, f.SourcePort) }

// To renders the original direction's destination.
func (f Flow) To() string { return endpoint(f.Dest, f.DestPort) }

// endpoint formats an address and a port, omitting a port the protocol does
// not have. An IPv6 address keeps the brackets a port needs and loses them
// when there is no port, which is what every other tool on the machine does.
func endpoint(addr netip.Addr, port uint16) string {
	if !addr.IsValid() {
		return ""
	}
	if port == 0 {
		return addr.String()
	}
	return netip.AddrPortFrom(addr, port).String()
}

// Count is one "label: number" pair of a summary, kept as a slice rather than
// a map so the order the UI draws is the order the tool decided on.
type Count struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// StateCount is how many connections of one protocol are in one state.
type StateCount struct {
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Count    int    `json:"count"`
}

// Talker is one of the flows moving the most bytes.
type Talker struct {
	Protocol string `json:"protocol"`
	From     string `json:"from"`
	To       string `json:"to"`
	Bytes    uint64 `json:"bytes"`
	Packets  uint64 `json:"packets"`
}

// TalkerLimit is how many of the busiest flows a summary keeps. It is a
// screenful on a normal terminal, and a list nobody scrolls is a list nobody
// reads.
const TalkerLimit = 10

// Connections is the connections screen: the table summarised, and — just as
// important — what was summarised and whether the byte figures exist.
type Connections struct {
	Source ConnSource `json:"source"`
	// Detail is the exact command or path the numbers came from, so the
	// screen can show its own working.
	Detail string `json:"detail"`
	// Note is why this source and not a better one, in one sentence. It is
	// empty when the best source was available.
	Note string `json:"note,omitempty"`

	Accounting Accounting   `json:"accounting"`
	Total      int          `json:"total"`
	Protocols  []Count      `json:"protocols"`
	States     []StateCount `json:"states"`
	// Talkers is empty whenever the kernel is not counting bytes. It is not
	// filled in with zeroes or with packet counts standing in for bytes: a
	// column that is not being measured is absent, not zero.
	Talkers []Talker `json:"talkers"`
}

// ParseConntrack reads the flow table, in the one layout both the conntrack
// command and the kernel's own file print it in.
//
// `conntrack -L -o extended` and /proc/net/nf_conntrack produce the same
// rows, which is why one parser reads both:
//
//	ipv4 2 tcp 6 431997 ESTABLISHED src=192.0.2.10 dst=198.51.100.7 \
//	  sport=51234 dport=443 packets=41 bytes=5180 \
//	  src=198.51.100.7 dst=192.0.2.10 sport=443 dport=51234 packets=39 \
//	  bytes=48211 [ASSURED] mark=0 use=1
//
// Everything after the fixed prefix is `key=value`, and a row carries the key
// twice: once for the original direction and once for the reply. The first
// occurrence wins for the endpoints — the original direction is the one the
// connection was opened in, and the one worth showing — while packets and
// bytes are summed across both, because the traffic over a connection is what
// went either way.
//
// Three shapes have to survive. A protocol with no state (udp, icmp) has no
// state token, so the state is taken only when it is there. A kernel that is
// not accounting prints no packets or bytes at all, which is reported as
// Accounted false rather than as zero. And conntrack's own summary line —
// "conntrack v1.4.7 (conntrack-tools): 344 flow entries have been shown." —
// is not a flow and is skipped like any other unparsable line.
func ParseConntrack(data string) []Flow {
	var flows []Flow
	for line := range strings.Lines(data) {
		if flow, ok := parseConntrackLine(line); ok {
			flows = append(flows, flow)
		}
	}
	return flows
}

// parseConntrackLine reads one row. It reports false for anything that is not
// one, which includes blank lines, the tools' summary line, and a row whose
// endpoints are missing or unparsable: a flow without a source and a
// destination is not something the screen can show, and dropping it is better
// than showing a row of empty columns.
func parseConntrackLine(line string) (Flow, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return Flow{}, false
	}

	// The layer-3 prefix ("ipv4 2", "ipv6 10") is present in the extended
	// output and in the kernel's file, and absent from a plain `conntrack
	// -L`. It is recognised by name rather than by counting tokens, so both
	// forms reach the same code below.
	if isLayer3(fields[0]) && len(fields) > 2 && isNumber(fields[1]) {
		fields = fields[2:]
	}
	if len(fields) < 3 || !isNumber(fields[1]) || !isNumber(fields[2]) {
		// protocol, protocol number, timeout: anything else is not a row.
		return Flow{}, false
	}

	flow := Flow{Protocol: strings.ToLower(fields[0])}
	rest := fields[3:]
	// A state token, where the protocol has states. It is a bare word: the
	// key=value pairs and the [FLAGS] are both excluded by shape rather than
	// by a list of state names, so a state this parser has never heard of
	// still reaches the screen.
	if len(rest) > 0 && isBareWord(rest[0]) {
		flow.State = rest[0]
		rest = rest[1:]
	}

	var packets, bytes uint64
	var accounted, haveSource, haveDest bool
	for _, field := range rest {
		switch field {
		case "[ASSURED]":
			flow.Assured = true
			continue
		case "[UNREPLIED]":
			flow.Unreplied = true
			continue
		}
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "src":
			if haveSource {
				continue // the reply direction's source is the destination.
			}
			if addr, err := netip.ParseAddr(value); err == nil {
				flow.Source, haveSource = addr, true
			}
		case "dst":
			if haveDest {
				continue
			}
			if addr, err := netip.ParseAddr(value); err == nil {
				flow.Dest, haveDest = addr, true
			}
		case "sport":
			if flow.SourcePort == 0 {
				flow.SourcePort = parsePort(value)
			}
		case "dport":
			if flow.DestPort == 0 {
				flow.DestPort = parsePort(value)
			}
		case "packets":
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				packets += n
				accounted = true
			}
		case "bytes":
			if n, err := strconv.ParseUint(value, 10, 64); err == nil {
				bytes += n
				accounted = true
			}
		}
	}
	if !haveSource || !haveDest {
		return Flow{}, false
	}
	flow.Packets, flow.Bytes, flow.Accounted = packets, bytes, accounted
	return flow, true
}

// isLayer3 reports whether a token is one of the address families conntrack
// prints. "unknown" is one of them, for a family the kernel has no name for.
func isLayer3(s string) bool {
	switch s {
	case "ipv4", "ipv6", "unknown":
		return true
	}
	return false
}

// isNumber reports whether a token is a bare decimal number.
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isBareWord reports whether a token is a state name: a word with no "=" in
// it and no brackets around it, which is what separates ESTABLISHED from
// src=192.0.2.10 and from [ASSURED].
func isBareWord(s string) bool {
	if s == "" || strings.Contains(s, "=") {
		return false
	}
	return !strings.HasPrefix(s, "[")
}

// parsePort reads a port number, returning zero for anything that is not one.
// Zero is also what a protocol without ports reports, and the two are the
// same as far as the screen is concerned: there is no port to show.
func parsePort(s string) uint16 {
	n, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// Summarise turns a flow table into the connections screen: the totals by
// protocol, the states within each protocol, and the busiest flows.
//
// The talkers list is filled only from flows the kernel actually counted. On
// a machine where accounting is off that leaves it empty, and empty is the
// correct answer: the alternative is to rank connections by a number nobody
// measured.
func Summarise(flows []Flow, source ConnSource, detail, note string,
	accounting Accounting) Connections {
	summary := Connections{
		Source: source, Detail: detail, Note: note,
		Accounting: accounting, Total: len(flows),
		Protocols: []Count{}, States: []StateCount{}, Talkers: []Talker{},
	}

	byProtocol := map[string]int{}
	byState := map[StateCount]int{}
	for _, flow := range flows {
		byProtocol[flow.Protocol]++
		state := flow.State
		if state == "" {
			// A protocol with no states still has a row, so the counts add
			// up to the total on screen. "stateless" is the honest label:
			// the kernel is not withholding a state, there is none.
			state = "stateless"
		}
		byState[StateCount{Protocol: flow.Protocol, State: state}]++
	}

	for protocol, count := range byProtocol {
		summary.Protocols = append(summary.Protocols, Count{Label: protocol, Count: count})
	}
	slices.SortFunc(summary.Protocols, func(a, b Count) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Label, b.Label)
	})

	for key, count := range byState {
		key.Count = count
		summary.States = append(summary.States, key)
	}
	slices.SortFunc(summary.States, func(a, b StateCount) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Protocol, b.Protocol); c != 0 {
			return c
		}
		return cmp.Compare(a.State, b.State)
	})

	accountedFlows := make([]Flow, 0, len(flows))
	for _, flow := range flows {
		if flow.Accounted && flow.Bytes > 0 {
			accountedFlows = append(accountedFlows, flow)
		}
	}
	slices.SortStableFunc(accountedFlows, func(a, b Flow) int {
		return cmp.Compare(b.Bytes, a.Bytes)
	})
	for i, flow := range accountedFlows {
		if i == TalkerLimit {
			break
		}
		summary.Talkers = append(summary.Talkers, Talker{
			Protocol: flow.Protocol, From: flow.From(), To: flow.To(),
			Bytes: flow.Bytes, Packets: flow.Packets,
		})
	}
	return summary
}
