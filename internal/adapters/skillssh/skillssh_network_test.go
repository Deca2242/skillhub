//go:build network

package skillssh_test

import (
	"testing"

	"skillhub/internal/adapters/proc"
	"skillhub/internal/adapters/skillssh"
)

// TestSearch_Network runs one real search against skills.sh with the real
// curl, only when -tags network is passed and the user approved it
// (docs/design.md section 11). It reads: it never installs anything.
func TestSearch_Network(t *testing.T) {
	client, err := skillssh.New(proc.New(), "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	skills, err := client.Search(t.Context(), "react", 5)
	if err != nil {
		t.Fatalf("Search(react) = %v", err)
	}
	if len(skills) == 0 || len(skills) > 5 {
		t.Fatalf("Search(react) returned %d skills, want 1 to 5", len(skills))
	}
	for _, s := range skills {
		if s.ID == "" || s.Source == "" || s.SkillID == "" || s.Name == "" || s.Installs < 0 {
			t.Errorf("incomplete skill %+v", s)
		}
	}
	if _, err := client.Search(t.Context(), "r", 5); err == nil {
		t.Error("Search(r) succeeded; skills.sh refuses one character and the adapter must too")
	}
}
