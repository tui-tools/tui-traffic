package traffic

import "testing"

func TestParseConntrackExtended(t *testing.T) {
	flows := ParseConntrack(read(t, "conntrack-extended.txt"))
	// Eight rows, and the tools' own summary line is not one of them.
	if len(flows) != 8 {
		t.Fatalf("got %d flows, want 8", len(flows))
	}

	tests := []struct {
		name string
		flow Flow
		want Flow
	}{
		{
			name: "an established connection carries both directions' bytes",
			flow: flows[0],
			want: Flow{Protocol: "tcp", State: "ESTABLISHED",
				Source: addr("192.0.2.14"), SourcePort: 51234,
				Dest: addr("198.51.100.7"), DestPort: 443,
				Packets: 8142 + 9033, Bytes: 11482113 + 48211904,
				Accounted: true, Assured: true},
		},
		{
			name: "an unreplied connection keeps its flag and its zero reply",
			flow: flows[2],
			want: Flow{Protocol: "tcp", State: "SYN_SENT",
				Source: addr("192.0.2.14"), SourcePort: 51301,
				Dest: addr("198.51.100.44"), DestPort: 8443,
				Packets: 3, Bytes: 180, Accounted: true, Unreplied: true},
		},
		{
			name: "udp has no state at all, and is not given one",
			flow: flows[4],
			want: Flow{Protocol: "udp",
				Source: addr("192.0.2.14"), SourcePort: 53114,
				Dest: addr("192.0.2.1"), DestPort: 53,
				Packets: 4, Bytes: 692, Accounted: true},
		},
		{
			name: "icmp has no ports either",
			flow: flows[6],
			want: Flow{Protocol: "icmp",
				Source: addr("192.0.2.14"), Dest: addr("198.51.100.7"),
				Packets: 8, Bytes: 672, Accounted: true},
		},
		{
			name: "ipv6 is read the same way",
			flow: flows[7],
			want: Flow{Protocol: "tcp", State: "ESTABLISHED",
				Source: addr("2001:db8::14"), SourcePort: 51500,
				Dest: addr("2001:db8:1::7"), DestPort: 443,
				Packets: 1882 + 2044, Bytes: 998112 + 8811233,
				Accounted: true, Assured: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.flow != tc.want {
				t.Errorf("got  %+v\nwant %+v", tc.flow, tc.want)
			}
		})
	}
}

// The plain output of `conntrack -L` has no layer-3 prefix. Both forms reach
// the same parser, so a tool that dropped `-o extended` would still work.
func TestParseConntrackWithoutTheLayer3Prefix(t *testing.T) {
	flows := ParseConntrack(read(t, "conntrack-plain.txt"))
	if len(flows) != 2 {
		t.Fatalf("got %d flows, want 2", len(flows))
	}
	if flows[0].Protocol != "tcp" || flows[0].State != "ESTABLISHED" {
		t.Errorf("first flow = %+v", flows[0])
	}
	if flows[1].Protocol != "udp" || flows[1].State != "" {
		t.Errorf("second flow = %+v", flows[1])
	}
}

// /proc/net/nf_conntrack is the same table with a zone column, which is one
// more key=value pair and changes nothing about how it is read.
func TestParseProcNFConntrack(t *testing.T) {
	flows := ParseConntrack(read(t, "proc-net-nf-conntrack.txt"))
	if len(flows) != 3 {
		t.Fatalf("got %d flows, want 3", len(flows))
	}
	if got := flows[2]; got.State != "CLOSE_WAIT" || !got.Source.Is6() {
		t.Errorf("the ipv6 row was misread: %+v", got)
	}
}

// A kernel that is not accounting prints no packets and no bytes. The
// difference between that and zero is the whole reason Accounted exists.
func TestParseConntrackWithoutAccounting(t *testing.T) {
	flows := ParseConntrack(read(t, "conntrack-no-accounting.txt"))
	if len(flows) != 3 {
		t.Fatalf("got %d flows, want 3", len(flows))
	}
	for _, flow := range flows {
		if flow.Accounted {
			t.Errorf("%s %s claims to be accounted: %+v", flow.From(), flow.To(), flow)
		}
		if flow.Bytes != 0 || flow.Packets != 0 {
			t.Errorf("an unaccounted flow carries counts: %+v", flow)
		}
	}
}

