package app_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

func TestSkillDocumentReadsFrontmatterContentAndCompanions(t *testing.T) {
	f := newFixture(t)
	longBody := strings.Repeat("Linea de contenido de prueba.\n", 1300) // > SkillDocumentMaxBytes
	f.installLock(map[string]map[string]string{
		"alpha": {
			"SKILL.md":           "---\nname: alpha\ndescription: Skill de prueba\nmetadata:\n  author: acme\n---\n\n# Alpha\n\nUso local.\n",
			"README.md":          "# Readme\n",
			"scripts/run.sh":     "#!/bin/sh\necho hi\n",
			"references/spec.md": "# Spec\n",
		},
		"nodesc": {
			"SKILL.md": "---\nname: nodesc\n---\n\nSin cabecera de descripcion.\n",
		},
		"bigdoc": {
			"SKILL.md": "---\nname: bigdoc\ndescription: Documento grande\n---\n\n" + longBody,
		},
	})

	a := f.app
	skills, err := a.Skills(t.Context())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	byName := make(map[string]domain.Skill, len(skills))
	for _, s := range skills {
		byName[s.Name] = s
	}

	t.Run("reads frontmatter, content and companions", func(t *testing.T) {
		doc, err := a.SkillDocument(byName["alpha"])
		if err != nil {
			t.Fatalf("SkillDocument(alpha): %v", err)
		}
		if doc.Missing {
			t.Fatalf("expected alpha document not to be missing")
		}
		if doc.Truncated {
			t.Fatalf("expected alpha document not to be truncated")
		}
		if doc.Frontmatter.Name != "alpha" || doc.Frontmatter.Description != "Skill de prueba" || doc.Frontmatter.Author != "acme" {
			t.Errorf("Frontmatter = %+v", doc.Frontmatter)
		}
		if !strings.Contains(doc.Content, "# Alpha\n\nUso local.") {
			t.Errorf("Content lacks body:\n%s", doc.Content)
		}
		wantCompanions := []app.CompanionEntry{
			{Name: "README.md", Kind: ports.EntryFile},
			{Name: "references", Kind: ports.EntryDir},
			{Name: "scripts", Kind: ports.EntryDir},
		}
		if !slices.Equal(doc.Companions, wantCompanions) {
			t.Errorf("Companions = %+v, want %+v", doc.Companions, wantCompanions)
		}
	})

	t.Run("missing description stays empty in frontmatter", func(t *testing.T) {
		doc, err := a.SkillDocument(byName["nodesc"])
		if err != nil {
			t.Fatalf("SkillDocument(nodesc): %v", err)
		}
		if doc.Frontmatter.Description != "" {
			t.Errorf("Description = %q, want empty", doc.Frontmatter.Description)
		}
		if doc.Frontmatter.Name != "nodesc" {
			t.Errorf("Name = %q, want nodesc", doc.Frontmatter.Name)
		}
	})

	t.Run("oversized document is bounded and marked truncated", func(t *testing.T) {
		doc, err := a.SkillDocument(byName["bigdoc"])
		if err != nil {
			t.Fatalf("SkillDocument(bigdoc): %v", err)
		}
		if !doc.Truncated {
			t.Fatalf("expected bigdoc to be truncated")
		}
		if len(doc.Content) > app.SkillDocumentMaxBytes {
			t.Errorf("len(Content) = %d, exceeds SkillDocumentMaxBytes (%d)", len(doc.Content), app.SkillDocumentMaxBytes)
		}
		if doc.ShownBytes != len(doc.Content) {
			t.Errorf("ShownBytes = %d, want %d", doc.ShownBytes, len(doc.Content))
		}
		if doc.OriginalBytes <= doc.ShownBytes {
			t.Errorf("OriginalBytes = %d, want > ShownBytes (%d)", doc.OriginalBytes, doc.ShownBytes)
		}
	})

	t.Run("missing SKILL.md reports Missing without error", func(t *testing.T) {
		dir := f.home.Path(".claude/skills/ghost")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hola\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sk := domain.Skill{
			ID:   "manual:ghost",
			Name: "ghost",
			Locations: []domain.Location{
				{AgentID: "claude-code", Path: dir, Kind: domain.LocationReal},
			},
		}
		doc, err := a.SkillDocument(sk)
		if err != nil {
			t.Fatalf("SkillDocument(ghost): %v", err)
		}
		if !doc.Missing {
			t.Errorf("expected Missing=true for skill without SKILL.md")
		}
		if !slices.Equal(doc.Companions, []app.CompanionEntry{{Name: "notes.txt", Kind: ports.EntryFile}}) {
			t.Errorf("Companions = %+v", doc.Companions)
		}
	})
}
