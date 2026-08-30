package traffic

import (
	"context"
	"math"
	"math/rand/v2"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Fake is the in-memory backend behind --demo and the tests. It is a machine
// that does not exist, with counters that move the way a real one's do.
//
// Every tool in the family has one, and on a read-only tool it carries more
// weight than usual: there is no command to preview, so the demo is the whole
// of what a reviewer can judge the tool by without a machine to point it at.
// So the numbers are not static. The counters advance with the wall clock at
// a rate that rises and falls, which is what makes the sparkline show a shape
// instead of a flat line, and what makes the sort order change while you
// watch it — the two things about this screen that a screenshot cannot show.
//
// The addresses are all from the documentation ranges (RFC 5737 and RFC
// 3849): nothing here is a real host, and nothing here came off anybody's
// machine.
type Fake struct {
	mu sync.Mutex
	// start is when the demo machine booted, so its lifetime totals are
	// plausible rather than round.
	start time.Time
	// last is the moment the counters were last advanced to.
	last  time.Time
	links []fakeLink
	// noise makes each interface's rate wander. It is seeded from a constant,
	// so two runs of the demo produce the same walk and a screenshot is
	// reproducible.
	noise *rand.Rand
}

// fakeLink is one interface of the demo machine: what it is, how much it
// carries, and where its counters have got to.
type fakeLink struct {
	name string
	link Link
	// meanRX and meanTX are its average throughput in bytes per second.
	meanRX, meanTX float64
	// period and phase shape the slow rise and fall around that average, so
	// the interfaces do not all peak together.
	period, phase float64
	// packet is the average packet size, which is what turns a byte rate into
	// a packet rate: a bulk transfer moves few large packets, and a chatty
	// service moves many small ones.
	packet float64

	counters Interface
}

// NewFake returns a demo machine with its counters already run in, so the
// totals look like a machine that has been up for a while rather than one
// that started when you did.
func NewFake() *Fake {
	now := time.Now()
	f := &Fake{
		start: now.Add(-37 * time.Hour),
		last:  now,
		//nolint:gosec // G404: this is a demo's jitter, not a secret: it is
		// seeded from a constant on purpose, so a screenshot is reproducible.
		noise: rand.New(rand.NewPCG(20260830, 1)),
		links: []fakeLink{
			{name: "enp3s0", meanRX: 4_100_000, meanTX: 820_000,
				period: 23, phase: 0.4, packet: 1_180,
				link: Link{State: "up", Carrier: true, Speed: 1000}},
			{name: "wlan0", meanRX: 260_000, meanTX: 74_000,
				period: 17, phase: 2.1, packet: 740,
				link: Link{State: "up", Carrier: true}},
			{name: "wg0", meanRX: 91_000, meanTX: 130_000,
				period: 31, phase: 4.7, packet: 620,
				link: Link{State: "unknown", Carrier: true}},
			{name: "docker0", meanRX: 38_000, meanTX: 610_000,
				period: 11, phase: 1.3, packet: 1_420,
				link: Link{State: "up", Carrier: true}},
			{name: "lo", meanRX: 12_000, meanTX: 12_000,
				period: 43, phase: 0, packet: 3_100,
				link: Link{State: "unknown", Carrier: true}},
			{name: "enp4s0", meanRX: 0, meanTX: 0,
				period: 1, phase: 0, packet: 1_000,
				link: Link{State: "down"}},
		},
	}
	// The lifetime totals: the average rate over the machine's uptime, which
	// is what the error and total columns show.
	uptime := now.Sub(f.start).Seconds()
	for i := range f.links {
		link := &f.links[i]
		link.counters.Name = link.name
		link.counters.RX = f.settle(link.meanRX*uptime, link.packet, 0.00004)
		link.counters.TX = f.settle(link.meanTX*uptime, link.packet, 0.00001)
	}
	return f
}

// settle turns a byte total into a full set of counters, with the errors and
// drops a long-running interface accumulates.
func (f *Fake) settle(bytes, packetSize, errorRate float64) Counters {
	packets := uint64(bytes / packetSize)
	return Counters{
		Bytes:     uint64(bytes),
		Packets:   packets,
		Errors:    uint64(float64(packets) * errorRate),
		Dropped:   uint64(float64(packets) * errorRate / 3),
		Multicast: packets / 900,
	}
}

// Name identifies the backend.
func (f *Fake) Name() string { return "demo" }

// Describe is the one-line summary shown in the header. It says what it is on
// every screen, because a demo mistaken for a real reading is worse than no
// demo at all.
func (f *Fake) Describe() string {
	return "a machine that does not exist  ·  demo (nothing here is read)"
}

// Sources names what the demo would be reading, so --report and --check under
// --demo describe the same shape a live run does.
func (f *Fake) Sources(_ context.Context) Sources {
	return Sources{
		Interfaces:  "demo counters",
		Connections: "demo conntrack table",
		Sockets:     "demo socket tables",
		Accounting:  AccountingOn,
	}
}

// Sample advances the demo machine's counters to now and returns them.
//
// The advance is by elapsed time rather than per call, so the rates the UI
// computes are the rates configured above however often it samples — which is
// what makes --interval work in the demo, and what keeps a slow terminal from
// showing a slow machine.
func (f *Fake) Sample(_ context.Context) (Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(f.last).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	f.last = now

	since := now.Sub(f.start).Seconds()
	interfaces := make([]Interface, 0, len(f.links))
	links := make(map[string]Link, len(f.links))
	for i := range f.links {
		link := &f.links[i]
		f.advance(link, since, elapsed)
		interfaces = append(interfaces, link.counters)
		links[link.name] = link.link
	}
	return Sample{At: now, Interfaces: interfaces, Links: links}, nil
}

// advance moves one interface's counters forward by the traffic it carried in
// the elapsed window.
func (f *Fake) advance(link *fakeLink, since, elapsed float64) {
	if elapsed <= 0 || link.meanRX+link.meanTX == 0 {
		return
	}
	// A slow swell, so the sparkline has a shape, plus a little noise so two
	// consecutive samples are never identical.
	swell := 1 + 0.55*math.Sin(2*math.Pi*since/link.period+link.phase)
	jitter := 0.85 + 0.3*f.noise.Float64()
	factor := swell * jitter

	rx := link.meanRX * factor * elapsed
	tx := link.meanTX * factor * elapsed
	link.counters.RX.Bytes += uint64(rx)
	link.counters.TX.Bytes += uint64(tx)
	link.counters.RX.Packets += uint64(rx / link.packet)
	link.counters.TX.Packets += uint64(tx / link.packet)
	// One interface drops the occasional packet, which is what the drop
	// column is there to show. It is the wireless one, because that is where
	// a reader of a real machine would expect to find it.
	if link.name == "wlan0" && f.noise.Float64() < 0.08 {
		link.counters.RX.Dropped++
	}
}

// Connections returns the demo machine's conntrack table, summarised. Byte
// accounting is on here, because the screen with the talkers on it is the one
// worth showing; the other case — the common one, where the kernel counts
// nothing — is what the note and the empty talkers list on a real machine
// look like, and the tests cover it.
func (f *Fake) Connections(_ context.Context) (Connections, error) {
	return Summarise(demoFlows(), SourceConntrack,
		strings.Join(conntrackCommand, " "), "", AccountingOn), nil
}

// Sockets returns the demo machine's socket tables, summarised.
func (f *Fake) Sockets(_ context.Context) (Sockets, error) {
	return SummariseSockets(demoSockets(), "demo socket tables"), nil
}

// addr is a small helper for the tables below: the addresses are all
// literals from the documentation ranges, so a parse failure here is a typo
// and nothing else.
func addr(s string) netip.Addr {
	parsed, err := netip.ParseAddr(s)
	if err != nil {
		panic("demo data has a malformed address: " + s)
	}
	return parsed
}

// demoFlows is the demo conntrack table: a machine serving some web traffic,
// talking to a database, running a VPN and doing its own DNS.
func demoFlows() []Flow {
	flow := func(protocol, state, src string, sport uint16, dst string,
		dport uint16, packets, bytes uint64) Flow {
		return Flow{
			Protocol: protocol, State: state,
			Source: addr(src), SourcePort: sport,
			Dest: addr(dst), DestPort: dport,
			Packets: packets, Bytes: bytes, Accounted: true,
			Assured: state == "ESTABLISHED",
		}
	}
	return []Flow{
		flow("tcp", "ESTABLISHED", "192.0.2.14", 51234, "198.51.100.7", 443, 8_142, 11_482_113),
		flow("tcp", "ESTABLISHED", "192.0.2.14", 51288, "198.51.100.7", 443, 3_918, 4_119_884),
		flow("tcp", "ESTABLISHED", "192.0.2.14", 45012, "192.0.2.61", 5432, 21_004, 2_884_517),
		flow("tcp", "ESTABLISHED", "198.51.100.31", 60122, "192.0.2.14", 22, 4_401, 1_204_998),
		flow("tcp", "TIME_WAIT", "192.0.2.14", 51002, "198.51.100.7", 443, 44, 18_220),
		flow("tcp", "TIME_WAIT", "192.0.2.14", 50988, "198.51.100.9", 80, 38, 14_119),
		flow("tcp", "TIME_WAIT", "192.0.2.14", 50941, "198.51.100.9", 80, 41, 15_002),
		flow("tcp", "SYN_SENT", "192.0.2.14", 51301, "198.51.100.44", 8443, 3, 180),
		flow("tcp", "CLOSE_WAIT", "192.0.2.14", 49880, "192.0.2.61", 5432, 902, 411_882),
		flow("tcp", "ESTABLISHED", "2001:db8::14", 51500, "2001:db8:1::7", 443, 1_882, 998_112),
		flow("udp", "", "192.0.2.14", 53114, "192.0.2.1", 53, 2, 288),
		flow("udp", "", "192.0.2.14", 53119, "192.0.2.1", 53, 2, 301),
		flow("udp", "", "192.0.2.14", 51820, "198.51.100.88", 51820, 91_233, 68_119_004),
		flow("udp", "", "192.0.2.14", 123, "198.51.100.123", 123, 12, 1_104),
		{Protocol: "icmp", Source: addr("192.0.2.14"), Dest: addr("198.51.100.7"),
			Packets: 4, Bytes: 336, Accounted: true},
	}
}

// demoSockets is the demo machine's socket tables: what it is listening on,
// and what it has open.
func demoSockets() []Socket {
	listen := func(protocol, address string, port uint16, uid uint32) Socket {
		state := "LISTEN"
		if strings.HasPrefix(protocol, "udp") {
			state = udpUnconnected
		}
		return Socket{Protocol: protocol, Local: addr(address), LocalPort: port,
			Remote: addr(unspecifiedFor(protocol)), State: state, UID: uid}
	}
	open := func(protocol, local string, lport uint16, remote string,
		rport uint16, state string, uid uint32) Socket {
		return Socket{Protocol: protocol, Local: addr(local), LocalPort: lport,
			Remote: addr(remote), RemotePort: rport, State: state, UID: uid}
	}
	return []Socket{
		listen("tcp", "0.0.0.0", 22, 0),
		listen("tcp", "0.0.0.0", 80, 0),
		listen("tcp", "0.0.0.0", 443, 0),
		listen("tcp", "127.0.0.1", 5432, 26),
		listen("tcp", "127.0.0.1", 6379, 979),
		listen("tcp6", "::", 22, 0),
		listen("tcp6", "::", 443, 0),
		listen("udp", "0.0.0.0", 53, 193),
		listen("udp", "0.0.0.0", 51820, 0),
		listen("udp6", "::", 546, 193),
		open("tcp", "192.0.2.14", 51234, "198.51.100.7", 443, "ESTABLISHED", 1000),
		open("tcp", "192.0.2.14", 51288, "198.51.100.7", 443, "ESTABLISHED", 1000),
		open("tcp", "192.0.2.14", 45012, "192.0.2.61", 5432, "ESTABLISHED", 26),
		open("tcp", "192.0.2.14", 22, "198.51.100.31", 60122, "ESTABLISHED", 0),
		open("tcp", "192.0.2.14", 51002, "198.51.100.7", 443, "TIME_WAIT", 1000),
		open("tcp", "192.0.2.14", 50988, "198.51.100.9", 80, "TIME_WAIT", 1000),
		open("tcp", "192.0.2.14", 49880, "192.0.2.61", 5432, "CLOSE_WAIT", 26),
		open("tcp6", "2001:db8::14", 51500, "2001:db8:1::7", 443, "ESTABLISHED", 1000),
		open("udp", "192.0.2.14", 51820, "198.51.100.88", 51820, udpConnected, 0),
	}
}

// unspecifiedFor is the "any address" of a protocol family, which is what a
// listening socket has for a peer.
func unspecifiedFor(protocol string) string {
	if strings.HasSuffix(protocol, "6") {
		return "::"
	}
	return "0.0.0.0"
}
