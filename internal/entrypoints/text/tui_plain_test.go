package text_test

import (
	"regexp"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
)

// agentNames is a readable name per harness id, as the app gives them.
func agentNames(id string) string {
	return map[string]string{"claude-code": "Claude Code", "opencode": "OpenCode", "codex": "Codex"}[id]
}

// internal are the words the plain summary never uses (S13).
var internal = regexp.MustCompile(`(?i)\b(lock|harness|destinos?|canónica|aparcada)\b`)

// Every plan starts with a question and two to four plain lines that name
// the agents by their readable name and never use the internal words.
func TestTUIPlanSummaryIsPlain(t *testing.T) {
	reload := []string{"claude-code", "opencode"}
	tests := []struct {
		name string
		p    app.Proposal
		want []string
	}{
		{"disable", app.Proposal{Request: app.Request{Action: app.ActionDisable, Key: "agent-browser"}, Reload: reload},
			[]string{"¿Desactivar agent-browser?", "Dejará de estar disponible en Claude Code y OpenCode.",
				"No se borra nada: la puedes volver a activar con [a].", "Después, recarga las skills en esos agentes."}},
		{"enable", app.Proposal{Request: app.Request{Action: app.ActionEnable, Key: "agent-browser"}, Reload: reload},
			[]string{"¿Activar agent-browser?", "Volverá a estar disponible en Claude Code y OpenCode.", "z enseguida"}},
		{"remove", app.Proposal{Request: app.Request{Action: app.ActionRemove, Key: "grill-me"}, Reload: reload,
			Remove: &app.RemovePreview{Key: "grill-me", Modified: "yes"}},
			[]string{"¿Eliminar grill-me?", "papelera de skillhub", "se deshace con z", "Tus cambios en ella también"}},
		{"unlink", app.Proposal{Request: app.Request{Action: app.ActionUnlink, Key: "archify", Harness: "claude-code"},
			Reload: []string{"claude-code"}, Toggle: &app.TogglePreview{}},
			[]string{"¿Quitar archify de Claude Code?", "Dejará de estar disponible en Claude Code.", "[l]"}},
		{"unlink still visible", app.Proposal{Request: app.Request{Action: app.ActionUnlink, Key: "archify", Harness: "opencode"},
			Toggle: &app.TogglePreview{StillVisible: []app.Sighting{{Harness: "opencode", Kind: "direct"}}}},
			[]string{"OpenCode la seguirá viendo por otra vía"}},
		{"link", app.Proposal{Request: app.Request{Action: app.ActionLink, Key: "archify", Harness: "claude-code"}, Reload: []string{"claude-code"}},
			[]string{"¿Añadir archify a Claude Code?", "Quedará disponible en Claude Code", "[l]"}},
		{"install", app.Proposal{Request: app.Request{Action: app.ActionInstall}, Reload: []string{"codex"},
			Install: &app.InstallPreview{Key: "tidy", Source: "acme/tools"}},
			[]string{"¿Instalar tidy?", "desde acme/tools", "disponible en Codex", "Historial (h)"}},
		{"install from a website", app.Proposal{Request: app.Request{Action: app.ActionInstall},
			Install: &app.InstallPreview{Key: "ui-taste", Source: "uizze.sh", Site: "https://uizze.sh/ui-taste"}},
			[]string{"¿Instalar ui-taste?", "Se instala desde el sitio web uizze.sh (no es GitHub)."}},
		{"repair", app.Proposal{Request: app.Request{Action: app.ActionRepair, Repair: "delete_link"}},
			[]string{"¿Borrar el enlace que no lleva a ninguna parte?", "Historial (h)"}},
		{"adopt-off", app.Proposal{Request: app.Request{Action: app.ActionAdoptOff}, Reload: []string{"codex"},
			Toggle: &app.TogglePreview{Skills: []app.SkillChange{{Key: "accessibility"}, {Key: "domain-driven-design"},
				{Key: "git-guardrails"}, {Key: "web-quality-audit"}}}},
			[]string{"¿Desactivar de verdad las 4 skills de OFF/?",
				"Se desactivan las 4 juntas: accessibility, domain-driven-design, git-guardrails y web-quality-audit.",
				"ningún agente las ve", "[a]"}},
		{"adopt-off of one", app.Proposal{Request: app.Request{Action: app.ActionAdoptOff},
			Toggle: &app.TogglePreview{Skills: []app.SkillChange{{Key: "accessibility"}}}},
			[]string{"¿Desactivar de verdad accessibility, la skill de OFF/?", "ningún agente la ve", "[a]"}},
		{"undo", app.Proposal{Request: app.Request{Action: app.ActionUndo}, Undo: &app.UndoPreview{Of: "disable"}},
			[]string{"¿Deshacer «desactivar»?", "Todo vuelve a como estaba"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := text.TUIPlanSummary(tt.p, agentNames)
			all := s.Question + "\n" + strings.Join(s.Lines, "\n")
			if !strings.HasPrefix(s.Question, "¿") || !strings.HasSuffix(s.Question, "?") {
				t.Errorf("question %q is not a question", s.Question)
			}
			if n := len(s.Lines); n < 2 || n > 4 {
				t.Errorf("summary has %d lines, want 2 to 4:\n%s", n, all)
			}
			for _, want := range tt.want {
				if !strings.Contains(all, want) {
					t.Errorf("summary lacks %q:\n%s", want, all)
				}
			}
			if w := internal.FindString(all); w != "" {
				t.Errorf("summary uses the internal word %q:\n%s", w, all)
			}
			for _, id := range []string{"claude-code", "opencode"} {
				if strings.Contains(all, id) {
					t.Errorf("summary names the agent by its id %q:\n%s", id, all)
				}
			}
		})
	}
}

