package text_test

import (
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/inventory"
	"skillhub/internal/shared/ports"
)

func TestTUIDocumentFormat(t *testing.T) {
	t.Run("sanitizes third-party text while keeping line breaks", func(t *testing.T) {
		doc := app.SkillDocument{
			SkillID:   "lock:evil",
			SkillName: "evil\x1b[31m\nfake",
			Dir:       "/tmp/skills/evil",
			Frontmatter: inventory.Frontmatter{
				Name:        "evil\rname",
				Description: "desc\u202ebidi",
				Author:      "author\x1b[2J",
			},
			Content: "Linea 1\tcol\nLinea 2\x1b[31mrojo\x1b[0m\r\nLinea 3\u202ebidi",
			Companions: []app.CompanionEntry{
				{Name: "a\x1b[31m.sh", Kind: ports.EntryFile},
				{Name: "refs", Kind: ports.EntryDir},
			},
		}

		got := text.TUIDocumentFormat(doc)
		if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\r') || strings.ContainsRune(got, '\u202e') {
			t.Fatalf("formatted document contains raw control or bidi characters:\n%q", got)
		}
		for _, want := range []string{
			`evil\x1b[31m\nfake`,
			`evil\rname`,
			`desc\u202ebidi`,
			`author\x1b[2J`,
			"Linea 1\tcol\nLinea 2\\x1b[31mrojo\\x1b[0m\\r\nLinea 3\\u202ebidi",
			`a\x1b[31m.sh`,
			"refs/",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("formatted document lacks %q:\n%s", want, got)
			}
		}
	})

	t.Run("missing description says so instead of repeating skill name", func(t *testing.T) {
		doc := app.SkillDocument{
			SkillID:   "lock:alpha",
			SkillName: "alpha",
			Dir:       "/tmp/skills/alpha",
			Frontmatter: inventory.Frontmatter{
				Name: "alpha",
			},
			Content: "# Alpha\n",
		}
		got := text.TUIDocumentFormat(doc)
		if !strings.Contains(got, text.TUIDocumentNoDescription) {
			t.Errorf("expected missing description notice %q:\n%s", text.TUIDocumentNoDescription, got)
		}
	})

	t.Run("truncated document shows explicit truncation notice", func(t *testing.T) {
		doc := app.SkillDocument{
			SkillID:       "lock:big",
			SkillName:     "big",
			Dir:           "/tmp/skills/big",
			Content:       "inicio",
			Truncated:     true,
			ShownBytes:    32768,
			OriginalBytes: 40000,
		}
		got := text.TUIDocumentFormat(doc)
		want := text.TUIDocumentTruncated(32768, 40000)
		if !strings.Contains(got, want) {
			t.Errorf("expected truncation notice %q:\n%s", want, got)
		}
	})
}
