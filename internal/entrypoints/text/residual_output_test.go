package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
)

func TestTUIChecksQuestionEscapesKeyAndPreservesSpanish(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		want      string
		checkSafe bool
	}{
		{
			name:      "control escape bidi newline and tab",
			key:       "evil" + hostile + "\n\tkey",
			want:      "¿En qué agentes está evil" + hostileShown + `\n\tkey` + "?",
			checkSafe: true,
		},
		{
			name: "ordinary ASCII key",
			key:  "agent-browser",
			want: "¿En qué agentes está agent-browser?",
		},
		{
			name: "ordinary Spanish unicode key",
			key:  "canción-ñandú",
			want: "¿En qué agentes está canción-ñandú?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUIChecksQuestion(tt.key)
			if got != tt.want {
				t.Errorf("TUIChecksQuestion(%q) = %q, want %q", tt.key, got, tt.want)
			}
			if tt.checkSafe {
				assertEscaped(t, "TUIChecksQuestion", got)
				if strings.ContainsAny(got, "\n\t") {
					t.Errorf("TUIChecksQuestion kept raw newline/tab: %q", got)
				}
			}
		})
	}
}

func TestTUIUnignoredEscapesSingularAndPreservesOtherCases(t *testing.T) {
	tests := []struct {
		name      string
		skills    []string
		entries   []string
		want      string
		checkSafe bool
	}{
		{
			name:      "singular skill with control escape bidi newline and tab",
			skills:    []string{"evil" + hostile + "\n\tname"},
			want:      "Ya no se ignora evil" + hostileShown + `\n\tname` + ": vuelve a aparecer en Skills.",
			checkSafe: true,
		},
		{
			name:      "singular skill and unmatched entry both escaped",
			skills:    []string{"one" + hostile + "\nrow"},
			entries:   []string{"ghost" + hostile + "\tcol"},
			want:      "Ya no se ignora one" + hostileShown + `\nrow` + ": vuelve a aparecer en Skills. Se quitó ghost" + hostileShown + `\tcol` + " de la lista de ignoradas.",
			checkSafe: true,
		},
		{
			name:      "plural skills escaped",
			skills:    []string{"a" + hostile, "b" + hostile},
			want:      "Ya no se ignoran a" + hostileShown + " y b" + hostileShown + ": vuelven a aparecer en Skills.",
			checkSafe: true,
		},
		{
			name:   "ordinary singular skill",
			skills: []string{"grill-with-docs"},
			want:   "Ya no se ignora grill-with-docs: vuelve a aparecer en Skills.",
		},
		{
			name:    "only unmatched entry",
			entries: []string{"old-name"},
			want:    "Se quitó old-name de la lista de ignoradas.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := text.TUIUnignored(tt.skills, tt.entries)
			if got != tt.want {
				t.Errorf("TUIUnignored(%q, %q) = %q, want %q", tt.skills, tt.entries, got, tt.want)
			}
			if tt.checkSafe {
				assertEscaped(t, "TUIUnignored "+tt.name, got)
				if strings.ContainsAny(got, "\n\t") {
					t.Errorf("TUIUnignored kept raw newline/tab: %q", got)
				}
			}
		})
	}
}
