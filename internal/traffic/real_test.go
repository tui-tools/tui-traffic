package traffic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRoot builds a filesystem that looks like the part of /proc and /sys
// this tool reads, out of the captured fixtures. It is what lets the whole
// read path — not only the parsers — run in a test, on a machine whose own
// /proc looks nothing like the one the fixtures came from.
func fakeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, fixture := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		content := fixture
		if !strings.Contains(fixture, "\n") && fixture != "" {
			content = read(t, fixture)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

// machine is the fixture tree used by most of the tests below: a machine with
// interfaces, sockets and no conntrack.
func machine() map[string]string {
	return map[string]string{
		procNetDev:        "proc-net-dev.txt",
		"proc/net/tcp":    "proc-net-tcp.txt",
		"proc/net/tcp6":   "proc-net-tcp6.txt",
		"proc/net/udp":    "proc-net-udp.txt",
		"proc/net/udp6":   "proc-net-udp6.txt",
		procConntrackAcct: "0\n",
	}
}

func TestRealSample(t *testing.T) {
	backend, err := newAt(fakeRoot(t, machine()), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	sample, err := backend.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if len(sample.Interfaces) != 11 {
		t.Errorf("got %d interfaces, want 11", len(sample.Interfaces))
	}
	if sample.At.IsZero() {
		t.Error("a sample without a moment is not a sample")
	}
	// There is no /sys in the fixture tree, and that is not an error: an
	// interface whose link state cannot be read is reported as unknown.
	for _, item := range sample.Interfaces {
		if sample.Links[item.Name].State != "unknown" {
			t.Errorf("%s = %+v, want the unknown link state",
				item.Name, sample.Links[item.Name])
		}
	}
}

func TestRealSockets(t *testing.T) {
	backend, err := newAt(fakeRoot(t, machine()), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	sockets, err := backend.Sockets(context.Background())
	if err != nil {
		t.Fatalf("Sockets: %v", err)
	}
	if sockets.Total == 0 {
		t.Fatal("the four tables parsed to nothing")
	}
	if len(sockets.Listening) == 0 {
		t.Error("a machine with an sshd on it has listening sockets")
	}
	if sockets.Source != socketSource {
		t.Errorf("Source = %q", sockets.Source)
	}
}

// IPv6 disabled is a configuration, not a failure: the two v6 tables are
// simply not there.
func TestRealSocketsWithoutIPv6(t *testing.T) {
	files := machine()
	delete(files, "proc/net/tcp6")
	delete(files, "proc/net/udp6")

	backend, err := newAt(fakeRoot(t, files), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	sockets, err := backend.Sockets(context.Background())
	if err != nil {
		t.Fatalf("Sockets: %v", err)
	}
	for _, count := range sockets.Protocols {
		if strings.HasSuffix(count.Label, "6") {
			t.Errorf("a v6 protocol appeared with no v6 table: %+v", sockets.Protocols)
		}
	}
}

// A machine with none of the four tables is a different matter: nothing was
// read, and an empty screen would be a claim rather than a reading.
func TestRealSocketsWithNoTableAtAll(t *testing.T) {
	backend, err := newAt(fakeRoot(t, map[string]string{
		procNetDev: "proc-net-dev.txt",
	}), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	if _, err := backend.Sockets(context.Background()); err == nil {
		t.Error("expected an error rather than a machine with no sockets")
	}
}

// The second source: a machine with connection tracking and without the
// conntrack tools. This is the path that only works as root, and the fixture
// tree is how it gets tested as anybody.
func TestRealConnectionsFromTheKernelFile(t *testing.T) {
	files := machine()
	files[procNFConntrack] = "proc-net-nf-conntrack.txt"
	files[procConntrackAcct] = "1\n"

	backend, err := newAt(fakeRoot(t, files), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	// The test must not depend on whether this machine has conntrack
	// installed, so the command is taken out of the picture.
	backend.conntrack = nil
	backend.conntrackErr = "the conntrack command was not found"

	connections, err := backend.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if connections.Source != SourceProcConntrack {
		t.Fatalf("Source = %q, want %q", connections.Source, SourceProcConntrack)
	}
	if connections.Total != 3 {
		t.Errorf("Total = %d, want 3", connections.Total)
	}
	if connections.Accounting != AccountingOn {
		t.Errorf("Accounting = %q, want on", connections.Accounting)
	}
	if len(connections.Talkers) == 0 {
		t.Error("accounting is on in this fixture and there are no talkers")
	}
	if !strings.Contains(connections.Note, "conntrack command was not found") {
		t.Errorf("the note does not say why the first source was skipped: %q",
			connections.Note)
	}
}

// The third source: no conntrack anywhere. The screen still has an answer,
// and the answer says what it is.
func TestRealConnectionsFallsBackToSockets(t *testing.T) {
	backend, err := newAt(fakeRoot(t, machine()), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	backend.conntrack = nil
	backend.conntrackErr = "the conntrack command was not found"

	connections, err := backend.Connections(context.Background())
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if connections.Source != SourceSockets {
		t.Fatalf("Source = %q, want %q", connections.Source, SourceSockets)
	}
	if connections.Total == 0 {
		t.Error("the capture has connected sockets in it")
	}
	if len(connections.Talkers) != 0 {
		t.Errorf("the fallback invented talkers: %+v", connections.Talkers)
	}
	for _, phrase := range []string{"not tracked connections", "conntrack"} {
		if !strings.Contains(connections.Note, phrase) {
			t.Errorf("the note is missing %q: %q", phrase, connections.Note)
		}
	}
}

func TestRealAccounting(t *testing.T) {
	tests := []struct {
		name string
		file string
		want Accounting
	}{
		{"off, which is the default on most kernels", "0\n", AccountingOff},
		{"on", "1\n", AccountingOn},
		// A machine with no connection tracking has no file, and the question
		// does not apply rather than having a no for an answer.
		{"no file at all", "", AccountingUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := machine()
			if tc.file == "" {
				delete(files, procConntrackAcct)
			} else {
				files[procConntrackAcct] = tc.file
			}
			backend, err := newAt(fakeRoot(t, files), nil)
			if err != nil {
				t.Fatalf("newAt: %v", err)
			}
			if got := backend.accounting(); got != tc.want {
				t.Errorf("accounting() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Sources runs nothing and needs no privilege: it is what --report prints on
// a machine where the reads themselves would fail.
func TestRealSources(t *testing.T) {
	backend, err := newAt(fakeRoot(t, machine()), nil)
	if err != nil {
		t.Fatalf("newAt: %v", err)
	}
	backend.conntrack = nil

	sources := backend.Sources(context.Background())
	if sources.Interfaces != "/proc/net/dev" {
		t.Errorf("Interfaces = %q", sources.Interfaces)
	}
	if sources.Sockets != socketSource {
		t.Errorf("Sockets = %q", sources.Sockets)
	}
	if !strings.Contains(sources.Connections, "no conntrack") {
		t.Errorf("Connections = %q, want the fallback named", sources.Connections)
	}
	if sources.Accounting != AccountingOff {
		t.Errorf("Accounting = %q", sources.Accounting)
	}
}

// A machine with no /proc/net/dev is not a machine this tool has anything to
// say about, and it says so at startup rather than showing an empty screen.
func TestNewRejectsAMachineWithNoCounters(t *testing.T) {
	if _, err := newAt(t.TempDir(), nil); err == nil {
		t.Error("expected an error for a root with no /proc/net/dev")
	}
}

// The compile-time proof that the read-only backend is the whole interface:
// there is no Build and no Run on it to call.
var (
	_ Backend = (*Real)(nil)
	_ Backend = (*Fake)(nil)
)
