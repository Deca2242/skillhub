package updates_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
	"skillhub/internal/updates"
)

// trickyKey is the line every fixture entry carries: an unknown key and a
// value with the characters encoding/json would escape.
const trickyKey = `"note<&>": "q&a <b>"`

func TestApplyThenUndoKeepsALockEntryWithHTMLCharactersByteIdentical(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	lockBefore := f.lockBytes()
	if !strings.Contains(lockBefore, trickyKey) {
		t.Fatalf("fixture lock lacks %s", trickyKey)
	}
	if _, err := f.svc.Apply(context.Background(), approval(c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	if got := f.lockBytes(); got != lockBefore {
		t.Fatalf("lock after apply and undo:\n%s\nwant:\n%s", got, lockBefore)
	}
}

func TestInterruptedApplyThenRecoverKeepsALockEntryWithHTMLCharactersByteIdentical(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	lockBefore := f.lockBytes()
	f.trip.armAfter("Lock.UpdateEntry.done", true)

	if !interrupted(func() { _, _ = f.svc.Apply(context.Background(), approval(c)) }) {
		t.Fatal("the apply was not interrupted")
	}
	f.reopen()
	if strings.Contains(f.lockBytes(), `"skillFolderHash": "`+c.FromSHA+`"`) {
		t.Fatal("interrupted before the lock entry was written")
	}
	if _, err := f.svc.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := f.lockBytes(); got != lockBefore {
		t.Fatalf("lock after recover:\n%s\nwant:\n%s", got, lockBefore)
	}
}

func TestApplyRejectsALockOfAnotherVersionBeforeTouchingTheFolder(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	f.home.WriteFile(".agents/.skill-lock.json", strings.Replace(f.lockBytes(), `"version": 3`, `"version": 4`, 1))
	before := f.agentsTree()

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, ports.ErrUnsupportedLockVersion) || res.Outcome != updates.OutcomeRejected {
		t.Fatalf("result %+v, err %v; want a rejection for the lock version", res, err)
	}
	f.assertAgentsTree(before, "after a refused apply")
	for _, call := range f.files.Calls() {
		if call.Op == "Rename" || call.Op == "CopyTree" {
			t.Fatalf("the folder was touched: %+v", call)
		}
	}
}

func TestApplyRejectsAnEntryWhoseRefChangedAfterStaging(t *testing.T) {
	for _, ref := range []string{"v9", ""} {
		t.Run("ref "+ref, func(t *testing.T) {
			f := newFixture(t)
			c1, v1 := f.publish("main", v1Files)
			f.gh.Tag(repo, "v1", c1)
			f.install(v1Files)
			staged := "v1"
			if ref == "v9" {
				staged = ""
			}
			f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1, ref: staged})
			c2, _ := f.publish("main", v2Files)
			f.gh.Tag(repo, "v1", c2)
			c := f.candidate(targetResult(t, f.check(), target).CandidateID)
			// Another tool changes the ref and keeps the hash.
			f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: v1, ref: ref})
			before := f.agentsTree()

			_, err := f.svc.Apply(context.Background(), approval(c))

			if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, updates.ErrLockEntryChanged) {
				t.Fatalf("err = %v, want a rejection for the changed ref", err)
			}
			f.assertAgentsTree(before, "after a refused apply")
		})
	}
}

// craftedGitHub serves a hand-made root tree; everything else comes from the
// embedded port.
type craftedGitHub struct {
	ports.GitHubPort
	tree treehash.Tree
}

func (c craftedGitHub) Tree(context.Context, string, string, string) (treehash.Tree, error) {
	return c.tree, nil
}

// hostileTree is a root tree whose skills/alpha folder holds nodes.
func hostileTree(nodes ...treehash.Node) treehash.Tree {
	root := treehash.Tree{SHA: strings.Repeat("3", 40), Nodes: []treehash.Node{
		{Path: "skills", Mode: treehash.ModeTree, SHA: strings.Repeat("1", 40)},
		{Path: "skills/alpha", Mode: treehash.ModeTree, SHA: strings.Repeat("2", 40)},
	}}
	for _, n := range nodes {
		n.Path = "skills/alpha/" + n.Path
		root.Nodes = append(root.Nodes, n)
	}
	return root
}

