package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

type skillShowJSON struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Aliases       []string          `json:"aliases"`
	Description   string            `json:"description"`
	SkillhubState string            `json:"skillhub_state"`
	State         string            `json:"state"`
	Issues        []string          `json:"issues"`
	Origin        domain.Origin     `json:"origin"`
	Sightings     []skillSightJSON  `json:"sightings"`
	Locations     []domain.Location `json:"locations"`
	Activity      []skillActJSON    `json:"activity"`
	DisabledSince string            `json:"disabled_since"`
	ReturnTo      []string          `json:"return_to"`
}

type skillSightJSON struct {
	Harness     string `json:"harness"`
	HarnessName string `json:"harness_name"`
	Kind        string `json:"kind"`
	Entry       string `json:"entry"`
}

type skillActJSON struct {
	At        string `json:"at"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	V2        bool   `json:"v2"`
	Operation string `json:"operation"`
	Undo      bool   `json:"undo"`
}

func decodeSkillShow(t *testing.T, out string) skillShowJSON {
	t.Helper()
	var s skillShowJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("skills show --json: %v\n%s", err, out)
	}
	return s
}

func TestSkillsShowHelpAndUnknownID(t *testing.T) {
	m := newMachine(t)
	help := m.ok("skills", "--help")
	if !strings.Contains(help.stdout, "show") {
		t.Errorf("skills --help does not list show:\n%s", help.stdout)
	}

	r := m.run("", "skills", "show", "no-existe")
	if r.code == 0 {
		t.Fatalf("expected non-zero exit for unknown id, got 0")
	}
	if r.stdout != "" {
		t.Errorf("expected empty stdout on error, got %q", r.stdout)
	}
	if !strings.Contains(r.stderr, "no hay ninguna skill «no-existe»") {
		t.Errorf("expected Spanish error message, got %q", r.stderr)
	}
}

func TestSkillsShowActiveDirectAndViaLink(t *testing.T) {
	m := newMachine(t)
	out := m.ok("skills", "show", "lock:agent-browser").stdout
	for _, want := range []string{
		"Skill: agent-browser",
		"ID: lock:agent-browser",
		"En skillhub: activa",
		"Claude Code: por un enlace",
		"Codex: directamente, sin enlace",
		"Repositorio: owner/agent-browser",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("skills show lacks %q:\n%s", want, out)
		}
	}

	j := decodeSkillShow(t, m.ok("skills", "show", "agent-browser", "--json").stdout)
	if j.ID != "lock:agent-browser" || j.SkillhubState != "active" {
		t.Errorf("unexpected JSON header: %+v", j)
	}
	if j.Origin.Kind != domain.OriginLock || j.Origin.Lock == nil || j.Origin.Lock.Source != "owner/agent-browser" {
		t.Errorf("unexpected JSON origin: %+v", j.Origin)
	}
	var seenClaude, seenCodex bool
	for _, sg := range j.Sightings {
		if sg.Harness == "claude-code" && sg.Kind == "link" {
			seenClaude = true
		}
		if sg.Harness == "codex" && sg.Kind == "direct" {
			seenCodex = true
		}
	}
	if !seenClaude || !seenCodex {
		t.Errorf("expected claude-code (link) and codex (direct) sightings, got %+v", j.Sightings)
	}
}

func TestSkillsShowDisabledAndIgnored(t *testing.T) {
	m := newMachine(t)
	// 1. Disabled by hand in OFF/
	offOut := m.ok("skills", "show", "accessibility").stdout
	if !strings.Contains(offOut, "En skillhub: "+text.TUIStateDisabledHand) {
		t.Errorf("OFF/ skill state mismatch:\n%s", offOut)
	}
	offJSON := decodeSkillShow(t, m.ok("skills", "show", "accessibility", "--json").stdout)
	if offJSON.SkillhubState != "disabled_by_hand" {
		t.Errorf("OFF/ JSON skillhub_state = %q, want disabled_by_hand", offJSON.SkillhubState)
	}

	// 2. Disabled via skillhub (parked)
	m.approve("skills", "disable", "agent-browser")
	disOut := m.ok("skills", "show", "agent-browser").stdout
	if !strings.Contains(disOut, "En skillhub: "+text.TUIStateDisabled) {
		t.Errorf("parked skill state mismatch:\n%s", disOut)
	}
	if !strings.Contains(disOut, "Desactivada") {
		t.Errorf("parked skill missing v2 disable activity:\n%s", disOut)
	}
	disJSON := decodeSkillShow(t, m.ok("skills", "show", "agent-browser", "--json").stdout)
	if disJSON.SkillhubState != "disabled" || disJSON.DisabledSince == "" {
		t.Errorf("parked JSON = %+v, want disabled with disabled_since", disJSON)
	}

	// 3. Ignored skill
	m.ok("settings", "set", "ignore", "archify")
	ignOut := m.ok("skills", "show", "archify").stdout
	if !strings.Contains(ignOut, "En skillhub: "+text.TUIStateIgnored) {
		t.Errorf("ignored skill state mismatch:\n%s", ignOut)
	}
	ignJSON := decodeSkillShow(t, m.ok("skills", "show", "archify", "--json").stdout)
	if ignJSON.SkillhubState != "ignored" {
		t.Errorf("ignored JSON skillhub_state = %q, want ignored", ignJSON.SkillhubState)
	}
}

func TestSkillsShowV1AndV2Activity(t *testing.T) {
	f := newFixture(t)
	v1 := skillFiles("alpha", "v1")
	f.publish("alpha", v1)
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": v1})
	f.publish("alpha", skillFiles("alpha", "v2"))
	f.ok("check")
	f.ok("updates", "apply", "alpha", "--yes")

	// Also record a v2 disable+enable via machine? Fixture has v1; let's test v2 on machine,
	// or add a v1 entry in machine's journal!
	out := f.ok("skills", "show", "alpha").stdout
	if !strings.Contains(out, "Actualización aplicada") {
		t.Errorf("expected v1 activity in skills show:\n%s", out)
	}
	j := decodeSkillShow(t, f.ok("skills", "show", "alpha", "--json").stdout)
	if len(j.Activity) != 1 || j.Activity[0].V2 || j.Activity[0].Action != "applied" {
		t.Errorf("expected 1 v1 applied activity in JSON, got %+v", j.Activity)
	}
}

func TestSkillsShowEscapesHostileTextInTextOnly(t *testing.T) {
	f := newFixture(t)
	f.installLock([]string{"bad"}, map[string]map[string]string{
		"bad": {"SKILL.md": "---\nname: bad\ndescription: \"Hola\\r\\u001b[2K\\u202emundo\"\n---\nbody\n"},
	})

	out := f.ok("skills", "show", "bad").stdout
	if !strings.Contains(out, "Hola"+hostileShown+"mundo") {
		t.Errorf("expected escaped description in text output:\n%s", out)
	}
	assertNothingActs(t, "skills show", out)

	j := decodeSkillShow(t, f.ok("skills", "show", "bad", "--json").stdout)
	if j.Description != "Hola"+hostile+"mundo" {
		t.Errorf("expected raw description in JSON, got %q", j.Description)
	}
}

func TestSkillsShowIncludesDraftPacks(t *testing.T) {
	p := newPackMachine(t)
	p.home.WriteFile(".agents/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n")
	p.home.Symlink(".claude/skills/alpha", "../../.agents/skills/alpha")
	p.home.WriteFile(".agents/.skill-lock.json", `{"version":3,"skills":{"alpha":{"source":"acme/skills","sourceType":"github","skillPath":"skills/alpha/SKILL.md","skillFolderHash":"1111"}}}`+"\n")

	p.ok("packs", "create", "seguridad", "--description", "Auditoría")
	p.ok("packs", "add", "seguridad", "lock:alpha")

	out := p.ok("skills", "show", "alpha").stdout
	if !strings.Contains(out, "Packs (borradores): seguridad") {
		t.Errorf("skills show missing pack membership:\n%s", out)
	}

	var parsed struct {
		Packs []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"packs"`
	}
	jsonOut := p.ok("skills", "show", "alpha", "--json").stdout
	if err := json.Unmarshal([]byte(jsonOut), &parsed); err != nil {
		t.Fatalf("skills show --json: %v\n%s", err, jsonOut)
	}
	if len(parsed.Packs) != 1 || parsed.Packs[0].Name != "seguridad" || parsed.Packs[0].Description != "Auditoría" {
		t.Fatalf("unexpected packs in skills show --json: %+v", parsed.Packs)
	}
}

