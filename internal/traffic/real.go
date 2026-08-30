package traffic

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// The files this tool reads. They are the kernel's own, they are world
// readable except where noted, and reading them is not running anything: the
// family's exec boundary is about processes, and os.ReadFile starts none.
const (
	procNetDev         = "proc/net/dev"
	procNFConntrack    = "proc/net/nf_conntrack"
	procConntrackAcct  = "proc/sys/net/netfilter/nf_conntrack_acct"
	procConntrackCount = "proc/sys/net/netfilter/nf_conntrack_count"
	sysClassNet        = "sys/class/net"
)

// socketTables are the four socket files, in the order the sockets screen
// reads them. A machine with IPv6 disabled has neither of the v6 files, and
// that is an ordinary configuration rather than a failure.
var socketTables = []struct{ protocol, path string }{
	{"tcp", "proc/net/tcp"},
	{"tcp6", "proc/net/tcp6"},
	{"udp", "proc/net/udp"},
	{"udp6", "proc/net/udp6"},
}

// socketSource is what the sockets screen names as its source.
const socketSource = "/proc/net/tcp, tcp6, udp, udp6"

// conntrackCommand is the one command this tool ever runs. It is a read: the
// table is printed and nothing in it is touched.
var conntrackCommand = []string{"conntrack", "-L", "-o", "extended"}

// Real is the backend that reads this machine.
//
// Almost all of it is os.ReadFile against /proc and /sys, which needs no
// privilege and cannot change anything. The one exception is the connection
// tracking table, which only root can see: the conntrack command is reached
// through the kit runner with the configured `sudo -n` prefix, and a machine
// where that does not work falls back to counting sockets and says so.
type Real struct {
	// root is the filesystem the paths above are resolved against. It is "/"
	// everywhere except in the tests, which point it at a captured tree.
	root string
	// conntrack is nil when this machine has no conntrack command, which is
	// most of them; conntrackErr then says why.
	conntrack    *runner.Runner
	conntrackErr string
}

// New builds the backend for this machine. sudoPrefix comes from the
// configuration ("sudo -n"); pass nil to read without escalation.
//
// It fails only when there is no /proc/net/dev to read, which means the tool
// is not on a Linux machine and there is nothing for it to do. A missing
// conntrack is not a failure: it is the common case, and the connections
// screen has an answer for it.
func New(sudoPrefix []string) (*Real, error) { return newAt("/", sudoPrefix) }

// newAt is New against an arbitrary root, which is what lets the tests run
// the whole read path over captured files.
func newAt(root string, sudoPrefix []string) (*Real, error) {
	real := &Real{root: root}
	if _, err := os.Stat(real.path(procNetDev)); err != nil {
		return nil, fmt.Errorf(
			"cannot read %s: this tool reads the Linux kernel's own counters, "+
				"and there are none here", real.path(procNetDev))
	}
	conntrack, err := runner.New(runner.Options{
		Bin: "conntrack",
		SearchPaths: []string{
			"/usr/sbin/conntrack", "/sbin/conntrack", "/usr/bin/conntrack",
		},
		SudoPrefix: sudoPrefix,
		// Reading the connection tracking table needs root, which is the
		// runner's default for a read and the reason this line is not here.
		InstallHint: "it comes with the conntrack-tools package",
	})
	if err != nil {
		real.conntrackErr = err.Error()
	} else {
		real.conntrack = conntrack
	}
	return real, nil
}

// path resolves one of the constants above against the backend's root.
func (r *Real) path(name string) string { return filepath.Join(r.root, name) }

// Name identifies the backend.
func (r *Real) Name() string { return "proc" }

// Describe is the one-line summary shown in the header: what is being read,
// and whether the conntrack table is among it.
func (r *Real) Describe() string {
	if r.conntrack == nil {
		return "/proc and /sys  ·  no conntrack on this machine"
	}
	return "/proc and /sys  ·  " + r.conntrack.Describe()
}

// Sources names where each screen's numbers come from, without taking a
// sample. It runs nothing and escalates for nothing: it looks at which
// binary exists and reads one sysctl, both of which a user who cannot get
// root can still do.
func (r *Real) Sources(_ context.Context) Sources {
	sources := Sources{
		Interfaces:  "/proc/net/dev",
		Sockets:     socketSource,
		Accounting:  r.accounting(),
		Connections: strings.Join(conntrackCommand, " "),
	}
	switch {
	case r.conntrack != nil:
		// The command is the first choice and the one it would try.
	case r.readable(procNFConntrack):
		sources.Connections = "/" + procNFConntrack
	default:
		sources.Connections = socketSource + " (no conntrack)"
	}
	return sources
}

