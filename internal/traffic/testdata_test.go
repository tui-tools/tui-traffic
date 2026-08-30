package traffic

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// documentationRanges are the address blocks reserved for examples and
// documentation: RFC 5737 for IPv4 and RFC 3849 for IPv6. Every address that
// appears in this repository is in one of them, or is a loopback or wildcard
// address that names no machine at all.
var documentationRanges = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// TestFixturesCarryNoRealAddress is the test that keeps a capture honest.
//
// The fixtures in testdata are real output from a real machine, which is the
// only kind worth testing a parser against — and that machine's addresses
// were replaced with the documentation ranges before the files were
// committed. This asserts the replacement, on every fixture, every time the
// suite runs: the next person to paste their own output into a new fixture
// finds out here rather than after it is published.
//
// It reads the files the way the parsers do, so it sees exactly the addresses
// the tool would: the hexadecimal columns of the socket tables are decoded
// rather than pattern-matched.
func TestFixturesCarryNoRealAddress(t *testing.T) {
	files, err := filepath.Glob("testdata/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			text := string(data)
			name := filepath.Base(path)

			var addrs []netip.Addr
			switch {
			case strings.HasPrefix(name, "conntrack"),
				strings.HasPrefix(name, "proc-net-nf-conntrack"):
				for _, flow := range ParseConntrack(text) {
					addrs = append(addrs, flow.Source, flow.Dest)
				}
			case strings.HasPrefix(name, "proc-net-tcp"),
				strings.HasPrefix(name, "proc-net-udp"):
				protocol := strings.TrimSuffix(strings.TrimPrefix(name, "proc-net-"), ".txt")
				sockets, err := ParseProcNetSockets(text, protocol)
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				for _, socket := range sockets {
					addrs = append(addrs, socket.Local, socket.Remote)
				}
			default:
				// The interface tables carry counters and names, no
				// addresses, so there is nothing to decode.
				return
			}

			if len(addrs) == 0 {
				t.Fatal("nothing parsed out of this fixture, so nothing was checked")
			}
			for _, addr := range addrs {
				assertDocumentationAddress(t, addr)
			}
		})
	}
}

// The demo data is written by hand rather than captured, and the same rule
// applies to it: a plausible address that belongs to somebody is not
// plausible, it is theirs.
func TestDemoDataCarriesNoRealAddress(t *testing.T) {
	for _, flow := range demoFlows() {
		assertDocumentationAddress(t, flow.Source)
		assertDocumentationAddress(t, flow.Dest)
	}
	for _, socket := range demoSockets() {
		assertDocumentationAddress(t, socket.Local)
		assertDocumentationAddress(t, socket.Remote)
	}
}

// assertDocumentationAddress fails for anything that could name a machine.
func assertDocumentationAddress(t *testing.T, addr netip.Addr) {
	t.Helper()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified() {
		// Loopback and the wildcard are the same on every machine and name
		// none of them.
		return
	}
	for _, prefix := range documentationRanges {
		if prefix.Contains(addr) {
			return
		}
	}
	t.Errorf("%s is not a documentation address: replace it with one from "+
		"192.0.2.0/24, 198.51.100.0/24 or 2001:db8::/32 before committing", addr)
}

// The interface tables carry no addresses, but they do carry names, and a
// container bridge or a VPN interface is named after something on the machine
// it came from. The captured names were replaced by generic ones, and this
// keeps the next capture honest about it in the one way a test can: no
// fixture may carry a host name.
func TestFixturesCarryNoHostName(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" || len(host) < 4 {
		t.Skip("this machine has no host name to look for")
	}
	files, err := filepath.Glob("testdata/*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // testdata is in the repository
		if err != nil {
			continue
		}
		if strings.Contains(string(data), host) {
			t.Errorf("%s carries this machine's host name", path)
		}
	}
}
