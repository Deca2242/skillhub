package updates_test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// appliedLock is the lock before with the entry's hash moved from one SHA to
// another and updatedAt set to t0, which is all an apply may change.
func appliedLock(before, from, to string) string {
	out := strings.Replace(before, `"skillFolderHash": "`+from+`"`, `"skillFolderHash": "`+to+`"`, 1)
	return strings.Replace(out, `"updatedAt": "2026-01-02T00:00:00.000Z"`, `"updatedAt": "2026-11-21T10:00:00.000Z"`, 1)
}

func TestApplyInstallsExactlyTheStagedVersion(t *testing.T) {
	f := newFixture(t)
	v1 := f.standard()
	c := f.staged()
	lockBefore := f.lockBytes()

	res, err := f.svc.Apply(context.Background(), approval(c))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if res.Outcome != updates.OutcomeApplied || res.Target != target || res.CandidateID != c.ID || res.SnapshotID == "" {
		t.Errorf("result = %+v", res)
	}
	if got := hashOf(t, f.folder()); got != c.ToSHA {
		t.Errorf("installed tree = %s, want upstream %s", got, c.ToSHA)
	}
	if got, want := f.lockBytes(), appliedLock(lockBefore, v1, c.ToSHA); got != want {
		t.Errorf("lock:\n%s\nwant:\n%s", got, want)
	}
	if got := f.candidate(c.ID).State; got != domain.CandidateApplied {
		t.Errorf("candidate state = %s, want applied", got)
	}
	if _, ok := f.operation(); ok {
		t.Error("operation record left after a finished apply")
	}
	j := f.journal()
	last := j[len(j)-1]
	if last.Action != domain.ActionApplied || last.Target != target || last.FromSHA != v1 || last.ToSHA != c.ToSHA ||
		last.SnapshotID != res.SnapshotID || !slices.Equal(last.Labels, c.Labels) {
		t.Errorf("journal = %+v", last)
	}
	snaps, err := f.svc.Snapshots(target)
	if err != nil || len(snaps) != 1 || snaps[0].ID != res.SnapshotID || snaps[0].Cause != domain.SnapshotBeforeApply || snaps[0].Keep {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	if got := hashOf(t, snaps[0].FolderCopyPath); got != v1 {
		t.Errorf("snapshot holds %s, want v1 %s", got, v1)
	}
	if string(snaps[0].LockEntryBefore) == "" || !strings.Contains(string(snaps[0].LockEntryBefore), v1) {
		t.Errorf("snapshot lock entry = %s", snaps[0].LockEntryBefore)
	}
	for _, leftover := range []string{c.StagedPath, f.home.Path(".agents/.skillhub-tmp")} {
		if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", leftover, err)
		}
	}
}

