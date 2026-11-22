package settings

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// TestSettingsStoreContract runs the shared ports.SettingsStore contract suite
// against both the in-memory fake and the TOML-backed adapter.
func TestSettingsStoreContract(t *testing.T) {
	stores := []struct {
		name string
		new  func(t *testing.T) ports.SettingsStore
	}{
		{
			name: "fakes.SettingsStore",
			new:  func(*testing.T) ports.SettingsStore { return fakes.NewSettingsStore() },
		},
		{
			name: "settings.Store",
			new:  func(t *testing.T) ports.SettingsStore { return New(testsupport.NewHome(t).Paths()) },
		},
	}

	for _, impl := range stores {
		t.Run(impl.name, func(t *testing.T) {
			t.Run("load missing returns defaults", func(t *testing.T) {
				s := impl.new(t)
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load(): %v", err)
				}
				if !reflect.DeepEqual(got, domain.DefaultSettings()) {
					t.Fatalf("Load() = %+v, want %+v", got, domain.DefaultSettings())
				}
			})

			t.Run("save with nil ignore normalizes to empty slice", func(t *testing.T) {
				s := impl.new(t)
				err := s.Save(domain.Settings{
					CheckInterval: time.Hour,
					Notifications: true,
					Ignore:        nil,
				})
				if err != nil {
					t.Fatalf("Save: %v", err)
				}
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if got.Ignore == nil || len(got.Ignore) != 0 {
					t.Fatalf("Ignore = %#v, want non-nil empty slice", got.Ignore)
				}
			})

			t.Run("save and load round trip", func(t *testing.T) {
				s := impl.new(t)
				want := domain.Settings{
					CheckInterval: 2 * time.Hour,
					Notifications: false,
					Ignore:        []string{"gentle-ai-skill", "custom-*"},
				}
				if err := s.Save(want); err != nil {
					t.Fatalf("Save: %v", err)
				}
				got, err := s.Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Load() = %+v, want %+v", got, want)
				}
			})

			t.Run("save rejects short interval", func(t *testing.T) {
				for _, interval := range []time.Duration{0, -time.Minute, 14*time.Minute + 59*time.Second} {
					s := impl.new(t)
					err := s.Save(domain.Settings{
						CheckInterval: interval,
						Notifications: true,
						Ignore:        []string{},
					})
					if !errors.Is(err, domain.ErrIntervalTooShort) {
						t.Fatalf("Save(%s) err = %v, want ErrIntervalTooShort", interval, err)
					}
				}
			})
		})
	}
}

func TestLoadPartialTOMLUsesDefaults(t *testing.T) {
	h := testsupport.NewHome(t)
	h.WriteFile(".config/skillhub/settings.toml", "notifications = false\n")

	store := New(h.Paths())
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := domain.Settings{
		CheckInterval: domain.DefaultCheckInterval,
		Notifications: false,
		Ignore:        []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadValidatesIntervalAndRejectsUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantKey string
		wantErr error
	}{
		{
			name:    "too short interval",
			toml:    "check_interval = \"1m\"\n",
			wantKey: "check_interval",
			wantErr: domain.ErrIntervalTooShort,
		},
		{
			name:    "negative interval",
			toml:    "check_interval = \"-5m\"\n",
			wantKey: "check_interval",
			wantErr: domain.ErrIntervalTooShort,
		},
		{
			name:    "unknown key",
			toml:    "check_intervl = \"2h\"\n",
			wantKey: "check_intervl",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := testsupport.NewHome(t)
			h.WriteFile(".config/skillhub/settings.toml", tc.toml)

			store := New(h.Paths())
			_, err := store.Load()
			if err == nil {
				t.Fatalf("Load() succeeded on %s, want error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("Load() err = %q, want it to name %q", err.Error(), tc.wantKey)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Load() err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestRejectedSaveLeavesExistingSettingsIntact(t *testing.T) {
	h := testsupport.NewHome(t)
	initial := "# keep exact bytes\ncheck_interval = \"1h\"\nnotifications = true\n"
	h.WriteFile(".config/skillhub/settings.toml", initial)

	store := New(h.Paths())
	err := store.Save(domain.Settings{
		CheckInterval: 5 * time.Minute,
		Notifications: false,
		Ignore:        []string{"x"},
	})
	if !errors.Is(err, domain.ErrIntervalTooShort) {
		t.Fatalf("Save err = %v, want ErrIntervalTooShort", err)
	}

	after, err := os.ReadFile(h.Paths().SettingsFile())
	if err != nil {
		t.Fatalf("read settings.toml: %v", err)
	}
	if string(after) != initial {
		t.Fatalf("settings.toml changed after rejected Save:\ngot:  %q\nwant: %q", after, initial)
	}
}

func TestSettingsReadOnlyDirPreservesFileAndLeavesNoTempFiles(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	if err := store.Save(domain.DefaultSettings()); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	before, err := os.ReadFile(h.Paths().SettingsFile())
	if err != nil {
		t.Fatalf("read settings.toml: %v", err)
	}

	dir := h.Paths().ConfigDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod 0555: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := store.Save(domain.Settings{CheckInterval: 2 * time.Hour, Notifications: false, Ignore: []string{}}); err == nil {
		t.Fatal("Save into read-only dir succeeded, want error")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("restore chmod 0755: %v", err)
	}
	after, err := os.ReadFile(h.Paths().SettingsFile())
	if err != nil {
		t.Fatalf("read settings.toml after failure: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("settings.toml changed after failed write:\ngot:  %s\nwant: %s", after, before)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %q in %s", e.Name(), dir)
		}
	}
}

func TestSettingsFailedRenameCleansTempFiles(t *testing.T) {
	h := testsupport.NewHome(t)
	store := New(h.Paths())

	// Make settings.toml a directory so CreateTemp succeeds and os.Rename fails.
	if err := os.MkdirAll(h.Paths().SettingsFile(), 0o755); err != nil {
		t.Fatalf("mkdir settings.toml: %v", err)
	}
	if err := store.Save(domain.DefaultSettings()); err == nil {
		t.Fatal("Save over directory succeeded, want error")
	}
	entries, err := os.ReadDir(h.Paths().ConfigDir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %q in %s", e.Name(), h.Paths().ConfigDir())
		}
	}
}
