package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// chmodExact sets mode on path, special bits included, and fails the test
// when the file system does not keep them.
func chmodExact(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	must(t, os.Chmod(path, mode))
	info, err := os.Lstat(path)
	must(t, err)
	if got := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); got != mode {
		t.Fatalf("%s has mode %v, want %v: the file system dropped a bit", path, got, mode)
	}
}

// specialBits are the trees the snapshot cannot keep exactly: a set-user-ID,
// set-group-ID or sticky bit on any node, the root included.
var specialBits = []struct {
	name string
	set  func(t *testing.T, e *env)
}{
	{"a setgid folder inside", func(t *testing.T, e *env) {
		chmodExact(t, e.home.Mkdir(".agents/skills/beta/shared"), 0o755|fs.ModeSetgid)
		e.home.WriteFile(".agents/skills/beta/shared/notes.md", "n\n")
	}},
	{"a setuid file", func(t *testing.T, e *env) {
		chmodExact(t, e.home.WriteFileMode(".agents/skills/beta/run", "#!/bin/sh\n", 0o755), 0o755|fs.ModeSetuid)
	}},
	{"a setgid file", func(t *testing.T, e *env) {
		chmodExact(t, e.home.WriteFileMode(".agents/skills/beta/run", "#!/bin/sh\n", 0o755), 0o755|fs.ModeSetgid)
	}},
	{"a sticky root", func(t *testing.T, e *env) {
		chmodExact(t, e.home.Path(".agents/skills/beta"), 0o755|fs.ModeSticky)
	}},
	{"a setgid root", func(t *testing.T, e *env) {
		chmodExact(t, e.home.Path(".agents/skills/beta"), 0o755|fs.ModeSetgid)
	}},
}

func TestATreeWithSpecialModeBitsIsRefusedWhenThePlanIsBuilt(t *testing.T) {
	for _, tt := range specialBits {
		t.Run(tt.name, func(t *testing.T) {
			e, fp := newEnv(t)
			tt.set(t, e)
			b := e.engine().NewBuilder(engine.Meta{Action: "remove"})

			if err := b.RemoveTree(fp.beta); !errors.Is(err, engine.ErrUnsupportedTree) {
				t.Errorf("RemoveTree error = %v, want ErrUnsupportedTree", err)
			}
			if _, err := b.Build(); !errors.Is(err, engine.ErrUnsupportedTree) {
				t.Errorf("Build error = %v, want ErrUnsupportedTree", err)
			}
		})
	}
}

func TestATreeThatGainsSpecialModeBitsIsRefusedByBeginBeforeCopying(t *testing.T) {
	for _, tt := range specialBits {
		t.Run(tt.name, func(t *testing.T) {
			e, fp := newEnv(t)
			eng := e.engine()
			// The bits come with content, so the tree hash alone already
			// differs for some cases; the sticky and setgid roots keep it.
			b := eng.NewBuilder(engine.Meta{Action: "remove"})
			must(t, b.RemoveTree(fp.beta))
			plan, err := b.Build()
			must(t, err)
			tt.set(t, e)

			run, err := eng.Begin(t.Context(), plan)
			if err == nil {
				must(t, run.Release())
				t.Fatal("Begin accepted the tree")
			}
			if len(e.files.writeLog()) != 0 || recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
				t.Errorf("Begin wrote %v before refusing", e.files.writeLog())
			}
		})
	}
	// The cases whose content does not change must fail for the bit itself.
	for _, name := range []string{"a sticky root", "a setgid root"} {
		e, fp := newEnv(t)
		eng := e.engine()
		b := eng.NewBuilder(engine.Meta{Action: "remove"})
		must(t, b.RemoveTree(fp.beta))
		plan, err := b.Build()
		must(t, err)
		for _, tt := range specialBits {
			if tt.name == name {
				tt.set(t, e)
			}
		}
		if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrUnsupportedTree) {
			t.Errorf("%s: Begin error = %v, want ErrUnsupportedTree", name, err)
		}
	}
}

