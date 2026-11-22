package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

func TestHelpIsInSpanish(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"--help"}, {"updates", "--help"}, {"help", "updates", "apply"}, {"check", "-h"}} {
		r := f.ok(args...)
		for _, english := range []string{"Usage:", "Flags:", "Available Commands", "help for", "[flags]"} {
			if strings.Contains(r.stdout, english) {
				t.Errorf("skillhub %v help contains %q:\n%s", args, english, r.stdout)
			}
		}
		if !strings.Contains(r.stdout, "Uso:") {
			t.Errorf("skillhub %v help has no «Uso:»:\n%s", args, r.stdout)
		}
	}
	if r := f.ok("--help"); !strings.Contains(r.stdout, "Comandos:") || !strings.Contains(r.stdout, "background") {
		t.Errorf("root help does not list the commands:\n%s", r.stdout)
	}
}

func TestUnknownCommandsAndFlagsAreErrors(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"nope"}, {"scan", "--nope"}, {"updates", "nope"}, {"scan", "extra"}} {
		r := f.run(nil, args...)
		if r.code != 2 || !strings.HasPrefix(r.stderr, "skillhub: ") {
			t.Errorf("skillhub %v = exit %d, stderr %q; want exit 2 with a message", args, r.code, r.stderr)
		}
	}
}

