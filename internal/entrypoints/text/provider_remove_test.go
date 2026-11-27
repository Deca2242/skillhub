package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

func TestProviderRemoveWarningPresentAndSanitized(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		wantProvider string
		wantClean    string
		group        bool
	}{
		{
			name:         "gentle-ai single",
			provider:     app.ProviderGentleAI,
			wantProvider: "gentle-ai",
		},
		{
			name:         "claude-plugin group",
			provider:     app.ProviderClaudePlugin,
			wantProvider: "Claude Code (plugin)",
			group:        true,
		},
		{
			name:         "claude-synced single",
			provider:     app.ProviderClaudeSynced,
			wantProvider: "Claude Code (skills sincronizadas)",
		},
		{
			name:         "cursor single",
			provider:     app.ProviderCursor,
			wantProvider: "Cursor",
		},
		{
			name:         "codex single",
			provider:     app.ProviderCodex,
			wantProvider: "Codex",
		},
		{
			name:         "unknown sanitized",
			provider:     "custom\x1b[31m\nprovider",
			wantProvider: "custom\\x1b[31m\\nprovider",
			wantClean:    "\x1b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := app.Request{
				Action:        app.ActionRemove,
				Key:           "dir:.codex/skills/.system/skill-creator",
				Group:         tc.group,
				AllowProvider: true,
			}
			prev := &app.RemovePreview{
				Key:       "skill-creator",
				SkillName: "skill-creator",
				Group:     tc.group,
				Provider:  tc.provider,
			}
			if tc.group {
				prev.Folders = []app.RemovedFolder{
					{Path: "/home/u/.codex/skills/.system/skill-creator", TreeSHA: "1234567890abcdef", Harnesses: []string{"codex"}},
				}
				prev.Targets = []string{"codex"}
			} else {
				prev.Folder = "/home/u/.codex/skills/.system/skill-creator"
				prev.TreeSHA = "1234567890abcdef"
			}
			p := app.Proposal{
				Request: req,
				PlanID:  "plan-provider-1",
				Reload:  []string{"codex"},
				Remove:  prev,
			}

			cliOut := text.Proposal(p)
			for _, want := range []string{
				tc.wantProvider,
				"solo elimina los archivos del disco",
				"manifiesto del proveedor",
				"volver a crearla",
				"plugin incompleto",
				"snapshot",
				"skillhub undo",
			} {
				if !strings.Contains(cliOut, want) {
					t.Errorf("CLI Proposal lacks %q:\n%s", want, cliOut)
				}
			}
			if tc.wantClean != "" && strings.Contains(cliOut, tc.wantClean) {
				t.Errorf("CLI Proposal leaked raw control sequence %q:\n%s", tc.wantClean, cliOut)
			}

			s := text.TUIPlanSummary(p, agentNames)
			all := s.Question + "\n" + strings.Join(s.Lines, "\n")
			for _, want := range []string{
				tc.wantProvider,
				"solo elimina los archivos del disco",
				"manifiesto del proveedor",
				"volver a crearla",
				"plugin incompleto",
				"snapshot",
				"skillhub undo",
			} {
				if !strings.Contains(all, want) {
					t.Errorf("TUIPlanSummary lacks %q:\n%s", want, all)
				}
			}
			if tc.wantClean != "" && strings.Contains(all, tc.wantClean) {
				t.Errorf("TUIPlanSummary leaked raw control sequence %q:\n%s", tc.wantClean, all)
			}
		})
	}
}

func TestNonProviderRemoveOmitsProviderWarning(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{Action: app.ActionRemove, Key: "dir:.claude/skills/humanizer"},
		PlanID:  "plan-plain",
		Remove: &app.RemovePreview{
			Key:     "humanizer",
			Folder:  "/home/u/.claude/skills/humanizer",
			TreeSHA: "1234567890abcdef",
		},
	}

	cliOut := text.Proposal(p)
	s := text.TUIPlanSummary(p, agentNames)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")

	for _, forbidden := range []string{"manifiesto del proveedor", "Aviso de proveedor", "plugin incompleto"} {
		if strings.Contains(cliOut, forbidden) {
			t.Errorf("non-provider CLI Proposal unexpectedly contains %q:\n%s", forbidden, cliOut)
		}
		if strings.Contains(all, forbidden) {
			t.Errorf("non-provider TUIPlanSummary unexpectedly contains %q:\n%s", forbidden, all)
		}
	}
}

func TestProviderCloneRemovePreviewAndStepFormatting(t *testing.T) {
	p := app.Proposal{
		Request: app.Request{
			Action:        app.ActionRemove,
			Key:           "git:.claude/skills/andrej-karpathy-skills/skills/karpathy-guidelines",
			AllowProvider: true,
		},
		PlanID: "plan-clone-1",
		Reload: []string{"claude"},
		Steps: []app.Step{
			{Kind: "removeClone", Path: "/home/u/.claude/skills/andrej-karpathy-skills"},
		},
		Remove: &app.RemovePreview{
			Key:         "git:.claude/skills/andrej-karpathy-skills/skills/karpathy-guidelines",
			SkillName:   "karpathy-guidelines",
			Folder:      "/home/u/.claude/skills/andrej-karpathy-skills",
			TreeSHA:     "abcdef1234567890",
			Clone:       true,
			CloneSkills: []string{"karpathy-guidelines", "second\x1b[31m-skill"},
			Provider:    app.ProviderClaudePlugin,
		},
	}

	cliOut := text.Proposal(p)
	for _, want := range []string{
		"Aviso de proveedor (Claude Code (plugin))",
		"Carpeta (clon completo): /home/u/.claude/skills/andrej-karpathy-skills",
		"Skills del clon que se eliminan juntas: karpathy-guidelines, second\\x1b[31m-skill",
		"borrar el clon /home/u/.claude/skills/andrej-karpathy-skills (queda en el snapshot)",
	} {
		if !strings.Contains(cliOut, want) {
			t.Errorf("CLI Proposal lacks %q:\n%s", want, cliOut)
		}
	}
	if strings.Contains(cliOut, "\x1b") {
		t.Errorf("CLI Proposal leaked raw escape sequence:\n%s", cliOut)
	}

	s := text.TUIPlanSummary(p, agentNames)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{
		"¿Eliminar karpathy-guidelines?",
		"Carpeta (clon completo): /home/u/.claude/skills/andrej-karpathy-skills",
		"Skills del clon que se eliminan juntas: karpathy-guidelines y second\\x1b[31m-skill",
		"Aviso de proveedor (Claude Code (plugin))",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("TUIPlanSummary lacks %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, "\x1b") {
		t.Errorf("TUIPlanSummary leaked raw escape sequence:\n%s", all)
	}

	undoStep := text.Step(app.Step{Kind: "restoreClone", Path: "/home/u/.claude/skills/andrej-karpathy-skills"})
	if undoStep != "restaurar el clon /home/u/.claude/skills/andrej-karpathy-skills desde el snapshot" {
		t.Errorf("restoreClone step = %q", undoStep)
	}
}
