package paths_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"skillhub/internal/shared/paths"
)

func TestGuardCheckLexical(t *testing.T) {
	root := t.TempDir()
	forbidden := filepath.Join(root, "forbidden")
	link := filepath.Join(root, "link")
	if err := os.Symlink(forbidden, link); err != nil {
		t.Fatal(err)
	}
	guard := paths.NewGuard(forbidden)
	for _, tt := range []struct {
		name, path string
		want       error
	}{
		{"relative", "relative", paths.ErrInvalidPath},
		{"forbidden prefix", filepath.Join(forbidden, "child"), paths.ErrForbidden},
		{"similar but separate prefix", forbidden + "-other", nil},
		{"does not resolve link", link, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := guard.CheckLexical(tt.path); !errors.Is(err, tt.want) {
				t.Fatalf("CheckLexical(%q) = %v; want %v", tt.path, err, tt.want)
			}
		})
	}
	if err := guard.Check(link); !errors.Is(err, paths.ErrForbidden) {
		t.Fatalf("Check(%q) = %v; want ErrForbidden", link, err)
	}
}
