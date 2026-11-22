package fs_test

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// linkWorld is the disk layout every LinkOps contract case starts from:
//
//	base/root/store/alpha/SKILL.md     a skill folder
//	base/root/store/beta/SKILL.md      another one
//	base/root/harness/                 an empty link directory
//	base/root/parked/                  an empty folder to move into
//	base/root/escape -> base/forbidden a directory link into the forbidden prefix
//	base/forbidden/keep.txt            under the forbidden prefix
//	base/forbidden/flink -> keep.txt   a link under the forbidden prefix
//	base/forbidden/out -> ../root/store/beta
//	                                   a link under the forbidden prefix that
//	                                   points outside it: reached through
//	                                   escape, only a check of the parent
//	                                   catches it
type linkWorld struct {
	ops       ports.LinkOps
	base      string
	root      string
	forbidden string
}

func (w linkWorld) at(rel string) string { return filepath.Join(w.root, filepath.FromSlash(rel)) }

// linkOpsImpls are the implementations that must honor the same contract.
var linkOpsImpls = []struct {
	name string
	make func(base, forbidden string) ports.LinkOps
}{
	{name: "real", make: func(_, forbidden string) ports.LinkOps {
		return fsadapter.NewLinkOps(paths.NewGuard(forbidden))
	}},
	{name: "fake", make: func(base, forbidden string) ports.LinkOps {
		return fakes.NewLinkOps(base, paths.NewGuard(forbidden))
	}},
}

func newLinkWorld(t *testing.T, make func(base, forbidden string) ports.LinkOps) linkWorld {
	t.Helper()
	base := t.TempDir()
	w := linkWorld{base: base, root: filepath.Join(base, "root"), forbidden: filepath.Join(base, "forbidden")}
	writeFile(t, w.at("store/alpha/SKILL.md"), "alpha")
	writeFile(t, w.at("store/beta/SKILL.md"), "beta")
	mustNoErr(t, "mkdir harness", os.MkdirAll(w.at("harness"), 0o755))
	mustNoErr(t, "mkdir parked", os.MkdirAll(w.at("parked"), 0o755))
	writeFile(t, filepath.Join(w.forbidden, "keep.txt"), "keep")
	plantLink(t, "keep.txt", filepath.Join(w.forbidden, "flink"))
	plantLink(t, "../root/store/beta", filepath.Join(w.forbidden, "out"))
	plantLink(t, w.forbidden, w.at("escape"))
	w.ops = make(base, w.forbidden)
	return w
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mustNoErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	mustNoErr(t, "write", os.WriteFile(path, []byte(content), 0o644))
}

func plantLink(t *testing.T, target, path string) {
	t.Helper()
	mustNoErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
	mustNoErr(t, "symlink", os.Symlink(target, path))
}

// diskTree describes every entry under root without following links:
// "dir <perm>", "file <perm> <content>" or "link <content>".
func diskTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch mode := info.Mode(); {
		case mode&iofs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "link " + target
		case mode.IsDir():
			out[rel] = fmt.Sprintf("dir %04o", mode.Perm())
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[rel] = fmt.Sprintf("file %04o %s", mode.Perm(), data)
		}
		return nil
	})
	mustNoErr(t, "walk", err)
	return out
}

// assertUnchanged fails when the disk under base differs from before.
func assertUnchanged(t *testing.T, base string, before map[string]string) {
	t.Helper()
	after := diskTree(t, base)
	if !maps.Equal(before, after) {
		t.Errorf("disk changed:\nbefore %v\nafter  %v", sortedEntries(before), sortedEntries(after))
	}
}

func sortedEntries(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+": "+v)
	}
	slices.Sort(out)
	return out
}

