package traffic

import (
	"os"
	"testing"
)

// read loads a captured fixture. Every parser test in this package runs
// against output taken off a real machine, with the addresses replaced by the
// documentation ranges; see testdata/README.md.
func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return string(data)
}

func TestParseProcNetDev(t *testing.T) {
	interfaces, err := ParseProcNetDev(read(t, "proc-net-dev.txt"))
	if err != nil {
		t.Fatalf("ParseProcNetDev: %v", err)
	}
	if len(interfaces) != 11 {
		t.Fatalf("got %d interfaces, want 11", len(interfaces))
	}

	byName := map[string]Interface{}
	for _, item := range interfaces {
		byName[item.Name] = item
	}

	tests := []struct {
		name string
		want Interface
	}{
		{"lo", Interface{Name: "lo",
			RX: Counters{Bytes: 100974363603, Packets: 19202696},
			TX: Counters{Bytes: 100974363603, Packets: 19202696}}},
		{"enp3s0", Interface{Name: "enp3s0",
			RX: Counters{Bytes: 103545567126, Packets: 90026648,
				Errors: 106952, Dropped: 45, Multicast: 1159495},
			TX: Counters{Bytes: 27606384821, Packets: 43789559, Dropped: 1346}}},
		// A long interface name still leaves the counters where they are:
		// this is the row a naive whitespace split reads as one field short.
		{"br-0a1b2c3d4e5f", Interface{Name: "br-0a1b2c3d4e5f",
			RX: Counters{Bytes: 51343767604, Packets: 6656036},
			TX: Counters{Bytes: 44916965568, Packets: 7668707, Dropped: 43155}}},
		// An interface that has moved nothing at all, which is most of them
		// on a machine running containers.
		{"vpn0aaaaaaaaaa", Interface{Name: "vpn0aaaaaaaaaa",
			TX: Counters{Dropped: 197790}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := byName[tc.name]
			if !ok {
				t.Fatalf("%s is missing from the parsed table", tc.name)
			}
			if got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestParseProcNetDevOverflow covers the two shapes the fixed-width columns
// produce on a machine that has been up long enough: a name run together with
// its first number because the field overflowed, and a name wider than the
// column.
func TestParseProcNetDevOverflow(t *testing.T) {
	interfaces, err := ParseProcNetDev(read(t, "proc-net-dev-overflow.txt"))
	if err != nil {
		t.Fatalf("ParseProcNetDev: %v", err)
	}
	if len(interfaces) != 3 {
		t.Fatalf("got %d interfaces, want 3", len(interfaces))
	}
	if got := interfaces[1]; got.Name != "enp0s31f6" ||
		got.RX.Bytes != 184467440737095516 {
		t.Errorf("a name run together with its counter was misread: %+v", got)
	}
	if got := interfaces[2]; got.Name != "veth9f1c2a3b4d5e6f7" || got.RX.Packets != 4455 {
		t.Errorf("a name wider than the column was misread: %+v", got)
	}
}

func TestParseProcNetDevRejectsWhatIsNotTheFile(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"nothing at all", ""},
		{"the headers and no rows", "Inter-|   Receive\n face |bytes packets\n"},
		{"a file that is not this one", "hello world\n"},
		{"rows with too few columns", "eth0: 1 2 3\n"},
		{"rows whose counters are not numbers", "eth0: a b c d e f g h i j k l m n o p\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseProcNetDev(tc.in); err == nil {
				t.Error("expected an error rather than a machine with no interfaces")
			}
		})
	}
}
