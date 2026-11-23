package text_test

import (
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

func TestSkillShowFormatsAllSectionsAndEscapesThirdPartyText(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	hostile := "\r\x1b[2K\u202e"

	out := text.SkillShow(text.SkillShowData{
		Skill: domain.Skill{
			ID:          "lock:alpha",
			Name:        "alpha" + hostile,
			Aliases:     []string{"a1"},
			Description: "Útil\n" + hostile + "falsa",
			Origin: domain.Origin{
				Kind: domain.OriginLock,
				Lock: &domain.LockOrigin{
					Source:     "acme/repo" + hostile,
					SkillPath:  "skills/alpha/SKILL.md",
					FolderHash: "1234567890abcdef",
					Ref:        "main",
				},
			},
			State: domain.StateUpToDate,
			Locations: []domain.Location{
				{AgentID: domain.StoreAgentID, Kind: domain.LocationReal, Path: "/home/u/.agents/skills/alpha", ContentTreeSHA: "abcdef1234567"},
				{AgentID: "claude-code", Kind: domain.LocationSymlink, Path: "/home/u/.claude/skills/alpha", ContentTreeSHA: "abcdef1234567"},
			},
		},
		SkillhubState: text.TUIStateActive,
		AvailableIn:   "Claude Code y Codex",
		Sightings: []app.Sighting{
			{Harness: "claude-code", Kind: app.SightingViaLink, Entry: "/home/u/.claude/skills/alpha"},
			{Harness: "codex", Kind: app.SightingDirect, Entry: "/home/u/.agents/skills/alpha"},
		},
		AgentName: func(id string) string {
			if id == "claude-code" {
				return "Claude Code"
			}
			if id == "codex" {
				return "Codex"
			}
			return id
		},
		Activity: []app.ActivityEntry{
			{At: now.Add(-10 * time.Minute), Action: domain.ActionApplied, Target: "v2:link", V2: true, Operation: app.OperationView{Action: string(app.ActionLink)}},
			{At: now.Add(-2 * time.Hour), Action: domain.ActionApplied, Target: "lock:alpha", V2: false},
		},
		Now: now,
	})

	for _, want := range []string{
		`Skill: alpha\r\x1b[2K\u202e`,
		"ID: lock:alpha",
		"Alias: a1",
		`Descripción: Útil\n\r\x1b[2K\u202efalsa`,
		"En skillhub: activa",
		"Disponible en: Claude Code y Codex",
		text.TUISkillDetailWhyHeader,
		"Claude Code: por un enlace (/home/u/.claude/skills/alpha)",
		"Codex: directamente, sin enlace (/home/u/.agents/skills/alpha)",
		text.TUIActivityHeader,
		"Añadida a un agente",
		"Actualización aplicada",
		`Repositorio: acme/repo\r\x1b[2K\u202e`,
		"Ref: main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SkillShow output lacks %q:\n%s", want, out)
		}
	}
	for _, raw := range []string{"\r", "\x1b", "\u202e"} {
		if strings.Contains(out, raw) {
			t.Errorf("SkillShow output contains raw %q:\n%s", raw, out)
		}
	}
}