func file(p string, size int64) treehash.Node {
	return treehash.Node{Path: p, Mode: treehash.ModeFile, SHA: strings.Repeat("a", 40), Size: size}
}

func TestCheckBlocksHostileUpstreamTrees(t *testing.T) {
	many := []treehash.Node{}
	for i := range 2001 {
		many = append(many, file(fmt.Sprintf("f%d.md", i), 1))
	}
	deep := strings.Repeat("d/", 24) + "f.md"
	tests := []struct {
		name   string
		nodes  []treehash.Node
		reason domain.BlockReason
	}{
		{"parent element", []treehash.Node{file("../escape.md", 1)}, domain.BlockUnsupportedEntry},
		{".git", []treehash.Node{file(".git/config", 1)}, domain.BlockUnsupportedEntry},
		{".GIT", []treehash.Node{file(".GIT/hooks/post-merge", 1)}, domain.BlockUnsupportedEntry},
		{"backslash", []treehash.Node{file(`a\b.md`, 1)}, domain.BlockUnsupportedEntry},
		{"NUL", []treehash.Node{file("a\x00b.md", 1)}, domain.BlockUnsupportedEntry},
		{"symlink", []treehash.Node{{Path: "link", Mode: treehash.ModeSymlink, SHA: strings.Repeat("b", 40), Size: 4}}, domain.BlockUnsupportedEntry},
		{"submodule", []treehash.Node{{Path: "sub", Mode: treehash.ModeSubmodule, SHA: strings.Repeat("c", 40)}}, domain.BlockUnsupportedEntry},
		{"too many files", many, domain.BlockLimitsExceeded},
		{"too large", []treehash.Node{file("big.md", 65<<20)}, domain.BlockLimitsExceeded},
		{"too deep", []treehash.Node{file(deep, 1)}, domain.BlockLimitsExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.standard()
			svc, err := updates.New(f.deps(craftedGitHub{GitHubPort: f.gh, tree: hostileTree(tt.nodes...)}))
			if err != nil {
				t.Fatal(err)
			}
			before := f.agentsTree()

			report, err := svc.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}

			tc := targetResult(t, report, target)
			if tc.Outcome != updates.CheckBlocked {
				t.Fatalf("outcome = %s (%v), want blocked", tc.Outcome, tc.Err)
			}
			if got := f.candidate(tc.CandidateID).BlockReason; got != tt.reason {
				t.Errorf("reason = %s, want %s", got, tt.reason)
			}
			if n := f.ghCalls("Blob"); n != 0 {
				t.Errorf("%d blobs fetched for a hostile tree", n)
			}
			if entries, _ := os.ReadDir(f.p.StagingDir()); len(entries) != 0 {
				t.Errorf("staging left behind: %v", entries)
			}
			f.assertAgentsTree(before, "after a hostile check")
		})
	}
}

func TestCheckNeverWritesABlobThatDoesNotMatchItsSHA(t *testing.T) {
	f := newFixture(t)
	f.standard()
	f.publish("main", v2Files)
	svc, err := updates.New(f.deps(lyingGitHub{f.gh}))
	if err != nil {
		t.Fatal(err)
	}

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if tc := targetResult(t, report, target); tc.Outcome != updates.CheckBlocked {
		t.Fatalf("outcome = %s, want blocked", tc.Outcome)
	}
	for _, call := range f.files.Calls() {
		if call.Op == "WriteFileAtomic" && strings.HasPrefix(call.Args[0], f.p.StagingDir()) {
			t.Errorf("unverified content written to staging: %s", call.Args[0])
		}
	}
}

