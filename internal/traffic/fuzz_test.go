package traffic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The family rule is that every package turning bytes it did not write into
// values the tool acts on carries a Go native fuzz test, seeded from the
// package's testdata — see
// https://github.com/tui-tools/tui-kit/blob/main/templates/FUZZING.md.
//
// This package has three parsers and there is one target for each. Every one
// of them reads a file another program writes, on a machine we have never
// seen, and hands the result to a screen a person reads as a fact about their
// network. So the assertions below are invariants rather than outputs: what a
// caller is allowed to assume for any input at all. A row that reaches the
// screen has a name, an address that parses, a state, and counts that were
// either measured or absent — never a blank column and never a number nobody
// produced.
//
// CI replays the seeds like any other test. Running the fuzzer itself is a
// thing you do, one target at a time:
//
//	go test -run=^$ -fuzz=FuzzParseConntrack -fuzztime=5m ./internal/traffic/

// seedFrom adds every fixture whose name starts with prefix to the corpus,
// alongside the shapes a real capture never has.
func seedFrom(f *testing.F, prefix string) {
	f.Helper()
	files, err := filepath.Glob("testdata/" + prefix + "*")
	if err != nil {
		f.Fatalf("glob: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
		if err != nil {
			f.Fatalf("read %s: %v", path, err)
		}
		f.Add(string(data))
	}
	// The shapes that are not a capture: nothing, a header with no rows, a
	// row cut in half by a read that raced the writer, a line of the wrong
	// file entirely, and the separators a parser splits on.
	for _, seed := range []string{
		"", "\n", " ", "\t", ":", "::", "=", "0", "-1",
		"Inter-|   Receive\n", "  sl  local_address rem_address\n",
		"eth0:", "eth0: 1 2 3\n", "ipv4     2 tcp",
		"src= dst=\n", "src=1 dst=2 packets=x bytes=y\n",
		strings.Repeat("A", 4096),
	} {
		f.Add(seed)
	}
}

// FuzzParseProcNetDev covers the interface counter table. What has to hold
// for every input is that the rows it produces can be drawn: a row with no
// name has no line on screen, and two rows with the same name would draw the
// same interface twice with different numbers.
func FuzzParseProcNetDev(f *testing.F) {
	seedFrom(f, "proc-net-dev")

	f.Fuzz(func(t *testing.T, data string) {
		interfaces, err := ParseProcNetDev(data)
		if err != nil {
			if interfaces != nil {
				t.Fatalf("failed and still returned %d interfaces", len(interfaces))
			}
			return
		}
		if len(interfaces) == 0 {
			t.Fatal("succeeded with no interfaces, which is what the error is for")
		}
		seen := map[string]bool{}
		for _, item := range interfaces {
			if item.Name == "" {
				t.Fatal("an interface with no name cannot be drawn")
			}
			if strings.ContainsAny(item.Name, " \t\n:|") {
				t.Fatalf("an interface name that would break the row: %q", item.Name)
			}
			if seen[item.Name] {
				t.Fatalf("%q appears twice", item.Name)
			}
			seen[item.Name] = true
		}
	})
}

// FuzzParseConntrack covers the flow table, in both the layouts it arrives
// in, and the summary built from it. The summary is inside the target because
// it is what the screen actually shows: a parser that returned something a
// summary then miscounted would pass a test of the parser alone.
func FuzzParseConntrack(f *testing.F) {
	seedFrom(f, "conntrack")
	seedFrom(f, "proc-net-nf-conntrack")

	f.Fuzz(func(t *testing.T, data string) {
		flows := ParseConntrack(data)
		for _, flow := range flows {
			if !flow.Source.IsValid() || !flow.Dest.IsValid() {
				t.Fatalf("a flow reached the screen with no endpoints: %+v", flow)
			}
			if flow.From() == "" || flow.To() == "" {
				t.Fatalf("a flow renders as an empty cell: %+v", flow)
			}
			if flow.Protocol == "" {
				t.Fatalf("a flow with no protocol: %+v", flow)
			}
			if strings.ContainsAny(flow.Protocol, " \t\n") {
				t.Fatalf("a protocol that would break the row: %q", flow.Protocol)
			}
			if strings.ContainsAny(flow.State, " \t\n") {
				t.Fatalf("a state that would break the row: %q", flow.State)
			}
			// The promise the whole screen rests on: a count that was not
			// measured is absent, never zero pretending to be a measurement.
			if !flow.Accounted && (flow.Bytes != 0 || flow.Packets != 0) {
				t.Fatalf("an unaccounted flow carries counts: %+v", flow)
			}
		}

		summary := Summarise(flows, SourceConntrack, "fuzz", "", AccountingOn)
		if summary.Total != len(flows) {
			t.Fatalf("Total = %d, want %d", summary.Total, len(flows))
		}
		var byProtocol, byState int
		for _, count := range summary.Protocols {
			if count.Label == "" || count.Count <= 0 {
				t.Fatalf("a protocol row that says nothing: %+v", count)
			}
			byProtocol += count.Count
		}
		for _, state := range summary.States {
			if state.State == "" || state.Count <= 0 {
				t.Fatalf("a state row that says nothing: %+v", state)
			}
			byState += state.Count
		}
		// Both breakdowns are of the same flows, so both add up to the total.
		// A screen whose columns do not add up is a screen nobody can trust.
		if byProtocol != summary.Total || byState != summary.Total {
			t.Fatalf("protocols add to %d and states to %d, but there are %d flows",
				byProtocol, byState, summary.Total)
		}
		if len(summary.Talkers) > TalkerLimit {
			t.Fatalf("%d talkers, want at most %d", len(summary.Talkers), TalkerLimit)
		}
		for i, talker := range summary.Talkers {
			if talker.Bytes == 0 {
				t.Fatalf("a talker with no bytes is not one: %+v", talker)
			}
			if i > 0 && summary.Talkers[i-1].Bytes < talker.Bytes {
				t.Fatalf("talkers are out of order: %+v", summary.Talkers)
			}
		}
	})
}

// FuzzParseProcNetSockets covers the socket tables and the summary drawn from
// them, including the fallback that turns sockets into a connections screen.
func FuzzParseProcNetSockets(f *testing.F) {
	seedFrom(f, "proc-net-tcp")
	seedFrom(f, "proc-net-udp")

	f.Fuzz(func(t *testing.T, data string) {
		for _, protocol := range []string{"tcp", "tcp6", "udp", "udp6"} {
			sockets, err := ParseProcNetSockets(data, protocol)
			if err != nil {
				t.Fatalf("a named protocol never fails: %v", err)
			}
			for _, socket := range sockets {
				if !socket.Local.IsValid() {
					t.Fatalf("a socket with no local address: %+v", socket)
				}
				if socket.LocalAddr() == "" {
					t.Fatalf("a socket renders as an empty cell: %+v", socket)
				}
				if socket.State == "" {
					t.Fatalf("a socket with no state: %+v", socket)
				}
				if strings.ContainsAny(socket.State, " \t\n") {
					t.Fatalf("a state that would break the row: %q", socket.State)
				}
				// A UDP socket must never carry a TCP state name: reporting a
				// listening socket as CLOSE is the specific lie this parser
				// exists to avoid.
				if strings.HasPrefix(protocol, "udp") &&
					socket.State != udpConnected && socket.State != udpUnconnected {
					t.Fatalf("a udp socket with a tcp state: %+v", socket)
				}
			}

			summary := SummariseSockets(sockets, "fuzz")
			if summary.Total != len(sockets) {
				t.Fatalf("Total = %d, want %d", summary.Total, len(sockets))
			}
			var counted int
			for _, state := range summary.States {
				counted += state.Count
			}
			if counted != summary.Total {
				t.Fatalf("the states add to %d, but there are %d sockets",
					counted, summary.Total)
			}
			if len(summary.Listening) > summary.Total {
				t.Fatalf("%d listening sockets out of %d",
					len(summary.Listening), summary.Total)
			}
			// The fallback counts the same sockets and never invents a byte
			// figure, whatever it was given.
			fallback := ConnectionsFromSockets(sockets, "fuzz", "fuzz")
			if fallback.Total > summary.Total {
				t.Fatalf("the fallback counted %d connections out of %d sockets",
					fallback.Total, summary.Total)
			}
			if len(fallback.Talkers) != 0 {
				t.Fatalf("the fallback invented talkers: %+v", fallback.Talkers)
			}
		}
	})
}
