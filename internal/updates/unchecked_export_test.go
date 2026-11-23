package updates_test

import (
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

func TestLockEntryCheckable(t *testing.T) {
	tests := []struct {
		name          string
		entry         domain.LockEntry
		wantCheckable bool
		wantWebSource bool
	}{
		{
			name:          "github owner/repo",
			entry:         domain.LockEntry{Name: "alpha", SourceType: "github", Source: "acme/skills"},
			wantCheckable: true,
			wantWebSource: false,
		},
		{
			name:          "well-known website",
			entry:         domain.LockEntry{Name: "ui-taste", SourceType: "well-known", Source: "uizze.sh"},
			wantCheckable: false,
			wantWebSource: true,
		},
		{
			name:          "other source type",
			entry:         domain.LockEntry{Name: "custom", SourceType: "gitlab", Source: "acme/skills"},
			wantCheckable: false,
			wantWebSource: false,
		},
		{
			name:          "github invalid repo",
			entry:         domain.LockEntry{Name: "broken", SourceType: "github", Source: "not-a-repo"},
			wantCheckable: false,
			wantWebSource: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkable, web := updates.LockEntryCheckable(tt.entry)
			if checkable != tt.wantCheckable || web != tt.wantWebSource {
				t.Errorf("LockEntryCheckable(%+v) = (%v, %v), want (%v, %v)",
					tt.entry, checkable, web, tt.wantCheckable, tt.wantWebSource)
			}
		})
	}
}

func TestCloneCheckable(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   bool
	}{
		{name: "https github", remote: "https://github.com/acme/skills.git", want: true},
		{name: "ssh scp github", remote: "git@github.com:acme/skills.git", want: true},
		{name: "gitlab remote", remote: "https://gitlab.com/acme/skills.git", want: false},
		{name: "empty remote", remote: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updates.CloneCheckable(tt.remote); got != tt.want {
				t.Errorf("CloneCheckable(%q) = %v, want %v", tt.remote, got, tt.want)
			}
		})
	}
}
