package cli_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
)

const v2Pending = "hay una operación de gestión de skills sin terminar; ejecuta «skillhub recover»"

func TestVersion1CommandsExplainAPendingVersion2Operation(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")
	p := f.home.Paths()
	record := engine.RecordFile(p)
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(p.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	homeBefore := f.homeTree()

	for _, args := range [][]string{{"check"}, {"updates", "skip", "alpha"}} {
		r := f.run(nil, args...)
		if r.code != 2 || r.stderr != "skillhub: "+v2Pending+"\n" {
			t.Errorf("skillhub %v = exit %d, stderr %q; want exit 2 and %q", args, r.code, r.stderr, v2Pending)
		}
	}
	// recover resolves the version 2 record through the app (A5 of L51);
	// an altered record is refused without writing anything.
	r := f.run(nil, "recover")
	if r.code != 2 || strings.Contains(r.stderr, v2Pending) {
		t.Errorf("recover of an altered record = exit %d, stderr %q; want exit 2 and the engine's refusal", r.code, r.stderr)
	}
	r = f.run(nil, "updates", "apply", "alpha", "--yes")
	if r.code != 2 || !strings.Contains(r.stdout, v2Pending) {
		t.Errorf("updates apply = exit %d, stdout %q; want exit 2 and the explanation", r.code, r.stdout)
	}

	if after, _ := os.ReadFile(p.StateFile()); string(after) != string(stateBefore) {
		t.Error("a refused command changed state.json")
	}
	if after := f.homeTree(); !maps.Equal(after, homeBefore) {
		t.Error("a refused command changed the home")
	}
}
