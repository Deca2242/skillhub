package engine_test

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"skillhub/internal/adapters/fs"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
)

// cloneFixture creates a valid standalone Git clone under relRoot (e.g.
// ".agents/skills/pack") with .git metadata (including empty dirs and 0444
// objects), worktree empty directories, varied permissions (0644, 0755, 0600,
// 0444, 0555 dir), and supported relative symlinks.
func cloneFixture(t *testing.T, e *env, relRoot string) string {
	t.Helper()
	writable(t, e.home.Root())

	e.home.WriteFile(relRoot+"/.git/HEAD", "ref: refs/heads/main\n")
	e.home.WriteFile(relRoot+"/.git/config", "[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
	e.home.WriteFileMode(relRoot+"/.git/index", "DIRC\x00\x00\x00\x02", 0o644)
	e.home.WriteFile(relRoot+"/.git/refs/heads/main", "1111111111111111111111111111111111111111\n")
	e.home.WriteFileMode(relRoot+"/.git/objects/11/223344", "compressed-git-object", 0o444)
	e.home.Mkdir(relRoot + "/.git/refs/tags")
	e.home.Mkdir(relRoot + "/.git/branches")
	e.home.Mkdir(relRoot + "/.git/objects/info")

	e.home.WriteFile(relRoot+"/skills/one/SKILL.md", "---\nname: one\n---\nOne.\n")
	e.home.WriteFileMode(relRoot+"/skills/one/bin/run.sh", "#!/bin/sh\necho one\n", 0o755)
	e.home.WriteFileMode(relRoot+"/skills/one/secret.txt", "token-placeholder\n", 0o600)
	e.home.WriteFile(relRoot+"/shared/ref.md", "shared\n")
	e.home.Symlink(relRoot+"/skills/one/README.md", "SKILL.md")
	e.home.Symlink(relRoot+"/skills/one/ref.md", "../../shared/ref.md")
	e.home.Symlink(relRoot+"/skills/one/dangling.md", "../../shared/not-there-yet.md")

	e.home.Mkdir(relRoot + "/empty")
	e.home.Mkdir(relRoot + "/skills/two/empty-sub")
	e.home.WriteFileMode(relRoot+"/ro-dir/locked.txt", "readonly\n", 0o444)
	must(t, os.Chmod(e.home.Path(relRoot+"/ro-dir"), 0o555))

	return e.home.Path(relRoot)
}

func TestOrdinaryGuardsStillRejectClones(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	before := observeWorld(t, e)
	eng := e.engine()

	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	if err := b.RemoveTree(cloneDir); !errors.Is(err, engine.ErrUnsupportedTree) {
		t.Errorf("RemoveTree(clone) = %v, want ErrUnsupportedTree", err)
	}
	if _, err := b.Build(); !errors.Is(err, engine.ErrUnsupportedTree) {
		t.Errorf("Build after RemoveTree(clone) = %v, want ErrUnsupportedTree", err)
	}

	b = eng.NewBuilder(engine.Meta{Action: "disable"})
	if err := b.Move(cloneDir, e.home.Path(".agents/.skillhub/disabled/pack")); !errors.Is(err, engine.ErrUnsupportedTree) {
		t.Errorf("Move(clone) = %v, want ErrUnsupportedTree", err)
	}

	assertWorld(t, "ordinary guards keep clone untouched", observeWorld(t, e), before)
}

func TestRemoveCloneSnapshotApplyVerifyAndUndoPreservesEverythingByteForByte(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	harnessLink := e.home.Path(".claude/skills/one")
	harnessTarget := "../../.agents/skills/pack/skills/one"
	e.home.Symlink(".claude/skills/one", harnessTarget)
	noOwnLevels(t, e)
	start := observeWorld(t, e)
	cloneBefore := tree(t, cloneDir)

	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
	must(t, b.Unlink(harnessLink, harnessTarget))
	must(t, b.RemoveClone(cloneDir))
	plan, err := b.Build()
	must(t, err)

	if len(plan.Steps) != 2 || plan.Steps[1].Kind != engine.StepRemoveClone {
		t.Fatalf("plan steps = %+v, want Unlink + RemoveClone", plan.Steps)
	}
	cloneSHA := plan.Steps[1].Pre[0].CloneSHA
	if len(cloneSHA) != 64 {
		t.Fatalf("CloneSHA = %q, want 64-char SHA-256", cloneSHA)
	}

	out, err := eng.Apply(t.Context(), plan)
	must(t, err)

	if _, err := os.Lstat(cloneDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clone still exists after Apply: %v", err)
	}
	if _, err := os.Lstat(harnessLink); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("harness link still exists after Apply: %v", err)
	}
	if recordExists(t, e.p) {
		t.Error("operation record still exists after Apply")
	}

	snap, err := e.clean().VerifySnapshot(out.SnapshotID)
	must(t, err)
	if len(snap.Trees) != 1 || snap.Trees[0].CloneSHA != cloneSHA || snap.Trees[0].TreeSHA != "" {
		t.Errorf("snapshot trees = %+v, want CloneSHA=%s", snap.Trees, cloneSHA)
	}
	if got := tree(t, filepath.Join(eng.SnapshotDir(out.SnapshotID), filepath.FromSlash(snap.Trees[0].Copy))); !reflect.DeepEqual(got, cloneBefore) {
		t.Errorf("snapshot copy differs from original clone: %v", diffKeys(cloneBefore, got))
	}

	j := journal(t, e)
	if len(j) != 1 || j[0].Action != domain.ActionApplied || j[0].Target != "v2:remove-clone" || j[0].SnapshotID != out.SnapshotID {
		t.Errorf("journal after Apply = %+v", j)
	}

	undoPlan, err := e.clean().UndoPlan(out.SnapshotID)
	must(t, err)
	if len(undoPlan.Steps) != 2 || undoPlan.Steps[0].Kind != engine.StepRestoreClone || undoPlan.Steps[1].Kind != engine.StepLink {
		t.Fatalf("undo plan steps = %+v, want RestoreClone + Link", undoPlan.Steps)
	}

	res, err := e.clean().Undo(t.Context(), out.SnapshotID)
	must(t, err)
	assertWorld(t, "after clone undo", observeWorld(t, e), start)

	j = journal(t, e)
	if len(j) != 2 || j[1].Action != domain.ActionUndone || j[1].Target != "v2:undo" || j[1].SnapshotID != res.SnapshotID {
		t.Errorf("journal after Undo = %+v", j)
	}
	if _, err := e.clean().Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrNothingToUndo) {
		t.Errorf("second undo = %v, want ErrNothingToUndo", err)
	}
}

