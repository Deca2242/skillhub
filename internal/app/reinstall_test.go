package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/ports"
)

// stowClaude moves ~/.claude to ~/dotfiles/a/claude and leaves ~/.claude as
// a relative symlink to it, as stow does, and registers the plugin clone
// again under its new real path. Every link already in ~/.claude/skills is
// now broken: it was relative to the old real directory.
func stowClaude(t *testing.T, m *machine) {
	t.Helper()
	must(t, os.MkdirAll(m.h.Path("dotfiles/a"), 0o755))
	must(t, os.Rename(m.h.Path(".claude"), m.h.Path("dotfiles/a/claude")))
	must(t, os.Symlink("dotfiles/a/claude", m.h.Path(".claude")))
	clone, err := filepath.EvalSymlinks(m.h.Path(graphtest.KarpathyClone))
	must(t, err)
	m.git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
}

// resolvesTo fails the test unless the link at rel resolves to the store
// folder of key.
func resolvesTo(t *testing.T, m *machine, rel, key string) {
	t.Helper()
	got, err := filepath.EvalSymlinks(m.h.Path(rel))
	if err != nil {
		t.Fatalf("%s does not resolve: %v", rel, err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(m.p.StoreDir(), key))
	must(t, err)
	if got != want {
		t.Fatalf("%s resolves to %s, want %s", rel, got, want)
	}
}

// With ~/.claude a symlink to ~/dotfiles/a/claude, link, install and the
// repoint repair, planned through the app, create a link relative to the
// real link directory, which resolves (L54).
func TestPlannersLinkUnderAStowedClaude(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanUnlink(bg, "find-skills", "claude-code")
	must(t, err)
	m.apply(p)
	stowClaude(t, m)

	p, err = m.app.PlanLink(bg, "find-skills", "claude-code")
	must(t, err)
	m.apply(p)
	resolvesTo(t, m, ".claude/skills/find-skills", "find-skills")

	m.publishTool()
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	p, err = m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	m.apply(p)
	resolvesTo(t, m, ".claude/skills/tidy", "tidy")

	// A link to a store folder that is gone, written as npx writes it:
	// relative to the real link directory, ~/dotfiles/a/claude/skills. The
	// store has a skill with its name to repoint it to.
	must(t, os.Remove(m.h.Path(".claude/skills/agent-browser")))
	m.h.Symlink(".claude/skills/agent-browser", "../../../../.agents/skills/gone/agent-browser")
	id := "broken_link:.claude/skills/agent-browser"
	findings, err := m.app.Doctor(bg)
	must(t, err)
	if !slices.ContainsFunc(findings, func(f app.Finding) bool { return f.ID == id }) {
		t.Fatalf("no finding %s: %+v", id, findings)
	}
	p, err = m.app.PlanRepair(bg, id, string(repair.RepointLink), "")
	must(t, err)
	m.apply(p)
	resolvesTo(t, m, ".claude/skills/agent-browser", "agent-browser")
}

// publishTool publishes acme/tools with one skill, tidy.
func (m *machine) publishTool() {
	m.t.Helper()
	m.gh.AddRepo("acme/tools", "main")
	writeTree(m.t, filepath.Join(m.upstream, "skills", "tidy"), map[string]string{
		"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\nbody\n",
	})
	m.gh.Commit(m.t, "acme/tools", "main", m.upstream)
}

// publish publishes repo with the skill name below skills/, with content
// as its SKILL.md body.
func (m *machine) publish(repo, name, content string) {
	m.t.Helper()
	dir := filepath.Join(m.t.TempDir(), "upstream")
	m.gh.AddRepo(repo, "main")
	writeTree(m.t, filepath.Join(dir, "skills", name), map[string]string{
		"SKILL.md": "---\nname: " + name + "\ndescription: " + name + "\n---\n" + content,
	})
	m.gh.Commit(m.t, repo, "main", dir)
}

// loseGrillMe deletes the store folder of grill-me and its Claude link: its
// lock entry is left without a folder, and Doctor offers reinstall.
func loseGrillMe(t *testing.T, m *machine) app.Proposal {
	t.Helper()
	must(t, os.RemoveAll(m.h.Path(".agents/skills/grill-me")))
	must(t, os.Remove(m.h.Path(".claude/skills/grill-me")))
	findings, err := m.app.Doctor(bg)
	must(t, err)
	i := slices.IndexFunc(findings, func(f app.Finding) bool { return f.Kind == "lock_entry_without_folder" && f.LockKey == "grill-me" })
	if i < 0 {
		t.Fatalf("no lock entry finding: %+v", findings)
	}
	p, err := m.app.PlanRepair(bg, findings[i].ID, string(repair.Reinstall), "")
	must(t, err)
	if p.PlanID != "" || p.Repair == nil || p.Repair.Reinstall == nil {
		t.Fatalf("reinstall offer = %+v", p.Repair)
	}
	return p
}

// homeLines lists every path, mode, file content hash and link content in
// the home, skillhub's state and cache left out: they keep the journal and
// the snapshots of what ran.
func homeLines(t *testing.T, m *machine) []string {
	t.Helper()
	var out []string
	skip := []string{m.p.StateDir(), m.p.CacheDir()}
	err := filepath.WalkDir(m.h.Root(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(skip, p) {
			return filepath.SkipDir
		}
		if slices.ContainsFunc(skip, func(s string) bool { return strings.HasPrefix(s, p+string(filepath.Separator)) }) {
			return nil // a level that only holds skillhub's state or cache
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := p + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			line += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := sha256.Sum256(data)
			line += " " + hex.EncodeToString(s[:])
		}
		out = append(out, line)
		return nil
	})
	must(t, err)
	return out
}

// homeDiff is what differs between two listings of the home, "" when
// nothing does.
func homeDiff(before, after []string) string {
	var b strings.Builder
	for _, l := range before {
		if !slices.Contains(after, l) {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range after {
		if !slices.Contains(before, l) {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// The reinstall repair, end to end through the app: Doctor offers it with
// the entry's source, ResolveInstall prepares that source, PlanReinstall
// plans replacing the entry, the serialized request is approved and
// applied, and undoing it leaves the home as it was before.
func TestReinstallRunsThroughTheApp(t *testing.T) {
	m := newMachine(t)
	offer := loseGrillMe(t, m)
	if offer.Repair.Reinstall.Input != "owner/grill-me/skills/grill-me" {
		t.Fatalf("reinstall input = %q", offer.Repair.Reinstall.Input)
	}
	m.publish("owner/grill-me", "grill-me", "new body\n")
	oldLock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)

	before := homeLines(t, m)
	stateBefore := m.persisted()
	rev, err := m.app.ResolveInstall(bg, offer.Repair.Reinstall.Input)
	must(t, err)
	resolved := homeLines(t, m)

	p, err := m.app.PlanReinstall(bg, rev, []string{"claude-code"})
	must(t, err)
	if p.PlanID == "" || p.Request.Action != app.ActionReinstall || p.Request.Install == nil || p.Install == nil {
		t.Fatalf("PlanReinstall = %+v", p)
	}
	if !strings.Contains(string(p.Install.ReplacedEntry), `"owner/grill-me"`) {
		t.Errorf("replaced entry = %s", p.Install.ReplacedEntry)
	}
	if !hasStep(p, "lockReplace", "grill-me") ||
		!hasStep(p, "move", m.h.Path(".agents/skills/grill-me")) {
		t.Fatalf("reinstall steps = %+v", p.Steps)
	}
	if d := homeDiff(resolved, homeLines(t, m)); d != "" {
		t.Fatal("planning a reinstall wrote to the home")
	}

	data, err := json.Marshal(p.Request)
	must(t, err)
	var back app.Request
	must(t, json.Unmarshal(data, &back))
	m.clock.Advance(3600e9)
	res, err := m.app.ApplyPlan(bg, back, p.PlanID)
	if err != nil {
		t.Fatalf("ApplyPlan of the serialized reinstall: %v", err)
	}
	body, err := os.ReadFile(m.h.Path(".agents/skills/grill-me/SKILL.md"))
	if err != nil || !strings.Contains(string(body), "new body") {
		t.Fatalf("reinstalled SKILL.md = %q, %v", body, err)
	}
	resolvesTo(t, m, ".claude/skills/grill-me", "grill-me")
	lock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if string(lock) == string(oldLock) || !strings.Contains(string(lock), rev.TreeSHA) || strings.Contains(string(lock), `"ref"`) {
		t.Fatalf("lock after reinstall:\n%s", lock)
	}
	if got := m.persisted().Managed["grill-me"]; got.Key != "grill-me" || !slices.Equal(got.Targets, []string{"claude-code"}) {
		t.Errorf("managed record = %+v", got)
	}
	line := m.lastV2()
	if line.Target != "v2:reinstall" || line.SnapshotID != res.SnapshotID {
		t.Fatalf("journal line = %+v", line)
	}

	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	if u.Undo == nil || u.Undo.Of != "reinstall" {
		t.Fatalf("PlanUndo = %+v", u.Undo)
	}
	if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: line.SnapshotID, PlanID: u.PlanID}); err != nil {
		t.Fatalf("undo the reinstall: %v", err)
	}
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Errorf("undoing the reinstall did not leave the home as it was:\n%s", d)
	}
	if lock, err := os.ReadFile(m.p.SkillsLockFile()); err != nil || string(lock) != string(oldLock) {
		t.Errorf("lock after undo:\n%s\nwant:\n%s", lock, oldLock)
	}
	after := m.persisted()
	if fmt.Sprint(after.Managed) != fmt.Sprint(stateBefore.Managed) || fmt.Sprint(after.Parked) != fmt.Sprint(stateBefore.Parked) {
		t.Errorf("state after undo: managed %v parked %v, want %v %v", after.Managed, after.Parked, stateBefore.Managed, stateBefore.Parked)
	}
}

// Each refusal of a reinstall is a typed *RefusalError that keeps its cause,
// and planning it writes nothing.
func TestReinstallRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *machine) string
		kind  error
		cause error
	}{
		{"no lock entry with the key", func(m *machine) string {
			m.publishTool()
			return "acme/tools"
		}, app.ErrNoEntryToReinstall, install.ErrNoEntryToReinstall},
		{"the entry pins a ref", func(m *machine) string {
			lock, err := os.ReadFile(m.p.SkillsLockFile())
			must(m.t, err)
			pinned := strings.Replace(string(lock), `"source": "owner/grill-me",`, `"source": "owner/grill-me",`+"\n      \"ref\": \"v1\",", 1)
			if pinned == string(lock) {
				m.t.Fatal("the lock has no grill-me source to pin")
			}
			must(m.t, os.WriteFile(m.p.SkillsLockFile(), []byte(pinned), 0o644))
			m.publish("owner/grill-me", "grill-me", "body\n")
			return "owner/grill-me/skills/grill-me"
		}, app.ErrEntryPinsRef, install.ErrEntryPinsRef},
		{"another source", func(m *machine) string {
			m.publish("other/grill-me", "grill-me", "body\n")
			return "other/grill-me/skills/grill-me"
		}, app.ErrSourceMismatch, install.ErrSourceMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			loseGrillMe(t, m)
			input := tt.setup(m)
			rev, err := m.app.ResolveInstall(bg, input)
			must(t, err)
			before := homeLines(t, m)
			_, err = m.app.PlanReinstall(bg, rev, []string{"claude-code"})
			var ref *app.RefusalError
			if !errors.As(err, &ref) || !errors.Is(err, tt.kind) || !errors.Is(err, tt.cause) {
				t.Fatalf("PlanReinstall = %v, want a refusal %v caused by %v", err, tt.kind, tt.cause)
			}
			if d := homeDiff(before, homeLines(t, m)); d != "" {
				t.Error("a refused reinstall wrote to the home")
			}
		})
	}
}

