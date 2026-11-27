package tui_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/entrypoints/text"
)

func publishCatalogRepo(t *testing.T, mc *machine, count int) {
	t.Helper()
	mc.gh.AddRepo("acme/catalog", "main")
	for i := range count {
		name := fmt.Sprintf("skill-%02d", i)
		writeTree(t, filepath.Join(mc.upstream, "skills", name), map[string]string{
			"SKILL.md": fmt.Sprintf("---\nname: %s\ndescription: desc %s\n---\nbody %s\n", name, name, name),
		})
	}
	mc.gh.Commit(t, "acme/catalog", "main", mc.upstream)
}

func TestInstallPickMultiSkillRepoEntersReviewWithoutInstalling(t *testing.T) {
	mc := newMachine(t)
	publishCatalogRepo(t, mc, 3)

	m := openSized(t, mc.newApp(), 80, 30)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/catalog")
	pressRun(m, "enter")

	out := screen(m)
	if !strings.Contains(out, "skill-00") || !strings.Contains(out, "skill-01") || !strings.Contains(out, "skill-02") {
		t.Fatalf("picker screen did not list the repo skills:\n%s", out)
	}
	if !strings.Contains(out, "enter revisar") {
		t.Fatalf("picker footer missing key hints:\n%s", out)
	}

	pressRun(m, "end")
	pressRun(m, "home")
	pressRun(m, "j")
	pressRun(m, "enter")

	review := screen(m)
	if !strings.Contains(review, "skill-01") || !strings.Contains(review, text.TUIInstallStep(2)) {
		t.Fatalf("selecting skill-01 did not enter review:\n%s", review)
	}
	if mc.exists(".agents/skills/skill-01") {
		t.Fatal("picking a skill installed it without approval")
	}
}

func TestInstallPickEscCancelsToNormal(t *testing.T) {
	mc := newMachine(t)
	publishCatalogRepo(t, mc, 2)

	m := openSized(t, mc.newApp(), 100, 30)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/catalog")
	pressRun(m, "enter")

	if !strings.Contains(screen(m), "skill-00") {
		t.Fatalf("expected picker screen, got:\n%s", screen(m))
	}

	pressRun(m, "esc")
	after := screen(m)
	if strings.Contains(after, "enter revisar") {
		t.Fatalf("esc did not leave picker mode:\n%s", after)
	}
}

func TestInstallPickEscapesHostileTextAndWindowsManySkills(t *testing.T) {
	mc := newMachine(t)
	mc.gh.AddRepo("acme/many", "main")
	writeTree(t, filepath.Join(mc.upstream, "skills", "00-hostile"), map[string]string{
		"SKILL.md": "---\nname: \"bad\\x1b[31m‮name\"\ndescription: d\n---\nbody\n",
	})
	for i := 1; i < 25; i++ {
		name := fmt.Sprintf("item-%02d", i)
		writeTree(t, filepath.Join(mc.upstream, "skills", name), map[string]string{
			"SKILL.md": fmt.Sprintf("---\nname: %s\ndescription: d\n---\nbody\n", name),
		})
	}
	mc.gh.Commit(t, "acme/many", "main", mc.upstream)

	m := openSized(t, mc.newApp(), 80, 24)
	pressRun(m, "3")
	pressRun(m, "enter")
	typeText(m, "acme/many")
	pressRun(m, "enter")

	out := screen(m)
	if strings.ContainsAny(out, "\r‮") {
		t.Fatalf("picker leaked unescaped control/bidi chars:\n%q", out)
	}
	if strings.Contains(out, "item-24") {
		t.Fatalf("expected windowed list to omit item-24 at top in 24-row terminal:\n%s", out)
	}
	pressRun(m, "end")
	outEnd := screen(m)
	if !strings.Contains(outEnd, "item-24") {
		t.Fatalf("expected item-24 visible after pressing end:\n%s", outEnd)
	}
}
