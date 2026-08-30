package main

import (
	"context"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuitraffic "github.com/tui-tools/tui-traffic"
)

// The embedded manifest is what the header reads and what the compatibility
// section of the README is generated from, so a malformed backends block
// fails here rather than on somebody's machine.
func TestEmbeddedManifestDeclaresItsBackend(t *testing.T) {
	m, err := manifest.Load(tuitraffic.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded tool.json does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Errorf("manifest name = %q, want %q", m.Name, toolName)
	}
	backend, ok := m.Backend(backendName)
	if !ok {
		t.Fatalf("no %s backend in the manifest", backendName)
	}
	if len(backend.VersionCommand) == 0 {
		t.Error("the backend declares no version command")
	}
}

func TestProbeCompatSkipsDemo(t *testing.T) {
	if got := probeCompat(context.Background(), true); len(got) != 0 {
		t.Errorf("demo probe = %+v, want nothing", got)
	}
}

// The probe runs against whatever this machine has, and on this tool that is
// usually nothing: conntrack is not installed on most machines. It must
// produce a Result either way — that is the promise: a compatibility probe
// never fails a tool, and an optional program that is absent is an answer.
func TestProbeCompatOnThisMachine(t *testing.T) {
	got := probeCompat(context.Background(), false)
	if len(got) != 1 {
		t.Fatalf("got %d results, want one per declared backend", len(got))
	}
	if got[0].Backend != backendName {
		t.Errorf("backend = %q, want %q", got[0].Backend, backendName)
	}
	t.Logf("this machine: %s %q (%s)", got[0].Backend, got[0].Version, got[0].Status)
}

// installed is what the header draws, and a badge for a program that is not
// on the machine would be noise on nearly every screen this tool shows.
func TestInstalledKeepsOnlyWhatAnswered(t *testing.T) {
	results := []compat.Result{
		{Backend: "conntrack", Version: "1.4.8"},
		{Backend: "absent", Detail: notAvailable},
	}
	kept := installed(results)
	if len(kept) != 1 || kept[0].Backend != "conntrack" {
		t.Errorf("installed = %+v, want only the one with a version", kept)
	}
}