func TestLinkOpsContractSymlink(t *testing.T) {
	for _, impl := range linkOpsImpls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("publishes the content exactly as given and leaves no temporary entry", func(t *testing.T) {
				for _, target := range []string{"../store/alpha", "/somewhere/absolute/alpha", "../store/missing"} {
					w := newLinkWorld(t, impl.make)
					path := w.at("harness/alpha")
					mustNoErr(t, "Symlink", w.ops.Symlink(target, path))
					info, err := os.Lstat(path)
					mustNoErr(t, "Lstat", err)
					if info.Mode()&iofs.ModeSymlink == 0 {
						t.Fatalf("%s is %v, want a symlink", path, info.Mode())
					}
					got, err := w.ops.Readlink(path)
					mustNoErr(t, "Readlink", err)
					if got != target {
						t.Errorf("Readlink = %q, want %q", got, target)
					}
					if names := dirNames(t, w.at("harness")); !slices.Equal(names, []string{"alpha"}) {
						t.Errorf("harness holds %v, want only alpha", names)
					}
				}
			})

			existing := map[string]func(t *testing.T, w linkWorld) string{
				"a file": func(t *testing.T, w linkWorld) string {
					writeFile(t, w.at("harness/x"), "mine")
					return w.at("harness/x")
				},
				"a folder":     func(_ *testing.T, w linkWorld) string { return w.at("store/alpha") },
				"an empty dir": func(_ *testing.T, w linkWorld) string { return w.at("harness") },
				"a link": func(t *testing.T, w linkWorld) string {
					plantLink(t, "../store/beta", w.at("harness/x"))
					return w.at("harness/x")
				},
				"a broken link": func(t *testing.T, w linkWorld) string {
					plantLink(t, "../store/gone", w.at("harness/x"))
					return w.at("harness/x")
				},
			}
			for name, plant := range existing {
				t.Run("refuses to replace "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					path := plant(t, w)
					before := diskTree(t, w.base)
					err := w.ops.Symlink("../store/alpha", path)
					if !errors.Is(err, iofs.ErrExist) {
						t.Fatalf("Symlink over %s = %v, want fs.ErrExist", name, err)
					}
					assertUnchanged(t, w.base, before)
				})
			}

			t.Run("fails when the parent folder is missing", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				before := diskTree(t, w.base)
				if err := w.ops.Symlink("../store/alpha", w.at("nowhere/alpha")); !errors.Is(err, iofs.ErrNotExist) {
					t.Fatalf("Symlink = %v, want fs.ErrNotExist", err)
				}
				assertUnchanged(t, w.base, before)
			})

			refused := map[string]struct {
				target, path func(w linkWorld) string
				want         error
			}{
				"a path under the forbidden prefix": {
					target: func(linkWorld) string { return "keep.txt" },
					path:   func(w linkWorld) string { return filepath.Join(w.forbidden, "new") },
					want:   paths.ErrForbidden,
				},
				"a path whose parent links into the forbidden prefix": {
					target: func(linkWorld) string { return "keep.txt" },
					path:   func(w linkWorld) string { return w.at("escape/new") },
					want:   paths.ErrForbidden,
				},
				"content that points into the forbidden prefix": {
					target: func(linkWorld) string { return "../escape/keep.txt" },
					path:   func(w linkWorld) string { return w.at("harness/new") },
					want:   paths.ErrForbidden,
				},
				"a relative path": {
					target: func(linkWorld) string { return "../store/alpha" },
					path:   func(linkWorld) string { return "harness/new" },
					want:   paths.ErrInvalidPath,
				},
				"an empty content": {
					target: func(linkWorld) string { return "" },
					path:   func(w linkWorld) string { return w.at("harness/new") },
					want:   paths.ErrInvalidPath,
				},
			}
			for name, tc := range refused {
				t.Run("refuses "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					before := diskTree(t, w.base)
					if err := w.ops.Symlink(tc.target(w), tc.path(w)); !errors.Is(err, tc.want) {
						t.Fatalf("Symlink = %v, want %v", err, tc.want)
					}
					assertUnchanged(t, w.base, before)
				})
			}
		})
	}
}

