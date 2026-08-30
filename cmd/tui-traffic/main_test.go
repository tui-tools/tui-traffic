package main

import (
	"os"
	"testing"
	"time"

	"github.com/tui-tools/tui-kit/config"
)

func TestParseFlags(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = devnull.Close() }()

	tests := []struct {
		name string
		args []string
		want options
	}{
		{"nothing", nil, options{}},
		{"demo", []string{"--demo"}, options{demo: true}},
		{"check", []string{"--check"}, options{check: true}},
		{"report", []string{"--report"}, options{report: true}},
		{"an interval", []string{"--interval", "2s"},
			options{interval: "2s"}},
		// An explicitly empty -sudo disables escalation, which has to be
		// distinguishable from not passing the flag at all.
		{"sudo turned off", []string{"--sudo", ""},
			options{sudo: "", sudoSet: true}},
		{"sudo replaced", []string{"--sudo", "doas"},
			options{sudo: "doas", sudoSet: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlags(tc.args, devnull)
			if err != nil {
				t.Fatalf("parseFlags: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestResolveInterval(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"the default", defaultInterval.String(), time.Second, false},
		{"a shorter one", "250ms", 250 * time.Millisecond, false},
		{"a longer one", "5s", 5 * time.Second, false},
		// Out of range is clamped and runs: the intent is clear, and below
		// the floor the two reads cost more than the traffic they measure.
		{"below the floor", "1ms", minInterval, false},
		{"above the ceiling", "10m", maxInterval, false},
		// Unparsable is an error rather than a silent fallback: somebody who
		// wrote "2" meant something by it, and starting at one second while
		// they watch for a change is worse than saying so.
		{"a bare number", "2", 0, true},
		{"a word", "fast", 0, true},
		{"nothing at all", "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Tool: toolName, Values: defaults()}
			cfg.Set(keyInterval, tc.value)
			got, err := resolveInterval(cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveInterval: %v", err)
			}
			if got != tc.want {
				t.Errorf("interval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := config.Config{Tool: toolName, Values: defaults()}
	applyOverrides(&cfg, options{interval: "3s", sudo: "", sudoSet: true})

	if got := cfg.String(keyInterval, ""); got != "3s" {
		t.Errorf("interval = %q, want 3s", got)
	}
	if got := cfg.String(config.KeySudo, "unset"); got != "" {
		t.Errorf("sudo = %q, want the empty prefix the flag asked for", got)
	}
}

// --version and --help are the two paths that must never need a backend, a
// terminal or a machine that can be read.
func TestVersionAndHelpNeedNothing(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%v) = %v", args, err)
		}
	}
}
