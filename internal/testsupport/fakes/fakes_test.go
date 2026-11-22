package fakes_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// Every double implements its port.
var (
	_ ports.SkillDirScanner   = (*fakes.SkillDirScanner)(nil)
	_ ports.FileOps           = (*fakes.FileOps)(nil)
	_ ports.FileOps           = (*fakes.FailingFileOps)(nil)
	_ ports.SkillsLockStore   = (*fakes.SkillsLockStore)(nil)
	_ ports.GitHubPort        = (*fakes.GitHub)(nil)
	_ ports.GitPort           = (*fakes.Git)(nil)
	_ ports.StateStore        = (*fakes.StateStore)(nil)
	_ ports.SettingsStore     = (*fakes.SettingsStore)(nil)
	_ ports.Notifier          = (*fakes.Notifier)(nil)
	_ ports.BackgroundService = (*fakes.BackgroundService)(nil)
	_ ports.ProcessRunner     = (*fakes.ProcessRunner)(nil)
	_ ports.Clock             = (*fakes.Clock)(nil)
)

var errPlanted = errors.New("planted failure")

func failOn(op string) func(fakes.Call) error {
	return func(c fakes.Call) error {
		if c.Op == op {
			return errPlanted
		}
		return nil
	}
}

// writeRepo writes a fixture repository folder and returns its root.
func writeRepo(t *testing.T, files map[string]string, exec ...string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if slices.Contains(exec, rel) {
			mode = 0o755
		}
		if err := os.WriteFile(abs, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(abs, mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGitHubServesTreesAndBlobsBuiltFromAFixtureRepository(t *testing.T) {
	ctx := context.Background()
	v1 := writeRepo(t, map[string]string{
		"README.md":                   "repo\n",
		"skills/a/SKILL.md":           "---\nname: a\n---\n",
		"skills/a/scripts/run.sh":     "#!/bin/sh\n",
		"skills/a/references/deep.md": "deep\n",
		"skills/b/SKILL.md":           "---\nname: b\n---\n",
	}, "skills/a/scripts/run.sh")

	gh := fakes.NewGitHub()
	gh.AddRepo("owner/repo", "main")
	c1 := gh.Commit(t, "owner/repo", "main", v1)

	if branch, err := gh.DefaultBranch(ctx, "owner/repo"); err != nil || branch != "main" {
		t.Fatalf("DefaultBranch = %q, %v", branch, err)
	}
	if got, err := gh.ResolveCommit(ctx, "owner/repo", "main"); err != nil || got != c1 {
		t.Fatalf("ResolveCommit(main) = %q, %v; want %s", got, err, c1)
	}

	tree, err := gh.Tree(ctx, "owner/repo", c1, "skills/a")
	if err != nil {
		t.Fatalf("Tree(skills/a): %v", err)
	}
	local, err := treehash.Dir(filepath.Join(v1, "skills", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if tree.SHA != local.SHA || len(tree.Nodes) != len(local.Nodes) {
		t.Fatalf("Tree(skills/a) = %s with %d nodes, want the fixture folder's %s with %d", tree.SHA, len(tree.Nodes), local.SHA, len(local.Nodes))
	}
	for i, node := range tree.Nodes {
		if node != local.Nodes[i] {
			t.Errorf("node %d = %+v, want %+v (paths relative to skills/a)", i, node, local.Nodes[i])
		}
		if node.IsTree() {
			continue
		}
		content, err := gh.Blob(ctx, "owner/repo", node.SHA)
		if err != nil {
			t.Fatalf("Blob(%s): %v", node.Path, err)
		}
		want, _ := os.ReadFile(filepath.Join(v1, "skills", "a", filepath.FromSlash(node.Path)))
		if string(content) != string(want) || treehash.BlobSHA(content) != node.SHA {
			t.Errorf("Blob(%s) = %q, want %q", node.Path, content, want)
		}
	}
	if root, err := gh.Tree(ctx, "owner/repo", c1, ""); err != nil || root.SHA == tree.SHA || len(root.Nodes) == 0 {
		t.Fatalf("Tree(root) = %s, %v", root.SHA, err)
	}

	// A new commit moves the branch; a tag and a full SHA keep pointing at
	// their commit.
	gh.Tag("owner/repo", "v1", c1)
	v2 := writeRepo(t, map[string]string{"skills/a/SKILL.md": "---\nname: a\n---\nv2\n"})
	c2 := gh.Commit(t, "owner/repo", "main", v2)
	for ref, want := range map[string]string{"main": c2, "v1": c1, c1: c1, c2: c2} {
		if got, err := gh.ResolveCommit(ctx, "owner/repo", ref); err != nil || got != want {
			t.Errorf("ResolveCommit(%s) = %q, %v; want %s", ref, got, err, want)
		}
	}
	if c1 == c2 {
		t.Fatal("two commits got the same SHA")
	}

	errCases := []struct {
		name string
		err  error
		want error
	}{
		{"an unknown repository", func() error { _, err := gh.DefaultBranch(ctx, "owner/none"); return err }(), ports.ErrRepoNotFound},
		{"an unknown ref", func() error { _, err := gh.ResolveCommit(ctx, "owner/repo", "nope"); return err }(), ports.ErrRefNotFound},
		{"a path that is not there", func() error { _, err := gh.Tree(ctx, "owner/repo", c1, "skills/zzz"); return err }(), ports.ErrRefNotFound},
		{"a path that is a file", func() error { _, err := gh.Tree(ctx, "owner/repo", c1, "README.md"); return err }(), ports.ErrRefNotFound},
		{"an unknown blob", func() error { _, err := gh.Blob(ctx, "owner/repo", "deadbeef"); return err }(), ports.ErrRefNotFound},
	}
	for _, tc := range errCases {
		if !errors.Is(tc.err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, tc.err, tc.want)
		}
	}

	gh.Fail = func(c fakes.Call) error {
		if c.Op == "Tree" {
			return ports.ErrRateLimited
		}
		return nil
	}
	if _, err := gh.Tree(ctx, "owner/repo", c1, ""); !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("injected Tree failure = %v, want ErrRateLimited", err)
	}
	if calls := gh.Calls(); len(calls) == 0 || calls[len(calls)-1].Op != "Tree" {
		t.Fatalf("calls were not recorded: %v", calls)
	}
}

func TestFileOpsInMemory(t *testing.T) {
	files := fakes.NewFileOps(paths.Guard{})
	files.AddFile("/home/u/.agents/skills/a/SKILL.md", "hola", 0o644)
	files.AddFile("/home/u/.agents/skills/a/scripts/run.sh", "#!/bin/sh", 0o755)
	files.AddSymlink("/home/u/.claude/skills/a", "../../.agents/skills/a")

	if data, err := files.ReadFile("/home/u/.agents/skills/a/SKILL.md"); err != nil || string(data) != "hola" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	if data, err := files.ReadFile("/home/u/.claude/skills/a/SKILL.md"); err != nil || string(data) != "hola" {
		t.Fatalf("ReadFile through a relative symlink = %q, %v", data, err)
	}
	if _, err := files.ReadFile("/home/u/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile(missing) = %v, want fs.ErrNotExist", err)
	}

	if err := files.CopyTree("/home/u/.agents/skills/a", "/snap/a"); err != nil {
		t.Fatal(err)
	}
	if err := files.CopyTree("/home/u/.agents/skills/a", "/snap/a"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CopyTree onto an existing dst = %v, want fs.ErrExist", err)
	}
	if mode, err := files.Mode("/snap/a/scripts/run.sh"); err != nil || mode.Perm() != 0o755 {
		t.Fatalf("copied mode = %v, %v", mode, err)
	}
	if mode, err := files.Mode("/home/u/.claude/skills/a"); err != nil || mode&fs.ModeSymlink == 0 {
		t.Fatalf("Mode of a symlink = %v, %v; want the link itself", mode, err)
	}

	if err := files.WriteFileAtomic("/home/u/.agents/skills/a/SKILL.md", []byte("nuevo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := files.Rename("/home/u/.agents/skills/a", "/home/u/.agents/old-a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := files.Exists("/home/u/.agents/skills/a"); ok {
		t.Fatal("Rename left the old path")
	}
	if err := files.Chmod("/home/u/.agents/old-a/scripts/run.sh", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := files.RemoveTree("/snap"); err != nil {
		t.Fatal(err)
	}
	if err := files.RemoveTree("/snap"); err != nil {
		t.Fatalf("RemoveTree of a missing path = %v, want nil", err)
	}
	want := map[string]string{
		"/home/u/.agents/old-a":                "dir",
		"/home/u/.agents/old-a/SKILL.md":       "file 0644 nuevo",
		"/home/u/.agents/old-a/scripts":        "dir",
		"/home/u/.agents/old-a/scripts/run.sh": "file 0644 #!/bin/sh",
		"/home/u/.claude/skills/a":             "link ../../.agents/skills/a",
	}
	got := files.Tree("/home/u")
	for path, desc := range want {
		if got[path] != desc {
			t.Errorf("Tree()[%s] = %q, want %q", path, got[path], desc)
		}
	}

	files.Fail = failOn("Rename")
	if err := files.Rename("/home/u/.agents/old-a", "/x"); !errors.Is(err, errPlanted) {
		t.Fatalf("injected Rename = %v", err)
	}
	if ok, _ := files.Exists("/home/u/.agents/old-a"); !ok {
		t.Fatal("a failed Rename changed the tree")
	}
}

func TestFileOpsRefusesForbiddenPaths(t *testing.T) {
	files := fakes.NewFileOps(paths.NewGuard("/forbidden"))
	if err := files.WriteFileAtomic("/forbidden/x", []byte("x"), 0o644); !errors.Is(err, paths.ErrForbidden) {
		t.Fatalf("WriteFileAtomic under a forbidden prefix = %v, want ErrForbidden", err)
	}
	if err := files.MkdirAll("relative", 0o755); !errors.Is(err, paths.ErrInvalidPath) {
		t.Fatalf("MkdirAll(relative) = %v, want ErrInvalidPath", err)
	}
	if len(files.Tree("/")) != 0 {
		t.Fatalf("a refused write changed the tree: %v", files.Tree("/"))
	}
}

func TestFailingFileOpsWrapsARealAdapter(t *testing.T) {
	inner := fakes.NewFileOps(paths.Guard{})
	wrapped := &fakes.FailingFileOps{Inner: inner}
	wrapped.Fail = failOn("WriteFileAtomic")
	if err := wrapped.MkdirAll("/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.WriteFileAtomic("/a/f", []byte("x"), 0o644); !errors.Is(err, errPlanted) {
		t.Fatalf("WriteFileAtomic = %v, want the planted failure", err)
	}
	if ok, _ := inner.Exists("/a/f"); ok {
		t.Fatal("the failing call reached the wrapped adapter")
	}
	if calls := wrapped.Calls(); len(calls) != 2 || calls[1].Args[0] != "/a/f" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSkillDirScanner(t *testing.T) {
	scanner := fakes.NewSkillDirScanner()
	scanner.Set("/home/u/.agents/skills",
		ports.DirEntry{Name: "b", Kind: ports.EntryDir, HasSkillMD: true},
		ports.DirEntry{Name: "a", Kind: ports.EntrySymlink, ResolvedPath: "/home/u/.agents/skills/b", HasSkillMD: true},
	)
	got, err := scanner.Scan("/home/u/.agents/skills")
	if err != nil || len(got) != 2 || got[0].Name != "a" || got[1].Path != "/home/u/.agents/skills/b" {
		t.Fatalf("Scan = %+v, %v; want sorted entries with paths", got, err)
	}
	if _, err := scanner.Scan("/home/u/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Scan(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestSkillsLockStore(t *testing.T) {
	store := fakes.NewSkillsLockStore(
		domain.LockEntry{Name: "plain", Source: "o/r", SkillFolderHash: "h1", InstalledAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z"},
		domain.LockEntry{Name: "with-commit", Source: "o/r", SkillFolderHash: "h2", Ref: "v1", Commit: "c-old"},
	)
	before, err := store.Read()
	if err != nil || before.Version != 3 || len(before.Entries) != 2 {
		t.Fatalf("Read = %+v, %v", before, err)
	}
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"plain", "with-commit"} {
		if err := store.UpdateEntry(name, ports.LockEntryUpdate{FolderHash: "new", UpdatedAt: at, Commit: "c-new"}); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := store.Read()
	plain, withCommit := after.Entries[0], after.Entries[1]
	if plain.SkillFolderHash != "new" || plain.UpdatedAt != "2026-11-21T12:00:00.000Z" || plain.Commit != "" || plain.InstalledAt != "2026-01-01T00:00:00.000Z" {
		t.Errorf("an entry without commit after the update = %+v; want hash and updatedAt only", plain)
	}
	if withCommit.Commit != "c-new" || withCommit.Ref != "v1" {
		t.Errorf("an entry with commit after the update = %+v; want the new commit and the same ref", withCommit)
	}
	if after.Revision == before.Revision {
		t.Error("a write did not change the revision")
	}
	if err := store.RestoreEntry("plain", before.Entries[0].Raw); err != nil {
		t.Fatal(err)
	}
	restored, _ := store.Read()
	if string(restored.Entries[0].Raw) != string(before.Entries[0].Raw) || restored.Entries[0].SkillFolderHash != "h1" {
		t.Fatalf("restored entry = %s, want %s", restored.Entries[0].Raw, before.Entries[0].Raw)
	}
	if err := store.UpdateEntry("absent", ports.LockEntryUpdate{}); !errors.Is(err, ports.ErrLockEntryNotFound) {
		t.Fatalf("UpdateEntry(absent) = %v", err)
	}
	store.SetVersion(2)
	if err := store.UpdateEntry("plain", ports.LockEntryUpdate{FolderHash: "x", UpdatedAt: at}); !errors.Is(err, ports.ErrUnsupportedLockVersion) {
		t.Fatalf("UpdateEntry on version 2 = %v, want ErrUnsupportedLockVersion", err)
	}
	if got, _ := store.Read(); got.Entries[0].SkillFolderHash != "h1" {
		t.Fatal("a refused write changed the entry")
	}
}

func TestGit(t *testing.T) {
	ctx := context.Background()
	git := fakes.NewGit()
	clone := git.AddClone("/home/u/.claude/skills/k", ports.CloneStatus{
		Remote: "https://github.com/o/k.git", Branch: "main", Upstream: "origin/main", Head: "c1", Clean: true,
	})
	clone.AddCommit("c1", "", "t1")
	clone.AddRemoteCommit("c2", "c1", "t2")

	if st, err := git.Describe(ctx, "/home/u/.claude/skills/k"); err != nil || st.Head != "c1" {
		t.Fatalf("Describe = %+v, %v", st, err)
	}
	if err := git.FastForward(ctx, clone.Root, "c2"); err == nil {
		t.Fatal("FastForward to a commit that was not fetched succeeded")
	}
	if err := git.Fetch(ctx, clone.Root, "c2"); err != nil {
		t.Fatal(err)
	}
	if ok, err := git.IsAncestor(ctx, clone.Root, "c1", "c2"); err != nil || !ok {
		t.Fatalf("IsAncestor(c1, c2) = %v, %v", ok, err)
	}
	if ok, _ := git.IsAncestor(ctx, clone.Root, "c2", "c1"); ok {
		t.Fatal("IsAncestor(c2, c1) = true")
	}
	if err := git.FastForward(ctx, clone.Root, "c2"); err != nil {
		t.Fatal(err)
	}
	if st, _ := git.Describe(ctx, clone.Root); st.Head != "c2" {
		t.Fatalf("Head after FastForward = %s", st.Head)
	}
	if sha, err := git.TreeSHA(ctx, clone.Root, "c2"); err != nil || sha != "t2" {
		t.Fatalf("TreeSHA(c2) = %s, %v", sha, err)
	}

	dirty := git.AddClone("/dirty", ports.CloneStatus{Head: "d1", Branch: "main", Upstream: "origin/main"})
	dirty.AddCommit("d1", "", "td1")
	dirty.AddRemoteCommit("d2", "d1", "td2")
	_ = git.Fetch(ctx, "/dirty", "d2")
	if err := git.FastForward(ctx, "/dirty", "d2"); !errors.Is(err, fakes.ErrNotFastForward) {
		t.Fatalf("FastForward of a dirty clone = %v, want ErrNotFastForward", err)
	}
	if _, err := git.Describe(ctx, "/nowhere"); !errors.Is(err, fakes.ErrUnknownClone) {
		t.Fatalf("Describe(unknown) = %v", err)
	}
}

func TestStateStore(t *testing.T) {
	ctx := context.Background()
	store := fakes.NewStateStore()

	release, err := store.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(ctx); !errors.Is(err, ports.ErrStateLocked) {
		t.Fatalf("a second Lock = %v, want ErrStateLocked", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release2, err := store.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock after release = %v", err)
	}
	_ = release2()

	state := domain.PersistedState{Candidates: []domain.UpdateCandidate{{ID: "c1", State: domain.CandidatePending}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].State = domain.CandidateApplied // the caller's copy, not the stored one
	loaded, _ := store.Load()
	if loaded.Candidates[0].State != domain.CandidatePending {
		t.Fatal("Save kept a reference to the caller's slice")
	}

	op := domain.Operation{ID: "op1", Kind: domain.OperationApply, Target: "lock:a"}
	if err := store.RecordOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(domain.PersistedState{}); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := store.Operation(); !ok || got.ID != "op1" {
		t.Fatalf("Save dropped the operation in progress: %+v %v", got, ok)
	}
	if err := store.ClearOperation(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Operation(); ok {
		t.Fatal("ClearOperation left the operation")
	}

	for _, a := range []domain.HistoryAction{domain.ActionApplied, domain.ActionUndone} {
		if err := store.AppendJournal(domain.HistoryEntry{Action: a}); err != nil {
			t.Fatal(err)
		}
	}
	if j, _ := store.Journal(); len(j) != 2 || j[0].Action != domain.ActionApplied {
		t.Fatalf("Journal = %+v", j)
	}

	store.Fail = failOn("RecordOperation")
	if err := store.RecordOperation(op); !errors.Is(err, errPlanted) {
		t.Fatalf("injected RecordOperation = %v", err)
	}
	if _, ok, _ := store.Operation(); ok {
		t.Fatal("a failed RecordOperation stored the operation")
	}
}

func TestStateStoreModelsTheVersion1Migration(t *testing.T) {
	v1 := domain.PersistedState{Skips: []domain.Skip{{Target: "lock:a", ToSHA: "b"}}}
	parked := map[string]domain.ParkedSkill{"demo": {Key: "demo", Folder: "/x"}}
	op := &domain.Operation{ID: "op-1", Kind: domain.OperationApply, Target: "lock:a"}

	t.Run("a version 1 state cannot hold version 2 fields", func(t *testing.T) {
		store := fakes.NewStateStore()
		if err := store.SeedVersion1(domain.PersistedState{Parked: parked}, nil); err == nil {
			t.Fatal("SeedVersion1 accepted version 2 fields")
		}
		if store.Version1() {
			t.Fatal("a refused seed marked the state version 1")
		}
	})

	t.Run("a recorded operation keeps it version 1 until a write after clearing", func(t *testing.T) {
		store := fakes.NewStateStore()
		if err := store.SeedVersion1(v1, op); err != nil {
			t.Fatal(err)
		}
		if err := store.Save(domain.PersistedState{Parked: parked}); !errors.Is(err, fakes.ErrMigrationBlocked) {
			t.Fatalf("Save = %v, want ErrMigrationBlocked", err)
		}
		if got, _ := store.Load(); len(got.Parked) != 0 || len(got.Skips) != 1 {
			t.Fatalf("a refused Save changed the state: %+v", got)
		}
		if err := store.Save(v1); err != nil || !store.Version1() {
			t.Fatalf("Save without version 2 fields = %v, Version1() = %v, want nil, true", err, store.Version1())
		}
		if err := store.ClearOperation(); err != nil || !store.Version1() {
			t.Fatalf("ClearOperation = %v, Version1() = %v, want nil, true", err, store.Version1())
		}
		if err := store.Save(v1); err != nil || store.Version1() {
			t.Fatalf("Save after clearing = %v, Version1() = %v, want nil, false", err, store.Version1())
		}
	})

	t.Run("recording an operation on a version 1 state without one migrates it", func(t *testing.T) {
		store := fakes.NewStateStore()
		if err := store.SeedVersion1(v1, nil); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordOperation(*op); err != nil {
			t.Fatal(err)
		}
		if store.Version1() {
			t.Fatal("RecordOperation left the state version 1")
		}
		if err := store.Save(domain.PersistedState{Parked: parked}); err != nil {
			t.Fatalf("Save of version 2 fields after the migration: %v", err)
		}
	})
}

func TestSettingsStore(t *testing.T) {
	store := fakes.NewSettingsStore()
	if s, err := store.Load(); err != nil || s.CheckInterval != time.Hour {
		t.Fatalf("Load without settings = %+v, %v; want the defaults", s, err)
	}
	short := domain.DefaultSettings()
	short.CheckInterval = time.Minute
	if err := store.Save(short); !errors.Is(err, domain.ErrIntervalTooShort) {
		t.Fatalf("Save(1m) = %v", err)
	}
	two := domain.DefaultSettings()
	two.CheckInterval = 2 * time.Hour
	if err := store.Save(two); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Load(); s.CheckInterval != 2*time.Hour {
		t.Fatalf("Load after Save = %+v", s)
	}
}

func TestNotifierBackgroundClockAndProcessRunner(t *testing.T) {
	ctx := context.Background()

	n := fakes.NewNotifier()
	if err := n.Notify(ctx, "skillhub", "hay 2 actualizaciones"); err != nil {
		t.Fatal(err)
	}
	if got := n.Sent(); len(got) != 1 || got[0].Body != "hay 2 actualizaciones" {
		t.Fatalf("Sent = %+v", got)
	}

	bg := fakes.NewBackgroundService()
	if err := bg.Enable(ctx, "/opt/skillhub"); err != nil {
		t.Fatal(err)
	}
	if st, _ := bg.Status(ctx); !st.Installed || !st.Enabled || !st.Active || bg.Binary() != "/opt/skillhub" {
		t.Fatalf("Status after Enable = %+v, binary %q", st, bg.Binary())
	}
	if err := bg.Disable(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := bg.Status(ctx); st != (ports.BackgroundStatus{}) {
		t.Fatalf("Status after Disable = %+v", st)
	}

	start := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	clock := fakes.NewClock(start)
	clock.Advance(90 * time.Minute)
	if got := clock.Now(); !got.Equal(start.Add(90 * time.Minute)) {
		t.Fatalf("Now after Advance = %v", got)
	}

	runner := fakes.NewProcessRunner()
	runner.Expect(ports.Command{Name: "notify-send", Args: []string{"a", "b"}}, ports.Result{Stdout: []byte("ok")}, nil)
	res, err := runner.Run(ctx, ports.Command{Name: "notify-send", Args: []string{"a", "b"}})
	if err != nil || string(res.Stdout) != "ok" {
		t.Fatalf("Run(expected) = %+v, %v", res, err)
	}
	if _, err := runner.Run(ctx, ports.Command{Name: "notify-send", Args: []string{"a", "b"}}); !errors.Is(err, fakes.ErrUnexpectedCommand) {
		t.Fatalf("a second call to a once-only expectation = %v, want ErrUnexpectedCommand", err)
	}
	if _, err := runner.Run(ctx, ports.Command{Name: "/usr/bin/gh"}); !errors.Is(err, ports.ErrInvalidCommand) {
		t.Fatalf("Run(path) = %v, want ErrInvalidCommand like the real runner", err)
	}
	if _, err := runner.Run(ctx, ports.Command{Name: "gh", Env: []string{"PATH=/x"}}); !errors.Is(err, ports.ErrInvalidCommand) {
		t.Fatalf("Run(PATH env) = %v, want ErrInvalidCommand like the real runner", err)
	}
	if got := runner.Commands(); len(got) != 4 {
		t.Fatalf("Commands() recorded %d calls, want 4", len(got))
	}
	if left := runner.Unused(); len(left) != 0 {
		t.Fatalf("Unused() = %v", left)
	}
}
