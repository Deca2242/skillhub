package text_test

import (
	"errors"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// Global removal proposals and TUI summaries show the exact folder and link
// paths, display readable names rather than dir:/parked: prefixes, and escape
// third-party values.
func TestGlobalRemoveTextsPresentExactPathsAndReadableNames(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "dir:.claude/skills/humanizer"},
		PlanID:  "id",
		Reload:  []string{"claude-code", "opencode"},
		Remove: &app.RemovePreview{
			Key:     "humanizer",
			Folder:  "/home/u/.claude/skills/humanizer",
			TreeSHA: "1234567890abcdef",
			Links: []app.LinkChange{
				{Path: "/home/u/.kiro/skills/humanizer", Target: "../../.claude/skills/humanizer", Harnesses: []string{"kiro-cli"}},
			},
		},
	}

	cliOut := text.Proposal(p)
	for _, want := range []string{
		"Plan: Eliminar humanizer\n",
		"Carpeta (activa): /home/u/.claude/skills/humanizer (árbol 1234567890ab)\n",
		"/home/u/.kiro/skills/humanizer → ../../.claude/skills/humanizer",
	} {
		if !strings.Contains(cliOut, want) {
			t.Errorf("CLI Proposal lacks %q:\n%s", want, cliOut)
		}
	}

	s := text.TUIPlanSummary(p, agentNames)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{
		"¿Eliminar humanizer?",
		"/home/u/.claude/skills/humanizer",
		"/home/u/.kiro/skills/humanizer",
		"papelera de skillhub",
		"se deshace con z",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("TUIPlanSummary lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(s.Question, "dir:") {
		t.Errorf("TUIPlanSummary question exposes raw prefix: %q", s.Question)
	}
	if n := len(s.Lines); n < 2 || n > 4 {
		t.Errorf("TUIPlanSummary lines = %d, want 2..4:\n%s", n, all)
	}

	done := text.TUIDone(app.Result{SnapshotID: "snap-1", Proposal: p}, agentNames)
	if !strings.Contains(done, "Listo: humanizer se movió a la papelera de skillhub.") || strings.Contains(done, "dir:") {
		t.Errorf("TUIDone = %q", done)
	}
}

// OFF/ removal preview and TUI summary indicate the hand-disabled OFF/ state.
func TestOFFRemovePreviewAndSummary(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "dir:.agents/skills/OFF/accessibility"},
		PlanID:  "id",
		Reload:  []string{"codex"},
		Remove: &app.RemovePreview{
			Key:     "accessibility",
			Off:     true,
			Folder:  "/home/u/.agents/skills/OFF/accessibility",
			TreeSHA: "abcdef123456",
		},
	}

	cliOut := text.Proposal(p)
	if !strings.Contains(cliOut, "Carpeta (desactivada a mano en OFF/): /home/u/.agents/skills/OFF/accessibility") {
		t.Errorf("CLI Proposal for OFF/:\n%s", cliOut)
	}
	s := text.TUIPlanSummary(p, agentNames)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{"¿Eliminar accessibility?", "OFF/", "/home/u/.agents/skills/OFF/accessibility"} {
		if !strings.Contains(all, want) {
			t.Errorf("TUIPlanSummary for OFF/ lacks %q:\n%s", want, all)
		}
	}
}

// Unsupported tree refusal explains Git clones in both CLI and TUI without
// mentioning CLI commands or internal words.
func TestUnsupportedTreeRefusalExplainsGitClone(t *testing.T) {
	err := &app.RefusalError{Kind: app.ErrUnsupportedTree, Err: errors.New("is a clone")}
	req := app.Request{Action: app.ActionRemove, Key: "dir:.claude/skills/my-clone"}
	cliMsg, ok := text.Refusal(err, req)
	if !ok || !strings.Contains(strings.ToLower(cliMsg), "git") {
		t.Errorf("Refusal(ErrUnsupportedTree) = %q, %v; want mention of git", cliMsg, ok)
	}
	tuiMsg, ok := text.TUIRefusal(err, req)
	if !ok || !strings.Contains(strings.ToLower(tuiMsg), "git") {
		t.Errorf("TUIRefusal(ErrUnsupportedTree) = %q, %v; want mention of git", tuiMsg, ok)
	}
}

