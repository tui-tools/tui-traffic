package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuitraffic "github.com/tui-tools/tui-traffic"
)

// backendName is the name the manifest gives the one program this tool
// drives. Everything else it reads is a file, and a file has no version to
// probe: /proc/net/dev has had the same columns since Linux 2.2.
const backendName = "conntrack"

// probeCompat reads the version of the backend the tool may drive, and
// classifies it against what the manifest declares: below the minimum,
// tested, or merely untested. The result goes in the header through
// ui.CompatFact, and its capability set answers caps.Has("feature") for any
// view that needs a recent backend.
//
// It never fails, and on this tool it usually finds nothing: conntrack is not
// installed on most machines, and the empty result that produces is an answer
// the connections screen already knows how to show. A compatibility probe
// that could stop a tool from starting would be worse than no probe, and one
// that treated a missing optional program as an error would be worse still.
func probeCompat(ctx context.Context, demo bool) []compat.Result {
	// --demo drives a machine that does not exist, so probing the real
	// conntrack would report a version that has nothing to do with what is on
	// screen.
	if demo {
		return nil
	}
	m, err := manifest.Load(tuitraffic.ManifestJSON)
	if err != nil {
		return nil
	}
	results := make([]compat.Result, 0, len(m.Backends))
	for _, backend := range m.Backends {
		results = append(results, compat.Probe(ctx, backend))
	}
	return results
}

// installed keeps the backends that answered with a version, which are the
// ones this machine actually has. It is what the header shows: a version
// badge for a program that is not installed would be noise, and on this tool
// it would be noise on nearly every machine.
func installed(results []compat.Result) []compat.Result {
	kept := make([]compat.Result, 0, len(results))
	for _, result := range results {
		if result.Version != "" {
			kept = append(kept, result)
		}
	}
	return kept
}
