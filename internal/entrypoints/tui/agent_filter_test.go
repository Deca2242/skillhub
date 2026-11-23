package tui_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
)

func TestAgentFilterShowsDetectedWithCountsAndDistinguishesUndetected(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	vis, err := a.Visibility(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claudeWant := 0
	for _, ids := range vis {
		if slices.Contains(ids, "claude-code") {
			claudeWant++
		}
	}

	m := openSized(t, a, 100, 35)
	pressRun(m, "g")
	got := screen(m)

	// Detected agent with skills shows its count.
	wantClaude := text.TUIAgentFilterItem("Claude Code", claudeWant)
	if !strings.Contains(got, wantClaude) {
		t.Errorf("agent filter lacks %q:\n%s", wantClaude, got)
	}

	// Detected agent with zero skills appears with 0 and sin skills.
	kiroName := a.AgentName("kiro-cli")
	wantKiro := text.TUIAgentFilterItem(kiroName, 0)
	if !strings.Contains(got, wantKiro) {
		t.Errorf("agent filter lacks zero-skill agent %q:\n%s", wantKiro, got)
	}

	// Known undetected agents are summarized as not detected, not listed one by one.
	if !strings.Contains(got, "no detectado") {
		t.Errorf("agent filter lacks undetected summary:\n%s", got)
	}
	if strings.Contains(got, "Zed") || strings.Contains(got, "Aider") {
		t.Errorf("agent filter lists individual undetected agents:\n%s", got)
	}

	// Moving down past the end stays on the last detected agent, never on the undetected summary.
	for range 40 {
		pressRun(m, "down")
	}
	afterDown := screen(m)
	if !strings.Contains(afterDown, "> ") || strings.Contains(afterDown, "> no detectado") {
		t.Errorf("cursor moved onto undetected summary:\n%s", afterDown)
	}

	// Choosing a zero-skill detected agent filters to 0 rows cleanly.
	moveTo(t, m, kiroName)
	pressRun(m, "enter")
	if m.RowCountForTest() != 0 {
		t.Errorf("Kiro shows %d rows, want 0", m.RowCountForTest())
	}
	if s := screen(m); !strings.Contains(s, text.TUISkillsNoneShown) {
		t.Errorf("empty filtered list lacks empty-state hint:\n%s", s)
	}
}

func TestAgentFilterCursorClampsWhenListShrinks(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := openSized(t, a, 100, 35)
	pressRun(m, "g")

	// Move cursor to the bottom of the agent filter list.
	for range 40 {
		pressRun(m, "down")
	}

	// Shrink the detected agents and visible skills while the filter is open.
	for _, rel := range []string{
		".pi/agent/skills",
		".opencode/skills",
		".kilocode/skills",
		".copilot/skills",
		".kiro/skills",
		".qwen/skills",
		".junie/skills",
		".config/devin/skills",
	} {
		if err := os.RemoveAll(mc.h.Path(rel)); err != nil {
			t.Fatal(err)
		}
	}
	runSyncCmd(m, m.Init())

	// Pressing enter must not panic and must keep a valid cursor inside the list.
	got := screen(m)
	if !strings.Contains(got, "> ") {
		t.Fatalf("cursor disappeared after list shrank:\n%s", got)
	}
	pressRun(m, "enter")
}

func TestGoldenAgentFilter(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			mc := newMachine(t)
			a := mc.newApp()
			m := openSized(t, a, width, 30)
			pressRun(m, "g")
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
	}
}

// Keep tui import verified even if only helpers are used.
var _ = tui.NewModel
