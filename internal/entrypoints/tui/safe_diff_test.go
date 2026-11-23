package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
)

// hostile is third-party text built to act on the terminal: \r returns to
// the start of the line, ESC[2K erases it and U+202E, the right-to-left
// override, reverses what follows. hostileShown is how it must print (A28).
var (
	rlo          = string(rune(0x202e))
	hostile      = "\r\x1b[2K" + rlo
	hostileShown = `\r\x1b[2K\` + "u202e"
)

// assertNothingActs fails when out holds a character of hostile as it is.
// The styles of the TUI use ESC, so only the erase sequence counts.
func assertNothingActs(t *testing.T, out string) {
	t.Helper()
	for _, raw := range []string{"\r", "\x1b[2K", rlo} {
		if strings.Contains(out, raw) {
			t.Errorf("the screen prints %q as it is:\n%q", raw, out)
		}
	}
}

func TestPresentDiffEscapesThirdPartyText(t *testing.T) {
	c := domain.UpdateCandidate{
		ID: "cand-1", Target: "lock:evil" + hostile + "name", Labels: []domain.Label{domain.LabelTextOnly},
	}
	r := app.ReviewDiff{Installed: []app.FileDiff{
		{Path: "a" + hostile + ".md", Change: domain.ChangeModified,
			Unified: "--- a/a" + hostile + ".md\n+++ b/a" + hostile + ".md\n@@ -1 +1 @@\n-uno" + hostile + "\n+dos ñandú 🎉\tcon tab\n"},
		{Path: "logo" + hostile + ".png", Change: domain.ChangeAdded, Binary: true},
		{Path: "run" + hostile + ".sh", Change: domain.ChangeModified},
	}}

	got := tui.PresentDiff(c, r, tui.DiffViewUser)

	for _, want := range []string{
		"Destino: lock:evil" + hostileShown + "name\n",
		"  modificado  a" + hostileShown + ".md\n",
		"  añadido  logo" + hostileShown + ".png\n",
		"--- a/a" + hostileShown + ".md\n+++ b/a" + hostileShown + ".md\n",
		"-uno" + hostileShown + "\n+dos ñandú 🎉\tcon tab\n",
		"logo" + hostileShown + ".png: ",
		"run" + hostileShown + ".sh: ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PresentDiff lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\r\x1b"+rlo) {
		t.Errorf("PresentDiff prints a character as it is:\n%q", got)
	}
}

// A file name with \n and \t stays on its header line of the diff, escaped
// like in the list of files: it cannot add lines of its own to the review.
func TestPresentDiffKeepsAFileNameOnItsDiffHeader(t *testing.T) {
	path := "nl\nname\tx.md"
	r := app.ReviewDiff{Installed: []app.FileDiff{{
		Path: path, Change: domain.ChangeRemoved,
		Unified: "--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +0,0 @@\n-x\n",
	}}}

	got := tui.PresentDiff(domain.UpdateCandidate{ID: "cand-1", Target: "lock:alpha"}, r, tui.DiffViewUser)

	name := `nl\nname\tx.md`
	for _, want := range []string{
		"  eliminado  " + name + "\n",
		"\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1 +0,0 @@\n-x\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PresentDiff lacks %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "\nname\t") {
		t.Errorf("the file name starts a line of its own:\n%q", got)
	}
}

// hostileUpdate installs alpha with a file whose name and content are
// hostile and publishes a version without that file and with a hostile
// description: its review holds a hostile file name, diff line and
// description, next to ordinary text.
func (f *fixture) hostileUpdate() {
	f.t.Helper()
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": {
		"SKILL.md":                "---\nname: alpha\ndescription: v1\n---\nbody v1\n",
		"notas" + hostile + ".md": "línea" + hostile + "oculta\n",
	}})
	f.publish("alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\ndescription: Útil" + hostile + "falsa\n---\nbody v2: ñandú 🎉\n",
	})
}

// The diff of Actualizar, the screen the user approves from, shows the
// hostile file name, diff line and description escaped, at 80 and 100
// columns.
func TestGoldenSafe(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("Diff%d", width), func(t *testing.T) {
			f := newFixture(t)
			f.hostileUpdate()
			a := f.newApp()
			if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
				t.Fatalf("Check: %v", err)
			}
			m := openSized(t, a, width, 40)
			pressRun(m, "2")
			pressRun(m, "enter")

			raw := m.View().Content
			assertNothingActs(t, raw)
			out := screen(m)
			name := "notas" + hostileShown + ".md"
			for _, want := range []string{
				"eliminado  " + name,
				"--- a/" + name,
				"+++ b/" + name,
				"-línea" + hostileShown + "oculta",
				"+description: Útil" + hostileShown + "falsa",
				"+body v2: ñandú 🎉",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the diff lacks %q:\n%s", want, out)
				}
			}
			teatest.RequireEqualOutput(t, []byte(out))
		})
	}
}