func TestCloneVerificationCatchesWhatTreeHashIgnores(t *testing.T) {
	cases := map[string]func(t *testing.T, copyDir string){
		"git metadata byte changed": func(t *testing.T, copyDir string) {
			must(t, os.WriteFile(filepath.Join(copyDir, ".git", "HEAD"), []byte("ref: refs/heads/other\n"), 0o644))
		},
		"empty directory removed": func(t *testing.T, copyDir string) {
			must(t, os.Remove(filepath.Join(copyDir, "empty")))
		},
		"empty git directory added": func(t *testing.T, copyDir string) {
			must(t, os.Mkdir(filepath.Join(copyDir, ".git", "extra-empty"), 0o755))
		},
		"file permission changed without changing execute bit": func(t *testing.T, copyDir string) {
			must(t, os.Chmod(filepath.Join(copyDir, "skills", "one", "secret.txt"), 0o644))
		},
		"directory permission changed": func(t *testing.T, copyDir string) {
			must(t, os.Chmod(filepath.Join(copyDir, "empty"), 0o700))
		},
		"symlink target changed": func(t *testing.T, copyDir string) {
			link := filepath.Join(copyDir, "skills", "one", "README.md")
			must(t, os.Remove(link))
			must(t, os.Symlink("secret.txt", link))
		},
		"setgid added on copy": func(t *testing.T, copyDir string) {
			chmodExact(t, filepath.Join(copyDir, "empty"), 0o755|iofs.ModeSetgid)
		},
	}

	for name, tamper := range cases {
		t.Run("during Begin/"+name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			before := observeWorld(t, e)
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			must(t, b.RemoveClone(cloneDir))
			plan, err := b.Build()
			must(t, err)

			e.files.afterCopy = func(dst string) { tamper(t, dst) }
			if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Fatalf("Begin error = %v, want ErrSnapshotMismatch", err)
			}
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Errorf("failed Begin left record or snapshots: %v", snapshotDirs(t, e.p))
			}
			assertWorld(t, "clone untouched after refused Begin", observeWorld(t, e), before)
		})

		t.Run("in stored snapshot before Undo/"+name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			must(t, b.RemoveClone(cloneDir))
			plan, err := b.Build()
			must(t, err)
			out, err := eng.Apply(t.Context(), plan)
			must(t, err)
			removed := observeWorld(t, e)

			copyDir := filepath.Join(eng.SnapshotDir(out.SnapshotID), "trees", "0")
			tamper(t, copyDir)

			if _, err := e.clean().VerifySnapshot(out.SnapshotID); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("VerifySnapshot = %v, want ErrSnapshotMismatch", err)
			}
			if _, err := e.clean().Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("Undo = %v, want ErrSnapshotMismatch", err)
			}
			assertWorld(t, "refused Undo leaves world intact", observeWorld(t, e), removed)
		})
	}
}

func TestRestoreStagingTamperIsRejectedBeforePublish(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
	must(t, b.RemoveClone(cloneDir))
	plan, err := b.Build()
	must(t, err)
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)
	removed := observeWorld(t, e)

	e.files.afterCopy = func(dst string) {
		if !strings.HasPrefix(dst, e.p.SnapshotsDir()) {
			_ = os.Chmod(filepath.Join(dst, "skills", "one", "secret.txt"), 0o644)
		}
	}
	if _, err := eng.Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrSnapshotMismatch) {
		t.Fatalf("Undo with tampered staging = %v, want ErrSnapshotMismatch", err)
	}
	assertWorld(t, "after refused staging", observeWorld(t, e), removed)
	if _, err := os.Lstat(cloneDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clone path should remain absent: %v", err)
	}
}