// readable reports whether a file can actually be opened, which for
// /proc/net/nf_conntrack is a question about privilege rather than existence:
// the file is there on every machine with connection tracking and only root
// can read it.
func (r *Real) readable(name string) bool {
	file, err := os.Open(r.path(name))
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// accounting reads whether the kernel counts bytes per tracked connection.
// The file is absent on a machine with no connection tracking at all, and
// that is reported as unknown rather than as off: there is no table for the
// question to be about.
func (r *Real) accounting() Accounting {
	data, err := os.ReadFile(r.path(procConntrackAcct))
	if err != nil {
		return AccountingUnknown
	}
	switch strings.TrimSpace(string(data)) {
	case "0":
		return AccountingOff
	case "":
		return AccountingUnknown
	default:
		return AccountingOn
	}
}

// Sample reads every interface's counters once, with the link state beside
// them.
func (r *Real) Sample(_ context.Context) (Sample, error) {
	data, err := os.ReadFile(r.path(procNetDev))
	if err != nil {
		return Sample{}, fmt.Errorf("cannot read %s: %w", r.path(procNetDev), err)
	}
	// The timestamp is taken after the read rather than before it, so a slow
	// read makes the window longer instead of making the rate larger.
	interfaces, err := ParseProcNetDev(string(data))
	at := time.Now()
	if err != nil {
		return Sample{}, err
	}
	return Sample{At: at, Interfaces: interfaces, Links: r.links(interfaces)}, nil
}

// links reads what /sys says about each interface. Everything here is
// optional: a virtual interface has no speed, a container bridge may vanish
// between the two reads, and neither is worth failing a sample over.
func (r *Real) links(interfaces []Interface) map[string]Link {
	links := make(map[string]Link, len(interfaces))
	for _, item := range interfaces {
		dir := filepath.Join(r.path(sysClassNet), item.Name)
		link := Link{State: "unknown"}
		if state := readTrimmed(filepath.Join(dir, "operstate")); state != "" {
			link.State = state
		}
		link.Carrier = readTrimmed(filepath.Join(dir, "carrier")) == "1"
		// An interface that is down reports its speed as -1, which is the
		// kernel's way of saying it does not know.
		if speed, err := strconv.Atoi(readTrimmed(filepath.Join(dir, "speed"))); err == nil && speed > 0 {
			link.Speed = speed
		}
		links[item.Name] = link
	}
	return links
}

// readTrimmed reads a small /sys file, returning the empty string for
// anything that cannot be read. Several of them return EINVAL rather than a
// value — the speed of an interface that is down is one — so an error here is
// an ordinary answer.
func readTrimmed(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // a path built from the fixed constants above, under /proc and /sys
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Connections summarises the connection tracking table, in the best way this
// machine allows, and says which way that was.
//
// There are three sources and they are tried in order, because they answer
// progressively less of the question:
//
//  1. `conntrack -L -o extended`, the whole table with its flags;
//  2. /proc/net/nf_conntrack, the same table, for a machine with connection
//     tracking but without the tools — readable only as root;
//  3. the socket tables, which are not the same thing at all and say so.
//
// Whichever answers, the summary carries the source and, when it is not the
// first one, a sentence explaining why. A number whose provenance is not on
// screen beside it is a number a reader cannot check.
func (r *Real) Connections(ctx context.Context) (Connections, error) {
	accounting := r.accounting()
	var reasons []string

	if r.conntrack != nil {
		output, err := r.conntrack.Read(ctx, conntrackCommand...)
		if err == nil {
			flows := ParseConntrack(output)
			return Summarise(flows, SourceConntrack,
				r.conntrack.Preview(runner.Command{Argv: conntrackCommand}),
				accountingNote(accounting), accounting), nil
		}
		reasons = append(reasons, "conntrack could not be read ("+
			runner.FirstLine(err.Error())+")")
	} else if r.conntrackErr != "" {
		reasons = append(reasons, runner.FirstLine(r.conntrackErr))
	}

	if data, err := os.ReadFile(r.path(procNFConntrack)); err == nil {
		flows := ParseConntrack(string(data))
		note := "read from the kernel's own file: " +
			strings.Join(reasons, "; ")
		if accountingNote(accounting) != "" {
			note += ". " + accountingNote(accounting)
		}
		return Summarise(flows, SourceProcConntrack, "/"+procNFConntrack,
			note, accounting), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		reasons = append(reasons, "/"+procNFConntrack+" is not readable "+
			"without root")
	}

	sockets, err := r.readSockets()
	if err != nil {
		return Connections{}, err
	}
	note := "these are open sockets, not tracked connections: " +
		strings.Join(reasons, "; ") +
		". A machine forwarding traffic for others tracks connections that " +
		"belong to no socket here, and those are not counted."
	return ConnectionsFromSockets(sockets, socketSource, note), nil
}

// accountingNote is the sentence the connections screen shows about missing
// byte figures. It is empty when the kernel is counting, because there is
// then nothing to explain.
func accountingNote(accounting Accounting) string {
	switch accounting {
	case AccountingOn:
		return ""
	case AccountingOff:
		return "The kernel is not counting bytes per connection " +
			"(net.netfilter.nf_conntrack_acct is 0), so there are no byte " +
			"figures to show."
	default:
		return "Whether the kernel counts bytes per connection could not be " +
			"read, so there may be no byte figures."
	}
}

// Sockets reads the four socket tables.
func (r *Real) Sockets(_ context.Context) (Sockets, error) {
	sockets, err := r.readSockets()
	if err != nil {
		return Sockets{}, err
	}
	return SummariseSockets(sockets, socketSource), nil
}

// readSockets reads whichever of the four tables this machine has. A machine
// with IPv6 disabled has no tcp6 and no udp6, and that is a configuration
// rather than a failure; a machine with none of the four at all is a failure,
// because then nothing was read and an empty screen would be a lie.
func (r *Real) readSockets() ([]Socket, error) {
	var (
		all   []Socket
		reads int
		last  error
	)
	for _, table := range socketTables {
		data, err := os.ReadFile(r.path(table.path))
		if err != nil {
			last = err
			continue
		}
		rows, err := ParseProcNetSockets(string(data), table.protocol)
		if err != nil {
			last = err
			continue
		}
		reads++
		all = append(all, rows...)
	}
	if reads == 0 {
		return nil, fmt.Errorf("no socket table could be read: %w", last)
	}
	return all, nil
}
