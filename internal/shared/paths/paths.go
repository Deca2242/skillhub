// Package paths is the only code in skillhub that resolves a path relative
// to the user's home or reads HOME and the XDG_* variables. Every other
// package receives a Paths value built from an explicit Env, which is what
// keeps tests hermetic.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ForbidPrefixEnv names the environment variable that lists path prefixes no
// skillhub code may touch, separated by os.PathListSeparator. Tests point it
// at the real home; a manual run against a copy of the home does the same.
const ForbidPrefixEnv = "SKILLHUB_FORBID_PREFIX"

var (
	// ErrForbidden reports a path under a forbidden prefix.
	ErrForbidden = errors.New("path is under a forbidden prefix")
	// ErrInvalidPath reports a path that is not of the required form.
	ErrInvalidPath = errors.New("invalid path")
)

// Env is the explicit environment a Paths value is built from. Empty or
// relative XDG values are ignored, as the XDG Base Directory spec requires.
type Env struct {
	Home          string
	XDGConfigHome string
	XDGCacheHome  string
	XDGStateHome  string
	// ForbidPrefix is a list of absolute prefixes separated by
	// os.PathListSeparator. Relative entries are ignored.
	ForbidPrefix string
}

// EnvFromOS reads Env from the process environment.
func EnvFromOS() Env {
	return Env{
		Home:          os.Getenv("HOME"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		XDGCacheHome:  os.Getenv("XDG_CACHE_HOME"),
		XDGStateHome:  os.Getenv("XDG_STATE_HOME"),
		ForbidPrefix:  os.Getenv(ForbidPrefixEnv),
	}
}

// FromOS builds Paths from the process environment. Only the composition
// root calls it.
func FromOS() (Paths, error) { return New(EnvFromOS()) }

// Paths resolves every location skillhub reads or writes.
type Paths struct {
	home       string
	configRoot string
	cacheRoot  string
	stateRoot  string
	skillsLock string
	guard      Guard
}

// New builds Paths from an explicit environment. It fails when the home is
// not an absolute path or when the home or an XDG root is forbidden.
func New(env Env) (Paths, error) {
	if env.Home == "" || !filepath.IsAbs(env.Home) {
		return Paths{}, fmt.Errorf("%w: home must be an absolute path, got %q", ErrInvalidPath, env.Home)
	}
	home := filepath.Clean(env.Home)
	p := Paths{
		home:       home,
		configRoot: xdgRoot(env.XDGConfigHome, filepath.Join(home, ".config")),
		cacheRoot:  xdgRoot(env.XDGCacheHome, filepath.Join(home, ".cache")),
		stateRoot:  xdgRoot(env.XDGStateHome, filepath.Join(home, ".local", "state")),
		guard:      NewGuard(env.ForbidPrefix),
	}
	// npx skills moves its global lock only when XDG_STATE_HOME is set
	// (docs/research/skills-cli-internals.md, fact 1).
	if isAbsSet(env.XDGStateHome) {
		p.skillsLock = filepath.Join(p.stateRoot, "skills", ".skill-lock.json")
	} else {
		p.skillsLock = filepath.Join(home, ".agents", ".skill-lock.json")
	}
	for _, root := range []string{p.home, p.configRoot, p.cacheRoot, p.stateRoot, p.skillsLock} {
		if err := p.guard.Check(root); err != nil {
			return Paths{}, err
		}
	}
	return p, nil
}

func isAbsSet(v string) bool { return v != "" && filepath.IsAbs(v) }

func xdgRoot(v, fallback string) string {
	if isAbsSet(v) {
		return filepath.Clean(v)
	}
	return fallback
}

// Home is the user's home directory.
func (p Paths) Home() string { return p.home }

// ConfigDir holds the files a person may edit.
func (p Paths) ConfigDir() string { return filepath.Join(p.configRoot, "skillhub") }

// SettingsFile is settings.toml.
func (p Paths) SettingsFile() string { return filepath.Join(p.ConfigDir(), "settings.toml") }

// AgentsLocalFile is the user's overlay of the embedded agents table.
func (p Paths) AgentsLocalFile() string { return filepath.Join(p.ConfigDir(), "agents.local.toml") }

// CacheDir holds disposable data that is rebuilt on demand.
func (p Paths) CacheDir() string { return filepath.Join(p.cacheRoot, "skillhub") }

// StagingDir holds one directory per staged candidate.
func (p Paths) StagingDir() string { return filepath.Join(p.CacheDir(), "staging") }

// ETagsFile caches GitHub ETags for conditional requests.
func (p Paths) ETagsFile() string { return filepath.Join(p.CacheDir(), "etags.json") }

// CatalogCacheFile caches skills.sh search results, which its terms encourage.
func (p Paths) CatalogCacheFile() string { return filepath.Join(p.CacheDir(), "catalog.json") }

// StateDir holds data that must persist.
func (p Paths) StateDir() string { return filepath.Join(p.stateRoot, "skillhub") }

// StateFile is state.json.
func (p Paths) StateFile() string { return filepath.Join(p.StateDir(), "state.json") }

// StateLockFile is the advisory file lock shared by every skillhub process.
// It is not the npx skills lock; see SkillsLockFile.
func (p Paths) StateLockFile() string { return filepath.Join(p.StateDir(), "lock") }

// SnapshotsDir holds the folder copies taken before an apply.
func (p Paths) SnapshotsDir() string { return filepath.Join(p.StateDir(), "snapshots") }

// JournalFile is the append-only history.
func (p Paths) JournalFile() string { return filepath.Join(p.StateDir(), "journal.jsonl") }

// SkillsLockFile is the global lock of npx skills.
func (p Paths) SkillsLockFile() string { return p.skillsLock }

// StoreDir is the universal skills store, ~/.agents/skills.
func (p Paths) StoreDir() string { return filepath.Join(p.home, ".agents", "skills") }

// ParkedDir is where version 2 parks disabled skills, one folder per store
// key: on the store's file system but outside every directory a harness
// reads (docs/design-v2.md section 5).
func (p Paths) ParkedDir() string { return filepath.Join(p.home, ".agents", ".skillhub", "disabled") }

// InstallStagingDir is where version 2 prepares installs: beside the store,
// on its file system, but outside every directory a harness reads
// (docs/design-v2.md section 11, point 6).
func (p Paths) InstallStagingDir() string {
	return filepath.Join(p.home, ".agents", ".skillhub", "staging")
}

// SystemdUserDir is where user units for the background check live.
func (p Paths) SystemdUserDir() string { return filepath.Join(p.configRoot, "systemd", "user") }

// GentleAIStateFile is gentle-ai's state, used to recognize its copies.
func (p Paths) GentleAIStateFile() string {
	return filepath.Join(p.home, ".gentle-ai", "state.json")
}

// Guard returns the guard built from the forbidden prefixes, for adapters
// that must reject forbidden paths themselves (FileOps).
func (p Paths) Guard() Guard { return p.guard }

// Join resolves a home-relative path, such as a harness directory from the
// agents table. It rejects empty, absolute and escaping paths, and paths that
// resolve under a forbidden prefix.
func (p Paths) Join(rel string) (string, error) {
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %q is not a path inside the home", ErrInvalidPath, rel)
	}
	joined := filepath.Join(p.home, rel)
	if err := p.guard.Check(joined); err != nil {
		return "", err
	}
	return joined, nil
}

// Check rejects a path that is not absolute or that is forbidden.
func (p Paths) Check(path string) error { return p.guard.Check(path) }

// HomeRel returns path relative to the home, slash-separated, and whether it
// is inside the home. Skill ids use it.
func (p Paths) HomeRel(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(p.home, filepath.Clean(path))
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