func TestRemoveCloneFailsClosedOnUnsupportedLayoutsAndNonClones(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *env) string
		want  error
	}{
		{
			name: "non-clone directory",
			setup: func(t *testing.T, e *env) string {
				return e.home.Path(".agents/skills/beta")
			},
			want: engine.ErrPrecondition,
		},
		{
			name: "symlink to clone",
			setup: func(t *testing.T, e *env) string {
				_ = cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".claude/skills/pack-link", "../../.agents/skills/pack")
				return e.home.Path(".claude/skills/pack-link")
			},
			want: engine.ErrPrecondition,
		},
		{
			name: "git file linked worktree",
			setup: func(t *testing.T, e *env) string {
				e.home.WriteFile(".agents/skills/wt/.git", "gitdir: /tmp/other/.git/worktrees/wt\n")
				e.home.WriteFile(".agents/skills/wt/SKILL.md", "---\nname: wt\n---\n")
				return e.home.Path(".agents/skills/wt")
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "git symlink",
			setup: func(t *testing.T, e *env) string {
				e.home.Mkdir(".agents/skills/other-git")
				e.home.Symlink(".agents/skills/symgit/.git", "../other-git")
				e.home.WriteFile(".agents/skills/symgit/SKILL.md", "---\nname: symgit\n---\n")
				return e.home.Path(".agents/skills/symgit")
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "clone sustaining linked worktrees",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.WriteFile(".agents/skills/pack/.git/worktrees/wt1/gitdir", "/tmp/wt1/.git\n")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "clone with submodules",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.WriteFile(".agents/skills/pack/.git/modules/sub/HEAD", "ref: refs/heads/main\n")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "clone with alternates",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.WriteFile(".agents/skills/pack/.git/objects/info/alternates", "/tmp/shared-objects\n")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "clone with commondir",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.WriteFile(".agents/skills/pack/.git/commondir", "../..\n")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "nested git repo",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.WriteFile(".agents/skills/pack/vendor/sub/.git/HEAD", "ref: refs/heads/main\n")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "symlink inside .git",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/.git/hooks/post-checkout", "../../skills/one/bin/run.sh")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "absolute symlink in clone",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/abs", e.home.Path(".agents/skills/alpha/SKILL.md"))
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "relative symlink escaping clone",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/skills/escape", "../../../alpha/SKILL.md")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "multi-hop symlink escaping clone",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/p/q/dirlink", "..")
				e.home.Symlink(".agents/skills/pack/r/s/t/hop", "../../../p/q/dirlink/../..")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "symlink pointing into .git",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/head-link", ".git/HEAD")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "symlink loop",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				e.home.Symlink(".agents/skills/pack/loop-a", "loop-b")
				e.home.Symlink(".agents/skills/pack/loop-b", "loop-a")
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "named pipe inside clone",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				must(t, syscall.Mkfifo(filepath.Join(p, "pipe"), 0o644))
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "setgid directory inside clone",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				chmodExact(t, filepath.Join(p, "empty"), 0o755|iofs.ModeSetgid)
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "setuid file inside .git",
			setup: func(t *testing.T, e *env) string {
				p := cloneFixture(t, e, ".agents/skills/pack")
				chmodExact(t, filepath.Join(p, ".git", "config"), 0o644|iofs.ModeSetuid)
				return p
			},
			want: engine.ErrUnsupportedTree,
		},
		{
			name: "forbidden prefix",
			setup: func(t *testing.T, e *env) string {
				_, fp := newEnv(t)
				return fp.forbidden
			},
			want: paths.ErrForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newEnv(t)
			target := tt.setup(t, e)
			before := observeWorld(t, e)
			eng := e.engine()

			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			err := b.RemoveClone(target)
			if !errors.Is(err, tt.want) {
				t.Errorf("RemoveClone(%s) = %v, want %v", target, err, tt.want)
			}
			if _, buildErr := b.Build(); !errors.Is(buildErr, tt.want) {
				t.Errorf("Build() = %v, want %v", buildErr, tt.want)
			}
			assertWorld(t, "refused RemoveClone leaves disk intact", observeWorld(t, e), before)
		})
	}
}

func TestRemoveCloneFailsClosedWithoutDirReader(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	before := observeWorld(t, e)

	fakeFiles := fakes.NewFileOps(e.p.Guard())
	eng := engine.New(engine.Deps{
		Paths: e.p,
		Files: fakeFiles,
		Links: fakes.NewLinkOps(e.home.Root(), e.p.Guard()),
		Lock:  e.lock,
		State: e.state,
		Dirs:  fs.NewScanner(e.p.Guard()),
		Clock: e.clock,
	})

	// Seed the fake filesystem with a minimal clone directory so Mode finds it.
	fakeFiles.AddFile(filepath.Join(cloneDir, ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
	fakeFiles.AddFile(filepath.Join(cloneDir, "SKILL.md"), "---\nname: pack\n---\n", 0o644)

	b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
	err := b.RemoveClone(cloneDir)
	if !errors.Is(err, engine.ErrUnsupportedTree) {
		t.Fatalf("RemoveClone without DirReader = %v, want ErrUnsupportedTree", err)
	}
	assertWorld(t, "real disk untouched", observeWorld(t, e), before)
}

func TestCloneUndoRefusesChangedDestinationWithoutTouchingIt(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
	must(t, b.RemoveClone(cloneDir))
	plan, err := b.Build()
	must(t, err)
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)

	// Recreate a new clone at the same path after removal.
	e.home.WriteFile(".agents/skills/pack/.git/HEAD", "ref: refs/heads/new\n")
	e.home.WriteFile(".agents/skills/pack/SKILL.md", "new\n")
	changed := observeWorld(t, e)

	if _, err := e.clean().Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrChangedSince) {
		t.Fatalf("Undo when clone recreated = %v, want ErrChangedSince", err)
	}
	assertWorld(t, "refused undo preserves recreated clone", observeWorld(t, e), changed)
}

func TestCloneApplyAndUndoRecoverFromFailuresAndCrashes(t *testing.T) {
	runApply := func(t *testing.T) (*env, func(*engine.Engine) error, string) {
		t.Helper()
		e, _ := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		link := e.home.Path(".claude/skills/one")
		target := "../../.agents/skills/pack/skills/one"
		e.home.Symlink(".claude/skills/one", target)
		noOwnLevels(t, e)
		b := e.engine().NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
		must(t, b.Unlink(link, target))
		must(t, b.RemoveClone(cloneDir))
		plan, err := b.Build()
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }, "applied"
	}

	runUndo := func(t *testing.T) (*env, func(*engine.Engine) error, string) {
		t.Helper()
		e, _ := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		link := e.home.Path(".claude/skills/one")
		target := "../../.agents/skills/pack/skills/one"
		e.home.Symlink(".claude/skills/one", target)
		noOwnLevels(t, e)
		b := e.clean().NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
		must(t, b.Unlink(link, target))
		must(t, b.RemoveClone(cloneDir))
		plan, err := b.Build()
		must(t, err)
		out, err := e.clean().Apply(t.Context(), plan)
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), out.SnapshotID); return err }, "undone"
	}

	for name, setup := range map[string]func(*testing.T) (*env, func(*engine.Engine) error, string){
		"remove-clone": runApply,
		"undo-clone":   runUndo,
	} {
		t.Run(name, func(t *testing.T) {
			e0, fn0, wantAction := setup(t)
			e0.files.arm(-1, failOnce)
			must(t, fn0(e0.engine()))
			writes := e0.files.writeLog()
			after := observeWorld(t, e0)
			commitIdx := slices.Index(writes, "AppendJournal "+wantAction)
			if commitIdx < 0 {
				t.Fatalf("AppendJournal %s not found in %v", wantAction, writes)
			}

			for n := range writes {
				modes := []faultMode{crashBefore, crashAfter}
				if n < commitIdx {
					modes = append(modes, failOnce)
					// A RemoveTree on a 0555 dir returns ErrPermission before
					// removeOwn makes it writable and retries; doneButFailed on
					// that call joins ErrPermission so the retry intentionally
					// recovers it.
					if !(strings.HasPrefix(writes[n], "RemoveTree ") && n+1 < len(writes) && strings.HasPrefix(writes[n+1], "Chmod ")) {
						modes = append(modes, doneButFailed)
					}
				}
				for _, mode := range modes {
					t.Run(writes[n]+" "+mode.String(), func(t *testing.T) {
						e, fn, _ := setup(t)
						start := observeWorld(t, e)
						e.files.arm(n, mode)
						if err := fn(e.engine()); !errors.Is(err, errInjected) {
							t.Fatalf("write %d %s: err = %v, want errInjected", n, mode, err)
						}
						committed := n > commitIdx || (n == commitIdx && mode == crashAfter)
						hadRecord := recordExists(t, e.p)
						res, err := e.clean().Recover(t.Context())
						must(t, err)
						if res.Found != hadRecord || (hadRecord && res.Committed != committed) {
							t.Fatalf("Recover = %+v (hadRecord=%t, wantCommitted=%t)", res, hadRecord, committed)
						}
						want := start
						if committed {
							want = after
						}
						assertWorld(t, "after recover", observeWorld(t, e), want)
					})
				}
			}
		})
	}
}