func TestApplyKeepsTheRefAndMovesAnExistingCommit(t *testing.T) {
	f := newFixture(t)
	c1, v1 := f.publish("main", v1Files)
	f.gh.Tag(repo, "v1", c1)
	f.install(v1Files)
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1, ref: "v1", commit: c1})
	c2, _ := f.publish("main", v2Files)
	f.gh.Tag(repo, "v1", c2)
	c := f.candidate(targetResult(t, f.check(), target).CandidateID)
	lockBefore := f.lockBytes()

	if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := strings.Replace(appliedLock(lockBefore, v1, c.ToSHA), `"commit": "`+c1+`"`, `"commit": "`+c2+`"`, 1)
	if got := f.lockBytes(); got != want {
		t.Errorf("lock:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyLeavesOutACommitTheEntryNeverHad(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()

	if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(f.lockBytes(), `"commit"`) {
		t.Errorf("an entry without commit received one:\n%s", f.lockBytes())
	}
}

func TestApplyRejectsAnApprovalThatIsNoLongerTrue(t *testing.T) {
	tests := []struct {
		name string
		// prepare changes the world after staging and returns the approval.
		prepare func(f *fixture, c domain.UpdateCandidate) updates.Approval
		want    error
	}{
		{
			name: "candidate superseded by a newer check",
			prepare: func(f *fixture, c domain.UpdateCandidate) updates.Approval {
				f.publish("main", v3Files)
				f.check()
				return approval(c)
			},
			want: updates.ErrNotPending,
		},
		{
			name: "approved to_sha is not the candidate's",
			prepare: func(_ *fixture, c domain.UpdateCandidate) updates.Approval {
				return updates.Approval{CandidateID: c.ID, ToSHA: c.FromSHA}
			},
			want: updates.ErrApprovalMismatch,
		},
		{
			name: "unknown candidate",
			prepare: func(_ *fixture, c domain.UpdateCandidate) updates.Approval {
				return updates.Approval{CandidateID: "nope", ToSHA: c.ToSHA}
			},
			want: updates.ErrCandidateNotFound,
		},
		{
			name: "installed folder changed after staging",
			prepare: func(f *fixture, c domain.UpdateCandidate) updates.Approval {
				f.home.WriteFile(".agents/skills/alpha/notes.md", "edited by hand\n")
				return approval(c)
			},
			want: updates.ErrInstalledChanged,
		},
		{
			name: "staged folder changed after staging",
			prepare: func(f *fixture, c domain.UpdateCandidate) updates.Approval {
				if err := os.WriteFile(filepath.Join(c.StagedPath, "notes.md"), []byte("tampered\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return approval(c)
			},
			want: updates.ErrStagedChanged,
		},
		{
			name: "staged folder removed",
			prepare: func(f *fixture, c domain.UpdateCandidate) updates.Approval {
				if err := os.RemoveAll(c.StagedPath); err != nil {
					t.Fatal(err)
				}
				return approval(c)
			},
			want: updates.ErrStagedChanged,
		},
		{
			name: "lock entry rewritten by npx skills after staging",
			prepare: func(f *fixture, c domain.UpdateCandidate) updates.Approval {
				f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: c.ToSHA})
				return approval(c)
			},
			want: updates.ErrLockEntryChanged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.standard()
			c := f.staged()
			a := tt.prepare(f, c)
			stateBefore := f.candidate(c.ID).State
			before := f.agentsTree()

			res, err := f.svc.Apply(context.Background(), a)

			if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want rejected with %v", err, tt.want)
			}
			if res.Outcome != updates.OutcomeRejected || !errors.Is(res.Err, tt.want) {
				t.Errorf("result = %+v", res)
			}
			f.assertAgentsTree(before, "after a rejected apply")
			if got := f.candidate(c.ID).State; got != stateBefore {
				t.Errorf("candidate state %s -> %s", stateBefore, got)
			}
			if _, ok := f.operation(); ok {
				t.Error("a rejected apply recorded an operation")
			}
		})
	}
}

func TestApplyFailureAtEveryStepLeavesTheStoreAndTheLockIdentical(t *testing.T) {
	succeeded := false
	for at := 1; !succeeded; at++ {
		f := newFixture(t)
		f.standard()
		c := f.staged()
		before := f.agentsTree()
		f.trip.arm(at, false)

		res, err := f.svc.Apply(context.Background(), approval(c))

		ops := f.trip.ops
		f.trip.arm(0, false)
		if at > len(ops) {
			// Nothing failed: every step ran and the apply must be whole.
			if err != nil {
				t.Fatalf("apply with no failure: %v", err)
			}
			succeeded = true
		}
		if err == nil {
			if got := hashOf(t, f.folder()); got != c.ToSHA {
				t.Fatalf("failure at call %d (%s): apply succeeded but installed %s", at, ops[at-1], got)
			}
			continue
		}
		step := ops[at-1]
		if res.Outcome == updates.OutcomeApplied || !errors.Is(err, errInjected) {
			t.Fatalf("failure at call %d (%s): result %+v, err %v", at, step, res, err)
		}
		f.assertAgentsTree(before, "failure at "+step)
		if op, ok := f.operation(); ok {
			t.Fatalf("failure at call %d (%s) left operation %+v", at, step, op)
		}
		if got := f.candidate(c.ID).State; got != domain.CandidatePending {
			t.Fatalf("failure at call %d (%s): candidate %s, want pending", at, step, got)
		}
		if snaps, _ := f.svc.Snapshots(target); len(snaps) != 0 {
			t.Fatalf("failure at call %d (%s) left snapshots %+v", at, step, snaps)
		}
	}
}