func TestLinkOpsContractRenameNoReplace(t *testing.T) {
	for _, impl := range linkOpsImpls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("moves a folder with its content", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				want := diskTree(t, w.at("store/alpha"))
				mustNoErr(t, "RenameNoReplace", w.ops.RenameNoReplace(w.at("store/alpha"), w.at("parked/alpha")))
				if _, err := os.Lstat(w.at("store/alpha")); !errors.Is(err, iofs.ErrNotExist) {
					t.Errorf("source still there: %v", err)
				}
				if got := diskTree(t, w.at("parked/alpha")); !maps.Equal(got, want) {
					t.Errorf("moved tree = %v, want %v", got, want)
				}
			})

			t.Run("moves a link itself, never what it points to", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				plantLink(t, "../store/alpha", w.at("harness/alpha"))
				mustNoErr(t, "RenameNoReplace", w.ops.RenameNoReplace(w.at("harness/alpha"), w.at("harness/alpha2")))
				got, err := os.Readlink(w.at("harness/alpha2"))
				mustNoErr(t, "Readlink", err)
				if got != "../store/alpha" {
					t.Errorf("moved link content = %q", got)
				}
				if _, err := os.Lstat(w.at("store/alpha/SKILL.md")); err != nil {
					t.Errorf("link target touched: %v", err)
				}
			})

			existing := map[string]func(t *testing.T, w linkWorld) string{
				"a file": func(t *testing.T, w linkWorld) string {
					writeFile(t, w.at("parked/x"), "mine")
					return w.at("parked/x")
				},
				"a folder":     func(_ *testing.T, w linkWorld) string { return w.at("store/beta") },
				"an empty dir": func(_ *testing.T, w linkWorld) string { return w.at("harness") },
				"a link": func(t *testing.T, w linkWorld) string {
					plantLink(t, "../store/beta", w.at("parked/x"))
					return w.at("parked/x")
				},
				"a broken link": func(t *testing.T, w linkWorld) string {
					plantLink(t, "../store/gone", w.at("parked/x"))
					return w.at("parked/x")
				},
			}
			for name, plant := range existing {
				t.Run("refuses to replace "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					dst := plant(t, w)
					before := diskTree(t, w.base)
					if err := w.ops.RenameNoReplace(w.at("store/alpha"), dst); !errors.Is(err, iofs.ErrExist) {
						t.Fatalf("RenameNoReplace onto %s = %v, want fs.ErrExist", name, err)
					}
					assertUnchanged(t, w.base, before)
				})
			}

			t.Run("fails when the source is missing", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				before := diskTree(t, w.base)
				if err := w.ops.RenameNoReplace(w.at("store/gone"), w.at("parked/gone")); !errors.Is(err, iofs.ErrNotExist) {
					t.Fatalf("RenameNoReplace = %v, want fs.ErrNotExist", err)
				}
				assertUnchanged(t, w.base, before)
			})

			refused := map[string]struct {
				from, to func(w linkWorld) string
				want     error
			}{
				"a source under the forbidden prefix": {
					from: func(w linkWorld) string { return filepath.Join(w.forbidden, "keep.txt") },
					to:   func(w linkWorld) string { return w.at("parked/keep.txt") },
					want: paths.ErrForbidden,
				},
				"a source whose parent links into the forbidden prefix": {
					from: func(w linkWorld) string { return w.at("escape/keep.txt") },
					to:   func(w linkWorld) string { return w.at("parked/keep.txt") },
					want: paths.ErrForbidden,
				},
				"a link reached through a parent link that points outside the prefix": {
					from: func(w linkWorld) string { return w.at("escape/out") },
					to:   func(w linkWorld) string { return w.at("parked/out") },
					want: paths.ErrForbidden,
				},
				"a destination under the forbidden prefix": {
					from: func(w linkWorld) string { return w.at("store/alpha") },
					to:   func(w linkWorld) string { return filepath.Join(w.forbidden, "alpha") },
					want: paths.ErrForbidden,
				},
				"a destination whose parent links into the forbidden prefix": {
					from: func(w linkWorld) string { return w.at("store/alpha") },
					to:   func(w linkWorld) string { return w.at("escape/alpha") },
					want: paths.ErrForbidden,
				},
				"a relative path": {
					from: func(linkWorld) string { return "store/alpha" },
					to:   func(w linkWorld) string { return w.at("parked/alpha") },
					want: paths.ErrInvalidPath,
				},
			}
			for name, tc := range refused {
				t.Run("refuses "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					before := diskTree(t, w.base)
					if err := w.ops.RenameNoReplace(tc.from(w), tc.to(w)); !errors.Is(err, tc.want) {
						t.Fatalf("RenameNoReplace = %v, want %v", err, tc.want)
					}
					assertUnchanged(t, w.base, before)
				})
			}
		})
	}
}

