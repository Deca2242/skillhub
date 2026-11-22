package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// maxLinkHops bounds symlink resolution so a link loop cannot hang a check.
const maxLinkHops = 40

// Guard rejects paths under a set of forbidden prefixes. A path is forbidden
// when its lexical form or its symlink-resolved form is under a prefix, or
// under the symlink-resolved form of a prefix. The zero Guard forbids nothing.
type Guard struct {
	prefixes []string
}

// NewGuard builds a Guard from a list of absolute prefixes separated by
// os.PathListSeparator. Empty and relative entries are ignored.
func NewGuard(list string) Guard {
	var g Guard
	for _, entry := range filepath.SplitList(list) {
		if !filepath.IsAbs(entry) {
			continue
		}
		clean := filepath.Clean(entry)
		g.prefixes = append(g.prefixes, clean)
		if resolved := resolve(clean); resolved != clean {
			g.prefixes = append(g.prefixes, resolved)
		}
	}
	return g
}

// Active reports whether the guard forbids anything.
func (g Guard) Active() bool { return len(g.prefixes) > 0 }

// Check returns ErrInvalidPath for a relative path and ErrForbidden for a
// path under a forbidden prefix.
func (g Guard) Check(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %q is not absolute", ErrInvalidPath, path)
	}
	if !g.Active() {
		return nil
	}
	// The lexical check runs first so a path under a prefix is rejected
	// without touching the file system under that prefix.
	clean := filepath.Clean(path)
	if g.matches(clean) || g.matches(resolve(clean)) {
		return fmt.Errorf("%w: %s", ErrForbidden, path)
	}
	return nil
}

func (g Guard) matches(path string) bool {
	for _, prefix := range g.prefixes {
		if within(path, prefix) {
			return true
		}
	}
	return false
}

func within(path, prefix string) bool {
	rel, err := filepath.Rel(prefix, path)
	return err == nil && filepath.IsLocal(rel)
}

// resolve returns the absolute, clean path with every existing symlink
// component resolved, dangling links included. Components that do not exist
// yet are kept as they are, so a path about to be created resolves too.
func resolve(path string) string {
	cur := filepath.Clean(path)
	rest := ""
	for hops := 0; hops < maxLinkHops; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		// cur does not resolve: follow it by hand when it is a dangling link,
		// otherwise move up one component.
		if info, err := os.Lstat(cur); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(cur)
			if err != nil {
				return filepath.Join(cur, rest)
			}
			if !filepath.IsAbs(target) {
				// A relative target counts from the directory that really
				// holds the link, which differs from the lexical parent when
				// that parent goes through a symlink.
				parent := filepath.Dir(cur)
				if resolved, err := filepath.EvalSymlinks(parent); err == nil {
					parent = resolved
				}
				target = filepath.Join(parent, target)
			}
			cur = filepath.Clean(target)
			hops++
			continue
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Join(cur, rest)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
	return filepath.Join(cur, rest)
}