func TestApplyRevertsWhenVerificationFails(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	before := f.agentsTree()
	// Something writes into the new folder between the copy and the swap.
	f.files.Fail = func(call fakes.Call) error {
		if call.Op == "Rename" && filepath.Dir(call.Args[1]) == f.p.StoreDir() &&
			filepath.Dir(call.Args[0]) == f.home.Path(".agents/.skillhub-tmp") && filepath.Base(call.Args[0]) == skillName {
			_ = os.WriteFile(filepath.Join(call.Args[0], "intruder.md"), []byte("x"), 0o644)
		}
		return nil
	}

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrVerifyFailed) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("result %+v, err %v; want a failed verification", res, err)
	}
	f.assertAgentsTree(before, "after a failed verification")
	j := f.journal()
	if last := j[len(j)-1]; last.Action != domain.ActionFailed || last.Target != target {
		t.Errorf("journal = %+v, want failed", last)
	}
}

func TestApplyRefusesWhileAnOperationIsInterrupted(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	op := domain.Operation{ID: "op-1", Kind: domain.OperationApply, Target: "lock:other", RestoreSnapshotID: "snap", StartedAt: t0}
	if err := f.state.inner.RecordOperation(op); err != nil {
		t.Fatal(err)
	}
	before := f.agentsTree()

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrInterrupted) || res.Outcome != updates.OutcomeRejected {
		t.Fatalf("result %+v, err %v; want ErrInterrupted", res, err)
	}
	f.assertAgentsTree(before, "after a refused apply")
	if err := f.svc.Skip(context.Background(), approval(c)); !errors.Is(err, updates.ErrInterrupted) {
		t.Errorf("Skip: %v, want ErrInterrupted", err)
	}
	if _, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}); !errors.Is(err, updates.ErrInterrupted) {
		t.Errorf("Undo: %v, want ErrInterrupted", err)
	}
	if got, ok := f.operation(); !ok || got != op {
		t.Errorf("operation = %+v, %v; want it untouched", got, ok)
	}
}

// npxWrites rewrites the lock on the given reads of Apply, the way npx skills
// rewrites the whole file, adding an entry named after the read.
func (f *fixture) npxWrites(v1 string, reads ...int) {
	f.lock.reads = 0
	f.lock.beforeRead = func(n int) {
		if slices.Contains(reads, n) {
			f.writeLock(
				lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1},
				lockEntry{name: "npx" + string(rune('a'+n)), source: "other/repo", skillPath: "SKILL.md", hash: "abc"},
			)
		}
	}
}

func TestApplyRetriesOnceWhenNpxSkillsWritesTheLock(t *testing.T) {
	f := newFixture(t)
	v1 := f.standard()
	c := f.staged()
	f.npxWrites(v1, 2) // read 1 revalidates; read 2 is the check before the swap

	if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	lock := f.lockBytes()
	if !strings.Contains(lock, `"npxc"`) || !strings.Contains(lock, c.ToSHA) {
		t.Errorf("lock lost the npx skills entry or the update:\n%s", lock)
	}
	if got := hashOf(t, f.folder()); got != c.ToSHA {
		t.Errorf("installed %s, want %s", got, c.ToSHA)
	}
}

func TestApplyFailsAndRevertsWhenNpxSkillsKeepsWritingTheLock(t *testing.T) {
	f := newFixture(t)
	v1 := f.standard()
	c := f.staged()
	store := f.agentsTree()
	f.npxWrites(v1, 2, 3)

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrLockConcurrent) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("result %+v, err %v; want ErrLockConcurrent", res, err)
	}
	want := maps.Clone(store)
	want[".skill-lock.json"] = "-rw-r--r-- " + f.lockBytes()
	f.assertAgentsTree(want, "after npx skills kept writing")
	if !strings.Contains(f.lockBytes(), `"npxd"`) {
		t.Errorf("npx skills' last write was lost:\n%s", f.lockBytes())
	}
	if got := f.candidate(c.ID).State; got != domain.CandidatePending {
		t.Errorf("candidate %s, want pending", got)
	}
}