func TestCheckBlocksATreeWhoseSHADoesNotMatchItsBlobs(t *testing.T) {
	f := newFixture(t)
	f.standard()
	commit, _ := f.publish("main", v2Files)
	root, err := f.gh.Tree(context.Background(), repo, commit, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range root.Nodes {
		if n.Path == "skills/alpha" {
			root.Nodes[i].SHA = strings.Repeat("f", 40) // every blob is honest
		}
	}
	svc, err := updates.New(f.deps(craftedGitHub{GitHubPort: f.gh, tree: root}))
	if err != nil {
		t.Fatal(err)
	}

	report, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	tc := targetResult(t, report, target)
	if tc.Outcome != updates.CheckBlocked || f.candidate(tc.CandidateID).BlockReason != domain.BlockHashMismatch {
		t.Fatalf("result = %+v, want blocked for a hash mismatch", tc)
	}
}

func TestApplyVerifiesTheLockEntryAfterWritingIt(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	before := f.agentsTree()
	f.lock.dropUpdates = true

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrVerifyFailed) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("result %+v, err %v; want a failed verification", res, err)
	}
	f.assertAgentsTree(before, "after a lost lock write")
}

func TestApplyFailsWhenTheFolderChangesWhileItIsSnapshotted(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	f.files.Fail = func(call fakes.Call) error {
		if call.Op == "CopyTree" && strings.HasPrefix(call.Args[1], f.p.SnapshotsDir()) {
			_ = os.WriteFile(filepath.Join(call.Args[0], "late.md"), []byte("late\n"), 0o644)
		}
		return nil
	}

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrInstalledChanged) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("result %+v, err %v; want a failed snapshot", res, err)
	}
	if got := hashOf(t, f.folder()); got == c.ToSHA {
		t.Error("the folder was replaced although its snapshot does not match it")
	}
	if snaps, _ := f.svc.Snapshots(target); len(snaps) != 0 {
		t.Errorf("snapshots = %+v, want none", snaps)
	}
	if _, ok := f.operation(); ok {
		t.Error("operation record left")
	}
}

func TestUndoFailsAndRevertsWhenTheRestoredFolderDoesNotMatchItsSnapshot(t *testing.T) {
	f := newFixture(t)
	_, applied, _ := f.applied()
	snap := findSnapshot(t, f, applied.SnapshotID)
	if err := os.WriteFile(filepath.Join(snap.FolderCopyPath, "notes.md"), []byte("rotted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterApply := f.agentsTree()

	_, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

	if !errors.Is(err, updates.ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyFailed", err)
	}
	f.assertAgentsTree(afterApply, "after a failed undo")
	if _, ok := f.operation(); ok {
		t.Error("operation record left")
	}
}

func TestPruningKeepsTheNewestFiveAndEveryKeptSnapshot(t *testing.T) {
	f := newFixture(t)
	f.standard()
	ctx := context.Background()
	var applies []string
	cycle := func() {
		t.Helper()
		f.clock.Advance(time.Minute)
		c := f.staged()
		res, err := f.svc.Apply(ctx, approval(c))
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		applies = append(applies, res.SnapshotID)
	}
	cycle()
	f.home.WriteFile(".agents/skills/alpha/notes.md", "edited\n")
	f.clock.Advance(time.Minute)
	undo, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target, Confirm: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	for range 6 {
		cycle()
		f.clock.Advance(time.Minute)
		if _, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target}); err != nil {
			t.Fatalf("Undo: %v", err)
		}
	}

	snaps, err := f.svc.Snapshots(target)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range snaps {
		got = append(got, s.ID)
	}
	want := []string{applies[6], applies[5], applies[4], applies[3], applies[2], undo.SavedSnapshotID}
	if !slices.Equal(got, want) {
		t.Fatalf("snapshots after pruning:\n got %v\nwant %v", got, want)
	}
}

func TestUndoRefusesWhenTheEntryRefChanged(t *testing.T) {
	f := newFixture(t)
	c, _, _ := f.applied()
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: c.ToSHA, ref: "v9"})
	before := f.agentsTree()

	_, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

	if !errors.Is(err, updates.ErrLockEntryChanged) {
		t.Fatalf("err = %v, want ErrLockEntryChanged", err)
	}
	f.assertAgentsTree(before, "after a refused undo")
}