func TestLinkOpsContractRemoveLink(t *testing.T) {
	for _, impl := range linkOpsImpls {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("removes a link whose content is exactly the target, not what it points to", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				plantLink(t, "../store/alpha", w.at("harness/alpha"))
				before := diskTree(t, w.base)
				mustNoErr(t, "RemoveLink", w.ops.RemoveLink(w.at("harness/alpha"), "../store/alpha"))
				delete(before, filepath.Join("root", "harness", "alpha"))
				assertUnchanged(t, w.base, before)
			})

			t.Run("removes a broken link", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				plantLink(t, "../store/gone", w.at("harness/gone"))
				mustNoErr(t, "RemoveLink", w.ops.RemoveLink(w.at("harness/gone"), "../store/gone"))
				if _, err := os.Lstat(w.at("harness/gone")); !errors.Is(err, iofs.ErrNotExist) {
					t.Errorf("link still there: %v", err)
				}
			})

			mismatches := map[string]struct {
				plant  func(t *testing.T, w linkWorld) string
				target string
			}{
				"a link with other content": {
					plant: func(t *testing.T, w linkWorld) string {
						plantLink(t, "../store/beta", w.at("harness/alpha"))
						return w.at("harness/alpha")
					},
					target: "../store/alpha",
				},
				"a link to the same place written differently": {
					plant: func(t *testing.T, w linkWorld) string {
						plantLink(t, w.at("store/alpha"), w.at("harness/alpha"))
						return w.at("harness/alpha")
					},
					target: "../store/alpha",
				},
				"a real folder": {
					plant:  func(_ *testing.T, w linkWorld) string { return w.at("store/alpha") },
					target: "../store/alpha",
				},
				"a regular file": {
					plant:  func(_ *testing.T, w linkWorld) string { return w.at("store/alpha/SKILL.md") },
					target: "alpha",
				},
			}
			for name, tc := range mismatches {
				t.Run("refuses "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					path := tc.plant(t, w)
					before := diskTree(t, w.base)
					if err := w.ops.RemoveLink(path, tc.target); !errors.Is(err, ports.ErrLinkMismatch) {
						t.Fatalf("RemoveLink = %v, want ErrLinkMismatch", err)
					}
					assertUnchanged(t, w.base, before)
				})
			}

			t.Run("reports a missing path as a mismatch that does not exist", func(t *testing.T) {
				w := newLinkWorld(t, impl.make)
				err := w.ops.RemoveLink(w.at("harness/gone"), "../store/gone")
				if !errors.Is(err, ports.ErrLinkMismatch) || !errors.Is(err, iofs.ErrNotExist) {
					t.Fatalf("RemoveLink = %v, want ErrLinkMismatch and fs.ErrNotExist", err)
				}
			})

			refused := map[string]struct {
				path   func(w linkWorld) string
				target string
				want   error
			}{
				"a link under the forbidden prefix": {
					path: func(w linkWorld) string { return filepath.Join(w.forbidden, "flink") }, target: "keep.txt", want: paths.ErrForbidden,
				},
				"a link whose parent links into the forbidden prefix": {
					path: func(w linkWorld) string { return w.at("escape/flink") }, target: "keep.txt", want: paths.ErrForbidden,
				},
				"a link reached through a parent link that points outside the prefix": {
					path: func(w linkWorld) string { return w.at("escape/out") }, target: "../root/store/beta", want: paths.ErrForbidden,
				},
				"a relative path": {
					path: func(linkWorld) string { return "harness/alpha" }, target: "../store/alpha", want: paths.ErrInvalidPath,
				},
			}
			for name, tc := range refused {
				t.Run("refuses "+name, func(t *testing.T) {
					w := newLinkWorld(t, impl.make)
					before := diskTree(t, w.base)
					if err := w.ops.RemoveLink(tc.path(w), tc.target); !errors.Is(err, tc.want) {
						t.Fatalf("RemoveLink = %v, want %v", err, tc.want)
					}
					assertUnchanged(t, w.base, before)
				})
			}
		})
	}
}

func TestLinkOpsContractReadlinkAndSameDevice(t *testing.T) {
	for _, impl := range linkOpsImpls {
		t.Run(impl.name, func(t *testing.T) {
			w := newLinkWorld(t, impl.make)
			plantLink(t, "../store/alpha", w.at("harness/alpha"))

			got, err := w.ops.Readlink(w.at("harness/alpha"))
			if err != nil || got != "../store/alpha" {
				t.Errorf("Readlink = %q, %v; want ../store/alpha", got, err)
			}
			if _, err := w.ops.Readlink(w.at("store/alpha")); err == nil {
				t.Error("Readlink of a folder succeeded")
			}
			if _, err := w.ops.Readlink(filepath.Join(w.forbidden, "flink")); !errors.Is(err, paths.ErrForbidden) {
				t.Errorf("Readlink under the forbidden prefix = %v, want ErrForbidden", err)
			}
			if _, err := w.ops.Readlink(w.at("escape/out")); !errors.Is(err, paths.ErrForbidden) {
				t.Errorf("Readlink through a parent link into the forbidden prefix = %v, want ErrForbidden", err)
			}

			same, err := w.ops.SameDevice(w.at("store/alpha"), w.at("harness"))
			if err != nil || !same {
				t.Errorf("SameDevice in one temp dir = %v, %v; want true", same, err)
			}
			if _, err := w.ops.SameDevice(w.at("store/gone"), w.at("harness")); !errors.Is(err, iofs.ErrNotExist) {
				t.Errorf("SameDevice of a missing path = %v, want fs.ErrNotExist", err)
			}
			if _, err := w.ops.SameDevice(w.at("harness"), filepath.Join(w.forbidden, "keep.txt")); !errors.Is(err, paths.ErrForbidden) {
				t.Errorf("SameDevice under the forbidden prefix = %v, want ErrForbidden", err)
			}
		})
	}
}

// hasPrefixEntry reports whether any name in dir starts with prefix.
func hasPrefixEntry(t *testing.T, dir, prefix string) bool {
	t.Helper()
	for _, name := range dirNames(t, dir) {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
