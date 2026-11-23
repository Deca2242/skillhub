package app_test

import (
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
)

func TestCompareAgentsTable(t *testing.T) {
	sightings := map[string][]app.Sighting{
		"skill-common": {
			{Harness: "claude-code", Kind: app.SightingViaLink, Entry: ".claude/skills/skill-common"},
			{Harness: "codex", Kind: app.SightingDirect, Entry: ".agents/skills/skill-common"},
		},
		"skill-first": {
			{Harness: "claude-code", Kind: app.SightingOwnCopy, Entry: ".claude/skills/skill-first"},
			{Harness: "opencode", Kind: app.SightingDirect, Entry: ".opencode/skills/skill-first"},
		},
		"skill-second": {
			{Harness: "codex", Kind: app.SightingDirect, Entry: ".agents/skills/skill-second"},
		},
		"skill-indet-first": {
			{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-first"},
			{Harness: "codex", Kind: app.SightingDirect, Entry: ".agents/skills/skill-indet-first"},
		},
		"skill-indet-both": {
			{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-both"},
			{Harness: "codex", Kind: app.SightingIndeterminate, Entry: ".agents/skills/skill-indet-both"},
		},
		"skill-other": {
			{Harness: "opencode", Kind: app.SightingDirect, Entry: ".opencode/skills/skill-other"},
		},
	}

	tests := []struct {
		name      string
		sightings map[string][]app.Sighting
		first     string
		second    string
		want      app.AgentComparison
	}{
		{
			name:      "two agents with common, exclusive and indeterminate",
			sightings: sightings,
			first:     "claude-code",
			second:    "codex",
			want: app.AgentComparison{
				First:      "claude-code",
				Second:     "codex",
				Common:     []string{"skill-common"},
				OnlyFirst:  []string{"skill-first"},
				OnlySecond: []string{"skill-second"},
				Indeterminate: []app.IndeterminateSkill{
					{
						ID:     "skill-indet-both",
						Agents: []string{"claude-code", "codex"},
						Reasons: []app.Sighting{
							{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-both"},
							{Harness: "codex", Kind: app.SightingIndeterminate, Entry: ".agents/skills/skill-indet-both"},
						},
					},
					{
						ID:     "skill-indet-first",
						Agents: []string{"claude-code"},
						Reasons: []app.Sighting{
							{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-first"},
						},
					},
				},
			},
		},
		{
			name:      "same agent twice puts all non-indeterminate in common",
			sightings: sightings,
			first:     "claude-code",
			second:    "claude-code",
			want: app.AgentComparison{
				First:      "claude-code",
				Second:     "claude-code",
				Common:     []string{"skill-common", "skill-first"},
				OnlyFirst:  []string{},
				OnlySecond: []string{},
				Indeterminate: []app.IndeterminateSkill{
					{
						ID:     "skill-indet-both",
						Agents: []string{"claude-code"},
						Reasons: []app.Sighting{
							{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-both"},
						},
					},
					{
						ID:     "skill-indet-first",
						Agents: []string{"claude-code"},
						Reasons: []app.Sighting{
							{Harness: "claude-code", Kind: app.SightingIndeterminate, Entry: ".claude/skills/skill-indet-first"},
						},
					},
				},
			},
		},
		{
			name:      "empty sightings",
			sightings: nil,
			first:     "claude-code",
			second:    "codex",
			want: app.AgentComparison{
				First:         "claude-code",
				Second:        "codex",
				Common:        []string{},
				OnlyFirst:     []string{},
				OnlySecond:    []string{},
				Indeterminate: []app.IndeterminateSkill{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := app.CompareAgents(tt.sightings, tt.first, tt.second)
			if got.First != tt.want.First || got.Second != tt.want.Second {
				t.Fatalf("agents = (%q, %q), want (%q, %q)", got.First, got.Second, tt.want.First, tt.want.Second)
			}
			if !slices.Equal(got.Common, tt.want.Common) {
				t.Errorf("Common = %v, want %v", got.Common, tt.want.Common)
			}
			if !slices.Equal(got.OnlyFirst, tt.want.OnlyFirst) {
				t.Errorf("OnlyFirst = %v, want %v", got.OnlyFirst, tt.want.OnlyFirst)
			}
			if !slices.Equal(got.OnlySecond, tt.want.OnlySecond) {
				t.Errorf("OnlySecond = %v, want %v", got.OnlySecond, tt.want.OnlySecond)
			}
			if !slices.EqualFunc(got.Indeterminate, tt.want.Indeterminate, func(a, b app.IndeterminateSkill) bool {
				return a.ID == b.ID && slices.Equal(a.Agents, b.Agents) && slices.Equal(a.Reasons, b.Reasons)
			}) {
				t.Errorf("Indeterminate = %+v, want %+v", got.Indeterminate, tt.want.Indeterminate)
			}
		})
	}
}

func TestCompareAgentsMatchesMachineSightingsOracle(t *testing.T) {
	m := newMachine(t)
	before := m.hash()

	sgs, err := m.app.Sightings(bg)
	must(t, err)

	const first, second = "claude-code", "codex"
	got := app.CompareAgents(sgs, first, second)

	// Independent oracle directly from sightings map.
	var wantCommon, wantFirst, wantSecond, wantIndet []string
	for id, list := range sgs {
		var hasA, hasB, indet bool
		for _, sg := range list {
			switch sg.Harness {
			case first:
				hasA = true
				if sg.Kind == app.SightingIndeterminate {
					indet = true
				}
			case second:
				hasB = true
				if sg.Kind == app.SightingIndeterminate {
					indet = true
				}
			}
		}
		switch {
		case indet:
			wantIndet = append(wantIndet, id)
		case hasA && hasB:
			wantCommon = append(wantCommon, id)
		case hasA:
			wantFirst = append(wantFirst, id)
		case hasB:
			wantSecond = append(wantSecond, id)
		}
	}
	slices.Sort(wantCommon)
	slices.Sort(wantFirst)
	slices.Sort(wantSecond)
	slices.Sort(wantIndet)

	if !slices.Equal(got.Common, wantCommon) {
		t.Errorf("Common = %v, want %v", got.Common, wantCommon)
	}
	if !slices.Equal(got.OnlyFirst, wantFirst) {
		t.Errorf("OnlyFirst = %v, want %v", got.OnlyFirst, wantFirst)
	}
	if !slices.Equal(got.OnlySecond, wantSecond) {
		t.Errorf("OnlySecond = %v, want %v", got.OnlySecond, wantSecond)
	}
	var gotIndet []string
	for _, item := range got.Indeterminate {
		gotIndet = append(gotIndet, item.ID)
		if len(item.Reasons) == 0 || !strings.Contains(item.Reasons[0].Kind, "indeterminate") {
			t.Errorf("Indeterminate[%s] missing reason: %+v", item.ID, item)
		}
	}
	if !slices.Equal(gotIndet, wantIndet) {
		t.Errorf("Indeterminate = %v, want %v", gotIndet, wantIndet)
	}
	if m.hash() != before {
		t.Fatal("CompareAgents or Sightings wrote something")
	}
}
