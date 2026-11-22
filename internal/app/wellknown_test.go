package app_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// The site of these tests publishes ui-taste as uizze.sh does (L84).
const (
	siteIndex = "https://uizze.sh/.well-known/agent-skills/index.json"
	siteSkill = "https://uizze.sh/.well-known/agent-skills/ui-taste"
	siteMD    = "---\nname: ui-taste\ndescription: Taste for UI.\n---\n\nUse taste.\n"
)

func uizze() *fakes.Web {
	w := fakes.NewWeb()
	w.Serve(siteIndex, []byte(`{"skills":[{"name":"ui-taste","description":"Taste for UI.","files":["SKILL.md","references/palette.md"]}]}`))
	w.Serve(siteSkill+"/SKILL.md", []byte(siteMD))
	w.Serve(siteSkill+"/references/palette.md", []byte("# Palette\n"))
	return w
}

// withWeb rebuilds the machine's app over state and web, as cmd/skillhub
// composes it; a nil web leaves the dependency out.
func (m *machine) withWeb(state ports.StateStore, web ports.HTTPSFetcher) {
	m.t.Helper()
	files := fsadapter.NewFileOps(m.p.Guard())
	agents, err := data.LoadAgents(m.p, files)
	must(m.t, err)
	lock := skillslock.New(m.p, files)
	m.app, err = app.New(app.Deps{
		Paths: m.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(m.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(m.p.Guard()),
		State: state, Settings: m.settings, GitHub: m.gh, Git: m.git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: m.clock,
		ProcDir: m.proc, Web: web,
	})
	must(m.t, err)
}

// A website install goes through the same review, plan, apply and undo as a
// GitHub one; the undo leaves the home byte for byte as before.
func TestAWebInstallIsReviewedPlannedAppliedAndUndone(t *testing.T) {
	m := newMachine(t)
	m.withWeb(m.state, uizze())
	before := homeLines(t, m)
	lock0, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)

	rev, err := m.app.ResolveInstall(bg, "https://uizze.sh@ui-taste")
	if err != nil {
		t.Fatalf("ResolveInstall = %v", err)
	}
	if rev.Site != "https://uizze.sh" || rev.Repo != "uizze.sh" || rev.Key != "ui-taste" || rev.Commit != "" ||
		rev.FollowsDefaultBranch || len(rev.FileChanges) != 2 {
		t.Errorf("revision = %+v", rev)
	}
	diffs, err := m.app.RevisionDiff(bg, rev)
	if err != nil || len(diffs) != 2 || !strings.Contains(diffs[0].Unified, "+Use taste.") {
		t.Errorf("RevisionDiff = %+v, %v", diffs, err)
	}
	p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
	if err != nil {
		t.Fatalf("PlanInstall = %v", err)
	}
	if p.Install == nil || p.Install.Site != "https://uizze.sh" || p.Install.Source != "uizze.sh" ||
		!strings.Contains(string(p.Install.LockEntry), `"sourceType": "well-known"`) || p.Operation != "v2:install" {
		t.Fatalf("proposal = %+v", p.Install)
	}
	res := m.apply(p)

	lock, err := skillslock.New(m.p, fsadapter.NewFileOps(m.p.Guard())).Read()
	must(t, err)
	i := slices.IndexFunc(lock.Entries, func(e domain.LockEntry) bool { return e.Name == "ui-taste" })
	if i < 0 || lock.Entries[i].SourceType != "well-known" || lock.Entries[i].Source != "uizze.sh" || lock.Entries[i].Ref != "" {
		t.Fatalf("lock entries = %+v", lock.Entries)
	}
	if _, err := os.Stat(filepath.Join(m.p.StoreDir(), "ui-taste", "references", "palette.md")); err != nil {
		t.Errorf("the store folder misses a file: %v", err)
	}
	resolvesTo(t, m, ".claude/skills/ui-taste", "ui-taste")
	if m.exists(".agents/.skillhub/staging") {
		t.Error("the staging is left after the install")
	}

	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	m.apply(u)
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Fatalf("undo of the web install left:\n%s", d)
	}
	if after, err := os.ReadFile(m.p.SkillsLockFile()); err != nil || string(after) != string(lock0) {
		t.Errorf("lock after undo:\n%s\nwant\n%s", after, lock0)
	}
}

// Planning again from the same request, as approving does, gives the same
// plan: the approval binds the exact reviewed tree and lock entry.
func TestAWebInstallPlansAgainToTheSameIdentifier(t *testing.T) {
	m := newMachine(t)
	m.withWeb(m.state, uizze())
	rev, err := m.app.ResolveInstall(bg, "https://uizze.sh@ui-taste")
	must(t, err)
	p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	again, err := m.app.Plan(bg, p.Request)
	if err != nil || again.PlanID != p.PlanID {
		t.Fatalf("Plan again = %s, %v; want %s", again.PlanID, err, p.PlanID)
	}
	// A request whose reviewed tree changed is another plan.
	changed := p.Request
	inst := *changed.Install
	inst.Revision.Prepared.Resolved.Digest = "sha256:" + strings.Repeat("0", 64)
	changed.Install = &inst
	if other, err := m.app.Plan(bg, changed); err == nil && other.PlanID == p.PlanID {
		t.Error("a request with another digest planned to the same identifier")
	}
	must(t, m.app.DiscardRevision(rev))
}

