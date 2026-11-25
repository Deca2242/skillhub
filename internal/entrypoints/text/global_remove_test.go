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