// A link directory below a symlink that the planner cannot resolve is a
// typed refusal, not a bare error.
func TestALinkedLinkDirIsARefusal(t *testing.T) {
	cause := fmt.Errorf("link /h/.claude/skills/x: %w: /h/.claude is a symlink", toggle.ErrLinkedLinkDir)
	err := app.RefusalForTest(cause)
	var ref *app.RefusalError
	if !errors.As(err, &ref) || !errors.Is(err, app.ErrLinkedLinkDir) || !errors.Is(err, toggle.ErrLinkedLinkDir) {
		t.Fatalf("refusal(%v) = %v", cause, err)
	}
}

// A parked record without a folder (park_entry of a lock entry whose folder
// is gone) is removed through the app: the preview has no folder, and the
// record goes away.
func TestRemoveOfAParkedRecordWithoutFolder(t *testing.T) {
	m := newMachine(t)
	loseGrillMe(t, m)
	findings, err := m.app.Doctor(bg)
	must(t, err)
	i := slices.IndexFunc(findings, func(f app.Finding) bool { return f.Kind == "lock_entry_without_folder" && f.LockKey == "grill-me" })
	p, err := m.app.PlanRepair(bg, findings[i].ID, string(repair.ParkEntry), "")
	must(t, err)
	m.apply(p)

	p, err = m.app.PlanRemove(bg, "grill-me")
	must(t, err)
	if p.Remove == nil || p.Remove.Folder != "" || !p.Remove.Parked || p.Remove.ParkedEntry == nil {
		t.Fatalf("remove preview = %+v", p.Remove)
	}
	m.apply(p)
	if _, ok := m.persisted().Parked["grill-me"]; ok {
		t.Fatal("remove left the parked record")
	}
}

// RevisionDiff is the diff of a prepared revision against nothing: every
// file is added, even when a folder with the key is in the store.
func TestRevisionDiffIsAgainstNothing(t *testing.T) {
	m := newMachine(t)
	m.publish("owner/grill-me", "grill-me", "new body\n")
	rev, err := m.app.ResolveInstall(bg, "owner/grill-me/skills/grill-me")
	must(t, err)
	diffs, err := m.app.RevisionDiff(bg, rev)
	must(t, err)
	if len(diffs) != 1 || diffs[0].Path != "SKILL.md" {
		t.Fatalf("RevisionDiff = %+v", diffs)
	}
	u := diffs[0].Unified
	if !strings.Contains(u, "+new body") || strings.Contains(u, "\n-") {
		t.Fatalf("diff of SKILL.md:\n%s", u)
	}
}
