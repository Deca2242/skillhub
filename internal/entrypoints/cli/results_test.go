package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// scanRow returns the line of scan's table that names skill.
func scanRow(t *testing.T, out, skill string) string {
	t.Helper()
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, skill+" ") {
			return line
		}
	}
	t.Fatalf("scan output has no row for %s:\n%s", skill, out)
	return ""
}

func TestASkillRemovedUpstream(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha", "beta")
	// alpha's folder is deleted upstream; the repository keeps beta.
	if err := os.RemoveAll(filepath.Join(f.upstream, "skills", "alpha")); err != nil {
		t.Fatal(err)
	}
	f.gh.Commit(t, repo, "main", f.upstream)

	r := f.ok("check")
	if !strings.Contains(r.stdout, "lock:alpha") || !strings.Contains(r.stdout, "al día (eliminada del repositorio de origen)") {
		t.Errorf("check output:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "revisión fallida") {
		t.Errorf("check reports a failure:\n%s", r.stdout)
	}

	r = f.ok("check", "--json")
	var out struct {
		Failed  bool `json:"failed"`
		Targets []struct {
			Target          string `json:"target"`
			Outcome         string `json:"outcome"`
			RemovedUpstream bool   `json:"removed_upstream"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("check --json: %v\n%s", err, r.stdout)
	}
	i := slices.IndexFunc(out.Targets, func(tj struct {
		Target          string `json:"target"`
		Outcome         string `json:"outcome"`
		RemovedUpstream bool   `json:"removed_upstream"`
	}) bool {
		return tj.Target == "lock:alpha"
	})
	if out.Failed || i < 0 || out.Targets[i].Outcome != "up_to_date" || !out.Targets[i].RemovedUpstream {
		t.Errorf("check --json = %+v", out)
	}

	r = f.ok("scan")
	if row := scanRow(t, r.stdout, "alpha"); !strings.Contains(row, "al día") || !strings.Contains(row, "eliminada del repositorio de origen") {
		t.Errorf("scan row = %q", row)
	}
	if len(f.persisted().Candidates) != 1 {
		t.Errorf("candidates = %+v, want only beta's", f.persisted().Candidates)
	}
}

func TestScanShowsTheStateOfTheLastCheck(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.publish("beta", skillFiles("beta", "v1"))
	f.installLock([]string{"alpha", "beta"}, map[string]map[string]string{
		"alpha": skillFiles("alpha", "v1"), "beta": skillFiles("beta", "v1"),
	})

	if row := scanRow(t, f.ok("scan").stdout, "alpha"); !strings.Contains(row, "sin revisar") {
		t.Errorf("before a check, alpha row = %q", row)
	}
	f.ok("check")

	r := f.ok("scan")
	if row := scanRow(t, r.stdout, "alpha"); !strings.Contains(row, "actualización disponible") {
		t.Errorf("alpha row = %q", row)
	}
	if row := scanRow(t, r.stdout, "beta"); !strings.Contains(row, "al día") {
		t.Errorf("beta row = %q", row)
	}

	// A failed check shows on the skill with no pending update; the pending
	// one keeps showing its update.
	f.gh.Fail = func(fakes.Call) error { return ports.ErrRateLimited }
	if r := f.run(nil, "check"); r.code != 2 {
		t.Fatalf("failed check = exit %d", r.code)
	}
	r = f.ok("scan", "--json")
	var skills []domain.Skill
	if err := json.Unmarshal([]byte(r.stdout), &skills); err != nil {
		t.Fatal(err)
	}
	states := map[string]domain.State{}
	for _, s := range skills {
		states[s.ID] = s.State
	}
	if states["lock:alpha"] != domain.StateUpdateAvailable || states["lock:beta"] != domain.StateCheckFailed {
		t.Errorf("states = %v, want alpha update_available and beta check_failed", states)
	}
}
