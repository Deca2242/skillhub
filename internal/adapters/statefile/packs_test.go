package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"skillhub/internal/packs"
	"skillhub/internal/shared/paths"
	"skillhub/internal/testsupport"
)

func TestStorePacksSidecarRoundTripAndIsolation(t *testing.T) {
	home := testsupport.NewHome(t)
	s := New(home.Paths())

	// Missing sidecar returns an empty non-nil slice.
	got, err := s.LoadPacks()
	if err != nil {
		t.Fatalf("LoadPacks(missing): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("LoadPacks(missing) = %#v, want empty slice", got)
	}

	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	want := []packs.Pack{
		{
			Name:        "seguridad",
			Description: "Auditoría",
			Members:     []string{"lock:alpha"},
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
	if err := s.SavePacks(want); err != nil {
		t.Fatalf("SavePacks: %v", err)
	}

	// Sidecar lives in StateDir/packs.json, not in state.json or journal.jsonl.
	sidecar := filepath.Join(home.Paths().StateDir(), "packs.json")
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("expected sidecar at %s: %v", sidecar, err)
	}
	if _, err := os.Stat(home.Paths().StateFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state.json should not be created by SavePacks, got err=%v", err)
	}

	got, err = s.LoadPacks()
	if err != nil {
		t.Fatalf("LoadPacks: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadPacks = %+v, want %+v", got, want)
	}
}

func TestStorePacksRejectsUnsupportedVersion(t *testing.T) {
	home := testsupport.NewHome(t)
	s := New(home.Paths())

	sidecar := filepath.Join(home.Paths().StateDir(), "packs.json")
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte(`{"version":99,"packs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.LoadPacks(); !errors.Is(err, ErrUnsupportedPacksVersion) {
		t.Fatalf("LoadPacks(v99) error = %v, want ErrUnsupportedPacksVersion", err)
	}
	if err := s.SavePacks([]packs.Pack{}); !errors.Is(err, ErrUnsupportedPacksVersion) {
		t.Fatalf("SavePacks(v99) error = %v, want ErrUnsupportedPacksVersion", err)
	}
}

func TestStorePacksRejectsDuplicateNamesAndEmptyMemberOnLoadAndSave(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "duplicate pack names",
			raw:  `{"version":1,"packs":[{"name":"dup","members":[]},{"name":"dup","members":["lock:a"]}]}`,
		},
		{
			name: "empty member identity",
			raw:  `{"version":1,"packs":[{"name":"core","members":[""]}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := testsupport.NewHome(t)
			s := New(home.Paths())
			sidecar := filepath.Join(home.Paths().StateDir(), "packs.json")
			if err := os.MkdirAll(filepath.Dir(sidecar), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecar, []byte(tc.raw), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := s.LoadPacks(); !errors.Is(err, packs.ErrCorruptSidecar) {
				t.Fatalf("LoadPacks(%s) error = %v, want ErrCorruptSidecar", tc.name, err)
			}
			if err := s.SavePacks([]packs.Pack{{Name: "ok", Members: []string{}}}); !errors.Is(err, packs.ErrCorruptSidecar) {
				t.Fatalf("SavePacks(%s) over corrupt file error = %v, want ErrCorruptSidecar", tc.name, err)
			}
			gotRaw, err := os.ReadFile(sidecar)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotRaw) != tc.raw {
				t.Fatalf("corrupt sidecar bytes modified:\ngot:  %s\nwant: %s", gotRaw, tc.raw)
			}
		})
	}

	// Also reject invalid pack invariants passed directly to SavePacks.
	home := testsupport.NewHome(t)
	s := New(home.Paths())
	if err := s.SavePacks([]packs.Pack{
		{Name: "dup", Members: []string{}},
		{Name: "dup", Members: []string{}},
	}); !errors.Is(err, packs.ErrCorruptSidecar) {
		t.Fatalf("SavePacks(duplicate) error = %v, want ErrCorruptSidecar", err)
	}
	if err := s.SavePacks([]packs.Pack{
		{Name: "core", Members: []string{""}},
	}); !errors.Is(err, packs.ErrCorruptSidecar) {
		t.Fatalf("SavePacks(empty member) error = %v, want ErrCorruptSidecar", err)
	}
}

func TestStorePacksHonorsPathsGuard(t *testing.T) {
	home := testsupport.NewHome(t)
	s := New(home.Paths())
	if err := s.paths.Check(s.packsFilePath()); err != nil {
		t.Fatalf("expected StateDir/packs.json to pass guard: %v", err)
	}
	if err := home.Paths().Check(filepath.Join(home.Paths().StateDir(), "packs.json")); err != nil {
		t.Fatalf("expected StateDir/packs.json to pass guard: %v", err)
	}
	// Verify a Store with a zero Paths (empty/forbidden) or invalid guard fails.
	forbidHome := t.TempDir()
	_, err := paths.New(paths.Env{
		Home:         forbidHome,
		XDGStateHome: forbidHome,
		ForbidPrefix: forbidHome,
	})
	if !errors.Is(err, paths.ErrForbidden) {
		t.Fatalf("paths.New with ForbidPrefix error = %v, want ErrForbidden", err)
	}
}