func TestTheSnapshotComparesTheWholeModeOfEveryNode(t *testing.T) {
	e, fp := newEnv(t)
	e.files.afterCopy = func(dst string) {
		_ = os.Chmod(filepath.Join(dst, "notes"), 0o755|fs.ModeSetgid)
	}
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "remove"})
	must(t, b.RemoveTree(fp.alpha))
	plan, err := b.Build()
	must(t, err)

	if _, err := eng.Begin(t.Context(), plan); !errors.Is(err, engine.ErrSnapshotMismatch) {
		t.Errorf("a copy with a setgid bit the folder lacks: error = %v, want ErrSnapshotMismatch", err)
	}
	if recordExists(t, e.p) || len(snapshotDirs(t, e.p)) != 0 {
		t.Error("a refused snapshot left a record or a snapshot")
	}
}

func TestPathsAndLinkTargetsMustBeUTF8(t *testing.T) {
	steps := map[string]func(b *engine.Builder, fp fixturePaths) error{
		"a folder to remove": func(b *engine.Builder, fp fixturePaths) error { return b.RemoveTree(fp.alpha + "\xff") },
		"a move destination": func(b *engine.Builder, fp fixturePaths) error { return b.Move(fp.alpha, fp.parked+"\xfe") },
		"a link path":        func(b *engine.Builder, fp fixturePaths) error { return b.Link(fp.parked+"\xff", "x") },
		"a link target":      func(b *engine.Builder, fp fixturePaths) error { return b.Link(fp.parked, "../\xff") },
		"an unlink target":   func(b *engine.Builder, fp fixturePaths) error { return b.Unlink(fp.link, "\xff") },
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			b := e.engine().NewBuilder(engine.Meta{Action: "test"})
			if err := step(b, fp); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("error = %v, want ErrInvalidStep", err)
			}
		})
	}

	// A plan carrying such a path, rehashed, is refused too: two of them
	// would serialize alike (U+FFFD) and share an identifier.
	e, fp := newEnv(t)
	eng := e.engine()
	b := eng.NewBuilder(engine.Meta{Action: "link"})
	must(t, b.Link(fp.parked, "x"))
	plan, err := b.Build()
	must(t, err)
	plan.Steps = []engine.Step{plan.Steps[0]}
	plan.Steps[0].Target = "\xff"
	plan.Steps[0].Post = []engine.Image{{Path: fp.parked, State: engine.StateLink, Target: "\xff"}}
	plan.ID, err = plan.ComputeID()
	must(t, err)
	if err := eng.Check(plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Check error = %v, want ErrInvalidStep", err)
	}
}

