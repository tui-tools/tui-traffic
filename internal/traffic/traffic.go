// Package traffic is the part of tui-traffic that is about its own subject:
// what the network on this machine is doing right now, and where each of
// those numbers came from.
//
// It is a read-only subject, and the package is shaped by that. There is no
// action table, no command builder and no Run: the Backend interface has read
// methods and nothing else, so there is no code path in the tool that could
// change anything even by mistake. What replaces the preview-and-confirm
// contract here is a different promise, and it runs through every type below:
// a number is either measured or absent, never estimated, and the source it
// was measured from travels with it.
package traffic

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// Counters are one direction of an interface's counters, as the kernel keeps
// them: monotonic totals since the interface came up, not rates.
type Counters struct {
	Bytes     uint64 `json:"bytes"`
	Packets   uint64 `json:"packets"`
	Errors    uint64 `json:"errors"`
	Dropped   uint64 `json:"dropped"`
	Multicast uint64 `json:"multicast,omitempty"`
}

// Interface is one interface's counters at one moment.
type Interface struct {
	Name string   `json:"name"`
	RX   Counters `json:"rx"`
	TX   Counters `json:"tx"`
}

// Link is what the kernel says about an interface's physical state. Speed is
// in megabits per second and is zero where there is nothing to report, which
// is every virtual interface and any physical one that is down.
type Link struct {
	State   string `json:"state"`
	Carrier bool   `json:"carrier"`
	Speed   int    `json:"speed,omitempty"`
}

// Sample is one reading of every interface's counters, with the moment it was
// taken. A sample on its own says nothing about throughput: two of them, and
// the time between, are what a rate is made of.
type Sample struct {
	At         time.Time       `json:"at"`
	Interfaces []Interface     `json:"interfaces"`
	Links      map[string]Link `json:"links,omitempty"`
}

// Rate is one interface's throughput between two samples, alongside the
// cumulative counters at the later of the two.
type Rate struct {
	Name string `json:"name"`
	Link Link   `json:"link"`

	RXBytesPerSecond   float64 `json:"rxBytesPerSecond"`
	TXBytesPerSecond   float64 `json:"txBytesPerSecond"`
	RXPacketsPerSecond float64 `json:"rxPacketsPerSecond"`
	TXPacketsPerSecond float64 `json:"txPacketsPerSecond"`

	// Total is the interface's counters at the later sample, which is what
	// the error and drop columns show: those are worth reading as totals
	// rather than as a rate, because one drop an hour ago still matters.
	Total Interface `json:"total"`

	// Window is the time between the two samples the rate was computed over.
	// A window that is not the one the user asked for is the honest
	// explanation for a number that looks wrong, so it travels with it.
	Window time.Duration `json:"windowMillis"`

	// Reset reports that a counter went backwards between the samples, which
	// happens when an interface is recreated or a 32-bit counter wraps. The
	// rate is reported as zero rather than as the enormous number the
	// subtraction would give.
	Reset bool `json:"reset,omitempty"`
}

// BytesPerSecond is the interface's throughput in both directions, which is
// what the list is sorted by: the reader is looking for the busy one.
func (r Rate) BytesPerSecond() float64 {
	return r.RXBytesPerSecond + r.TXBytesPerSecond
}

// Up reports whether the interface is carrying traffic. The kernel reports
// "unknown" for interfaces that have no notion of a carrier — loopback, and
// most tunnels — and those are up as far as anybody using them is concerned.
func (r Rate) Up() bool {
	return r.Link.State == "up" || r.Link.State == "unknown"
}

// RatesBetween computes the throughput of every interface present in both
// samples. An interface that appeared between them has no rate to report yet
// and is skipped: the alternative is to divide its lifetime total by the
// window, which would show a brand new interface as the busiest on the
// machine.
//
// The result is sorted busiest first, then by name, because the reason
// somebody opened this screen is to find out which interface is moving data.
func RatesBetween(prev, cur Sample) []Rate {
	window := cur.At.Sub(prev.At)
	if window <= 0 {
		return nil
	}
	seconds := window.Seconds()

	before := make(map[string]Interface, len(prev.Interfaces))
	for _, item := range prev.Interfaces {
		before[item.Name] = item
	}

	rates := make([]Rate, 0, len(cur.Interfaces))
	for _, now := range cur.Interfaces {
		was, ok := before[now.Name]
		if !ok {
			continue
		}
		rate := Rate{
			Name:   now.Name,
			Link:   cur.Links[now.Name],
			Total:  now,
			Window: window,
		}
		rxBytes, rxReset := delta(was.RX.Bytes, now.RX.Bytes)
		txBytes, txReset := delta(was.TX.Bytes, now.TX.Bytes)
		rxPackets, rxPacketReset := delta(was.RX.Packets, now.RX.Packets)
		txPackets, txPacketReset := delta(was.TX.Packets, now.TX.Packets)

		rate.RXBytesPerSecond = float64(rxBytes) / seconds
		rate.TXBytesPerSecond = float64(txBytes) / seconds
		rate.RXPacketsPerSecond = float64(rxPackets) / seconds
		rate.TXPacketsPerSecond = float64(txPackets) / seconds
		rate.Reset = rxReset || txReset || rxPacketReset || txPacketReset
		rates = append(rates, rate)
	}
	SortRates(rates)
	return rates
}

// delta subtracts two readings of a monotonic counter. A counter that went
// backwards was reset or wrapped, and there is no honest rate to report for
// that window: zero, flagged, is the answer.
func delta(was, now uint64) (uint64, bool) {
	if now < was {
		return 0, true
	}
	return now - was, false
}

// SortRates puts the busiest interface first, then falls back to the name so
// the order is stable while nothing is moving.
func SortRates(rates []Rate) {
	slices.SortStableFunc(rates, func(a, b Rate) int {
		if c := cmp.Compare(b.BytesPerSecond(), a.BytesPerSecond()); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

// Sources names where each screen's numbers come from on this machine, and
// whether the kernel is counting bytes per connection. It is what --report
// and the --check JSON carry, and it is deliberately cheap to answer: it
// looks for a binary and reads one sysctl, so it works for a user who cannot
// escalate and on a machine where nothing can be read at all.
type Sources struct {
	Interfaces  string     `json:"interfaces"`
	Connections string     `json:"connections"`
	Sockets     string     `json:"sockets"`
	Accounting  Accounting `json:"conntrackAccounting"`
}

// Backend is the boundary between the UI and the machine, and every method on
// it is a read. There is no Build and no Run, because tui-traffic changes
// nothing: the interface is the enforcement, not a convention.
type Backend interface {
	// Name identifies the backend ("proc", "demo").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Sources names where the numbers will come from, without taking a
	// sample. It escalates for nothing and works on a machine where the
	// reads themselves would fail.
	Sources(ctx context.Context) Sources

	// Sample reads every interface's counters once.
	Sample(ctx context.Context) (Sample, error)
	// Connections summarises the connection tracking table, or says what it
	// counted instead.
	Connections(ctx context.Context) (Connections, error)
	// Sockets reads the socket tables.
	Sockets(ctx context.Context) (Sockets, error)
}