// interruptAfterTheLockWrite interrupts an apply right after its lock write
// and reopens the stores; it returns the candidate and the store before.
func (f *fixture) interruptAfterTheLockWrite() (domain.UpdateCandidate, map[string]string) {
	f.t.Helper()
	f.standard()
	c := f.staged()
	before := f.agentsTree()
	f.trip.armAfter("Lock.UpdateEntry.done", true)
	if !interrupted(func() { _, _ = f.svc.Apply(context.Background(), approval(c)) }) {
		f.t.Fatal("the apply was not interrupted")
	}
	f.reopen()
	return c, before
}

func TestRecoverLeavesALockEntryAnotherToolRewrote(t *testing.T) {
	f := newFixture(t)
	_, before := f.interruptAfterTheLockWrite()
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: "0123"})
	npx := f.lockBytes()

	res, err := f.svc.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := f.lockBytes(); got != npx {
		t.Fatalf("recover rewrote the other tool's entry:\n%s", got)
	}
	if res.Detail == "" {
		t.Error("recover did not say it left the lock entry")
	}
	want := before
	want[".skill-lock.json"] = "-rw-r--r-- " + npx
	f.assertAgentsTree(want, "after recover")
}

func TestRecoverNeverChangesTheRefOfAnEntry(t *testing.T) {
	f := newFixture(t)
	c, before := f.interruptAfterTheLockWrite()
	f.writeLock(lockEntry{name: skillName, source: repo, skillPath: skillPath, hash: c.ToSHA, ref: "v9"})
	npx := f.lockBytes()

	res, err := f.svc.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := f.lockBytes(); got != npx {
		t.Fatalf("recover changed the entry with a new ref:\n%s", got)
	}
	if res.Detail == "" {
		t.Error("recover did not say it left the lock entry")
	}
	want := before
	want[".skill-lock.json"] = "-rw-r--r-- " + npx
	f.assertAgentsTree(want, "after recover")
}

func TestApplyRejectsAStagedPathOutsideTheStagingDirectory(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	elsewhere := filepath.Join(f.p.CacheDir(), "elsewhere")
	if err := os.Rename(c.StagedPath, elsewhere); err != nil {
		t.Fatal(err)
	}
	store := statefile.New(f.p)
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range st.Candidates {
		if st.Candidates[i].ID == c.ID {
			st.Candidates[i].StagedPath = elsewhere
		}
	}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	before := f.agentsTree()

	_, err = f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, updates.ErrRejected) || !errors.Is(err, updates.ErrStagedChanged) {
		t.Fatalf("err = %v, want a rejection for the staged path", err)
	}
	f.assertAgentsTree(before, "after a refused apply")
}

func TestCheckGroupsEntriesByRef(t *testing.T) {
	f := newFixture(t)
	a1, av1 := f.publishSkill("main", "alpha", v1Files)
	_, bv1 := f.publishSkill("main", "beta", v1Files)
	f.gh.Tag(repo, "v1", a1)
	f.installSkill("alpha", v1Files)
	f.installSkill("beta", v1Files)
	f.writeLock(
		lockEntry{name: "alpha", source: repo, skillPath: "skills/alpha/SKILL.md", hash: av1, ref: "v1"},
		lockEntry{name: "beta", source: repo, skillPath: "skills/beta/SKILL.md", hash: bv1},
	)
	f.publishSkill("main", "alpha", v2Files)
	f.publishSkill("main", "beta", v2Files)

	report := f.check()

	if tc := targetResult(t, report, "lock:alpha"); tc.Outcome != updates.CheckUpToDate {
		t.Errorf("pinned alpha = %s (%v), want up_to_date at v1", tc.Outcome, tc.Err)
	}
	if tc := targetResult(t, report, "lock:beta"); tc.Outcome != updates.CheckUpdateAvailable {
		t.Errorf("beta = %s (%v), want update_available on main", tc.Outcome, tc.Err)
	}
}