func TestCloneUndoRollsBackRestoreCloneWhenLaterStepFails(t *testing.T) {
	e, _ := newEnv(t)
	cloneDir := cloneFixture(t, e, ".agents/skills/pack")
	link := e.home.Path(".claude/skills/one")
	target := "../../.agents/skills/pack/skills/one"
	e.home.Symlink(".claude/skills/one", target)
	noOwnLevels(t, e)

	eng := e.clean()
	b := eng.NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
	must(t, b.Unlink(link, target))
	must(t, b.RemoveClone(cloneDir))
	plan, err := b.Build()
	must(t, err)
	out, err := eng.Apply(t.Context(), plan)
	must(t, err)

	// Make the later Link step fail during Undo by planting a link at link's
	// parent? No, fail the Symlink write via faultFiles after RestoreClone runs!
	removed := observeWorld(t, e)
	undoEng := e.engine()
	e.files.afterWrite = func(_ int, line string) {
		if strings.HasPrefix(line, "RenameNoReplace ") {
			// Fail the next write (progress update or Symlink) so Undo rolls back
			// StepRestoreClone (which holds ro-dir 0555!).
			e.files.afterWrite = nil
			e.files.arm(0, failOnce)
		}
	}
	if _, err := undoEng.Undo(t.Context(), out.SnapshotID); !errors.Is(err, errInjected) {
		t.Fatalf("Undo error = %v, want errInjected", err)
	}
	assertWorld(t, "after failed undo rollback", observeWorld(t, e), removed)
	if _, err := os.Lstat(cloneDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clone path should remain absent after rollback: %v", err)
	}
}

func TestCloneTreeDoesNotMixTreeSHAAndCloneSHA(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	// Ordinary Tree image must still produce identical Plan.ID and omit clone_sha.
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.RemoveTree(fp.beta))
	plan, err := b.Build()
	must(t, err)
	if plan.Steps[0].Pre[0].CloneSHA != "" {
		t.Errorf("ordinary RemoveTree set CloneSHA = %q", plan.Steps[0].Pre[0].CloneSHA)
	}
	treeSHA, err := treehash.Dir(fp.beta)
	must(t, err)
	if plan.Steps[0].Pre[0].TreeSHA != treeSHA.SHA {
		t.Errorf("ordinary RemoveTree TreeSHA = %q, want %q", plan.Steps[0].Pre[0].TreeSHA, treeSHA.SHA)
	}
}

// posixACLPayload encodes a valid Linux v2 POSIX ACL xattr with an explicit
// named-user entry for uid 65534 (nobody) so no external setfacl process is
// needed in hermetic tests.
func posixACLPayload(perm uint16) []byte {
	type aclEntry struct {
		tag  uint16
		perm uint16
		id   uint32
	}
	const undefID = ^uint32(0)
	entries := []aclEntry{
		{tag: 0x0001, perm: perm, id: undefID}, // ACL_USER_OBJ
		{tag: 0x0002, perm: 4, id: 65534},      // ACL_USER (nobody: r--)
		{tag: 0x0004, perm: perm, id: undefID}, // ACL_GROUP_OBJ
		{tag: 0x0010, perm: perm, id: undefID}, // ACL_MASK
		{tag: 0x0020, perm: 4, id: undefID},    // ACL_OTHER
	}
	buf := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(buf[0:4], 2) // POSIX_ACL_XATTR_VERSION
	for i, en := range entries {
		off := 4 + i*8
		binary.LittleEndian.PutUint16(buf[off:off+2], en.tag)
		binary.LittleEndian.PutUint16(buf[off+2:off+4], en.perm)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], en.id)
	}
	return buf
}

func setXattrOrSkip(t *testing.T, path, attr string, data []byte) {
	t.Helper()
	err := unix.Lsetxattr(path, attr, data, 0)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) {
		t.Skipf("filesystem does not support %s on %s: %v", attr, path, err)
	}
	must(t, err)
}

func TestRemoveCloneFailsClosedOnUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *env, cloneDir string)
	}{
		{
			name: "hard link between worktree files",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				must(t, os.Link(filepath.Join(cloneDir, "shared", "ref.md"), filepath.Join(cloneDir, "shared", "ref-hardlink.md")))
			},
		},
		{
			name: "hard link from worktree to outside clone",
			setup: func(t *testing.T, e *env, cloneDir string) {
				must(t, os.Link(filepath.Join(cloneDir, "skills", "one", "SKILL.md"), e.home.Path(".agents/outside-hardlink.md")))
			},
		},
		{
			name: "hard link inside .git object to outside",
			setup: func(t *testing.T, e *env, cloneDir string) {
				must(t, os.Link(filepath.Join(cloneDir, ".git", "objects", "11", "223344"), e.home.Path(".agents/outside-git-obj")))
			},
		},
		{
			name: "hard-linked symlink inside clone",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				must(t, unix.Linkat(unix.AT_FDCWD, filepath.Join(cloneDir, "skills", "one", "README.md"),
					unix.AT_FDCWD, filepath.Join(cloneDir, "skills", "one", "README-hardlink.md"), 0))
			},
		},
		{
			name: "xattr on clone root",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, cloneDir, "user.skillhub", []byte("root-attr"))
			},
		},
		{
			name: "xattr on .git directory",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, ".git"), "user.skillhub", []byte("git-attr"))
			},
		},
		{
			name: "xattr on file inside .git",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, ".git", "config"), "user.skillhub", []byte("git-file-attr"))
			},
		},
		{
			name: "xattr on worktree file",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, "skills", "one", "SKILL.md"), "user.skillhub", []byte("file-attr"))
			},
		},
		{
			name: "xattr on worktree subdir",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, "empty"), "user.skillhub", []byte("dir-attr"))
			},
		},
		{
			name: "POSIX access ACL on clone root",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, cloneDir, "system.posix_acl_access", posixACLPayload(7))
			},
		},
		{
			name: "POSIX default ACL on clone root",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, cloneDir, "system.posix_acl_default", posixACLPayload(7))
			},
		},
		{
			name: "POSIX access ACL on .git directory",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, ".git"), "system.posix_acl_access", posixACLPayload(7))
			},
		},
		{
			name: "POSIX access ACL on file inside .git",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, ".git", "config"), "system.posix_acl_access", posixACLPayload(6))
			},
		},
		{
			name: "POSIX access ACL on worktree file",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, "skills", "one", "SKILL.md"), "system.posix_acl_access", posixACLPayload(6))
			},
		},
		{
			name: "POSIX default ACL on worktree subdir",
			setup: func(t *testing.T, _ *env, cloneDir string) {
				setXattrOrSkip(t, filepath.Join(cloneDir, "empty"), "system.posix_acl_default", posixACLPayload(7))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			tt.setup(t, e, cloneDir)
			before := observeWorld(t, e)
			eng := e.engine()

			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			err := b.RemoveClone(cloneDir)
			if !errors.Is(err, engine.ErrUnsupportedTree) {
				t.Errorf("RemoveClone error = %v, want ErrUnsupportedTree", err)
			}
			if _, buildErr := b.Build(); !errors.Is(buildErr, engine.ErrUnsupportedTree) {
				t.Errorf("Build error = %v, want ErrUnsupportedTree", buildErr)
			}
			assertWorld(t, "refused RemoveClone leaves disk and journal intact", observeWorld(t, e), before)
		})
	}
}

