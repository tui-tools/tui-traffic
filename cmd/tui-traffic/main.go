// Command tui-traffic shows what the network on one Linux machine is doing
// right now: throughput per interface with a sparkline over the last minute,
// the connection tracking table summarised, and the sockets that are
// listening or established.
//
// It is read-only, and not by default: there is no action key, no confirm
// dialog and no command builder anywhere in it. Every number comes from a
// file the kernel already exports, and the one program it ever runs —
// conntrack, to read a table only root can see — is a read.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-traffic/config.toml and ~/.config/tui-traffic/config.toml.
const toolName = "tui-traffic"

// keyInterval is this tool's own configuration key: how often it samples.
const keyInterval = "interval"

// defaultInterval is one second, which is what makes the numbers on screen
// throughput rather than an average over something longer.
const defaultInterval = time.Second

// The interval is bounded at both ends. Below the floor the two reads of
// /proc/net/dev cost more than the traffic they measure and the rate becomes
// mostly noise; above the ceiling the screen is a history rather than a
// picture of now, and the sparkline's minute of memory would cover half a
// day.
const (
	minInterval = 100 * time.Millisecond
	maxInterval = 30 * time.Second
)

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys the tool understands. Only these
// are read from the environment (TUI_TRAFFIC_INTERVAL, …), so an unrelated
// variable can never leak into the configuration.
func defaults() map[string]string {
	return map[string]string{
		keyInterval:     defaultInterval.String(),
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
	}
}

// options holds the parsed command line.
type options struct {
	demo        bool
	check       bool
	report      bool
	interval    string
	themePath   string
	sudo        string
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a machine that does not exist, without reading this one")
	fs.BoolVar(&opts.check, "check", false,
		"take one sample of all three screens, print it as JSON and exit "+
			"(no UI, nothing is changed); it takes about one interval to answer, "+
			"because a rate is two reads and the time between them")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.StringVar(&opts.interval, "interval", "",
		"how often to sample, e.g. 500ms or 2s (overrides the config file)")
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-traffic — what the network is doing right now\n\n"+
			"Usage:\n  tui-traffic [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_TRAFFIC_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sudo" {
			opts.sudoSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program. Every
// tool in the family has this function, and it is worth keeping it
// recognisable.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	interval, err := resolveInterval(cfg)
	if err != nil {
		return err
	}

	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine where no backend can be.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It
	// takes no sample and needs no privilege, and it comes before the backend
	// is required: a machine the tool cannot read at all is a machine whose
	// bug report still has to be filable.
	if opts.report {
		return runReport(cfg, opts, interval, os.Stdout)
	}

	// The conntrack version is probed once, at startup, and shown in the
	// header. A machine without it gets an empty result rather than an error:
	// most machines do not have it, and the connections screen has an answer
	// for that.
	backendCompat := probeCompat(context.Background(), opts.demo)

	backend, err := pickBackend(cfg, opts)
	if err != nil {
		return err
	}

	// --check is the other non-interactive path: it samples once and prints,
	// and never starts a terminal program.
	if opts.check {
		return runCheck(backend, backendCompat, interval, os.Stdout)
	}

	program := tea.NewProgram(newApp(backend, theme.New(), backendCompat, interval),
		tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.interval != "" {
		cfg.Set(keyInterval, opts.interval)
	}
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
}

// resolveInterval reads the sampling interval and holds it to the bounds.
//
// An interval that cannot be parsed is an error rather than a silent fallback
// to the default: somebody who wrote `--interval 2` meant something by it,
// and starting at one second while they watch for a change is worse than
// saying so. One that is merely out of range is clamped and runs, because the
// intent is clear and the tool still works.
func resolveInterval(cfg config.Config) (time.Duration, error) {
	text := cfg.String(keyInterval, defaultInterval.String())
	interval, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf(
			"%s is not a duration: write it as 500ms, 1s or 2s", text)
	}
	return min(max(interval, minInterval), maxInterval), nil
}

// pickBackend returns the demo backend or the real one.
func pickBackend(cfg config.Config, opts options) (traffic.Backend, error) {
	if opts.demo {
		return traffic.NewFake(), nil
	}
	return traffic.New(cfg.SudoPrefix())
}
