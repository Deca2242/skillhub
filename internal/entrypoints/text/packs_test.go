package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

func TestPackListAndShowEscapeThirdPartyText(t *testing.T) {
	hostile := "\r\x1b[2K\u202e"
	listOut := text.PackList([]app.PackSummary{
		{Name: "pack" + hostile, Description: "Desc\n" + hostile, MemberCount: 2},
	})
	if !strings.Contains(listOut, `pack\r\x1b[2K\u202e`) {
		t.Errorf("PackList did not escape name:\n%s", listOut)
	}
	for _, raw := range []string{"\r", "\x1b", "\u202e"} {
		if strings.Contains(listOut, raw) {
			t.Errorf("PackList contains raw %q:\n%s", raw, listOut)
		}
	}

	showOut := text.PackShow(app.PackView{
		Name:        "seguridad",
		Description: "Audit" + hostile,
		Members: []app.PackMemberView{
			{
				ID:       "lock:alpha",
				Name:     "alpha",
				Presence: app.MemberObserved,
				Health:   app.MemberHealthIssues,
				State:    domain.StateUpToDate,
				Issues:   []domain.Issue{domain.IssueBrokenLink},
			},
			{
				ID:       "lock:ghost" + hostile,
				Presence: app.MemberNotObserved,
				Health:   app.MemberHealthNotObserved,
			},
		},
	})

	for _, want := range []string{
		"Pack (borrador): seguridad",
		`Descripción: Audit\r\x1b[2K\u202e`,
		"lock:alpha",
		"observada",
		"con problemas",
		"enlace roto",
		`lock:ghost\r\x1b[2K\u202e`,
		"no observada",
	} {
		if !strings.Contains(showOut, want) {
			t.Errorf("PackShow lacks %q:\n%s", want, showOut)
		}
	}
	if strings.Contains(strings.ToLower(showOut), "faltante") || strings.Contains(strings.ToLower(showOut), "missing") {
		t.Errorf("PackShow must not label unobserved member as missing/faltante:\n%s", showOut)
	}
	for _, raw := range []string{"\r", "\x1b", "\u202e"} {
		if strings.Contains(showOut, raw) {
			t.Errorf("PackShow contains raw %q:\n%s", raw, showOut)
		}
	}
}