func TestMixedCloneAndGenericStepsRejected(t *testing.T) {
	clonePlan := func(t *testing.T, e *env, cloneDir string) engine.Plan {
		t.Helper()
		b := e.clean().NewBuilder(engine.Meta{Action: "remove-clone"})
		must(t, b.RemoveClone(cloneDir))
		p, err := b.Build()
		must(t, err)
		return p
	}

	t.Run("builder rejects same-root and nested compositions", func(t *testing.T) {
		tests := []struct {
			name  string
			build func(t *testing.T, e *env, fp fixturePaths, cloneDir string, eng *engine.Engine) error
		}{
			{
				name: "RemoveClone then Link on same root",
				build: func(_ *testing.T, _ *env, _ fixturePaths, cloneDir string, eng *engine.Engine) error {
					b := eng.NewBuilder(engine.Meta{Action: "repair"})
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					if err := b.Link(cloneDir, "../beta"); err != nil {
						return err
					}
					_, err := b.Build()
					return err
				},
			},
			{
				name: "RemoveClone then Move into same root",
				build: func(_ *testing.T, _ *env, fp fixturePaths, cloneDir string, eng *engine.Engine) error {
					b := eng.NewBuilder(engine.Meta{Action: "repair"})
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					if err := b.Move(fp.beta, cloneDir); err != nil {
						return err
					}
					_, err := b.Build()
					return err
				},
			},
			{
				name: "RemoveTree inside clone then RemoveClone",
				build: func(_ *testing.T, _ *env, _ fixturePaths, cloneDir string, eng *engine.Engine) error {
					b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
					if err := b.RemoveTree(filepath.Join(cloneDir, "shared")); err != nil {
						return err
					}
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					_, err := b.Build()
					return err
				},
			},
			{
				name: "Link inside clone then RemoveClone",
				build: func(_ *testing.T, _ *env, _ fixturePaths, cloneDir string, eng *engine.Engine) error {
					b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
					if err := b.Link(filepath.Join(cloneDir, "extra-link"), "shared/ref.md"); err != nil {
						return err
					}
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					_, err := b.Build()
					return err
				},
			},
			{
				name: "Require inside clone then RemoveClone",
				build: func(_ *testing.T, _ *env, _ fixturePaths, cloneDir string, eng *engine.Engine) error {
					sub := filepath.Join(cloneDir, "shared")
					subTree, err := treehash.Dir(sub)
					if err != nil {
						return err
					}
					b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
					if err := b.Require(engine.Image{Path: sub, State: engine.StateTree, TreeSHA: subTree.SHA}); err != nil {
						return err
					}
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					_, err = b.Build()
					return err
				},
			},
			{
				name: "Move parent dir then RemoveClone nested child",
				build: func(_ *testing.T, e *env, _ fixturePaths, cloneDir string, eng *engine.Engine) error {
					parent := filepath.Dir(cloneDir)
					b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
					if err := b.Move(parent, e.home.Path(".agents/skills-moved")); err != nil {
						return err
					}
					if err := b.RemoveClone(cloneDir); err != nil {
						return err
					}
					_, err := b.Build()
					return err
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e, fp := newEnv(t)
				cloneDir := cloneFixture(t, e, ".agents/skills/pack")
				before := observeWorld(t, e)
				err := tt.build(t, e, fp, cloneDir, e.engine())
				if !errors.Is(err, engine.ErrInvalidStep) {
					t.Errorf("build error = %v, want ErrInvalidStep", err)
				}
				assertWorld(t, "refused mixed builder leaves world intact", observeWorld(t, e), before)
			})
		}
	})

	t.Run("Check, Begin, and forged pending record reject mixed clone compositions", func(t *testing.T) {
		e, fp := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		basePlan := clonePlan(t, e, cloneDir)
		cloneImg := basePlan.Steps[0].Pre[0]
		out, err := e.clean().Apply(t.Context(), basePlan)
		must(t, err)
		snapDir := filepath.Join(e.clean().SnapshotDir(out.SnapshotID), "trees", "0")

		e2, fp2 := newEnv(t)
		cloneDir2 := cloneFixture(t, e2, ".agents/skills/pack")
		cloneImg2 := cloneImg
		cloneImg2.Path = cloneDir2
		treeSHA, err := treehash.Dir(fp2.beta)
		must(t, err)

		forgedPlans := map[string]engine.Plan{
			"RemoveClone + Link on same root": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind: engine.StepRemoveClone,
						Path: cloneDir2,
						Pre:  []engine.Image{cloneImg2},
						Post: []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
					},
					{
						Kind:   engine.StepLink,
						Path:   cloneDir2,
						Target: "../beta",
						Pre:    []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
						Post:   []engine.Image{{Path: cloneDir2, State: engine.StateLink, Target: "../beta"}},
					},
				},
			},
			"RemoveClone + RestoreTree on same root": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind: engine.StepRemoveClone,
						Path: cloneDir2,
						Pre:  []engine.Image{cloneImg2},
						Post: []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
					},
					{
						Kind: engine.StepRestoreTree,
						Path: cloneDir2,
						Src:  snapDir,
						Pre:  []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
						Post: []engine.Image{{Path: cloneDir2, State: engine.StateTree, TreeSHA: treeSHA.SHA}},
					},
				},
			},
			"RemoveTree + RestoreClone on same root": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind: engine.StepRemoveTree,
						Path: fp2.beta,
						Pre:  []engine.Image{{Path: fp2.beta, State: engine.StateTree, TreeSHA: treeSHA.SHA}},
						Post: []engine.Image{{Path: fp2.beta, State: engine.StateAbsent}},
					},
					{
						Kind: engine.StepRestoreClone,
						Path: fp2.beta,
						Src:  snapDir,
						Pre:  []engine.Image{{Path: fp2.beta, State: engine.StateAbsent}},
						Post: []engine.Image{{Path: fp2.beta, State: engine.StateClone, CloneSHA: cloneImg2.CloneSHA}},
					},
				},
			},
			"Unlink + RestoreClone on same root": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind:   engine.StepUnlink,
						Path:   fp2.link,
						Target: fp2.linkTarget,
						Pre:    []engine.Image{{Path: fp2.link, State: engine.StateLink, Target: fp2.linkTarget}},
						Post:   []engine.Image{{Path: fp2.link, State: engine.StateAbsent}},
					},
					{
						Kind: engine.StepRestoreClone,
						Path: fp2.link,
						Src:  snapDir,
						Pre:  []engine.Image{{Path: fp2.link, State: engine.StateAbsent}},
						Post: []engine.Image{{Path: fp2.link, State: engine.StateClone, CloneSHA: cloneImg2.CloneSHA}},
					},
				},
			},
			"RemoveClone + RestoreClone on same root": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind: engine.StepRemoveClone,
						Path: cloneDir2,
						Pre:  []engine.Image{cloneImg2},
						Post: []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
					},
					{
						Kind: engine.StepRestoreClone,
						Path: cloneDir2,
						Src:  snapDir,
						Pre:  []engine.Image{{Path: cloneDir2, State: engine.StateAbsent}},
						Post: []engine.Image{{Path: cloneDir2, State: engine.StateClone, CloneSHA: cloneImg2.CloneSHA}},
					},
				},
			},
			"RestoreClone + Link inside clone": {
				Meta: engine.Meta{Action: "repair", Targets: []string{}},
				Steps: []engine.Step{
					{
						Kind: engine.StepRestoreClone,
						Path: e2.home.Path(".agents/skills/new-pack"),
						Src:  snapDir,
						Pre:  []engine.Image{{Path: e2.home.Path(".agents/skills/new-pack"), State: engine.StateAbsent}},
						Post: []engine.Image{{Path: e2.home.Path(".agents/skills/new-pack"), State: engine.StateClone, CloneSHA: cloneImg2.CloneSHA}},
					},
					{
						Kind:   engine.StepLink,
						Path:   e2.home.Path(".agents/skills/new-pack/extra"),
						Target: "SKILL.md",
						Pre:    []engine.Image{{Path: e2.home.Path(".agents/skills/new-pack/extra"), State: engine.StateAbsent}},
						Post:   []engine.Image{{Path: e2.home.Path(".agents/skills/new-pack/extra"), State: engine.StateLink, Target: "SKILL.md"}},
					},
				},
			},
		}
		_ = fp

		for name, p := range forgedPlans {
			t.Run(name, func(t *testing.T) {
				p = reID(t, p)
				before := observeWorld(t, e2)
				eng2 := e2.clean()
				if err := eng2.Check(p); !errors.Is(err, engine.ErrInvalidStep) {
					t.Errorf("Check = %v, want ErrInvalidStep", err)
				}
				if run, err := eng2.Begin(t.Context(), p); !errors.Is(err, engine.ErrInvalidStep) {
					t.Errorf("Begin = %v, want ErrInvalidStep", err)
					if run != nil {
						_ = run.Release()
					}
				}

				rec := engine.Record{
					Format:        "skillhub.operation",
					Version:       2,
					PlanID:        p.ID,
					Plan:          p,
					SnapshotID:    out.SnapshotID,
					SnapshotSHA:   strings.Repeat("a", 64),
					LastCompleted: -1,
				}
				data, err := json.Marshal(rec)
				must(t, err)
				must(t, os.MkdirAll(filepath.Dir(engine.RecordFile(e2.p)), 0o755))
				must(t, os.WriteFile(engine.RecordFile(e2.p), data, 0o600))
				t.Cleanup(func() { _ = os.Remove(engine.RecordFile(e2.p)) })

				if _, _, err := eng2.PendingRecord(); !errors.Is(err, engine.ErrUnsupportedRecord) {
					t.Errorf("PendingRecord = %v, want ErrUnsupportedRecord", err)
				}
				if _, err := eng2.Recover(t.Context()); !errors.Is(err, engine.ErrUnsupportedRecord) {
					t.Errorf("Recover = %v, want ErrUnsupportedRecord", err)
				}
				must(t, os.Remove(engine.RecordFile(e2.p)))
				assertWorld(t, "forged mixed plan leaves world intact", observeWorld(t, e2), before)
			})
		}
	})
}