func TestSkillsShowSucceedsAndOmitsPacksWhenSidecarUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bad json", "{not-json"},
		{"unsupported version", `{"version":99,"packs":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPackMachine(t)
			p.home.WriteFile(".agents/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: Alpha skill\n---\nbody\n")
			p.home.Symlink(".claude/skills/alpha", "../../.agents/skills/alpha")
			p.home.WriteFile(".agents/.skill-lock.json", `{"version":3,"skills":{"alpha":{"source":"acme/skills","sourceType":"github","skillPath":"skills/alpha/SKILL.md","skillFolderHash":"1111"}}}`+"\n")

			sidecar := filepath.Join(p.home.Paths().StateDir(), "packs.json")
			if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecar, []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}

			textRes := p.run("skills", "show", "alpha")
			if textRes.code != 0 || textRes.stderr != "" {
				t.Fatalf("skills show failed: code=%d stderr=%q", textRes.code, textRes.stderr)
			}
			if strings.Contains(textRes.stdout, "Packs (borradores):") {
				t.Errorf("expected packs line omitted when sidecar unavailable:\n%s", textRes.stdout)
			}

			jsonRes := p.run("skills", "show", "alpha", "--json")
			if jsonRes.code != 0 || jsonRes.stderr != "" {
				t.Fatalf("skills show --json failed: code=%d stderr=%q", jsonRes.code, jsonRes.stderr)
			}
			var rawMap map[string]any
			if err := json.Unmarshal([]byte(jsonRes.stdout), &rawMap); err != nil {
				t.Fatalf("invalid json: %v\n%s", err, jsonRes.stdout)
			}
			if _, exists := rawMap["packs"]; exists {
				t.Errorf("expected packs key omitted when sidecar unavailable, got:\n%s", jsonRes.stdout)
			}
		})
	}
}
