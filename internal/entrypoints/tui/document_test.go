package tui_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport/fakes"
)

func TestSkillDocumentViewInTUI(t *testing.T) {
	f := newFixture(t)
	longBody := strings.Repeat("Linea larga de ejemplo para truncar.\n", 1100)
	f.installLock([]string{"alpha", "nodesc", "bigdoc", "evil"}, map[string]map[string]string{
		"alpha": {
			"SKILL.md":       "---\nname: alpha\ndescription: Skill limpia\nmetadata:\n  author: acme\n---\n\n# Alpha\n\nLinea 1\tcol\nLinea 2\n",
			"README.md":      "# Readme\n",
			"scripts/run.sh": "#!/bin/sh\n",
		},
		"nodesc": {
			"SKILL.md": "---\nname: nodesc\n---\n\nSin descripcion.\n",
		},
		"bigdoc": {
			"SKILL.md": "---\nname: bigdoc\ndescription: Muy grande\n---\n\n" + longBody,
		},
		"evil": {
			"SKILL.md": "---\nname: evil\ndescription: desc\\u202ebidi\n---\n\nCuerpo\x1b[31mrojo\x1b[0m\r\nSiguiente linea\u202ebidi\n",
		},
	})

	t.Run("opens from detail, shows frontmatter and companions, and returns without changing skill", func(t *testing.T) {
		m := openSized(t, f.newApp(), 100, 40)
		selectSkill(t, m, "alpha")

		pressRun(m, "enter")
		detailBefore := screen(m)
		if !strings.Contains(detailBefore, "Skill: alpha") {
			t.Fatalf("expected skill detail screen:\n%s", detailBefore)
		}

		pressRun(m, "v")
		docScreen := screen(m)
		for _, want := range []string{
			text.TUIDocumentTitle("alpha"),
			"Nombre: alpha",
			"Descripción: Skill limpia",
			"Autor: acme",
			"Archivos acompañantes (2):",
			"README.md",
			"scripts/",
			"# Alpha",
			"Linea 1",
		} {
			if !strings.Contains(docScreen, want) {
				t.Errorf("document screen lacks %q:\n%s", want, docScreen)
			}
		}

		pressRun(m, "esc")
		detailAfter := screen(m)
		if detailAfter != detailBefore {
			t.Errorf("returning from document changed skill detail:\nbefore:\n%s\nafter:\n%s", detailBefore, detailAfter)
		}

		pressRun(m, "esc")
		skillsScreen := screen(m)
		if !strings.Contains(skillsScreen, "Skills (4)") {
			t.Errorf("esc from detail did not return to Skills:\n%s", skillsScreen)
		}
	})

	t.Run("missing description states absence instead of repeating skill name", func(t *testing.T) {
		m := openSized(t, f.newApp(), 100, 40)
		selectSkill(t, m, "nodesc")
		pressRun(m, "v")
		got := screen(m)
		if !strings.Contains(got, text.TUIDocumentNoDescription) {
			t.Errorf("expected %q:\n%s", text.TUIDocumentNoDescription, got)
		}
		pressRun(m, "esc")
		if got := screen(m); !strings.Contains(got, "Skills (4)") {
			t.Errorf("esc from direct document view did not return to Skills:\n%s", got)
		}
	})

	t.Run("oversized document shows truncation notice", func(t *testing.T) {
		m := openSized(t, f.newApp(), 100, 35)
		selectSkill(t, m, "bigdoc")
		pressRun(m, "v")
		got := collectScrolledDocScreen(m)
		if !strings.Contains(got, "Contenido truncado:") {
			t.Errorf("expected truncation notice:\n%s", got)
		}
	})

	t.Run("control sequences and bidi marks are escaped while keeping line breaks", func(t *testing.T) {
		m := openSized(t, f.newApp(), 100, 35)
		selectSkill(t, m, "evil")
		pressRun(m, "v")
		got := screen(m)
		if strings.ContainsRune(got, '\r') || strings.ContainsRune(got, '\u202e') {
			t.Fatalf("document screen leaked raw control/bidi rune:\n%q", got)
		}
		for _, want := range []string{`\x1b[31mrojo\x1b[0m\r`, `\u202ebidi`} {
			if !strings.Contains(got, want) {
				t.Errorf("document screen lacks escaped %q:\n%s", want, got)
			}
		}
		if !strings.Contains(got, "Cuerpo") || !strings.Contains(got, "Siguiente linea") {
			t.Errorf("document screen lost line breaks:\n%s", got)
		}
	})

	t.Run("detail view advertises v hint at 80x24 and preserves back behavior", func(t *testing.T) {
		m := openSized(t, f.newApp(), 80, 24)
		selectSkill(t, m, "alpha")
		pressRun(m, "enter")
		detail := screen(m)
		if !strings.Contains(detail, text.TUIKeysSkillDetail) {
			t.Fatalf("detail view at 80x24 lacks key hint %q:\n%s", text.TUIKeysSkillDetail, detail)
		}
		pressRun(m, "v")
		if doc := screen(m); !strings.Contains(doc, text.TUIDocumentTitle("alpha")) {
			t.Fatalf("v from detail did not open document view:\n%s", doc)
		}
		pressRun(m, "esc")
		if back := screen(m); !strings.Contains(back, "Skill: alpha") {
			t.Fatalf("esc from document did not return to detail:\n%s", back)
		}
		pressRun(m, "esc")
		if skills := screen(m); !strings.Contains(skills, "Skills (4)") {
			t.Fatalf("esc from detail did not return to Skills:\n%s", skills)
		}
	})

	t.Run("failing read escapes control and bidi in status and does not open document view", func(t *testing.T) {
		ff := newFixture(t)
		ff.installLock([]string{"alpha"}, map[string]map[string]string{
			"alpha": {"SKILL.md": "---\nname: alpha\n---\n# Alpha\n"},
		})
		p := ff.home.Paths()
		var failDoc atomic.Bool
		files := &fakes.FailingFileOps{
			Faults: fakes.Faults{
				Fail: func(c fakes.Call) error {
					if failDoc.Load() && c.Op == "ReadFile" && strings.HasSuffix(c.Args[0], "SKILL.md") {
						return fmt.Errorf("boom\x1b[31m\nfake\u202e")
					}
					return nil
				},
			},
			Inner: fsadapter.NewFileOps(p.Guard()),
		}
		a, err := app.New(app.Deps{
			Paths:      p,
			Agents:     []domain.AgentProfile{{ID: "claude-code", GlobalDir: ".claude/skills"}},
			Files:      files,
			Dirs:       fsadapter.NewScanner(p.Guard()),
			Lock:       skillslock.New(p, files),
			State:      statefile.New(p),
			Settings:   settings.New(p),
			GitHub:     ff.gh,
			Git:        ff.git,
			Notifier:   ff.notifier,
			Background: ff.bg,
			Clock:      ff.clock,
		})
		if err != nil {
			t.Fatalf("app.New: %v", err)
		}

		m := openSized(t, a, 100, 30)
		selectSkill(t, m, "alpha")
		failDoc.Store(true)
		pressRun(m, "v")
		got := screen(m)

		if strings.ContainsRune(got, '\u202e') || strings.Contains(got, "\x1b[31m\nfake") {
			t.Fatalf("status leaked raw control/bidi sequence:\n%q", got)
		}
		if !strings.Contains(got, `boom\x1b[31m\nfake\u202e`) {
			t.Errorf("status lacks escaped single-line error:\n%s", got)
		}
		if strings.Contains(got, text.TUIDocumentTitle("alpha")) {
			t.Errorf("document view opened despite failing read:\n%s", got)
		}
		if !strings.Contains(got, "Skills (1)") {
			t.Errorf("expected to remain on Skills screen after failing read:\n%s", got)
		}
	})
}

