package traffic

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Socket is one row of a socket table.
type Socket struct {
	// Protocol is the table it was read from: tcp, tcp6, udp or udp6.
	Protocol   string     `json:"protocol"`
	Local      netip.Addr `json:"-"`
	LocalPort  uint16     `json:"localPort"`
	Remote     netip.Addr `json:"-"`
	RemotePort uint16     `json:"remotePort"`
	// State is the TCP state for a TCP socket, and "connected" or
	// "unconnected" for a UDP one, which has no states of its own.
	State string `json:"state"`
	UID   uint32 `json:"uid"`
	Inode uint64 `json:"inode"`
}

// LocalAddr renders the local end the way the user reads it.
func (s Socket) LocalAddr() string { return endpoint(s.Local, s.LocalPort) }

// RemoteAddr renders the peer, or the empty string when there is none.
func (s Socket) RemoteAddr() string {
	if !s.Remote.IsValid() || s.Remote.IsUnspecified() && s.RemotePort == 0 {
		return ""
	}
	return endpoint(s.Remote, s.RemotePort)
}

// Listening reports whether the socket is waiting for someone rather than
// talking to them. For TCP that is the LISTEN state; for UDP, which has no
// states, it is a socket with no peer — the same thing `ss` shows as UNCONN.
func (s Socket) Listening() bool {
	if strings.HasPrefix(s.Protocol, "udp") {
		return s.State == udpUnconnected
	}
	return s.State == "LISTEN"
}

// Sockets is the sockets screen: what is listening, and the shape of
// everything else.
type Sockets struct {
	// Source names the files the rows came from.
	Source string `json:"source"`
	Total  int    `json:"total"`
	// Established is the count of TCP sockets in ESTABLISHED, which is the
	// number a reader means by "connections open right now".
	Established int `json:"established"`
	// Listening is the full list of sockets accepting, sorted by port, which
	// is the answer to "what is exposed on this machine".
	Listening []Socket `json:"listening"`
	// Protocols and States are the counts across every socket, listening
	// ones included.
	Protocols []Count      `json:"protocols"`
	States    []StateCount `json:"states"`
}

// udpUnconnected is the state given to a UDP socket with no peer. UDP has no
// connection states, so this is the tool's word rather than the kernel's, and
// it is deliberately not one of the TCP names.
const udpUnconnected = "unconnected"

// udpConnected is a UDP socket that has been connect()ed to a peer.
const udpConnected = "connected"

// tcpStates maps the hexadecimal state column of /proc/net/tcp to the names
// the kernel uses for them internally. The numbering is part of the kernel's
// ABI and has not changed since these files were introduced.
var tcpStates = map[uint64]string{
	0x01: "ESTABLISHED",
	0x02: "SYN_SENT",
	0x03: "SYN_RECV",
	0x04: "FIN_WAIT1",
	0x05: "FIN_WAIT2",
	0x06: "TIME_WAIT",
	0x07: "CLOSE",
	0x08: "CLOSE_WAIT",
	0x09: "LAST_ACK",
	0x0A: "LISTEN",
	0x0B: "CLOSING",
	0x0C: "NEW_SYN_RECV",
}

// procNetSocketColumns is the number of fields a row must have before the
// ones this parser reads are all present: the inode is the tenth.
const procNetSocketColumns = 10

// ParseProcNetSockets reads one of the kernel's socket tables:
// /proc/net/tcp, tcp6, udp or udp6. The protocol is passed in rather than
// guessed, because the four files have the same layout and differ only in
// which one you opened.
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when ...
//	 0: 0A0200C0:0035 00000000:0000 0A 00000000:00000000 00:00000000 ...
//
// The addresses are hexadecimal and in host byte order, which on every
// machine this tool runs on means little-endian: 0A0200C0 is 192.0.2.10 read
// backwards a byte at a time. IPv6 is the same trick four times over, one per
// 32-bit word, which is why ::1 appears as a run of zeroes ending in 01000000
// rather than in 00000001.
//
// A row that does not parse is skipped rather than failing the read: these
// files are written while the machine is using them, and a socket that closed
// mid-read is not a reason to show the user nothing.
func ParseProcNetSockets(data, protocol string) ([]Socket, error) {
	if protocol == "" {
		return nil, fmt.Errorf("no protocol given for a socket table")
	}
	isUDP := strings.HasPrefix(protocol, "udp")

	var sockets []Socket
	first := true
	for line := range strings.Lines(data) {
		fields := strings.Fields(line)
		if first {
			// The header line, which every one of these files has.
			first = false
			if len(fields) > 1 && fields[0] == "sl" {
				continue
			}
		}
		if len(fields) < procNetSocketColumns {
			continue
		}
		local, localPort, ok := parseHexAddrPort(fields[1])
		if !ok {
			continue
		}
		remote, remotePort, ok := parseHexAddrPort(fields[2])
		if !ok {
			continue
		}
		code, err := strconv.ParseUint(fields[3], 16, 8)
		if err != nil {
			continue
		}
		uid, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}

		state := tcpStates[code]
		if isUDP {
			// UDP has no states. The kernel still fills the column in — 07
			// for an unbound socket, 01 for a connected one — and reporting
			// that as "CLOSE" would be worse than useless: it would say a
			// listening socket is closed.
			state = udpUnconnected
			if remote.IsValid() && !remote.IsUnspecified() {
				state = udpConnected
			}
		}
		if state == "" {
			// A state number this kernel has and this table does not. It is
			// shown as what it is rather than dropped or guessed at.
			state = "state-" + strconv.FormatUint(code, 10)
		}

		sockets = append(sockets, Socket{
			Protocol: protocol,
			Local:    local, LocalPort: localPort,
			Remote: remote, RemotePort: remotePort,
			State: state, UID: uint32(uid), Inode: inode,
		})
	}
	return sockets, nil
}