func TestParseConntrackSkipsWhatIsNotAFlow(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"nothing", ""},
		{"the summary line on its own",
			"conntrack v1.4.8 (conntrack-tools): 0 flow entries have been shown.\n"},
		{"an error the command prints",
			"conntrack v1.4.8 (conntrack-tools): Operation failed: such file or directory\n"},
		{"a row with no endpoints",
			"ipv4     2 tcp      6 431995 ESTABLISHED mark=0 use=1\n"},
		{"a row whose address is not one",
			"ipv4     2 tcp      6 120 ESTABLISHED src=not-an-address dst=198.51.100.7 sport=1 dport=2\n"},
		{"a truncated row", "ipv4     2 tcp\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if flows := ParseConntrack(tc.in); len(flows) != 0 {
				t.Errorf("got %d flows, want none: %+v", len(flows), flows)
			}
		})
	}
}

func TestSummarise(t *testing.T) {
	flows := ParseConntrack(read(t, "conntrack-extended.txt"))
	summary := Summarise(flows, SourceConntrack, "conntrack -L -o extended", "",
		AccountingOn)

	if summary.Total != 8 {
		t.Errorf("Total = %d, want 8", summary.Total)
	}
	if summary.Source != SourceConntrack {
		t.Errorf("Source = %q", summary.Source)
	}
	// Protocols, busiest first: five tcp — four over IPv4 and one over IPv6 —
	// then two udp and one icmp.
	want := []Count{{Label: "tcp", Count: 5}, {Label: "udp", Count: 2},
		{Label: "icmp", Count: 1}}
	if len(summary.Protocols) != len(want) {
		t.Fatalf("protocols = %+v", summary.Protocols)
	}
	for i, count := range want {
		if summary.Protocols[i] != count {
			t.Errorf("protocols[%d] = %+v, want %+v", i, summary.Protocols[i], count)
		}
	}

	// A protocol with no states still gets a row, so the counts add up.
	var stateless int
	for _, state := range summary.States {
		if state.State == "stateless" {
			stateless += state.Count
		}
	}
	if stateless != 3 {
		t.Errorf("stateless rows = %d, want the two udp flows and the icmp one", stateless)
	}

	// The talkers are the accounted flows, biggest first.
	if len(summary.Talkers) == 0 {
		t.Fatal("accounting is on and there are no talkers")
	}
	for i := 1; i < len(summary.Talkers); i++ {
		if summary.Talkers[i-1].Bytes < summary.Talkers[i].Bytes {
			t.Fatalf("talkers are not sorted: %+v", summary.Talkers)
		}
	}
	if got := summary.Talkers[0]; got.From != "192.0.2.14:51820" {
		t.Errorf("busiest talker = %+v, want the VPN flow", got)
	}
}

// The assertion this screen exists for: with accounting off there are no
// byte figures, and the summary shows none rather than a column of zeroes.
func TestSummariseWithoutAccountingHasNoTalkers(t *testing.T) {
	flows := ParseConntrack(read(t, "conntrack-no-accounting.txt"))
	summary := Summarise(flows, SourceConntrack, "conntrack -L -o extended",
		accountingNote(AccountingOff), AccountingOff)

	if len(summary.Talkers) != 0 {
		t.Errorf("talkers were invented from unmeasured bytes: %+v", summary.Talkers)
	}
	if summary.Accounting != AccountingOff {
		t.Errorf("Accounting = %q", summary.Accounting)
	}
	if summary.Note == "" {
		t.Error("a screen missing a column has to say why")
	}
	if summary.Total != 3 {
		t.Errorf("Total = %d, want 3: the connections are still counted", summary.Total)
	}
}

func TestEndpointOmitsAPortThatDoesNotExist(t *testing.T) {
	tests := []struct {
		name string
		flow Flow
		want string
	}{
		{"ipv4 with a port", Flow{Source: addr("192.0.2.14"), SourcePort: 443},
			"192.0.2.14:443"},
		{"ipv4 without one", Flow{Source: addr("192.0.2.14")}, "192.0.2.14"},
		{"ipv6 with a port", Flow{Source: addr("2001:db8::14"), SourcePort: 443},
			"[2001:db8::14]:443"},
		{"ipv6 without one", Flow{Source: addr("2001:db8::14")}, "2001:db8::14"},
		{"no address at all", Flow{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.flow.From(); got != tc.want {
				t.Errorf("From() = %q, want %q", got, tc.want)
			}
		})
	}
}
