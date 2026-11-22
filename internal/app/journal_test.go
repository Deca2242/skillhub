package app_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// failJournal fails the journal write number fail, counting from 1, and lets
// every other write through.
type failJournal struct {
	ports.StateStore
	fail, writes int
}

func (f *failJournal) AppendJournal(e domain.HistoryEntry) error {
	f.writes++
	if f.writes == f.fail {
		return fmt.Errorf("journal write %d: disk full", f.writes)
	}
	return f.StateStore.AppendJournal(e)
}

// withState rebuilds the machine's app over state, as newMachine composes it.
func (m *machine) withState(state ports.StateStore) {
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
		ProcDir: m.proc,
	})
	must(m.t, err)
}

// settle recovers until no version 2 operation is pending, at most three
// times, and fails the test if one is still pending.
func settle(t *testing.T, m *machine) {
	t.Helper()
	for range 3 {
		pending, err := m.app.ManagedPending()
		must(t, err)
		if !pending {
			return
		}
		if _, err := m.app.RecoverOperation(bg); err != nil {
			t.Logf("recover: %v", err)
		}
	}
	if pending, _ := m.app.ManagedPending(); pending {
		t.Fatal("the operation is still pending after three recovers")
	}
}

// A journal write that fails while an install or a reinstall runs, or while
// it is recovered, never takes the staging the recovery needs: after the
// recovers, the home is as before the revision was resolved, or the skill is
// fully installed with no staging left (regression of B2).
func TestAFailedJournalWriteIsRecoveredWithTheStaging(t *testing.T) {
	for _, action := range []string{"install", "reinstall"} {
		for fail := 1; fail <= 3; fail++ {
			t.Run(fmt.Sprintf("%s, journal write %d", action, fail), func(t *testing.T) {
				m := newMachine(t)
				input, key := "acme/tools", "tidy"
				if action == "reinstall" {
					loseGrillMe(t, m)
					m.publish("owner/grill-me", "grill-me", "new body\n")
					input, key = "owner/grill-me/skills/grill-me", "grill-me"
				} else {
					m.publishTool()
				}
				before := homeLines(t, m)
				m.withState(&failJournal{StateStore: m.state, fail: fail})

				rev, err := m.app.ResolveInstall(bg, input)
				must(t, err)
				var p app.Proposal
				if action == "reinstall" {
					p, err = m.app.PlanReinstall(bg, rev, []string{"claude-code"})
				} else {
					p, err = m.app.PlanInstall(bg, rev, []string{"claude-code"})
				}
				must(t, err)
				res, err := m.app.ApplyPlan(bg, p.Request, p.PlanID)
				if err != nil && res.SnapshotID == "" {
					// What an entry point does with a revision that did not run.
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
				if _, err := os.Stat(filepath.Join(m.p.StoreDir(), key, "SKILL.md")); err != nil {
					t.Fatalf("neither as before nor installed:\n%s", homeDiff(before, after))
				}
				resolvesTo(t, m, ".claude/skills/"+key, key)
			})
		}
	}
}

// While a version 2 record is pending, DiscardRevision leaves the staging
// alone: it belongs to the pending operation.
func TestDiscardRevisionWaitsForAPendingOperation(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	crashAfterBegin(t, m, p)
	if err := m.app.DiscardRevision(rev); err != nil && !errors.Is(err, app.ErrOperationPending) {
		t.Fatalf("DiscardRevision with a pending record = %v", err)
	}
	if _, err := os.Stat(rev.Prepared.StagedPath); err != nil {
		t.Fatalf("DiscardRevision touched the staging of a pending operation: %v", err)
	}
}

// An undo refused while another install is pending, its copy already in
// the store and the staging empty, does not tidy that staging away: the
// pending install's recover moves the copy back there.
func TestARefusedUndoLeavesThePendingStaging(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	first := m.apply(p)

	m.publish("acme/more", "neat", "neat body\n")
	m.withState(&failJournal{StateStore: m.state, fail: 1})
	rev, err = m.app.ResolveInstall(bg, "acme/more")
	must(t, err)
	p, err = m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	if res, err := m.app.ApplyPlan(bg, p.Request, p.PlanID); err == nil || res.SnapshotID != "" {
		t.Fatalf("the install with a failing journal = %+v, %v", res, err)
	}
	if pending, _ := m.app.ManagedPending(); !pending {
		t.Skip("the failed journal write left nothing pending")
	}

	undo := app.Request{Action: app.ActionUndo, Snapshot: first.SnapshotID}
	if _, err := m.app.ApplyPlan(bg, undo, "any"); err == nil {
		t.Fatal("an undo ran while an operation was pending")
	}
	if !m.exists(".agents/.skillhub/staging") {
		t.Fatal("the refused undo removed the staging the pending install needs")
	}
	settle(t, m)
	if m.exists(".agents/.skillhub/staging/" + filepath.Base(rev.Prepared.StagedPath)) {
		t.Fatal("recover left the staged copy")
	}
}
