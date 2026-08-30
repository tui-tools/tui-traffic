package traffic

import (
	"net/netip"
	"testing"
)

func TestParseHexAddrPort(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		addr  string
		port  uint16
		wants bool
	}{
		// The addresses are little-endian: 0A0200C0 read backwards a byte at
		// a time is C0.00.02.0A, which is 192.0.2.10.
		{"an ipv4 address", "0A0200C0:0035", "192.0.2.10", 53, true},
		{"the wildcard", "00000000:0016", "0.0.0.0", 22, true},
		{"loopback", "0100007F:9F3D", "127.0.0.1", 40765, true},
		// IPv6 is four 32-bit words, each stored least significant byte
		// first, which is why ::1 ends in 01000000 rather than 00000001.
		{"ipv6 loopback",
			"00000000000000000000000001000000:1F90", "::1", 8080, true},
		{"the ipv6 wildcard",
			"00000000000000000000000000000000:01BB", "::", 443, true},
		// An IPv4-mapped socket in the v6 table is the same socket a v4 tool
		// shows, and reading it as v6 would list 127.0.0.1 twice under two
		// different names.
		{"an ipv4-mapped address",
			"0000000000000000FFFF00000100007F:C355", "127.0.0.1", 50005, true},
		{"a documentation ipv6 address",
			"B80D0120000000000000000014000000:C91C", "2001:db8::14", 51484, true},
		{"no port", "0A0200C0", "", 0, false},
		{"a port that is not hexadecimal", "0A0200C0:zzzz", "", 0, false},
		{"an address of the wrong length", "0A02:0035", "", 0, false},
		{"an address that is not hexadecimal", "ZZZZZZZZ:0035", "", 0, false},
		{"nothing", "", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, port, ok := parseHexAddrPort(tc.in)
			if ok != tc.wants {
				t.Fatalf("ok = %v, want %v", ok, tc.wants)
			}
			if !ok {
				return
			}
			if addr.String() != tc.addr || port != tc.port {
				t.Errorf("got %s:%d, want %s:%d", addr, port, tc.addr, tc.port)
			}
		})
	}
}

func TestParseProcNetTCP(t *testing.T) {
	sockets, err := ParseProcNetSockets(read(t, "proc-net-tcp.txt"), "tcp")
	if err != nil {
		t.Fatalf("ParseProcNetSockets: %v", err)
	}
	if len(sockets) != 74 {
		t.Fatalf("got %d sockets, want 74", len(sockets))
	}

	first := sockets[0]
	want := Socket{Protocol: "tcp", Local: netip.MustParseAddr("192.0.2.10"),
		LocalPort: 53, Remote: netip.MustParseAddr("0.0.0.0"),
		State: "LISTEN", UID: 0, Inode: 27190034}
	if first != want {
		t.Errorf("got  %+v\nwant %+v", first, want)
	}
	if !first.Listening() {
		t.Error("a socket in LISTEN is listening")
	}
	if first.RemoteAddr() != "" {
		t.Errorf("a listening socket has no peer, got %q", first.RemoteAddr())
	}

	var established int
	for _, socket := range sockets {
		if socket.State == "ESTABLISHED" {
			established++
			if socket.RemoteAddr() == "" {
				t.Errorf("an established socket with no peer: %+v", socket)
			}
		}
	}
	if established == 0 {
		t.Error("the capture has established connections in it")
	}
}

func TestParseProcNetTCP6(t *testing.T) {
	sockets, err := ParseProcNetSockets(read(t, "proc-net-tcp6.txt"), "tcp6")
	if err != nil {
		t.Fatalf("ParseProcNetSockets: %v", err)
	}
	if len(sockets) == 0 {
		t.Fatal("the v6 table parsed to nothing")
	}
	// The v6 table is where the IPv4-mapped sockets live, and they must come
	// out as the v4 addresses they are.
	var mapped int
	for _, socket := range sockets {
		if socket.Local.Is4() {
			mapped++
		}
	}
	if mapped == 0 {
		t.Error("no ipv4-mapped socket was unmapped")
	}
}