// After a plan runs the TUI says what changed, how to undo it and what to
// reload, in plain words.
func TestTUIDoneIsPlain(t *testing.T) {
	res := app.Result{SnapshotID: "s1", Proposal: app.Proposal{
		Request: app.Request{Action: app.ActionDisable, Key: "agent-browser"}, Reload: []string{"claude-code", "opencode"},
	}}
	got := text.TUIWithUndo(text.TUIDone(res, agentNames))
	want := "Listo: agent-browser está desactivada. [z] deshacer\nRecarga las skills en Claude Code y OpenCode."
	if got != want {
		t.Errorf("TUIDone = %q, want %q", got, want)
	}
	undo := app.Result{SnapshotID: "s2", Proposal: app.Proposal{Request: app.Request{Action: app.ActionUndo}}}
	if got := text.TUIDone(undo, agentNames); got != "Listo: la operación se deshizo." {
		t.Errorf("TUIDone of an undo = %q, which offers no undo", got)
	}
}

// The version 1 update and its undo start with the same kind of summary.
func TestTUIUpdateSummariesArePlain(t *testing.T) {
	s := text.TUIUpdateSummary("alpha", 2, true)
	all := s.Question + "\n" + strings.Join(s.Lines, "\n")
	for _, want := range []string{"¿Actualizar alpha?", "2 archivo(s)", "copia permanente", "Historial (h)"} {
		if !strings.Contains(all, want) {
			t.Errorf("update summary lacks %q:\n%s", want, all)
		}
	}
	u := text.TUIUndoUpdateSummary("alpha", "snap-1")
	if u.Question != "¿Deshacer la actualización de alpha?" || !strings.Contains(strings.Join(u.Lines, "\n"), "snap-1") {
		t.Errorf("undo summary = %+v", u)
	}
}

func TestJoinNames(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""}, {[]string{"A"}, "A"}, {[]string{"A", "B"}, "A y B"}, {[]string{"A", "B", "C"}, "A, B y C"},
	}
	for _, tt := range tests {
		if got := text.JoinNames(tt.in); got != tt.want {
			t.Errorf("JoinNames(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The panel of a skill moved to OFF/ by hand says, in plain words, who still
// sees it and why (L83).
func TestTUIPanelOffHandSaysWhoStillSeesIt(t *testing.T) {
	tests := []struct {
		agents []string
		want   string
	}{
		{[]string{"Codex", "OpenCode", "Pi"}, "La desactivaste a mano moviéndola a OFF/, pero Codex, OpenCode y Pi la siguen viendo porque buscan en subcarpetas."},
		{[]string{"Codex"}, "La desactivaste a mano moviéndola a OFF/, pero Codex la sigue viendo porque busca en subcarpetas."},
		{nil, "La desactivaste a mano moviéndola a OFF/, y ningún agente la ve."},
	}
	for _, tt := range tests {
		if got := text.TUIPanelOffHand(tt.agents); got != tt.want {
			t.Errorf("TUIPanelOffHand(%v) = %q, want %q", tt.agents, got, tt.want)
		}
	}
	got := text.TUIPanelAdoptOff([]string{"a", "b", "c"}, "b")
	if want := "Desactivarla de verdad la guarda aparte, donde ningún agente la ve, junto con las otras 2 skills de OFF/ (a y c)."; got != want {
		t.Errorf("TUIPanelAdoptOff = %q, want %q", got, want)
	}
}