type dirOnlyFiles struct {
	engine.Files
	dirReader engine.DirReader
}

func (d dirOnlyFiles) ReadDir(dir string) ([]iofs.DirEntry, error) {
	return d.dirReader.ReadDir(dir)
}

type failMetaFiles struct {
	engine.Files
	failPath string
	failErr  error
}

func (f *failMetaFiles) ReadDir(dir string) ([]iofs.DirEntry, error) {
	return f.Files.(engine.DirReader).ReadDir(dir)
}

func (f *failMetaFiles) EntryMetadata(path string) (uint64, bool, bool, error) {
	if path == f.failPath {
		return 0, false, false, f.failErr
	}
	return f.Files.(engine.EntryMetadataReader).EntryMetadata(path)
}

func TestRemoveCloneFailsClosedWhenMetadataInspectionUnavailable(t *testing.T) {
	t.Run("missing EntryMetadataReader even with DirReader", func(t *testing.T) {
		e, _ := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		before := observeWorld(t, e)

		eng := engine.New(engine.Deps{
			Paths: e.p,
			Files: dirOnlyFiles{Files: e.files, dirReader: e.files},
			Links: fs.NewLinkOps(e.p.Guard()),
			Lock:  e.lock,
			State: e.state,
			Dirs:  fs.NewScanner(e.p.Guard()),
			Clock: e.clock,
		})
		b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
		err := b.RemoveClone(cloneDir)
		if !errors.Is(err, engine.ErrUnsupportedTree) {
			t.Fatalf("RemoveClone without EntryMetadataReader = %v, want ErrUnsupportedTree", err)
		}
		assertWorld(t, "disk and journal untouched", observeWorld(t, e), before)
	})

	targets := map[string]struct {
		rel string
		err error
	}{
		"root dir EOPNOTSUPP":      {rel: "", err: unix.EOPNOTSUPP},
		"git dir ENOSYS":           {rel: ".git", err: unix.ENOSYS},
		"git config EACCES":        {rel: ".git/config", err: unix.EACCES},
		"worktree file EOPNOTSUPP": {rel: "skills/one/SKILL.md", err: unix.EOPNOTSUPP},
		"worktree symlink EACCES":  {rel: "skills/one/README.md", err: unix.EACCES},
	}

	for name, tc := range targets {
		t.Run(name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			before := observeWorld(t, e)
			failPath := cloneDir
			if tc.rel != "" {
				failPath = filepath.Join(cloneDir, filepath.FromSlash(tc.rel))
			}

			eng := engine.New(engine.Deps{
				Paths: e.p,
				Files: &failMetaFiles{Files: e.files, failPath: failPath, failErr: tc.err},
				Links: fs.NewLinkOps(e.p.Guard()),
				Lock:  e.lock,
				State: e.state,
				Dirs:  fs.NewScanner(e.p.Guard()),
				Clock: e.clock,
			})
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			err := b.RemoveClone(cloneDir)
			if !errors.Is(err, engine.ErrUnsupportedTree) || !errors.Is(err, tc.err) {
				t.Fatalf("RemoveClone error = %v, want ErrUnsupportedTree wrapping %v", err, tc.err)
			}
			assertWorld(t, "disk and journal untouched on inspection error", observeWorld(t, e), before)
		})
	}
}

