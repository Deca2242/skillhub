package app_test

import (
	"errors"
	"slices"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/app"
	"skillhub/internal/data"
)

func TestDetectedAgentsIncludesZeroSkillHarnessesAndUndetected(t *testing.T) {
	m := newMachine(t)
	before := m.hash()

	got, err := m.app.DetectedAgents(bg)
	must(t, err)

	// graphtest.Machine creates broken links in kiro, qwen, junie and devin,
	// so those harnesses are detected even though they see zero skills.
	for _, want := range []string{"claude-code", "codex", "kiro-cli", "qwen-code", "junie", "devin"} {
		if !slices.Contains(got.Detected, want) {
			t.Errorf("DetectedAgents.Detected lacks %q: %v", want, got.Detected)
		}
		if slices.Contains(got.Undetected, want) {
			t.Errorf("DetectedAgents.Undetected includes detected harness %q", want)
		}
	}

	// Diagnostic profiles are never listed as harnesses.
	if slices.Contains(got.Detected, "claude-omni") || slices.Contains(got.Undetected, "claude-omni") {
		t.Errorf("DetectedAgents included diagnostic profile claude-omni: %+v", got)
	}

	// Detected and Undetected partition the known harnesses table.
	harnesses, err := data.LoadHarnesses(m.p, fsadapter.NewFileOps(m.p.Guard()))
	must(t, err)
	if len(got.Detected)+len(got.Undetected) != len(harnesses) {
		t.Errorf("len(Detected)+len(Undetected) = %d+%d, want %d", len(got.Detected), len(got.Undetected), len(harnesses))
	}

	if m.hash() != before {
		t.Fatal("DetectedAgents wrote something")
	}
}

func TestDetectedAgentsNeedsTheVersion2Ports(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.DetectedAgents(bg); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("DetectedAgents error = %v, want ErrMissingDependency", err)
	}
}
