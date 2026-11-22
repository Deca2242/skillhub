package updates_test

import (
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// webEntry is the entry npx skills 1.9.0, or skillhub, writes for a skill
// of a website that publishes /.well-known/agent-skills/ (L84).
const webEntry = `    "ui-taste": {
      "source": "uizze.sh",
      "sourceType": "well-known",
      "sourceUrl": "https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md",
      "skillFolderHash": "",
      "sourceBaseUrl": "https://uizze.sh",
      "wellKnownDigest": "sha256:2d434533f48272256b0cc82ef373d04fb12060ed1a004997719336d6cf1cd814",
      "installedAt": "2026-11-22T10:00:00.000Z",
      "updatedAt": "2026-11-22T10:00:00.000Z"
    }`

// A website skill is not checked yet, and says why: the check neither fails
// nor marks an error, it reads nothing of the site, and it changes nothing.
func TestCheckLeavesAWebsiteSkillForLater(t *testing.T) {
	f := newFixture(t)
	writeTree(t, f.home.Path(".agents/skills/ui-taste"), map[string]string{
		"SKILL.md": "---\nname: ui-taste\ndescription: Taste for UI.\n---\n\nUse taste.\n",
	})
	f.home.WriteFile(".agents/.skill-lock.json", "{\n  \"version\": 3,\n  \"skills\": {\n"+webEntry+"\n  }\n}\n")
	before, lock := f.agentsTree(), f.lockBytes()

	report := f.check()

	tc := targetResult(t, report, domain.LockID("ui-taste"))
	if tc.Outcome != updates.CheckNotChecked || tc.Err != nil || !tc.WebSource {
		t.Errorf("ui-taste = %+v, want not_checked, no error, a web source", tc)
	}
	if report.Failed() {
		t.Error("the check failed over a website skill")
	}
	if len(report.NewCandidates) != 0 {
		t.Errorf("new candidates = %v", report.NewCandidates)
	}
	f.assertAgentsTree(before, "after a check")
	if got := f.lockBytes(); got != lock {
		t.Errorf("lock after a check:\n%s", got)
	}
}

// A GitHub entry is no web source.
func TestCheckMarksOnlyWebsiteSkillsAsWebSources(t *testing.T) {
	f := newFixture(t)
	_, v1 := f.publish("main", v1Files)
	f.install(v1Files)
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1})
	if tc := targetResult(t, f.check(), domain.LockID(skillName)); tc.WebSource {
		t.Errorf("%s = %+v, want no web source", skillName, tc)
	}
}