func TestCloneSnapshotBeginVerifyAndUndoRefuseMetadataChanges(t *testing.T) {
	mutations := map[string]func(t *testing.T, dir string){
		"hard link": func(t *testing.T, dir string) {
			must(t, os.Link(filepath.Join(dir, "shared", "ref.md"), filepath.Join(dir, "shared", "ref-hard.md")))
		},
		"xattr": func(t *testing.T, dir string) {
			setXattrOrSkip(t, filepath.Join(dir, ".git", "config"), "user.skillhub", []byte("tamper"))
		},
		"POSIX ACL": func(t *testing.T, dir string) {
			setXattrOrSkip(t, filepath.Join(dir, "empty"), "system.posix_acl_default", posixACLPayload(7))
		},
	}

	for name, mutate := range mutations {
		t.Run("before Check and Begin/"+name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			must(t, b.RemoveClone(cloneDir))
			plan, err := b.Build()
			must(t, err)

			mutate(t, cloneDir)
			before := observeWorld(t, e)
			if err := eng.Check(plan); !errors.Is(err, engine.ErrUnsupportedTree) {
				t.Errorf("Check = %v, want ErrUnsupportedTree", err)
			}
			if run, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrUnsupportedTree) {
				t.Errorf("Begin = %v, want ErrUnsupportedTree", err)
				if run != nil {
					_ = run.Release()
				}
			}
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Errorf("refused Begin left record or snapshots: %v", snapshotDirs(t, e.p))
			}
			assertWorld(t, "refused Check/Begin leaves disk and journal intact", observeWorld(t, e), before)
		})

		t.Run("during Begin copy/"+name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			before := observeWorld(t, e)
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			must(t, b.RemoveClone(cloneDir))
			plan, err := b.Build()
			must(t, err)

			e.files.afterCopy = func(dst string) { mutate(t, dst) }
			if run, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("Begin = %v, want ErrSnapshotMismatch", err)
				if run != nil {
					_ = run.Release()
				}
			}
			if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Errorf("failed Begin left record or snapshots: %v", snapshotDirs(t, e.p))
			}
			assertWorld(t, "refused Begin leaves disk and journal intact", observeWorld(t, e), before)
		})

		t.Run("in stored snapshot before Verify and Undo/"+name, func(t *testing.T) {
			e, _ := newEnv(t)
			cloneDir := cloneFixture(t, e, ".agents/skills/pack")
			eng := e.engine()
			b := eng.NewBuilder(engine.Meta{Action: "remove-clone"})
			must(t, b.RemoveClone(cloneDir))
			plan, err := b.Build()
			must(t, err)
			out, err := eng.Apply(t.Context(), plan)
			must(t, err)
			removed := observeWorld(t, e)

			copyDir := filepath.Join(eng.SnapshotDir(out.SnapshotID), "trees", "0")
			mutate(t, copyDir)
			if _, err := e.clean().VerifySnapshot(out.SnapshotID); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("VerifySnapshot = %v, want ErrSnapshotMismatch", err)
			}
			if _, err := e.clean().Undo(t.Context(), out.SnapshotID); !errors.Is(err, engine.ErrSnapshotMismatch) {
				t.Errorf("Undo = %v, want ErrSnapshotMismatch", err)
			}
			assertWorld(t, "refused Verify/Undo leaves disk and journal intact", observeWorld(t, e), removed)
		})
	}
}

func TestCloneRemovePlusUnlinkRecoversAcrossRepeatedInterruptions(t *testing.T) {
	setupApply := func(t *testing.T) (*env, func(*engine.Engine) error, string) {
		t.Helper()
		e, _ := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		link := e.home.Path(".claude/skills/one")
		target := "../../.agents/skills/pack/skills/one"
		e.home.Symlink(".claude/skills/one", target)
		noOwnLevels(t, e)
		b := e.clean().NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
		must(t, b.Unlink(link, target))
		must(t, b.RemoveClone(cloneDir))
		plan, err := b.Build()
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Apply(t.Context(), plan); return err }, "applied"
	}

	setupUndo := func(t *testing.T) (*env, func(*engine.Engine) error, string) {
		t.Helper()
		e, _ := newEnv(t)
		cloneDir := cloneFixture(t, e, ".agents/skills/pack")
		link := e.home.Path(".claude/skills/one")
		target := "../../.agents/skills/pack/skills/one"
		e.home.Symlink(".claude/skills/one", target)
		noOwnLevels(t, e)
		b := e.clean().NewBuilder(engine.Meta{Action: "remove-clone", Targets: []string{"claude-code"}})
		must(t, b.Unlink(link, target))
		must(t, b.RemoveClone(cloneDir))
		plan, err := b.Build()
		must(t, err)
		out, err := e.clean().Apply(t.Context(), plan)
		must(t, err)
		return e, func(eng *engine.Engine) error { _, err := eng.Undo(t.Context(), out.SnapshotID); return err }, "undone"
	}

	for name, setup := range map[string]func(*testing.T) (*env, func(*engine.Engine) error, string){
		"remove-clone+unlink": setupApply,
		"undo-clone+link":     setupUndo,
	} {
		t.Run(name, func(t *testing.T) {
			e0, fn0, wantAction := setup(t)
			e0.files.arm(-1, failOnce)
			must(t, fn0(e0.engine()))
			writes := e0.files.writeLog()
			commitIdx := slices.Index(writes, "AppendJournal "+wantAction)
			if commitIdx <= 0 {
				t.Fatalf("AppendJournal %s idx = %d in %v", wantAction, commitIdx, writes)
			}

			// Probe how many writes a clean Recover performs right before commit.
			eProbe, fnProbe, _ := setup(t)
			eProbe.files.arm(commitIdx, crashBefore)
			if err := fnProbe(eProbe.engine()); !errors.Is(err, errInjected) {
				t.Fatalf("probe apply error = %v", err)
			}
			eProbe.files.arm(-1, failOnce)
			resProbe, err := eProbe.engine().Recover(t.Context())
			must(t, err)
			if !resProbe.Found || !resProbe.RolledBack {
				t.Fatalf("probe Recover = %+v, want Found && RolledBack", resProbe)
			}
			recWrites := eProbe.files.writeLog()
			if len(recWrites) == 0 {
				t.Fatal("probe Recover made no writes")
			}

			// Fail Recover at every single write index (crashBefore and crashAfter),
			// then run a subsequent Recover to verify convergence.
			for k := range recWrites {
				for _, mode := range []faultMode{crashBefore, crashAfter} {
					t.Run(recWrites[k]+" "+mode.String(), func(t *testing.T) {
						e, fn, _ := setup(t)
						start := observeWorld(t, e)
						e.files.arm(commitIdx, crashBefore)
						if err := fn(e.engine()); !errors.Is(err, errInjected) {
							t.Fatalf("initial run error = %v, want errInjected", err)
						}

						e.files.arm(k, mode)
						if _, err := e.engine().Recover(t.Context()); !errors.Is(err, errInjected) {
							t.Fatalf("first Recover at write %d (%s): err = %v, want errInjected", k, recWrites[k], err)
						}

						hadRecord := recordExists(t, e.p)
						res, err := e.clean().Recover(t.Context())
						must(t, err)
						if res.Found != hadRecord || (hadRecord && !res.RolledBack) {
							t.Fatalf("second Recover = %+v (hadRecord=%t), want Found==hadRecord && RolledBack", res, hadRecord)
						}
						assertWorld(t, "after repeated-interruption Recover", observeWorld(t, e), start)
					})
				}
			}
		})
	}
}