func TestResolveInstallRefusesAWebSourceItCannotInstall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		web   func(*fakes.Web)
		kind  error
	}{
		{"an http site", "http://uizze.sh@ui-taste", nil, app.ErrUnsupportedSource},
		{"no index", "https://example.com@x", nil, app.ErrNoWellKnown},
		{"a skill the site lacks", "https://uizze.sh@missing", nil, app.ErrNoSkill},
		{"an unreachable site", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Fail(siteIndex, &fakes.WebError{URL: siteIndex, Reason: "offline"})
		}, app.ErrSiteUnavailable},
		{"no curl", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Fail(siteIndex, &fakes.WebError{URL: siteIndex, Reason: "curl_missing"})
		}, app.ErrSiteUnavailable},
		{"a file that fails", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Fail(siteSkill+"/references/palette.md", &fakes.WebError{URL: siteSkill, Reason: "http", Status: 500})
		}, app.ErrSiteUnavailable},
		{"a file too large", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Fail(siteSkill+"/references/palette.md", &fakes.WebError{URL: siteSkill, Reason: "too_large"})
		}, app.ErrWebTooLarge},
		{"an unsafe file", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Serve(siteIndex, []byte(`{"skills":[{"name":"ui-taste","description":"d","files":["SKILL.md","a b.md"]}]}`))
		}, app.ErrUnsafeWebFile},
		{"a digest mismatch", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Serve(siteIndex, []byte(`{"$schema":"https://schemas.agentskills.io/discovery/0.2.0/schema.json","skills":[`+
				`{"name":"ui-taste","type":"skill-md","description":"d","url":"ui-taste/SKILL.md","digest":"sha256:`+strings.Repeat("a", 64)+`"}]}`))
		}, app.ErrWebDigestMismatch},
		{"an archive", "https://uizze.sh@ui-taste", func(w *fakes.Web) {
			w.Serve(siteIndex, []byte(`{"$schema":"https://schemas.agentskills.io/discovery/0.2.0/schema.json","skills":[`+
				`{"name":"ui-taste","type":"archive","description":"d","url":"x.tgz","digest":"sha256:`+strings.Repeat("a", 64)+`"}]}`))
		}, app.ErrWebArchive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			w := uizze()
			if tt.web != nil {
				tt.web(w)
			}
			m.withWeb(m.state, w)
			before := homeLines(t, m)
			_, err := m.app.ResolveInstall(bg, tt.input)
			re, ok := errors.AsType[*app.RefusalError](err)
			if !ok || !errors.Is(err, tt.kind) {
				t.Fatalf("ResolveInstall = %v, want a refusal %v", err, tt.kind)
			}
			if re.Kind != tt.kind {
				t.Errorf("kind = %v, want %v", re.Kind, tt.kind)
			}
			if d := homeDiff(before, homeLines(t, m)); d != "" {
				t.Errorf("a refused web install left:\n%s", d)
			}
		})
	}
}

func TestResolveInstallOfAWebSourceNeedsTheFetcher(t *testing.T) {
	m := newMachine(t)
	m.withWeb(m.state, nil)
	if _, err := m.app.ResolveInstall(bg, "https://uizze.sh@ui-taste"); !errors.Is(err, app.ErrMissingDependency) {
		t.Fatalf("ResolveInstall without a fetcher = %v, want ErrMissingDependency", err)
	}
}

// While a version 2 record is pending, resolving a web install touches
// nothing, not even the network: the staging belongs to that operation (L63).
func TestResolveInstallOfAWebSourceWaitsForAPendingOperation(t *testing.T) {
	m := newMachine(t)
	w := uizze()
	m.withWeb(m.state, w)
	stray := strayCopy(t, m)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	crashAfterBegin(t, m, p)
	before := homeLines(t, m)

	if _, err := m.app.ResolveInstall(bg, "https://uizze.sh@ui-taste"); !errors.Is(err, app.ErrOperationPending) {
		t.Fatalf("ResolveInstall with a pending record = %v, want ErrOperationPending", err)
	}
	if calls := w.Calls(); len(calls) != 0 {
		t.Errorf("ResolveInstall read the site while an operation was pending: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(stray, "SKILL.md")); err != nil {
		t.Fatalf("ResolveInstall touched the staging of a pending operation: %v", err)
	}
	if d := homeDiff(before, homeLines(t, m)); d != "" {
		t.Fatalf("ResolveInstall touched the home of a pending operation:\n%s", d)
	}
}

// A journal write that fails while a web install runs never takes the
// staging recover needs: after recover the home is as before the revision
// was resolved, or the skill is installed with no staging left (L63).
func TestAFailedJournalWriteDuringAWebInstallIsRecovered(t *testing.T) {
	for fail := 1; fail <= 3; fail++ {
		t.Run(fmt.Sprintf("journal write %d", fail), func(t *testing.T) {
			m := newMachine(t)
			before := homeLines(t, m)
			m.withWeb(&failJournal{StateStore: m.state, fail: fail}, uizze())
			rev, err := m.app.ResolveInstall(bg, "https://uizze.sh@ui-taste")
			must(t, err)
			p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
			must(t, err)
			res, err := m.app.ApplyPlan(bg, p.Request, p.PlanID)
			if err != nil && res.SnapshotID == "" {
				if derr := m.app.DiscardRevision(rev); derr != nil {
					t.Fatalf("DiscardRevision: %v", derr)
				}
			}
			settle(t, m)
			after := homeLines(t, m)
			if homeDiff(before, after) == "" {
				return
			}
			if m.exists(".agents/.skillhub/staging") {
				t.Fatalf("the staging is left:\n%s", homeDiff(before, after))
			}
			if _, err := os.Stat(filepath.Join(m.p.StoreDir(), "ui-taste", "SKILL.md")); err != nil {
				t.Fatalf("neither as before nor installed:\n%s", homeDiff(before, after))
			}
			resolvesTo(t, m, ".claude/skills/ui-taste", "ui-taste")
		})
	}
}