func TestApplyRevertedAfterJournalingLeavesNothingToUndo(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.staged()
	before := f.agentsTree()
	f.trip.armAfter("State.AppendJournal", false) // the record cannot be cleared

	res, err := f.svc.Apply(context.Background(), approval(c))

	if !errors.Is(err, errInjected) || res.Outcome != updates.OutcomeFailed {
		t.Fatalf("result %+v, err %v; want a failed apply", res, err)
	}
	f.assertAgentsTree(before, "after the revert")
	if _, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}); !errors.Is(err, updates.ErrNothingToUndo) {
		t.Fatalf("Undo = %v, want ErrNothingToUndo", err)
	}
}

func TestUndoSavesOneKeptCopyOfTheSameContent(t *testing.T) {
	f := newFixture(t)
	f.applied()
	f.home.WriteFile(".agents/skills/alpha/notes.md", "edited after the apply\n")
	ctx := context.Background()

	first, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target})
	if !errors.Is(err, updates.ErrUndoNeedsConfirmation) {
		t.Fatalf("Undo = %v", err)
	}
	second, err := f.svc.Undo(ctx, updates.UndoRequest{Target: target, Confirm: true})
	if err != nil {
		t.Fatalf("confirmed Undo: %v", err)
	}

	if second.SavedSnapshotID != first.SavedSnapshotID {
		t.Errorf("saved %s, then %s; want the same copy", first.SavedSnapshotID, second.SavedSnapshotID)
	}
	snaps, _ := f.svc.Snapshots(target)
	kept := 0
	for _, s := range snaps {
		if s.Keep {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("%d kept copies, want 1: %+v", kept, snaps)
	}
}

func TestRecoverRemovesTheIncompleteCopyOfAnInterruptedUndo(t *testing.T) {
	f := newFixture(t)
	_, applied, _ := f.applied()
	// The copy's metadata is the last write of a snapshot.
	f.files.Fail = func(call fakes.Call) error {
		if call.Op == "WriteFileAtomic" && filepath.Base(call.Args[0]) == "snapshot.json" {
			panic(interruption{op: "snapshot.json"})
		}
		return nil
	}
	if !interrupted(func() { _, _ = f.svc.Undo(context.Background(), updates.UndoRequest{Target: target}) }) {
		t.Fatal("the undo was not interrupted")
	}
	f.files.Fail = nil
	f.reopen()
	if entries, _ := os.ReadDir(f.p.SnapshotsDir()); len(entries) != 2 {
		t.Fatalf("snapshots dir = %v, want the apply's and the incomplete copy", entries)
	}

	res, err := f.svc.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	// The entry points say it in the user's language from the count.
	if res.IncompleteSnapshots != 1 || res.Operation.ID != "" {
		t.Errorf("Recover() = %+v, want one incomplete snapshot and no operation", res)
	}

	entries, _ := os.ReadDir(f.p.SnapshotsDir())
	if len(entries) != 1 || entries[0].Name() != applied.SnapshotID {
		t.Fatalf("snapshots dir = %v, want only %s", entries, applied.SnapshotID)
	}
	if _, err := f.svc.Recover(context.Background()); !errors.Is(err, updates.ErrNoOperation) {
		t.Errorf("second Recover = %v, want ErrNoOperation", err)
	}
}

func TestUndoRevertedAfterJournalingCanBeUndoneAgain(t *testing.T) {
	f := newFixture(t)
	_, applied, beforeApply := f.applied()
	afterApply := f.agentsTree()
	f.trip.armAfter("State.AppendJournal", false) // the record cannot be cleared

	_, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})

	if !errors.Is(err, errInjected) {
		t.Fatalf("Undo = %v, want the injected failure", err)
	}
	f.assertAgentsTree(afterApply, "after the reverted undo")
	if _, ok := f.operation(); ok {
		t.Fatal("operation record left")
	}

	res, err := f.svc.Undo(context.Background(), updates.UndoRequest{Target: target})
	if err != nil {
		t.Fatalf("second Undo: %v", err)
	}
	if res.SnapshotID != applied.SnapshotID {
		t.Errorf("undid %s, want %s", res.SnapshotID, applied.SnapshotID)
	}
	f.assertAgentsTree(beforeApply, "after the second undo")
}
