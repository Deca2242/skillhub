//go:build network

package httpsget_test

import (
	"testing"

	"skillhub/internal/adapters/httpsget"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/manage/install"
)

// TestUizzeIndex_Network reads the well-known index of uizze.sh with the
// real curl, only when -tags network is passed and the user approved it
// (docs/design.md section 11). It reads the index and nothing else: no
// skill file is downloaded and nothing is installed.
func TestUizzeIndex_Network(t *testing.T) {
	web, err := httpsget.New(proc.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idx, err := install.FetchIndex(t.Context(), web, "https://uizze.sh")
	if err != nil {
		t.Fatalf("FetchIndex(https://uizze.sh) = %v", err)
	}
	if idx.URL != "https://uizze.sh/.well-known/agent-skills/index.json" || idx.Base != "https://uizze.sh" {
		t.Errorf("index read at %s, base %s", idx.URL, idx.Base)
	}
	found := false
	for _, e := range idx.Entries {
		if e.Name == "" || e.Description == "" {
			t.Errorf("incomplete entry %+v", e)
		}
		found = found || e.Name == "ui-taste"
	}
	if !found {
		t.Errorf("ui-taste is not in the index: %+v", idx.Entries)
	}
}