func TestApplyFailsWhenNpxSkillsRewritesTheTargetEntry(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	f.lock.reads = 0
	f.lock.beforeRead = func(n int) {
		if n == 2 {
			f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: "0123"})
		}
	}
	store := f.agentsTree()

	_, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrLockEntryChanged) {
		t.Fatalf("err = %v, want ErrLockEntryChanged", err)
	}
	want := maps.Clone(store)
	want[".skill-lock.json"] = "-rw-r--r-- " + f.lockBytes()
	f.assertAgentsTree(want, "after npx skills rewrote the entry")
	if !strings.Contains(f.lockBytes(), `"0123"`) {
		t.Errorf("npx skills' entry was overwritten:\n%s", f.lockBytes())
	}
}

// twoSkills installs alpha and beta at v1, both in the lock, publishes v2 of
// both and checks; it returns their candidates.
func (f *fixture) twoSkills() (alpha, beta domain.UpdateCandidate) {
	f.t.Helper()
	_, a1 := f.publishSkill("main", "alpha", v1Files)
	_, b1 := f.publishSkill("main", "beta", v1Files)
	f.installSkill("alpha", v1Files)
	f.installSkill("beta", v1Files)
	f.writeLock(
		lockEntry{name: "alpha", source: repo, skillPath: "skills/alpha/SKILL.md", hash: a1},
		lockEntry{name: "beta", source: repo, skillPath: "skills/beta/SKILL.md", hash: b1},
	)
	f.publishSkill("main", "alpha", v2Files)
	f.publishSkill("main", "beta", v3Files)
	r := f.check()
	return f.candidate(targetResult(f.t, r, "lock:alpha").CandidateID), f.candidate(targetResult(f.t, r, "lock:beta").CandidateID)
}

func TestApplyAllGoesOnAfterAFailureAndReportsPerTarget(t *testing.T) {
	f := newFixture(t)
	alpha, beta := f.twoSkills()
	alphaBefore := hashOf(t, filepath.Join(f.p.StoreDir(), "alpha"))
	f.files.Fail = func(call fakes.Call) error {
		if call.Op == "Rename" && call.Args[0] == filepath.Join(f.p.StoreDir(), "alpha") {
			return errInjected
		}
		return nil
	}

	results := f.svc.ApplyAll(context.Background(), []updates.Approval{
		approval(alpha),
		{CandidateID: beta.ID, ToSHA: alpha.ToSHA},
		approval(beta),
	})

	got := make([]updates.Outcome, len(results))
	for i, r := range results {
		got[i] = r.Outcome
	}
	want := []updates.Outcome{updates.OutcomeFailed, updates.OutcomeRejected, updates.OutcomeApplied}
	if !slices.Equal(got, want) {
		t.Fatalf("outcomes = %v, want %v (%+v)", got, want, results)
	}
	if results[0].Target != "lock:alpha" || results[2].Target != "lock:beta" || !errors.Is(results[1].Err, updates.ErrApprovalMismatch) {
		t.Errorf("results = %+v", results)
	}
	if h := hashOf(t, filepath.Join(f.p.StoreDir(), "alpha")); h != alphaBefore {
		t.Errorf("failed alpha not reverted: %s", h)
	}
	if h := hashOf(t, filepath.Join(f.p.StoreDir(), "beta")); h != beta.ToSHA {
		t.Errorf("beta = %s, want %s", h, beta.ToSHA)
	}
}

func TestApplyAllLeavesOutLocallyModifiedCandidates(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.home.WriteFile(".agents/skills/alpha/mine.md", "my edit\n")
	c := f.staged()
	before := f.agentsTree()

	results := f.svc.ApplyAll(context.Background(), []updates.Approval{approval(c)})

	if len(results) != 1 || results[0].Outcome != updates.OutcomeExcluded || !errors.Is(results[0].Err, updates.ErrLocallyModifiedExcluded) {
		t.Fatalf("results = %+v", results)
	}
	f.assertAgentsTree(before, "after ApplyAll excluded the candidate")

	// One by one it applies, and its snapshot is kept from pruning.
	res, err := f.svc.Apply(context.Background(), approval(c))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	snaps, _ := f.svc.Snapshots(target)
	if len(snaps) != 1 || snaps[0].ID != res.SnapshotID || !snaps[0].Keep {
		t.Errorf("snapshots = %+v, want one kept", snaps)
	}
}
