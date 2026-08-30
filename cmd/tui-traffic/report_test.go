package main

import (
	"os"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
)

// baseConfig is the configuration a report is rendered against: the defaults,
// with nothing read from disk or from the environment.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

// render runs a report and returns the block.
func render(t *testing.T, cfg config.Config, opts options) string {
	t.Helper()
	var out strings.Builder
	if err := runReport(cfg, opts, defaultInterval, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	return out.String()
}

// TestRunReportDemo checks the half of the block this tool owns. What has to
// be right is that --demo says demo, that it names what the fake stands in
// for, and that nothing on the machine was read to produce any of it.
func TestRunReportDemo(t *testing.T) {
	got := render(t, baseConfig(), options{demo: true, report: true})
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: proc\n",
		"interval: 1s\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// The two lines only this tool adds are the ones that explain most of what
// anybody will report about it: where each screen reads from on this machine,
// and whether the kernel counts bytes per connection.
func TestRunReportNamesItsSources(t *testing.T) {
	got := render(t, baseConfig(), options{report: true})
	for _, want := range []string{
		"mode: live\n",
		"backend: proc (",
		"interfaces from: /proc/net/dev\n",
		"sockets from: /proc/net/tcp",
		"conntrack accounting: ",
		"connections from: ",
		"backends: conntrack ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

// A machine without conntrack is the common case, and the report has to say
// "absent" rather than leaving it out: a missing line reads as a tool that
// did not look.
func TestDescribeBackends(t *testing.T) {
	tests := []struct {
		name string
		in   []compat.Result
		want string
	}{
		{"a version was read",
			[]compat.Result{{Backend: "conntrack", Version: "1.4.8"}},
			"conntrack 1.4.8"},
		{"the program is not on the machine",
			[]compat.Result{{Backend: "conntrack", Detail: notAvailable + ": …"}},
			"conntrack absent"},
		{"it is here and said nothing, which is a different bug",
			[]compat.Result{{Backend: "conntrack", Detail: "exit status 1"}},
			"conntrack (version unread)"},
		{"nothing was probed at all", nil, "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeBackends(tc.in); got != tc.want {
				t.Errorf("describeBackends = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunReportKeepsItsPrivacyPromise is the assertion the bug form depends
// on. The block is pasted into a public issue, so the user name, the home
// path or the host name appearing in it would be a disclosure rather than a
// cosmetic slip — and this tool's block carries addresses nowhere near it for
// the same reason: --report takes no sample.
func TestRunReportKeepsItsPrivacyPromise(t *testing.T) {
	got := render(t, baseConfig(), options{report: true})

	if strings.Contains(got, "/home/") {
		t.Errorf("report carries a home path:\n%s", got)
	}
	if host, err := os.Hostname(); err == nil {
		assertAbsent(t, got, host, "host name")
	}
	if u, err := user.Current(); err == nil {
		assertAbsent(t, got, u.Username, "user name")
	}
}

// assertAbsent fails when name appears in a value of the block. The keys are
// fixed by the kit and carry nothing about the machine, so only values are
// looked at; the three values a name can legitimately collide with — the
// distribution, the kernel and the terminal, none of which this tool supplies
// — are skipped, because a machine called "fedora" running Fedora is not a
// leak and failing on it would be a test of the machine rather than the code.
func assertAbsent(t *testing.T, block, name, what string) {
	t.Helper()
	if name == "" {
		return
	}
	for _, line := range strings.Split(block, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			// The headline, which carries only the tool and the versions.
			key, value = "", line
		}
		if key == "distro" || key == "kernel" || key == "term" {
			continue
		}
		if strings.Contains(value, name) {
			t.Errorf("report carries the %s %q on %q", what, name, line)
		}
	}
}

// TestScrubHome covers the one value this tool passes into the block that
// could name its user: the escalation prefix resolves to an absolute path,
// and a sudo somebody built in their own home directory would arrive here
// named after them.
func TestScrubHome(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a home path", "/home/ana/bin/sudo -n conntrack -L",
			"~elsewhere~ -n conntrack -L"},
		{"root's home", "/root/bin/doas conntrack -L",
			"~elsewhere~ conntrack -L"},
		{"a path that names nobody", "/usr/bin/sudo -n conntrack -L",
			"/usr/bin/sudo -n conntrack -L"},
		{"nothing to scrub", "conntrack was not found", "conntrack was not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubHome(tc.in); got != tc.want {
				t.Errorf("scrubHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The interval is on the block because it is what the rates were divided by,
// and a report of numbers that look wrong is usually a report about the
// window they were measured over.
func TestRunReportCarriesTheInterval(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{report: true},
		2500*time.Millisecond, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	if !strings.Contains(out.String(), "interval: 2.5s\n") {
		t.Errorf("the interval is missing from the block:\n%s", out.String())
	}
}
