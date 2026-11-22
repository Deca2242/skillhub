package paths_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/shared/paths"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func mustNew(t *testing.T, env paths.Env) paths.Paths {
	t.Helper()
	p, err := paths.New(env)
	if err != nil {
		t.Fatalf("paths.New(%+v): %v", env, err)
	}
	return p
}

func TestLocations(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	cfg, cache, state := filepath.Join(xdg, "cfg"), filepath.Join(xdg, "cache"), filepath.Join(xdg, "state")

	tests := []struct {
		name string
		env  paths.Env
		want map[string]string
	}{
		{
			name: "defaults under the home when no XDG variable is set",
			env:  paths.Env{Home: home},
			want: map[string]string{
				"Home":              home,
				"ConfigDir":         filepath.Join(home, ".config", "skillhub"),
				"SettingsFile":      filepath.Join(home, ".config", "skillhub", "settings.toml"),
				"AgentsLocalFile":   filepath.Join(home, ".config", "skillhub", "agents.local.toml"),
				"CacheDir":          filepath.Join(home, ".cache", "skillhub"),
				"StagingDir":        filepath.Join(home, ".cache", "skillhub", "staging"),
				"ETagsFile":         filepath.Join(home, ".cache", "skillhub", "etags.json"),
				"StateDir":          filepath.Join(home, ".local", "state", "skillhub"),
				"StateFile":         filepath.Join(home, ".local", "state", "skillhub", "state.json"),
				"StateLockFile":     filepath.Join(home, ".local", "state", "skillhub", "lock"),
				"SnapshotsDir":      filepath.Join(home, ".local", "state", "skillhub", "snapshots"),
				"JournalFile":       filepath.Join(home, ".local", "state", "skillhub", "journal.jsonl"),
				"SkillsLockFile":    filepath.Join(home, ".agents", ".skill-lock.json"),
				"StoreDir":          filepath.Join(home, ".agents", "skills"),
				"ParkedDir":         filepath.Join(home, ".agents", ".skillhub", "disabled"),
				"InstallStagingDir": filepath.Join(home, ".agents", ".skillhub", "staging"),
				"SystemdUserDir":    filepath.Join(home, ".config", "systemd", "user"),
				"GentleAIStateFile": filepath.Join(home, ".gentle-ai", "state.json"),
			},
		},
		{
			name: "XDG roots move skillhub dirs and XDG_STATE_HOME moves the skills lock",
			env:  paths.Env{Home: home, XDGConfigHome: cfg, XDGCacheHome: cache, XDGStateHome: state},
			want: map[string]string{
				"Home":              home,
				"ConfigDir":         filepath.Join(cfg, "skillhub"),
				"SettingsFile":      filepath.Join(cfg, "skillhub", "settings.toml"),
				"AgentsLocalFile":   filepath.Join(cfg, "skillhub", "agents.local.toml"),
				"CacheDir":          filepath.Join(cache, "skillhub"),
				"StagingDir":        filepath.Join(cache, "skillhub", "staging"),
				"ETagsFile":         filepath.Join(cache, "skillhub", "etags.json"),
				"StateDir":          filepath.Join(state, "skillhub"),
				"StateFile":         filepath.Join(state, "skillhub", "state.json"),
				"StateLockFile":     filepath.Join(state, "skillhub", "lock"),
				"SnapshotsDir":      filepath.Join(state, "skillhub", "snapshots"),
				"JournalFile":       filepath.Join(state, "skillhub", "journal.jsonl"),
				"SkillsLockFile":    filepath.Join(state, "skills", ".skill-lock.json"),
				"StoreDir":          filepath.Join(home, ".agents", "skills"),
				"ParkedDir":         filepath.Join(home, ".agents", ".skillhub", "disabled"),
				"InstallStagingDir": filepath.Join(home, ".agents", ".skillhub", "staging"),
				"SystemdUserDir":    filepath.Join(cfg, "systemd", "user"),
				"GentleAIStateFile": filepath.Join(home, ".gentle-ai", "state.json"),
			},
		},
		{
			name: "relative XDG values are ignored as the XDG spec requires",
			env:  paths.Env{Home: home, XDGConfigHome: "rel/cfg", XDGCacheHome: "rel/cache", XDGStateHome: "rel/state"},
			want: map[string]string{
				"ConfigDir":      filepath.Join(home, ".config", "skillhub"),
				"CacheDir":       filepath.Join(home, ".cache", "skillhub"),
				"StateDir":       filepath.Join(home, ".local", "state", "skillhub"),
				"SkillsLockFile": filepath.Join(home, ".agents", ".skill-lock.json"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustNew(t, tt.env)
			got := map[string]string{
				"Home":              p.Home(),
				"ConfigDir":         p.ConfigDir(),
				"SettingsFile":      p.SettingsFile(),
				"AgentsLocalFile":   p.AgentsLocalFile(),
				"CacheDir":          p.CacheDir(),
				"StagingDir":        p.StagingDir(),
				"ETagsFile":         p.ETagsFile(),
				"StateDir":          p.StateDir(),
				"StateFile":         p.StateFile(),
				"StateLockFile":     p.StateLockFile(),
				"SnapshotsDir":      p.SnapshotsDir(),
				"JournalFile":       p.JournalFile(),
				"SkillsLockFile":    p.SkillsLockFile(),
				"StoreDir":          p.StoreDir(),
				"ParkedDir":         p.ParkedDir(),
				"InstallStagingDir": p.InstallStagingDir(),
				"SystemdUserDir":    p.SystemdUserDir(),
				"GentleAIStateFile": p.GentleAIStateFile(),
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestNewRejectsInvalidOrForbiddenRoots(t *testing.T) {
	forbidden := t.TempDir()
	allowed := t.TempDir()

	tests := []struct {
		name    string
		env     paths.Env
		wantErr error
	}{
		{"empty home", paths.Env{}, paths.ErrInvalidPath},
		{"relative home", paths.Env{Home: "some/home"}, paths.ErrInvalidPath},
		{"home equal to the forbidden prefix", paths.Env{Home: forbidden, ForbidPrefix: forbidden}, paths.ErrForbidden},
		{"home under the forbidden prefix", paths.Env{Home: filepath.Join(forbidden, "sub"), ForbidPrefix: forbidden}, paths.ErrForbidden},
		{"XDG_STATE_HOME under the forbidden prefix", paths.Env{Home: allowed, XDGStateHome: filepath.Join(forbidden, ".local", "state"), ForbidPrefix: forbidden}, paths.ErrForbidden},
		{"XDG_CONFIG_HOME under the forbidden prefix", paths.Env{Home: allowed, XDGConfigHome: filepath.Join(forbidden, ".config"), ForbidPrefix: forbidden}, paths.ErrForbidden},
		{"XDG_CACHE_HOME under the forbidden prefix", paths.Env{Home: allowed, XDGCacheHome: filepath.Join(forbidden, ".cache"), ForbidPrefix: forbidden}, paths.ErrForbidden},
		{"second prefix of a list", paths.Env{Home: forbidden, ForbidPrefix: allowed + string(os.PathListSeparator) + forbidden}, paths.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := paths.New(tt.env)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("paths.New error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckRejectsForbiddenPaths(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "realhome")
	aliasHome := filepath.Join(base, "alias")
	fakeHome := filepath.Join(base, "fake")
	for _, dir := range []string{realHome, fakeHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The prefix is given through a symlink: paths under its target are forbidden too.
	if err := os.Symlink(realHome, aliasHome); err != nil {
		t.Fatal(err)
	}
	// A link inside the fake home that points into the forbidden prefix.
	if err := os.Symlink(realHome, filepath.Join(fakeHome, "escape")); err != nil {
		t.Fatal(err)
	}
	// A dangling link whose target, inside the prefix, does not exist yet.
	if err := os.Symlink(filepath.Join(realHome, "not-yet"), filepath.Join(fakeHome, "dangling")); err != nil {
		t.Fatal(err)
	}
	// A dangling relative link reached through a symlinked directory: its
	// target counts from where the link really is, not from the path used.
	deep := filepath.Join(base, "other", "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(deep, filepath.Join(fakeHome, "via")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "realhome", "not-yet"), filepath.Join(deep, "link")); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, paths.Env{Home: fakeHome, ForbidPrefix: aliasHome})

	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{"path in the fake home", filepath.Join(fakeHome, ".agents", "skills", "x"), nil},
		{"a dangling relative symlink behind a symlinked directory", filepath.Join(fakeHome, "via", "link", "x"), paths.ErrForbidden},
		{"the forbidden prefix itself", aliasHome, paths.ErrForbidden},
		{"a path under the prefix alias", filepath.Join(aliasHome, ".agents", "skills"), paths.ErrForbidden},
		{"a path under the prefix target", filepath.Join(realHome, ".agents"), paths.ErrForbidden},
		{"an unclean path that climbs into the prefix", fakeHome + "/../realhome/.claude", paths.ErrForbidden},
		{"a symlink that resolves into the prefix", filepath.Join(fakeHome, "escape", "skills", "new"), paths.ErrForbidden},
		{"a dangling symlink whose target is under the prefix", filepath.Join(fakeHome, "dangling", "x"), paths.ErrForbidden},
		{"a sibling that only shares a name prefix", realHome + "2", nil},
		{"a relative path", "relative/x", paths.ErrInvalidPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Check(tt.path)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("Check(%q) = %v, want nil", tt.path, err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Check(%q) = %v, want %v", tt.path, err, tt.wantErr)
			}
			if gErr := p.Guard().Check(tt.path); tt.wantErr == paths.ErrForbidden && !errors.Is(gErr, paths.ErrForbidden) {
				t.Fatalf("Guard().Check(%q) = %v, want ErrForbidden", tt.path, gErr)
			}
		})
	}
}

func TestJoinResolvesHomeRelativePaths(t *testing.T) {
	base := t.TempDir()
	forbidden := filepath.Join(base, "forbidden")
	home := filepath.Join(base, "home")
	for _, dir := range []string{forbidden, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(forbidden, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, paths.Env{Home: home, ForbidPrefix: forbidden})

	tests := []struct {
		name    string
		rel     string
		want    string
		wantErr error
	}{
		{"harness dir", ".claude/skills", filepath.Join(home, ".claude", "skills"), nil},
		{"climbs out of the home", "../outside", "", paths.ErrInvalidPath},
		{"absolute path", "/etc/passwd", "", paths.ErrInvalidPath},
		{"empty path", "", "", paths.ErrInvalidPath},
		{"resolves through a link into the prefix", "link/skills", "", paths.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Join(tt.rel)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Join(%q) error = %v, want %v", tt.rel, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("Join(%q) = %q, %v; want %q", tt.rel, got, err, tt.want)
			}
		})
	}
}

func TestHomeRel(t *testing.T) {
	home := t.TempDir()
	p := mustNew(t, paths.Env{Home: home})

	tests := []struct {
		name   string
		path   string
		want   string
		inside bool
	}{
		{"inside the home", filepath.Join(home, ".agents", "skills", "a"), ".agents/skills/a", true},
		{"the home itself", home, ".", true},
		{"outside the home", "/opt/skills/a", "", false},
		{"sibling sharing a name prefix", home + "x/a", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, inside := p.HomeRel(tt.path)
			if got != tt.want || inside != tt.inside {
				t.Fatalf("HomeRel(%q) = %q, %v; want %q, %v", tt.path, got, inside, tt.want, tt.inside)
			}
		})
	}
}

func TestFromOSReadsTheProcessEnvironment(t *testing.T) {
	// testsupport.Main set HOME, the XDG roots and the forbidden prefixes;
	// the test compares against them instead of changing the environment.
	want := paths.Env{
		Home:          os.Getenv("HOME"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		XDGCacheHome:  os.Getenv("XDG_CACHE_HOME"),
		XDGStateHome:  os.Getenv("XDG_STATE_HOME"),
		ForbidPrefix:  os.Getenv(paths.ForbidPrefixEnv),
	}
	if want.Home == "" || want.XDGConfigHome == "" || want.XDGCacheHome == "" || want.XDGStateHome == "" || want.ForbidPrefix == "" {
		t.Fatalf("testsupport.Main did not set the environment: %+v", want)
	}
	if env := paths.EnvFromOS(); env != want {
		t.Fatalf("EnvFromOS() = %+v, want %+v", env, want)
	}
	p, err := paths.FromOS()
	if err != nil {
		t.Fatalf("FromOS: %v", err)
	}
	checks := map[string][2]string{
		"Home":           {p.Home(), want.Home},
		"ConfigDir":      {p.ConfigDir(), filepath.Join(want.XDGConfigHome, "skillhub")},
		"CacheDir":       {p.CacheDir(), filepath.Join(want.XDGCacheHome, "skillhub")},
		"StateDir":       {p.StateDir(), filepath.Join(want.XDGStateHome, "skillhub")},
		"SkillsLockFile": {p.SkillsLockFile(), filepath.Join(want.XDGStateHome, "skills", ".skill-lock.json")},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	// Rejected lexically: nothing under the prefix is touched.
	for _, prefix := range filepath.SplitList(want.ForbidPrefix) {
		if err := p.Check(filepath.Join(prefix, "x")); !errors.Is(err, paths.ErrForbidden) {
			t.Errorf("FromOS did not carry the forbidden prefix %q: %v", prefix, err)
		}
	}
}
