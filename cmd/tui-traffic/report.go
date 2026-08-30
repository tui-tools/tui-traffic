package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// notAvailable is the runner's own wording for a program this machine does
// not have. It is what separates "conntrack is not installed" — the common
// case, and not a fault — from "conntrack is here and the probe could not
// read a version off it".
const notAvailable = "command not available"

// noVersionDetail is why the backend line carries no version of its own. This
// tool's backend is /proc, which has no version to read; the one program it
// drives has one, and it is on the backends line below.
const noVersionDetail = "the kernel's own files, so there is no one version to read"

// runReport prints the block a bug report needs and exits. Every tool in the
// family has this function, and it is worth keeping it recognisable.
//
// Everything generic — the kit version, the distribution, the kernel, the
// terminal, where the binary came from — is collected by the kit, so the whole
// family answers --report in the same shape. What this one adds is the two
// facts that explain most of what people will report about it: which source
// each screen would read on this machine, and whether the kernel is counting
// bytes per connection. A connections screen with no byte column is the first
// thing anybody notices, and `conntrack accounting: off` is the answer.
//
// It takes no sample and runs no command beyond the version probe. A user who
// cannot escalate is the one who most needs to be able to file a usable bug,
// and the missing privilege may be the bug.
func runReport(cfg config.Config, opts options, interval time.Duration,
	out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probe the header and --check use. There is one version probe
	// in a tool and this is it.
	backends := probeCompat(context.Background(), opts.demo)

	// pickBackend returns an interface, so its error path is taken by name
	// rather than by comparing the value to nil: a typed nil in an interface
	// is not nil, and a report is the wrong place to learn that.
	var (
		backendName, selectError string
		sources                  traffic.Sources
		haveSources              bool
	)
	if backend, err := pickBackend(cfg, opts); err != nil {
		selectError = err.Error()
	} else {
		backendName = backend.Name()
		sources, haveSources = backend.Sources(context.Background()), true
	}

	info := report.Info{
		Tool:          toolName,
		Version:       version,
		Backend:       backendName,
		BackendDetail: noVersionDetail,
		Demo:          opts.demo,
		Sudo:          cfg.String(config.KeySudo, ""),
		Theme:         palette.Name,
	}
	info.Extra = append(info.Extra, report.Field{
		Key: "interval", Value: interval.String(),
	})
	if opts.demo {
		// The fake says "demo" for its name, and naming what it stands in for
		// beside it is what keeps a demo report from reading as a report
		// about this machine.
		info.Backend = "demo"
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: "proc",
		})
	} else {
		info.Extra = append(info.Extra, report.Field{
			Key: "backends", Value: describeBackends(backends),
		})
	}
	if haveSources {
		info.Extra = append(info.Extra,
			report.Field{Key: "interfaces from", Value: sources.Interfaces},
			report.Field{Key: "connections from", Value: scrubHome(sources.Connections)},
			report.Field{Key: "sockets from", Value: sources.Sockets},
			report.Field{Key: "conntrack accounting", Value: string(sources.Accounting)},
		)
	}
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: scrubHome(selectError),
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// describeBackends renders every backend the tool probes as one line: the
// version where there is one, "absent" where the program is not on the
// machine. Absent is the ordinary answer here and reads as one — it is why
// the connections screen falls back — but it has to be told apart from a
// program that is present and answered nothing, which is a different bug.
func describeBackends(results []compat.Result) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		name := strings.TrimSpace(result.Backend)
		if name == "" {
			continue
		}
		switch {
		case result.Version != "":
			parts = append(parts, name+" "+result.Version)
		case strings.Contains(result.Detail, notAvailable):
			parts = append(parts, name+" absent")
		default:
			parts = append(parts, name+" (version unread)")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// homePath matches a path under a home directory. The block is pasted into a
// public issue, so a value a tool hands to report.Extra is its own
// responsibility: the kit scrubs what it collected itself and cannot know
// what is inside a message a tool passes on.
//
// This tool has one place a path can reach the block: the escalation prefix
// resolves to an absolute path, and a `sudo` somebody built into their own
// home directory would arrive here named after them.
var homePath = regexp.MustCompile(`(/home|/root)(/[^\s:]*)?`)

// scrubHome replaces such a path with the placeholder the kit uses for the
// same reason.
func scrubHome(s string) string {
	return homePath.ReplaceAllString(s, "~elsewhere~")
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no sample, no privileges, nothing about you: paste it into "+
		"a %s issue)",
	toolName)
