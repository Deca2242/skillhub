package tui_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/shared/domain"
)

func typeQuery(m *tui.Model, query string) {
	pressRun(m, "/")
	for _, r := range query {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	pressRun(m, "enter")
}

func TestPresentSkillsSearchesDescriptionAliasAndOrigin(t *testing.T) {
	skills := []domain.Skill{
		{
			ID:          "lock:alpha",
			Name:        "alpha",
			Aliases:     []string{"first-alias", "second-alias"},
			Description: "Automatiza pruebas de navegador.",
			Origin: domain.Origin{
				Kind: domain.OriginLock,
				Lock: &domain.LockOrigin{
					Source:     "vercel-labs/agent-tools",
					SkillPath:  "skills/alpha/SKILL.md",
					FolderHash: "abc123",
				},
			},
			State: domain.StateUpToDate,
		},
		{
			ID:          "git:beta",
			Name:        "beta",
			Description: "Gestiona despliegues alpha en contenedores.",
			Origin: domain.Origin{
				Kind: domain.OriginGitClone,
				GitClone: &domain.GitCloneOrigin{
					Root:   "/tmp/beta",
					Remote: "https://github.com/acme/deploy-skills.git",
				},
			},
			State: domain.StateUpToDate,
		},
	}

	cases := []struct {
		name            string
		query           string
		wantIDs         []string
		wantMatchFields map[string][]string
	}{
		{
			name:    "description match",
			query:   "navegador",
			wantIDs: []string{"lock:alpha"},
			wantMatchFields: map[string][]string{
				"lock:alpha": {text.SearchFieldDescription},
			},
		},
		{
			name:    "alias match as own field",
			query:   "second-alias",
			wantIDs: []string{"lock:alpha"},
			wantMatchFields: map[string][]string{
				"lock:alpha": {text.SearchFieldAlias},
			},
		},
		{
			name:    "aliases do not match across separator",
			query:   "first-alias, second-alias",
			wantIDs: nil,
		},
		{
			name:    "lock repository match",
			query:   "vercel-labs",
			wantIDs: []string{"lock:alpha"},
			wantMatchFields: map[string][]string{
				"lock:alpha": {text.SearchFieldRepo},
			},
		},
		{
			name:    "git clone remote match",
			query:   "deploy-skills",
			wantIDs: []string{"git:beta"},
			wantMatchFields: map[string][]string{
				"git:beta": {text.SearchFieldRemote},
			},
		},
		{
			name:    "visible row name match hides match reason for alpha",
			query:   "alpha",
			wantIDs: []string{"lock:alpha", "git:beta"},
			wantMatchFields: map[string][]string{
				"lock:alpha": nil,
				"git:beta":   {text.SearchFieldDescription},
			},
		},
		{
			name:    "multiple hidden fields match",
			query:   "agent",
			wantIDs: []string{"lock:alpha"},
			wantMatchFields: map[string][]string{
				"lock:alpha": {text.SearchFieldRepo},
			},
		},
		{
			name:    "empty query has no match reasons",
			query:   "",
			wantIDs: []string{"lock:alpha", "git:beta"},
			wantMatchFields: map[string][]string{
				"lock:alpha": nil,
				"git:beta":   nil,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tui.PresentSkills(skills, tc.query)
			var gotIDs []string
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
				if wantFields, ok := tc.wantMatchFields[r.ID]; ok {
					if !slices.Equal(r.MatchFields, wantFields) {
						t.Errorf("row %s MatchFields = %v, want %v", r.ID, r.MatchFields, wantFields)
					}
				}
			}
			if !slices.Equal(gotIDs, tc.wantIDs) {
				t.Errorf("PresentSkills(%q) IDs = %v, want %v", tc.query, gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestTUISearchPanelShowsMatchReason(t *testing.T) {
	mc := newMachine(t)
	m := openSized(t, mc.newApp(), 100, 30)

	// Search by description (humanizer has "own copy" in description, not in name).
	typeQuery(m, "own copy")
	got := screen(m)
	if !strings.Contains(got, "humanizer") {
		t.Fatalf("search by description did not find humanizer:\n%s", got)
	}
	if !strings.Contains(got, "Coincide en: descripción") {
		t.Fatalf("panel did not show match reason for description:\n%s", got)
	}

	// Search by lock repository ("owner/agent-browser" -> "owner/").
	m2 := openSized(t, mc.newApp(), 100, 30)
	typeQuery(m2, "owner/")
	got = screen(m2)
	if !strings.Contains(got, "agent-browser") {
		t.Fatalf("search by lock repository did not find agent-browser:\n%s", got)
	}
	if !strings.Contains(got, "Coincide en: repositorio") {
		t.Fatalf("panel did not show match reason for repository:\n%s", got)
	}
}

func TestGoldenSearch(t *testing.T) {
	for _, width := range []int{80, 100} {
		mc := newMachine(t)
		m := openSized(t, mc.newApp(), width, 30)
		typeQuery(m, "own copy")
		t.Run(fmt.Sprintf("Search%d", width), func(t *testing.T) {
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
	}
}