func TestNestedOrRepeatedPathsAreRefused(t *testing.T) {
	tests := map[string]func(b *engine.Builder, fp fixturePaths){
		"a move into its own source": func(b *engine.Builder, fp fixturePaths) {
			_ = b.Move(fp.alpha, filepath.Join(fp.alpha, "inside"))
		},
		"a link inside a folder moved before": func(b *engine.Builder, fp fixturePaths) {
			_ = b.Move(fp.alpha, fp.parked)
			_ = b.Link(filepath.Join(fp.alpha, "new"), "x")
		},
		"a link inside a folder removed before": func(b *engine.Builder, fp fixturePaths) {
			_ = b.RemoveTree(fp.beta)
			_ = b.Link(filepath.Join(fp.beta, "new"), "x")
		},
		"a move out of a folder removed before": func(b *engine.Builder, fp fixturePaths) {
			_ = b.RemoveTree(fp.alpha)
			_ = b.Move(filepath.Join(fp.alpha, "notes"), fp.parked)
		},
		"a move then a removal of the same path": func(b *engine.Builder, fp fixturePaths) {
			_ = b.Move(fp.alpha, fp.parked)
			_ = b.RemoveTree(fp.parked)
		},
		"a link then an unlink of the same path": func(b *engine.Builder, fp fixturePaths) {
			_ = b.Link(fp.alpha+"-link", "x")
			_ = b.Unlink(fp.alpha+"-link", "x")
		},
		"a link, then an unlink of the replacement": func(b *engine.Builder, fp fixturePaths) {
			_ = b.Unlink(fp.link, fp.linkTarget)
			_ = b.Link(fp.link, "../../.agents/skills/beta")
			_ = b.Unlink(fp.link, "../../.agents/skills/beta")
		},
		"a link inside a path replaced in place": func(b *engine.Builder, fp fixturePaths) {
			_ = b.RemoveTree(fp.beta)
			_ = b.Link(fp.beta, "../alpha")
			_ = b.Link(filepath.Join(fp.beta, "x"), "y")
		},
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			b := e.engine().NewBuilder(engine.Meta{Action: "test"})
			steps(b, fp)
			if _, err := b.Build(); !errors.Is(err, engine.ErrInvalidStep) {
				t.Errorf("Build error = %v, want ErrInvalidStep", err)
			}
		})
	}

	// A path an earlier step left absent may be used again: the in-place
	// replacements of docs/design-v2.md section 8.
	allowed := map[string]func(b *engine.Builder, fp fixturePaths) error{
		"an unlink then a link of the same path": func(b *engine.Builder, fp fixturePaths) error {
			return errors.Join(b.Unlink(fp.link, fp.linkTarget), b.Link(fp.link, "../../.agents/skills/beta"))
		},
		"a removal then a link of the same path": func(b *engine.Builder, fp fixturePaths) error {
			return errors.Join(b.RemoveTree(fp.beta), b.Link(fp.beta, "alpha"))
		},
		"a move away then a link of the same path": func(b *engine.Builder, fp fixturePaths) error {
			return errors.Join(b.Move(fp.alpha, fp.parked), b.Link(fp.alpha, "../.skillhub/disabled/alpha"))
		},
		"a move to where a folder was moved from": func(b *engine.Builder, fp fixturePaths) error {
			return errors.Join(b.Move(fp.alpha, fp.parked), b.Move(fp.beta, fp.alpha))
		},
	}
	for name, steps := range allowed {
		t.Run(name, func(t *testing.T) {
			e, fp := newEnv(t)
			b := e.engine().NewBuilder(engine.Meta{Action: "test"})
			must(t, steps(b, fp))
			if _, err := b.Build(); err != nil {
				t.Errorf("Build: %v", err)
			}
		})
	}

	// Paths that only share a prefix are not nested.
	e, fp := newEnv(t)
	b := e.engine().NewBuilder(engine.Meta{Action: "test"})
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.Link(fp.alpha+"-2", "x"))
	if _, err := b.Build(); err != nil {
		t.Errorf("siblings sharing a prefix: %v", err)
	}

	// A rehashed plan with a destination inside its source fails Check.
	eng := e.engine()
	b = eng.NewBuilder(engine.Meta{Action: "move"})
	must(t, b.Move(fp.alpha, fp.parked))
	plan, err := b.Build()
	must(t, err)
	inside := filepath.Join(fp.alpha, "inside")
	plan.Steps = []engine.Step{plan.Steps[0]}
	plan.Steps[0].Dst = inside
	plan.Steps[0].Pre = []engine.Image{plan.Steps[0].Pre[0], {Path: inside, State: engine.StateAbsent}}
	plan.Steps[0].Post = []engine.Image{plan.Steps[0].Post[0], {Path: inside, State: engine.StateTree, TreeSHA: plan.Steps[0].Pre[0].TreeSHA}}
	plan.ID, err = plan.ComputeID()
	must(t, err)
	if err := eng.Check(plan); !errors.Is(err, engine.ErrInvalidStep) {
		t.Errorf("Check error = %v, want ErrInvalidStep", err)
	}
}

// Steps on the same lock entry still chain: park, then put back.
func TestLockStepsOnOneEntryStillFollowEachOther(t *testing.T) {
	e, _ := newEnv(t)
	b := e.engine().NewBuilder(engine.Meta{Action: "test"})
	must(t, b.LockPark("alpha"))
	must(t, b.LockUnpark(domain.ParkedEntry{Name: "alpha", Raw: []byte(`{"source":"owner/alpha"}`), Before: "beta"}))
	if _, err := b.Build(); err != nil {
		t.Errorf("Build: %v", err)
	}
}