// UDP has no connection states. The kernel fills the column in anyway — 07,
// which the TCP table calls CLOSE — and reporting a listening socket as
// closed would be worse than showing no state at all.
func TestParseProcNetUDPHasNoTCPStates(t *testing.T) {
	sockets, err := ParseProcNetSockets(read(t, "proc-net-udp.txt"), "udp")
	if err != nil {
		t.Fatalf("ParseProcNetSockets: %v", err)
	}
	if len(sockets) == 0 {
		t.Fatal("the udp table parsed to nothing")
	}
	for _, socket := range sockets {
		switch socket.State {
		case udpUnconnected, udpConnected:
		default:
			t.Fatalf("a udp socket carries a tcp state: %+v", socket)
		}
		if socket.State == udpUnconnected && !socket.Listening() {
			t.Errorf("an unconnected udp socket is what ss calls UNCONN: %+v", socket)
		}
	}
}

func TestParseProcNetSocketsSkipsWhatItCannotRead(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"nothing", ""},
		{"the header on its own",
			"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"},
		{"a row cut short", "   0: 0A0200C0:0035 00000000:0000 0A\n"},
		{"a row of something else entirely", "hello world and some more words here\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sockets, err := ParseProcNetSockets(tc.in, "tcp")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(sockets) != 0 {
				t.Errorf("got %d sockets, want none", len(sockets))
			}
		})
	}
}

func TestParseProcNetSocketsNeedsAProtocol(t *testing.T) {
	if _, err := ParseProcNetSockets("", ""); err == nil {
		t.Error("a socket table with no protocol name is a programming error")
	}
}

func TestSummariseSockets(t *testing.T) {
	sockets := demoSockets()
	summary := SummariseSockets(sockets, "demo socket tables")

	if summary.Total != len(sockets) {
		t.Errorf("Total = %d, want %d", summary.Total, len(sockets))
	}
	if summary.Established != 5 {
		t.Errorf("Established = %d, want the five ESTABLISHED sockets", summary.Established)
	}
	if len(summary.Listening) != 10 {
		t.Fatalf("Listening = %d rows, want 10", len(summary.Listening))
	}
	// Listening sockets are sorted by port: the answer to "what is exposed"
	// is read down the port column.
	for i := 1; i < len(summary.Listening); i++ {
		if summary.Listening[i-1].LocalPort > summary.Listening[i].LocalPort {
			t.Fatalf("listening sockets are not sorted by port: %+v", summary.Listening)
		}
	}
	var total int
	for _, state := range summary.States {
		total += state.Count
	}
	if total != summary.Total {
		t.Errorf("the state counts add up to %d, not to the %d sockets", total, summary.Total)
	}
}

// The fallback is a different measurement and says so. What it must not do is
// claim a byte figure it never had.
func TestConnectionsFromSockets(t *testing.T) {
	summary := ConnectionsFromSockets(demoSockets(), socketSource,
		"there is no conntrack on this machine")

	if summary.Source != SourceSockets {
		t.Errorf("Source = %q, want %q", summary.Source, SourceSockets)
	}
	if summary.Accounting != AccountingUnknown {
		t.Errorf("Accounting = %q: nothing counted bytes here", summary.Accounting)
	}
	if len(summary.Talkers) != 0 {
		t.Errorf("the fallback invented talkers: %+v", summary.Talkers)
	}
	if summary.Note == "" {
		t.Error("a fallback has to say it is one")
	}
	// The listening sockets are not connections and are not counted as any.
	if summary.Total != 9 {
		t.Errorf("Total = %d, want the nine sockets that have a peer", summary.Total)
	}
	// And the v6 sockets are counted under the protocol, not under "tcp6":
	// a connection over IPv6 is still a TCP connection.
	for _, count := range summary.Protocols {
		if count.Label == "tcp6" || count.Label == "udp6" {
			t.Errorf("protocols carry the address family: %+v", summary.Protocols)
		}
	}
}