func TestFlagErrorsAreInSpanish(t *testing.T) {
	f := newFixture(t)
	r := f.run(nil, "scan", "--nope")
	if r.code != 2 || r.stderr != "skillhub: opción desconocida: --nope; usa «skillhub scan --help»\n" {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestTUICommandOpensTheTUI(t *testing.T) {
	f := newFixture(t)
	if r := f.ok("tui"); r.stdout != "tui\n" || f.tuiCalls != 1 {
		t.Errorf("tui = %+v, calls %d", r, f.tuiCalls)
	}
}

func TestScan(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	before := f.homeTree()

	r := f.ok("scan")
	for _, want := range []string{"NOMBRE", "alpha", "lock de skills", "sin revisar"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("scan output lacks %q:\n%s", want, r.stdout)
		}
	}

	r = f.ok("scan", "--json")
	var skills []domain.Skill
	if err := json.Unmarshal([]byte(r.stdout), &skills); err != nil {
		t.Fatalf("scan --json is not a list of skills: %v\n%s", err, r.stdout)
	}
	if len(skills) != 1 || skills[0].ID != "lock:alpha" || skills[0].State != domain.StateUnchecked {
		t.Errorf("scan --json = %+v", skills)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("scan changed the home")
	}
	if _, err := os.Stat(f.home.Paths().StateFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scan wrote state.json: %v", err)
	}
}

func TestCheck(t *testing.T) {
	t.Run("nothing pending exits 0, even with --exit-code", func(t *testing.T) {
		f := newFixture(t)
		f.publish("alpha", skillFiles("alpha", "v1"))
		f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": skillFiles("alpha", "v1")})
		r := f.ok("check", "--exit-code")
		if !strings.Contains(r.stdout, "Actualizaciones pendientes: 0") || !strings.Contains(r.stdout, "al día") {
			t.Errorf("check output:\n%s", r.stdout)
		}
	})

	t.Run("pending updates are a success without --exit-code and 1 with it", func(t *testing.T) {
		f := newFixture(t)
		f.standard("alpha")
		before := f.homeTree()
		r := f.ok("check")
		if !strings.Contains(r.stdout, "Actualizaciones pendientes: 1") || !strings.Contains(r.stdout, "actualización disponible") {
			t.Errorf("check output:\n%s", r.stdout)
		}
		if r := f.run(nil, "check", "--exit-code"); r.code != 1 {
			t.Errorf("check --exit-code = %d, want 1\n%s%s", r.code, r.stdout, r.stderr)
		}
		if after := f.homeTree(); !maps.Equal(before, after) {
			t.Error("check changed the home outside skillhub's own state")
		}
		if c := f.candidate("lock:alpha"); c.State != domain.CandidatePending {
			t.Errorf("candidate state = %s", c.State)
		}
		if f.installedSHA("alpha") == c(f).ToSHA {
			t.Error("check applied the update")
		}
	})

	t.Run("a failed check exits 2 and says why", func(t *testing.T) {
		f := newFixture(t)
		f.standard("alpha")
		f.gh.Fail = func(fakes.Call) error { return ports.ErrRateLimited }
		r := f.run(nil, "check")
		if r.code != 2 || !strings.Contains(r.stdout, "revisión fallida") {
			t.Errorf("check = exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stderr, "skillhub: ") {
			t.Errorf("stderr = %q", r.stderr)
		}
	})

	t.Run("--json", func(t *testing.T) {
		f := newFixture(t)
		f.standard("alpha")
		r := f.ok("check", "--json")
		var out struct {
			Ran     bool `json:"ran"`
			Failed  bool `json:"failed"`
			Pending int  `json:"pending"`
			Targets []struct {
				Target      string `json:"target"`
				Outcome     string `json:"outcome"`
				CandidateID string `json:"candidate_id"`
			} `json:"targets"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatalf("check --json: %v\n%s", err, r.stdout)
		}
		if !out.Ran || out.Failed || out.Pending != 1 || len(out.Targets) != 1 ||
			out.Targets[0].Target != "lock:alpha" || out.Targets[0].Outcome != "update_available" ||
			out.Targets[0].CandidateID != c(f).ID {
			t.Errorf("check --json = %+v", out)
		}
	})
}

// c returns alpha's open candidate.
func c(f *fixture) domain.UpdateCandidate { return f.candidate("lock:alpha") }

func TestCheckIfDue(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	if r := f.ok("check", "--if-due"); !strings.Contains(r.stdout, "Actualizaciones pendientes: 1") {
		t.Fatalf("first check --if-due did not run:\n%s", r.stdout)
	}
	calls := len(f.gh.Calls())
	f.clock.Advance(59 * time.Minute)
	r := f.ok("check", "--if-due")
	if !strings.Contains(r.stdout, "La revisión todavía no toca") || len(f.gh.Calls()) != calls {
		t.Errorf("check --if-due ran before the interval:\n%s", r.stdout)
	}
	f.clock.Advance(time.Minute)
	if r := f.ok("check", "--if-due"); strings.Contains(r.stdout, "todavía no toca") || len(f.gh.Calls()) == calls {
		t.Errorf("check --if-due did not run at the interval:\n%s", r.stdout)
	}
}

func TestCheckNotify(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check", "--notify")
	sent := f.notifier.Sent()
	if len(sent) != 1 || sent[0].Title != "skillhub" || !strings.Contains(sent[0].Body, "alpha") {
		t.Fatalf("sent = %+v", sent)
	}
	f.ok("check", "--notify")
	if got := f.notifier.Sent(); len(got) != 1 {
		t.Errorf("an unchanged pending list was announced again: %+v", got)
	}
	f.ok("check")
	if got := f.notifier.Sent(); len(got) != 1 {
		t.Errorf("check without --notify sent a notification: %+v", got)
	}
}

func TestUpdatesListAndShow(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	if r := f.ok("updates", "list"); !strings.Contains(r.stdout, "No hay actualizaciones pendientes") {
		t.Errorf("updates list before a check:\n%s", r.stdout)
	}
	f.ok("check")
	cand := c(f)

	r := f.ok("updates", "list")
	for _, want := range []string{"lock:alpha", cand.ID, short(cand.ToCommit), "pendiente", "solo texto"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("updates list lacks %q:\n%s", want, r.stdout)
		}
	}
	r = f.ok("updates", "list", "--json")
	var cands []domain.UpdateCandidate
	if err := json.Unmarshal([]byte(r.stdout), &cands); err != nil || len(cands) != 1 || cands[0].ID != cand.ID {
		t.Errorf("updates list --json = %v, %v\n%s", cands, err, r.stdout)
	}

	r = f.ok("updates", "show", "alpha")
	for _, want := range []string{cand.ID, short(cand.ToSHA), "SKILL.md", "-body v1", "+body v2"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("updates show lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestApply(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := newFixture(t)
		f.standard("alpha")
		f.ok("check")
		return f
	}
	assertApplied := func(t *testing.T, f *fixture, cand domain.UpdateCandidate) {
		t.Helper()
		if got := f.installedSHA("alpha"); got != cand.ToSHA {
			t.Errorf("installed SHA = %s, want %s", got, cand.ToSHA)
		}
		j := f.journal()
		if len(j) == 0 || j[len(j)-1].Action != domain.ActionApplied || j[len(j)-1].Target != "lock:alpha" {
			t.Errorf("journal = %+v", j)
		}
		lock, err := os.ReadFile(f.home.Paths().SkillsLockFile())
		if err != nil || !strings.Contains(string(lock), cand.ToSHA) {
			t.Errorf("lock does not record %s: %v\n%s", cand.ToSHA, err, lock)
		}
	}

	t.Run("without a terminal and without --yes it refuses", func(t *testing.T) {
		f := setup(t)
		cand := c(f)
		before := f.homeTree()
		r := f.run(nil, "updates", "apply", "alpha")
		if r.code != 2 || !strings.Contains(r.stderr, "--yes") {
			t.Errorf("apply = exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, cand.ID) {
			t.Errorf("apply did not print the plan:\n%s", r.stdout)
		}
		if after := f.homeTree(); !maps.Equal(before, after) {
			t.Error("a refused apply changed the home")
		}
		if got := c(f).State; got != domain.CandidatePending {
			t.Errorf("candidate is %s, want pending", got)
		}
	})

	t.Run("in a terminal a no refuses", func(t *testing.T) {
		f := setup(t)
		f.interactive = true
		before := f.homeTree()
		r := f.run(strings.NewReader("n\n"), "updates", "apply", "alpha")
		if r.code != 2 || !strings.Contains(r.stdout, "¿Aplicar") {
			t.Errorf("apply = exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		if after := f.homeTree(); !maps.Equal(before, after) {
			t.Error("a declined apply changed the home")
		}
	})

	t.Run("in a terminal a yes applies", func(t *testing.T) {
		f := setup(t)
		f.interactive = true
		cand := c(f)
		r := f.run(strings.NewReader("s\n"), "updates", "apply", "alpha")
		if r.code != 0 || !strings.Contains(r.stdout, "aplicada: lock:alpha") {
			t.Errorf("apply = exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		assertApplied(t, f, cand)
	})

	t.Run("--yes applies without a terminal", func(t *testing.T) {
		f := setup(t)
		cand := c(f)
		r := f.ok("updates", "apply", "lock:alpha", "--yes")
		if !strings.Contains(r.stdout, "aplicada: lock:alpha") {
			t.Errorf("apply --yes:\n%s", r.stdout)
		}
		assertApplied(t, f, cand)
	})

	t.Run("--dry-run prints the plan and changes nothing", func(t *testing.T) {
		f := setup(t)
		cand := c(f)
		before := f.homeTree()
		r := f.ok("updates", "apply", "alpha", "--yes", "--dry-run")
		if !strings.Contains(r.stdout, cand.ID) || !strings.Contains(r.stdout, "Simulación") {
			t.Errorf("apply --dry-run:\n%s", r.stdout)
		}
		if after := f.homeTree(); !maps.Equal(before, after) || len(f.journal()) != 0 {
			t.Error("apply --dry-run changed something")
		}
	})

	t.Run("a candidate superseded before the confirmation is rejected", func(t *testing.T) {
		f := setup(t)
		f.interactive = true
		printed := c(f)
		before := f.homeTree()
		// While the user reads the plan, upstream moves and a check runs.
		stdin := &hookReader{r: strings.NewReader("s\n"), hook: func() {
			f.clock.Advance(time.Hour)
			f.publish("alpha", skillFiles("alpha", "v3"))
			f.ok("check")
		}}
		r := f.run(stdin, "updates", "apply", "alpha")
		if r.code != 2 || !strings.Contains(r.stdout, "rechazada: lock:alpha") {
			t.Errorf("apply = exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		if after := f.homeTree(); !maps.Equal(before, after) {
			t.Error("a superseded approval changed the home")
		}
		if now := c(f); now.ID == printed.ID || now.State != domain.CandidatePending {
			t.Errorf("open candidate = %s %s, want a new pending one", now.ID, now.State)
		}
	})
}

// hookReader runs hook once, before the first read: what happens while the
// user looks at the prompt.
type hookReader struct {
	r    io.Reader
	hook func()
}

func (h *hookReader) Read(p []byte) (int, error) {
	if h.hook != nil {
		h.hook()
		h.hook = nil
	}
	return h.r.Read(p)
}

func TestApplyAll(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha", "beta", "gamma")
	// beta was edited by hand after install.
	f.home.WriteFile(".agents/skills/beta/NOTES.md", "mine\n")
	f.ok("check")
	alpha, gamma := c(f), f.candidate("lock:gamma")
	// gamma's staged copy is damaged, so its revalidation rejects it.
	if err := os.WriteFile(filepath.Join(gamma.StagedPath, "SKILL.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := f.run(nil, "updates", "apply", "--all", "--yes")
	if r.code != 2 {
		t.Errorf("apply --all with a rejected candidate = exit %d, want 2", r.code)
	}
	for _, want := range []string{"Quedan fuera:", "lock:beta", "modificada localmente", "aplicada: lock:alpha", "rechazada: lock:gamma"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("apply --all lacks %q:\n%s", want, r.stdout)
		}
	}
	if got := f.installedSHA("alpha"); got != alpha.ToSHA {
		t.Errorf("alpha was not applied after gamma's rejection")
	}
	if got := f.installedSHA("beta"); got == f.candidate("lock:beta").ToSHA {
		t.Error("apply --all applied the locally modified beta")
	}

	t.Run("a locally modified skill is applied when named", func(t *testing.T) {
		beta := f.candidate("lock:beta")
		f.ok("updates", "apply", "beta", "--yes")
		if got := f.installedSHA("beta"); got != beta.ToSHA {
			t.Errorf("beta installed SHA = %s, want %s", got, beta.ToSHA)
		}
	})
}

func TestNameMatchingSeveralRowsIsRefused(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.home.WriteFile(".claude/skills/alpha/SKILL.md", "---\nname: alpha\n---\nclone\n")
	f.home.WriteFile(".claude/skills/alpha/.git/HEAD", "ref: refs/heads/main\n")
	// An up-to-date clone: its GitHub head is its HEAD.
	f.gh.AddRepo("acme/other", "main")
	head := f.gh.Commit(t, "acme/other", "main", f.home.Path(".claude/skills/alpha"))
	f.git.AddClone(f.home.Path(".claude/skills/alpha"), ports.CloneStatus{
		Remote: "https://github.com/acme/other.git", Branch: "main", Upstream: "origin/main", Head: head, Clean: true,
	})
	f.ok("check")
	before := f.homeTree()
	for _, args := range [][]string{{"updates", "apply", "alpha", "--yes"}, {"updates", "skip", "alpha"}, {"updates", "show", "alpha"}, {"undo", "alpha"}} {
		r := f.run(nil, args...)
		if r.code != 2 || !strings.Contains(r.stderr, "lock:alpha") || !strings.Contains(r.stderr, "git:.claude/skills/alpha") {
			t.Errorf("skillhub %v = exit %d, stderr %q; want both ids", args, r.code, r.stderr)
		}
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("an ambiguous name changed the home")
	}
	f.ok("updates", "apply", "lock:alpha", "--yes")
}

func TestSkip(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")
	cand := c(f)
	if r := f.ok("updates", "skip", "alpha", "--dry-run"); !strings.Contains(r.stdout, "Simulación") || c(f).State != domain.CandidatePending {
		t.Errorf("skip --dry-run changed the candidate:\n%s", r.stdout)
	}
	r := f.ok("updates", "skip", "alpha")
	if !strings.Contains(r.stdout, "omitida") {
		t.Errorf("skip output:\n%s", r.stdout)
	}
	st := f.persisted()
	if len(st.Skips) != 1 || st.Skips[0].Target != "lock:alpha" || st.Skips[0].ToSHA != cand.ToSHA {
		t.Errorf("skips = %+v", st.Skips)
	}
	if r := f.ok("updates", "list"); !strings.Contains(r.stdout, "No hay actualizaciones pendientes") {
		t.Errorf("a skipped update is still listed:\n%s", r.stdout)
	}
}

func TestUndoAndHistory(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	v1 := f.installedSHA("alpha")
	f.ok("check")
	f.ok("updates", "apply", "alpha", "--yes")

	before := f.homeTree()
	if r := f.ok("undo", "alpha", "--dry-run"); !strings.Contains(r.stdout, "Simulación") {
		t.Errorf("undo --dry-run:\n%s", r.stdout)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("undo --dry-run changed the home")
	}

	r := f.ok("undo", "alpha")
	if !strings.Contains(r.stdout, "Deshecha") {
		t.Errorf("undo output:\n%s", r.stdout)
	}
	if got := f.installedSHA("alpha"); got != v1 {
		t.Errorf("installed SHA after undo = %s, want %s", got, v1)
	}

	r = f.ok("history")
	for _, want := range []string{"aplicada", "deshecha", "lock:alpha"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("history lacks %q:\n%s", want, r.stdout)
		}
	}
	r = f.ok("history", "--json")
	var entries []domain.HistoryEntry
	if err := json.Unmarshal([]byte(r.stdout), &entries); err != nil || len(entries) != 2 ||
		entries[0].Action != domain.ActionApplied || entries[1].Action != domain.ActionUndone {
		t.Errorf("history --json = %+v, %v", entries, err)
	}
}

func TestUndoOfAChangedFolderNeedsAConfirmation(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")
	f.ok("updates", "apply", "alpha", "--yes")
	f.home.WriteFile(".agents/skills/alpha/SKILL.md", "edited after the apply\n")
	edited := f.installedSHA("alpha")

	r := f.run(nil, "undo", "alpha")
	if r.code != 2 || !strings.Contains(r.stdout, "cambió después de aplicar") || !strings.Contains(r.stderr, "terminal") {
		t.Errorf("undo of a changed folder = exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := f.installedSHA("alpha"); got != edited {
		t.Error("undo restored without the confirmation")
	}

	f.interactive = true
	r = f.run(strings.NewReader("s\n"), "undo", "alpha")
	if r.code != 0 || !strings.Contains(r.stdout, "Deshecha") {
		t.Errorf("confirmed undo = exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := f.installedSHA("alpha"); got == edited {
		t.Error("the confirmed undo did not restore")
	}
}

func TestInterruptedOperation(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")
	cand := c(f)
	// An apply that died before its snapshot completed: nothing changed yet.
	f.record(domain.Operation{
		ID: "op1", Kind: domain.OperationApply, Target: "lock:alpha", CandidateID: cand.ID,
		RestoreSnapshotID: "20261121T100000-alpha", StartedAt: t0,
	})
	before := f.homeTree()

	for _, args := range [][]string{{"updates", "apply", "alpha", "--yes"}, {"updates", "apply", "--all", "--yes"}, {"updates", "skip", "alpha"}, {"undo", "alpha"}} {
		r := f.run(nil, args...)
		refusal := "skillhub: hay una operación interrumpida; ejecuta «skillhub recover» antes de continuar\n"
		if r.code != 2 || !strings.HasSuffix(r.stderr, refusal) || r.stdout != "" {
			t.Errorf("skillhub %v = exit %d, stdout %q, stderr %q; want a refusal before any plan", args, r.code, r.stdout, r.stderr)
		}
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("a refused command changed the home")
	}
	for _, args := range [][]string{{"scan"}, {"updates", "list"}, {"history"}, {"check"}} {
		if r := f.run(nil, args...); !strings.Contains(r.stderr, "operación interrumpida") {
			t.Errorf("skillhub %v does not warn about the operation: %q", args, r.stderr)
		}
	}
	r := f.ok("scan", "--json")
	var skills []domain.Skill
	if err := json.Unmarshal([]byte(r.stdout), &skills); err != nil || !slices.Contains(skills[0].Issues, domain.IssueInterruptedUpdate) {
		t.Errorf("scan --json = %+v, %v; want interrupted_update", skills, err)
	}

	if r := f.ok("recover", "--dry-run"); !strings.Contains(r.stdout, "lock:alpha") || !strings.Contains(r.stdout, "Simulación") {
		t.Errorf("recover --dry-run:\n%s", r.stdout)
	}
	if _, ok, _ := f.state().Operation(); !ok {
		t.Fatal("recover --dry-run cleared the operation")
	}

	r = f.ok("recover")
	if !strings.Contains(r.stdout, "lock:alpha") || !strings.Contains(r.stdout, "no llegó a cambiar nada") {
		t.Errorf("recover output:\n%s", r.stdout)
	}
	if _, ok, _ := f.state().Operation(); ok {
		t.Error("recover left the operation")
	}
	if j := f.journal(); len(j) != 1 || j[0].Action != domain.ActionRecovered {
		t.Errorf("journal = %+v", j)
	}
	if r := f.run(nil, "scan"); strings.Contains(r.stderr, "operación interrumpida") {
		t.Errorf("scan still warns after recover: %q", r.stderr)
	}
	f.ok("updates", "apply", "alpha", "--yes")
	if got := f.installedSHA("alpha"); got != cand.ToSHA {
		t.Error("the candidate could not be applied after recover")
	}
}

func TestRecoverWithoutOperation(t *testing.T) {
	f := newFixture(t)
	r := f.ok("recover")
	if !strings.Contains(r.stdout, "No hay ninguna operación interrumpida") {
		t.Errorf("recover with nothing to do:\n%s", r.stdout)
	}

	// An undo interrupted while it copied the folder leaves only an
	// incomplete snapshot.
	f.home.WriteFile(".local/state/skillhub/snapshots/20261121T100000-alpha/folder/SKILL.md", "partial\n")
	r = f.ok("recover")
	if !strings.Contains(r.stdout, "No había ninguna operación interrumpida") || !strings.Contains(r.stdout, "restos incompletos") {
		t.Errorf("recover of leftovers:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "Se recuperó") {
		t.Errorf("leftovers were reported as a recovery:\n%s", r.stdout)
	}
	// All in Spanish, and with no undo named: nothing says one ran (L65).
	if want := "No había ninguna operación interrumpida; se eliminaron restos incompletos: 1 snapshot incompleto.\n"; r.stdout != want {
		t.Errorf("recover of leftovers = %q, want %q", r.stdout, want)
	}
	if _, err := os.Stat(f.home.Path(".local/state/skillhub/snapshots/20261121T100000-alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the incomplete snapshot is still there: %v", err)
	}
}

// With no operation pending, a copy an install left in its staging is an
// orphan: recover removes it, the staging levels with it, and says so (D1).
func TestRecoverRemovesAnOrphanInstallStaging(t *testing.T) {
	f := newFixture(t)
	f.home.WriteFile(".agents/.skillhub/staging/tidy-0123/SKILL.md", "---\nname: tidy\n---\nhalf\n")
	r := f.ok("recover", "--dry-run")
	if want := "No hay ninguna operación interrumpida.\nSe eliminaría la copia de una instalación sin terminar.\n"; !strings.HasPrefix(r.stdout, want) {
		t.Errorf("recover --dry-run = %q, want it to start with %q", r.stdout, want)
	}
	if _, err := os.Stat(f.home.Path(".agents/.skillhub/staging/tidy-0123")); err != nil {
		t.Fatalf("recover --dry-run removed the staging: %v", err)
	}
	r = f.ok("recover")
	if want := "No había ninguna operación interrumpida; se eliminaron restos incompletos: la copia de una instalación sin terminar.\n"; r.stdout != want {
		t.Errorf("recover = %q, want %q", r.stdout, want)
	}
	if _, err := os.Lstat(f.home.Path(".agents/.skillhub")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("recover left the staging or its empty parent: %v", err)
	}
	if r := f.ok("recover"); !strings.Contains(r.stdout, "No hay ninguna operación interrumpida") {
		t.Errorf("the second recover:\n%s", r.stdout)
	}
}

func TestSettings(t *testing.T) {
	f := newFixture(t)
	r := f.ok("settings", "show")
	for _, want := range []string{"check_interval", "1h", "notifications", "sí"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("settings show lacks %q:\n%s", want, r.stdout)
		}
	}
	f.ok("settings", "set", "check_interval", "2h")
	data, err := os.ReadFile(f.home.Paths().SettingsFile())
	if err != nil || !strings.Contains(string(data), "2h") {
		t.Errorf("settings.toml = %q, %v", data, err)
	}
	r = f.run(nil, "settings", "set", "check_interval", "10m")
	if r.code != 2 || !strings.Contains(r.stderr, "15m") {
		t.Errorf("an interval under 15m = exit %d, stderr %q", r.code, r.stderr)
	}
	r = f.run(nil, "settings", "set", "colour", "red")
	if r.code != 2 || !strings.Contains(r.stderr, "colour") {
		t.Errorf("an unknown key = exit %d, stderr %q", r.code, r.stderr)
	}
	if r := f.ok("settings", "set", "notifications", "false", "--dry-run"); !strings.Contains(r.stdout, "Simulación") {
		t.Errorf("settings set --dry-run:\n%s", r.stdout)
	}
	if data2, _ := os.ReadFile(f.home.Paths().SettingsFile()); string(data2) != string(data) {
		t.Error("settings set --dry-run wrote settings.toml")
	}
}

func TestBackground(t *testing.T) {
	f := newFixture(t)
	if r := f.ok("background", "status"); !strings.Contains(r.stdout, "desactivada") || !strings.Contains(r.stdout, "skillhub background enable") {
		t.Errorf("background status:\n%s", r.stdout)
	}
	if r := f.ok("background", "enable", "--dry-run"); !strings.Contains(r.stdout, "Simulación") || f.bg.Binary() != "" {
		t.Errorf("background enable --dry-run enabled it:\n%s", r.stdout)
	}
	f.ok("background", "enable")
	if got := f.bg.Binary(); got != "/opt/skillhub/bin/skillhub" {
		t.Errorf("the timer points at %q", got)
	}
	if r := f.ok("background", "status"); !strings.Contains(r.stdout, "activada") {
		t.Errorf("background status after enable:\n%s", r.stdout)
	}
	f.ok("background", "disable")
	if st, _ := f.bg.Status(t.Context()); st.Installed {
		t.Error("background disable left the units")
	}
}

func TestCompositionFailureIsReported(t *testing.T) {
	var stdout, stderr strings.Builder
	code := cliRun([]string{"scan"}, &stdout, &stderr, func() (*app.App, error) { return nil, errors.New("no home") })
	if code != 2 || !strings.Contains(stderr.String(), "no home") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
	}
}