func TestGroupRemovePreviewAndSummary(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "dir:.claude/skills/shared-pack", Group: true},
		PlanID:  "id-group",
		Reload:  []string{"claude-code", "opencode"},
		Remove: &app.RemovePreview{
			Key:       "shared-pack",
			SkillName: "shared-pack",
			Group:     true,
			SkillID:   "dir:.claude/skills/shared-pack",
			Folders: []app.RemovedFolder{
				{Path: "/home/u/.agents/skills/shared-pack", TreeSHA: "1111111111111111", InStore: true, Harnesses: []string{"opencode"}},
				{Path: "/home/u/.claude/skills/shared-pack", TreeSHA: "1111111111111111", Harnesses: []string{"claude-code", "opencode"}},
			},
			Links: []app.LinkChange{
				{Path: "/home/u/.kiro/skills/shared-pack-alias", Target: "../../.claude/skills/shared-pack", Harnesses: []string{"kiro-cli"}},
			},
			KeptCopies: []app.KeptCopy{
				{Path: "/home/u/.codex/skills/shared-pack", Name: "shared-pack", Harnesses: []string{"codex"}, Reason: "git"},
			},
			Homonyms: []app.Homonym{
				{Path: "/home/u/.gemini/skills/shared-pack", Name: "shared-pack", Identical: false, Harnesses: []string{"gemini-cli"}},
			},
			Targets:         []string{"claude-code", "kiro-cli"},
			RetainedTargets: []string{"codex", "gemini-cli", "opencode"},
		},
	}

	cliOut := text.Proposal(p)
	for _, want := range []string{
		"Plan: Eliminar por completo shared-pack\n",
		"/home/u/.agents/skills/shared-pack",
		"/home/u/.claude/skills/shared-pack",
		"/home/u/.kiro/skills/shared-pack-alias",
		"/home/u/.codex/skills/shared-pack",
		"git",
		"/home/u/.gemini/skills/shared-pack",
	} {
		if !strings.Contains(cliOut, want) {
			t.Errorf("Proposal lacks %q:\n%s", want, cliOut)
		}
	}

	namer := func(id string) string {
		if id == "kiro-cli" {
			return "Kiro CLI"
		}
		return agentNames(id)
	}
	s := text.TUIPlanSummary(p, namer)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{
		"¿Eliminar por completo shared-pack?",
		"/home/u/.agents/skills/shared-pack",
		"/home/u/.claude/skills/shared-pack",
		"/home/u/.kiro/skills/shared-pack-alias",
		"/home/u/.codex/skills/shared-pack",
		"/home/u/.gemini/skills/shared-pack",
		"Claude Code",
		"Kiro CLI",
		"se deshace",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("TUIPlanSummary lacks %q:\n%s", want, all)
		}
	}
	// opencode has a retained copy, so it must NOT be reported as losing access.
	for _, line := range s.Lines {
		if strings.Contains(line, "Dejará de estar disponible en") && strings.Contains(line, "OpenCode") {
			t.Errorf("TUIPlanSummary wrongly says OpenCode loses access: %q", line)
		}
	}
	if strings.Contains(s.Question, "dir:") {
		t.Errorf("TUIPlanSummary question exposes raw prefix: %q", s.Question)
	}

	done := text.TUIDone(app.Result{SnapshotID: "snap-1", Proposal: p}, namer)
	if !strings.Contains(done, "shared-pack") || !strings.Contains(done, "papelera de skillhub") || strings.Contains(done, "dir:") {
		t.Errorf("TUIDone = %q", done)
	}
}

func TestProjectRemovePreviewAndSummaryWithLinks(t *testing.T) {
	withLinks := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "project:.claude/skills/golang-code-style"},
		PlanID:  "id-proj-links",
		Remove: &app.RemovePreview{
			Key:         "project:.claude/skills/golang-code-style",
			SkillName:   "golang-code-style",
			SkillID:     "project:.claude/skills/golang-code-style",
			Project:     true,
			ProjectRoot: ".claude/skills",
			Folder:      "/tmp/proj/.claude/skills/golang-code-style",
			TreeSHA:     "abcdef1234567890",
			Links: []app.LinkChange{
				{Path: "/tmp/proj/.agents/skills/golang-code-style", Target: "../../.claude/skills/golang-code-style"},
			},
		},
	}

	cliOut := text.Proposal(withLinks)
	for _, want := range []string{
		"/tmp/proj/.claude/skills/golang-code-style",
		"Enlaces que se quitan:",
		"/tmp/proj/.agents/skills/golang-code-style → ../../.claude/skills/golang-code-style",
		"skillhub undo",
	} {
		if !strings.Contains(cliOut, want) {
			t.Errorf("Proposal with project links lacks %q:\n%s", want, cliOut)
		}
	}

	s := text.TUIPlanSummary(withLinks, agentNames)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{
		"¿Eliminar golang-code-style (.claude/skills de este proyecto)?",
		"/tmp/proj/.claude/skills/golang-code-style",
		"/tmp/proj/.agents/skills/golang-code-style",
		"z",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("TUIPlanSummary with project links lacks %q:\n%s", want, all)
		}
	}

	withoutLinks := withLinks
	withoutLinksRemove := *withLinks.Remove
	withoutLinksRemove.Links = nil
	withoutLinks.Remove = &withoutLinksRemove

	plainOut := text.Proposal(withoutLinks)
	if strings.Contains(plainOut, "Enlaces que se quitan") || strings.Contains(plainOut, "enlaces simbólicos del proyecto") {
		t.Errorf("Proposal without project links unexpectedly mentions links:\n%s", plainOut)
	}
	plainSum := text.TUIPlanSummary(withoutLinks, agentNames)
	plainAll := strings.Join(plainSum.Lines, "\n")
	if strings.Contains(plainAll, "enlaces del proyecto") {
		t.Errorf("TUIPlanSummary without project links unexpectedly mentions links:\n%s", plainAll)
	}
}