func TestGoldenDocument(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
	}{
		{name: "Document80", width: 80},
		{name: "Document100", width: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.installLock([]string{"alpha"}, map[string]map[string]string{
				"alpha": {
					"SKILL.md":       "---\nname: alpha\ndescription: Skill de uso local\nmetadata:\n  author: acme\n---\n\n# Alpha\n\nInstrucciones locales.\n",
					"README.md":      "# Readme\n",
					"scripts/run.sh": "#!/bin/sh\n",
				},
			})
			m := openSized(t, f.newApp(), tc.width, 30)
			selectSkill(t, m, "alpha")
			pressRun(m, "v")
			teatest.RequireEqualOutput(t, []byte(normalizeDocPaths(screen(m), f.home.Paths().StoreDir())))
		})
	}
}

func normalizeDocPaths(s, storeDir string) string {
	_ = storeDir
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inRuta := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(l, "│"), "│"))
		if strings.HasPrefix(trimmed, "Ruta: ") {
			out = append(out, "│  Ruta: <STORE>/alpha")
			if !strings.HasSuffix(trimmed, "/alpha") {
				inRuta = true
			}
			continue
		}
		if inRuta {
			if strings.HasSuffix(trimmed, "/alpha") || trimmed == "" {
				inRuta = false
			}
			if trimmed != "" {
				continue
			}
		}
		l = strings.TrimRight(l, " ")
		l = strings.TrimSuffix(l, "│")
		l = strings.TrimRight(l, " ")
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func collectScrolledDocScreen(m *tui.Model) string {
	var b strings.Builder
	b.WriteString(screen(m))
	pressRun(m, "end")
	b.WriteString("\n")
	b.WriteString(screen(m))
	return b.String()
}

var _ = app.SkillDocumentMaxBytes
