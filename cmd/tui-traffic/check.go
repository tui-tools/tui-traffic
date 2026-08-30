package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-traffic/internal/traffic"
)

// checkTimeout bounds the whole read. The interval is added to it, because
// two samples with the interval between them is the shape of the thing: a
// check at --interval 2s has two seconds of waiting in it that are not a
// hung machine.
const checkTimeout = 30 * time.Second

// checkReport is what --check prints: one sample of all three screens.
//
// It is a report of the read path and nothing else. There is no write path in
// this tool for it to avoid, which is what makes it safe to run from cron
// against a production machine — and the reason the smoke test asserts the
// machine is untouched afterwards anyway.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`
	// Interval is the window the rates were measured over, written the way a
	// person would: "1s".
	Interval string `json:"interval"`
	// Sources is where each screen's numbers came from on this machine, and
	// whether the kernel counts bytes per connection. It is the first thing
	// to read: a connections total from the socket fallback is a different
	// number from one out of the conntrack table.
	Sources traffic.Sources `json:"sources"`

	Interfaces  []traffic.Rate      `json:"interfaces"`
	Connections traffic.Connections `json:"connections"`
	Sockets     traffic.Sockets     `json:"sockets"`

	// Compat is what the version probe found, one entry per backend the
	// manifest declares. It is reported rather than asserted: a machine
	// without conntrack is an ordinary machine, not a failed read.
	Compat []compat.Result `json:"compat"`
}

// runCheck takes one sample of every screen and prints it as JSON.
//
// It returns an error only when the tool itself could not read, which is why
// the exit code is not a verdict about the network. A machine with a saturated
// link and no conntrack is a successful run of tui-traffic: the numbers are in
// the JSON, and a script reads them from there rather than from the exit code.
//
// The two samples are what makes it slow. A rate is a difference over time and
// there is no way to have one from a single read: the first sample is taken,
// the interval passes, the second is taken, and the difference is the answer.
// A tool that printed a rate instantly would be printing an average since boot.
func runCheck(backend traffic.Backend, backends []compat.Result,
	interval time.Duration, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		checkTimeout+interval)
	defer cancel()

	first, err := backend.Sample(ctx)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}

	second, err := backend.Sample(ctx)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}

	connections, err := backend.Connections(ctx)
	if err != nil {
		return fmt.Errorf("%s could not summarise connections: %w",
			backend.Name(), err)
	}
	sockets, err := backend.Sockets(ctx)
	if err != nil {
		return fmt.Errorf("%s could not read the socket tables: %w",
			backend.Name(), err)
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(checkReport{
		Tool:        toolName,
		Version:     version,
		Backend:     backend.Name(),
		Describe:    backend.Describe(),
		Interval:    interval.String(),
		Sources:     backend.Sources(ctx),
		Interfaces:  traffic.RatesBetween(first, second),
		Connections: connections,
		Sockets:     sockets,
		Compat:      backends,
	})
}