// parseHexAddrPort reads one "ADDRESS:PORT" column. The address is 8 hex
// digits for IPv4 and 32 for IPv6, and anything else is not one.
func parseHexAddrPort(field string) (netip.Addr, uint16, bool) {
	text, portText, ok := strings.Cut(field, ":")
	if !ok {
		return netip.Addr{}, 0, false
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	raw, err := hex.DecodeString(text)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	switch len(raw) {
	case 4:
		return netip.AddrFrom4([4]byte{raw[3], raw[2], raw[1], raw[0]}), uint16(port), true
	case 16:
		// Four 32-bit words, each stored least significant byte first.
		var addr [16]byte
		for word := range 4 {
			for i := range 4 {
				addr[word*4+i] = raw[word*4+3-i]
			}
		}
		// An IPv4-mapped address in the v6 table is the same socket a v4
		// tool would show, and unmapping it is what makes ::ffff:127.0.0.1
		// read as 127.0.0.1 next to its neighbours.
		return netip.AddrFrom16(addr).Unmap(), uint16(port), true
	default:
		return netip.Addr{}, 0, false
	}
}

// SummariseSockets counts a set of socket rows into the sockets screen.
func SummariseSockets(sockets []Socket, source string) Sockets {
	summary := Sockets{
		Source: source, Total: len(sockets),
		Listening: []Socket{}, Protocols: []Count{}, States: []StateCount{},
	}

	byProtocol := map[string]int{}
	byState := map[StateCount]int{}
	for _, socket := range sockets {
		byProtocol[socket.Protocol]++
		byState[StateCount{Protocol: socket.Protocol, State: socket.State}]++
		if socket.State == "ESTABLISHED" {
			summary.Established++
		}
		if socket.Listening() {
			summary.Listening = append(summary.Listening, socket)
		}
	}

	slices.SortStableFunc(summary.Listening, func(a, b Socket) int {
		if c := cmp.Compare(a.LocalPort, b.LocalPort); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Protocol, b.Protocol); c != 0 {
			return c
		}
		return cmp.Compare(a.LocalAddr(), b.LocalAddr())
	})

	for protocol, count := range byProtocol {
		summary.Protocols = append(summary.Protocols, Count{Label: protocol, Count: count})
	}
	slices.SortFunc(summary.Protocols, func(a, b Count) int {
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
	return summary
}

// ConnectionsFromSockets is the connections screen on a machine that tracks
// no connections. It counts the sockets that have a peer, which is the
// closest honest answer the kernel offers when there is no conntrack table to
// read, and it says so: the source is `sockets`, the note explains why, and
// there are no byte figures because nothing counted any.
//
// The two numbers are not the same and the screen never pretends they are. A
// machine forwarding traffic for others tracks connections that belong to no
// socket of its own, and those are exactly the ones this fallback cannot see.
func ConnectionsFromSockets(sockets []Socket, detail, note string) Connections {
	peered := make([]Flow, 0, len(sockets))
	for _, socket := range sockets {
		if socket.Listening() || !socket.Remote.IsValid() ||
			socket.Remote.IsUnspecified() {
			continue
		}
		protocol := strings.TrimSuffix(socket.Protocol, "6")
		peered = append(peered, Flow{
			Protocol: protocol, State: socket.State,
			Source: socket.Local, SourcePort: socket.LocalPort,
			Dest: socket.Remote, DestPort: socket.RemotePort,
		})
	}
	return Summarise(peered, SourceSockets, detail, note, AccountingUnknown)
}
