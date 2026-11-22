package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/statefile"
	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
)

var bgCtx = context.Background()

// The texts the version 2 commands print, as the user reads them.
const (
	dryRunLine    = "Simulación: no se cambió nada.\n"
	planIDLine    = "Identificador del plan: "
	yesNeedsPlan  = "--yes necesita --plan <id>"
	planChanged   = "el plan cambió desde que lo viste"
	notConfirmed  = "no se confirmó; no se cambió nada"
	needsTerminal = "sin una terminal, aprueba el plan con --yes --plan <id>; no se cambió nada"
	managedV2     = "Hay una operación de gestión sin terminar; «skillhub recover» la resuelve"
)

// approve shows the plan of args with --dry-run, then approves the
// identifier it printed with --yes --plan, as a script would.
func (m *machine) approve(args ...string) result {
	m.t.Helper()
	shown := m.ok(append(slices.Clone(args), "--dry-run")...)
	return m.ok(append(slices.Clone(args), "--yes", "--plan", planID(m.t, shown.stdout))...)
}

// findingID is the id of the first Doctor finding of kind.
func (m *machine) findingID(kind string) string {
	m.t.Helper()
	a, err := m.newApp()
	must(m.t, err)
	findings, err := a.Doctor(bgCtx)
	must(m.t, err)
	for _, f := range findings {
		if f.Kind == kind {
			return f.ID
		}
	}
	m.t.Fatalf("no %s finding", kind)
	return ""
}

// ghostLink adds a broken link to a skill that exists nowhere and returns
// its finding's id; its only repair is deleting it.
func (m *machine) ghostLink() string {
	m.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	return "broken_link:.claude/skills/ghost"
}

// publishTool publishes acme/tools with one skill, tidy.
func (m *machine) publishTool() {
	m.t.Helper()
	m.gh.AddRepo("acme/tools", "main")
	writeTree(m.t, filepath.Join(m.upstream, "skills", "tidy"), map[string]string{
		"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\nbody\nsecond line\n",
	})
	m.gh.Commit(m.t, "acme/tools", "main", m.upstream)
}

// Every command that writes shows its plan with --dry-run and writes
// nothing: the hash of the whole home, skillhub's state included, is
// unchanged and the exit code is 0.
func TestEveryWriteCommandDryRunWritesNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *machine) []string
		// skip are paths a dry run may write.
		skip func(m *machine) []string
	}{
		{name: "disable", setup: func(*machine) []string { return []string{"skills", "disable", "agent-browser"} }},
		{name: "enable", setup: func(m *machine) []string {
			m.approve("skills", "disable", "agent-browser")
			return []string{"skills", "enable", "agent-browser"}
		}},
		{name: "unlink", setup: func(*machine) []string { return []string{"skills", "unlink", "archify", "--from", "claude-code"} }},
		{name: "link", setup: func(m *machine) []string {
			m.approve("skills", "unlink", "archify", "--from", "claude-code")
			return []string{"skills", "link", "archify", "--to", "claude-code"}
		}},
		{name: "remove", setup: func(*machine) []string { return []string{"skills", "remove", "grill-me"} }},
		{name: "adopt-off", setup: func(*machine) []string { return []string{"skills", "adopt-off"} }},
		{
			name: "install",
			setup: func(m *machine) []string {
				m.publishTool()
				// The copy prepared for the review is discarded (B2).
				return []string{"skills", "install", "acme/tools", "--to", "claude-code"}
			},
		},
		{name: "doctor fix", setup: func(m *machine) []string {
			return []string{"doctor", "fix", m.ghostLink(), "--repair", "delete_link"}
		}},
		{name: "undo", setup: func(m *machine) []string {
			m.approve("skills", "disable", "agent-browser")
			return []string{"undo", "v2:disable", "--to", m.lastV2().SnapshotID}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			args := tt.setup(m)
			var skip []string
			if tt.skip != nil {
				skip = tt.skip(m)
			}
			before := m.hash(skip...)
			r := m.run("", append(args, "--dry-run")...)
			if r.code != 0 {
				t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, planIDLine) || !strings.HasSuffix(r.stdout, dryRunLine) {
				t.Errorf("stdout lacks the plan identifier or the dry-run line:\n%s", r.stdout)
			}
			if after := m.hash(skip...); after != before {
				t.Error("a dry run changed the home or skillhub's state")
			}
		})
	}
}

// The default flow asks; only a yes in a terminal applies the plan shown.
func TestTheConfirmationCoversThePlanShown(t *testing.T) {
	t.Run("yes", func(t *testing.T) {
		m := newMachine(t)
		m.interactive = true
		r := m.run("s\n", "skills", "disable", "agent-browser")
		if r.code != 0 || !strings.Contains(r.stdout, "¿Aplicar este plan?") {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
		}
		if m.exists(".agents/skills/agent-browser") {
			t.Fatal("the confirmed plan did not run")
		}
		if _, ok := m.persisted().Parked["agent-browser"]; !ok {
			t.Fatal("the confirmed disable did not park the skill")
		}
		snap := m.lastV2().SnapshotID
		if !strings.Contains(r.stdout, "skillhub undo v2:disable --to "+snap) {
			t.Errorf("stdout does not tell how to undo it:\n%s", r.stdout)
		}
	})
	t.Run("no", func(t *testing.T) {
		m := newMachine(t)
		m.interactive = true
		before := m.hash()
		r := m.run("n\n", "skills", "disable", "agent-browser")
		if r.code != 2 || !strings.Contains(r.stderr, notConfirmed) {
			t.Fatalf("exit %d, stderr %q; want 2 and %q", r.code, r.stderr, notConfirmed)
		}
		if m.hash() != before {
			t.Fatal("a refused confirmation changed something")
		}
	})
	t.Run("no terminal", func(t *testing.T) {
		m := newMachine(t)
		before := m.hash()
		r := m.run("s\n", "skills", "disable", "agent-browser")
		if r.code != 2 || !strings.Contains(r.stderr, needsTerminal) || !strings.Contains(r.stdout, planIDLine) {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %q; want 2, the plan and %q", r.code, r.stdout, r.stderr, needsTerminal)
		}
		if m.hash() != before {
			t.Fatal("a command without a terminal changed something")
		}
	})
}

// --yes approves only the identifier passed with --plan: without it, it
// refuses and explains; with the identifier shown, it applies.
func TestYesApprovesOnlyThePlanItNames(t *testing.T) {
	t.Run("without --plan", func(t *testing.T) {
		m := newMachine(t)
		before := m.hash()
		r := m.run("", "skills", "disable", "agent-browser", "--yes")
		if r.code != 2 || !strings.Contains(r.stderr, yesNeedsPlan) || !strings.Contains(r.stderr, "--dry-run") {
			t.Fatalf("exit %d, stderr %q; want 2 and how to get the identifier", r.code, r.stderr)
		}
		if m.hash() != before {
			t.Fatal("--yes without --plan changed something")
		}
	})
	t.Run("--plan without --yes", func(t *testing.T) {
		m := newMachine(t)
		before := m.hash()
		r := m.run("", "skills", "disable", "agent-browser", "--plan", strings.Repeat("0", 64))
		if r.code != 2 || !strings.Contains(r.stderr, "--plan solo se usa junto con --yes") {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		if m.hash() != before {
			t.Fatal("--plan without --yes changed something")
		}
	})
	t.Run("the plan shown", func(t *testing.T) {
		m := newMachine(t)
		m.approve("skills", "disable", "agent-browser")
		if m.exists(".agents/skills/agent-browser") || m.exists(".claude/skills/agent-browser") {
			t.Fatal("--yes --plan did not run the plan")
		}
	})
	t.Run("an old plan", func(t *testing.T) {
		m := newMachine(t)
		shown := m.ok("skills", "disable", "agent-browser", "--dry-run")
		old := planID(t, shown.stdout)
		// A harness gains a link to the skill after the plan was shown.
		m.h.Symlink(".gemini/skills/agent-browser", "../../.agents/skills/agent-browser")
		before := m.hash()
		r := m.run("", "skills", "disable", "agent-browser", "--yes", "--plan", old)
		if r.code != 2 || !strings.Contains(r.stderr, planChanged) {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s; want 2 and %q", r.code, r.stdout, r.stderr, planChanged)
		}
		if m.hash() != before {
			t.Fatal("an old plan ran, or the refusal wrote something")
		}
		if strings.Contains(r.stderr, old) || !strings.Contains(r.stderr, "ahora es ") {
			t.Errorf("the refusal should name the new plan, not run it: %q", r.stderr)
		}
	})
	t.Run("a made-up plan", func(t *testing.T) {
		m := newMachine(t)
		before := m.hash()
		r := m.run("", "skills", "remove", "grill-me", "--yes", "--plan", strings.Repeat("a", 64))
		if r.code != 2 || !strings.Contains(r.stderr, planChanged) || m.hash() != before {
			t.Fatalf("exit %d, stderr %q; want 2, %q and nothing written", r.code, r.stderr, planChanged)
		}
	})
}

// Every version 2 command runs end to end with --yes --plan.
func TestEveryCommandAppliesWithYesAndPlan(t *testing.T) {
	m := newMachine(t)

	m.approve("skills", "unlink", "archify", "--from", "claude-code")
	if m.exists(".claude/skills/archify") {
		t.Fatal("unlink left the link")
	}
	m.approve("skills", "link", "archify", "--to", "claude-code")
	if target, err := os.Readlink(m.h.Path(".claude/skills/archify")); err != nil || target != "../../.agents/skills/archify" {
		t.Fatalf("link = %q, %v", target, err)
	}
	m.approve("skills", "disable", "agent-browser")
	m.approve("skills", "enable", "agent-browser")
	if !m.exists(".agents/skills/agent-browser") || !m.exists(".claude/skills/agent-browser") {
		t.Fatal("enable did not bring the skill back")
	}
	m.approve("skills", "adopt-off")
	if m.exists(".agents/skills/OFF/accessibility") || !m.exists(".agents/skills/OFF/README.md") {
		t.Fatal("adopt-off did not park OFF/ or touched its README")
	}
	m.approve("doctor", "fix", m.ghostLink(), "--repair", "delete_link")
	if m.exists(".claude/skills/ghost") {
		t.Fatal("doctor fix left the broken link")
	}
	r := m.approve("skills", "remove", "grill-me")
	if m.exists(".agents/skills/grill-me") {
		t.Fatal("remove left the folder")
	}
	snap := m.lastV2().SnapshotID
	if !strings.Contains(r.stdout, "skillhub undo v2:remove --to "+snap) {
		t.Errorf("remove does not tell how to undo it:\n%s", r.stdout)
	}
	m.approve("undo", "v2:remove", "--to", snap)
	if !m.exists(".agents/skills/grill-me") || !m.exists(".claude/skills/grill-me") {
		t.Fatal("undo did not restore the removed skill")
	}
}

// Install shows the complete review of the prepared copy (files, labels and
// the diff against nothing), explains new_permissions, and applies with
// the identifier it showed.
func TestInstallShowsTheReviewAndAppliesTheApprovedPlan(t *testing.T) {
	m := newMachine(t)
	m.publishTool()
	shown := m.ok("skills", "install", "acme/tools", "--to", "claude-code", "--dry-run")
	for _, want := range []string{
		"SKILL.md", "permisos nuevos", "+second line",
		"Toda instalación lleva la etiqueta «permisos nuevos»",
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("the review lacks %q:\n%s", want, shown.stdout)
		}
	}
	id := planID(t, shown.stdout)
	m.clock.Advance(3600e9)
	m.ok("skills", "install", "acme/tools", "--to", "claude-code", "--yes", "--plan", id)
	if !m.exists(".agents/skills/tidy/SKILL.md") || !m.exists(".claude/skills/tidy") {
		t.Fatal("install did not publish the skill and its link")
	}
	lock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if !strings.Contains(string(lock), `"tidy"`) || strings.Contains(string(lock), `"ref"`) {
		t.Fatalf("lock after install:\n%s", lock)
	}
}

// A refusal names its cause in Spanish and exits 2 without writing.
func TestRefusalsExplainTheirCause(t *testing.T) {
	t.Run("a neighbour to enable first", func(t *testing.T) {
		m := newMachine(t)
		m.approve("skills", "disable", "agent-browser")
		m.approve("skills", "disable", "archify")
		before := m.hash()
		r := m.run("", "skills", "enable", "agent-browser", "--dry-run")
		if r.code != 2 || !strings.Contains(r.stderr, "archify") || !strings.Contains(r.stderr, "skillhub skills enable archify") {
			t.Fatalf("exit %d, stderr %q; want 2 naming archify to enable first", r.code, r.stderr)
		}
		if m.hash() != before {
			t.Fatal("a refusal wrote something")
		}
	})
	t.Run("an unknown skill", func(t *testing.T) {
		m := newMachine(t)
		r := m.run("", "skills", "remove", "nope", "--dry-run")
		if r.code != 2 || !strings.Contains(r.stderr, "no hay ninguna skill administrable «nope»") {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
	})
	t.Run("no harness", func(t *testing.T) {
		m := newMachine(t)
		r := m.run("", "skills", "link", "archify", "--dry-run")
		if r.code != 2 || !strings.Contains(r.stderr, "«skillhub skills link» necesita --to <harness>") {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
	})
	t.Run("an unknown harness", func(t *testing.T) {
		m := newMachine(t)
		r := m.run("", "skills", "unlink", "archify", "--from", "nope", "--dry-run")
		if r.code != 2 || !strings.Contains(r.stderr, "«nope» no es un agente conocido") {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
	})
}

// After a remove, a failed save of the superseded candidates is shown as a
// success with a warning (A4 of L51): the skill is gone and undoable.
func TestASupersedeSaveFailureIsASuccessWithAWarning(t *testing.T) {
	m := newMachine(t)
	must(t, statefileWithCandidate(m, "lock:grill-me"))
	m.state = func(s ports.StateStore) ports.StateStore { return &failSupersede{StateStore: s} }
	r := m.approve("skills", "remove", "grill-me")
	if m.exists(".agents/skills/grill-me") {
		t.Fatal("remove did not run")
	}
	if !strings.Contains(r.stderr, "aviso: ") || !strings.Contains(r.stderr, "no se pudieron marcar como reemplazadas") {
		t.Errorf("stderr lacks the warning: %q", r.stderr)
	}
	if !strings.Contains(r.stdout, "skillhub undo v2:remove --to ") {
		t.Errorf("stdout does not report the success:\n%s", r.stdout)
	}
}

// failSupersede fails the save that marks a candidate superseded.
type failSupersede struct{ ports.StateStore }

func (f *failSupersede) Save(st domain.PersistedState) error {
	for _, c := range st.Candidates {
		if c.State == domain.CandidateSuperseded {
			return errors.New("disk full")
		}
	}
	return f.StateStore.Save(st)
}

// statefileWithCandidate records an open update candidate of target.
func statefileWithCandidate(m *machine, target string) error {
	st := m.persisted()
	st.Candidates = append(st.Candidates, domain.UpdateCandidate{
		ID: "c1", Target: target, State: domain.CandidatePending, ToSHA: strings.Repeat("1", 40),
		Labels: []domain.Label{}, FileChanges: []domain.FileChange{},
	})
	return statefile.New(m.p).Save(st)
}

func statefileJournal(m *machine) ([]domain.HistoryEntry, error) { return statefile.New(m.p).Journal() }

// The approval does not compute the reload list nor the running harnesses
// between planning again and Begin (A1 of L51): the process directory is
// read for the preview and, after the operation, for the result, never in
// between.
func TestApplyPlanReadsTheProcessesOnlyAfterTheOperation(t *testing.T) {
	m := newMachine(t)
	var seen []int
	m.dirs = func(d ports.SkillDirScanner) ports.SkillDirScanner {
		return &procWatch{SkillDirScanner: d, proc: m.proc, seen: &seen, journal: func() int { return len(v2Lines(m)) }}
	}
	a, err := m.newApp()
	must(t, err)
	p, err := a.PlanDisable(bgCtx, "agent-browser")
	must(t, err)
	seen = nil
	res, err := a.ApplyPlan(bgCtx, p.Request, p.PlanID)
	must(t, err)
	if len(seen) == 0 || slices.Contains(seen, 0) {
		t.Fatalf("journal lines at each read of the process directory: %v; want every read after the operation", seen)
	}
	if !slices.Contains(res.Proposal.Reload, "claude-code") {
		t.Errorf("the result lost the reload list: %v", res.Proposal.Reload)
	}
}

// procWatch records, at each scan of the process directory, how many
// version 2 lines the journal holds.
type procWatch struct {
	ports.SkillDirScanner
	proc    string
	seen    *[]int
	journal func() int
}

func (w *procWatch) Scan(dir string) ([]ports.DirEntry, error) {
	if dir == w.proc {
		*w.seen = append(*w.seen, w.journal())
	}
	return w.SkillDirScanner.Scan(dir)
}

func v2Lines(m *machine) []domain.HistoryEntry {
	j, err := statefileJournal(m)
	must(m.t, err)
	var out []domain.HistoryEntry
	for _, h := range j {
		if strings.HasPrefix(h.Target, "v2:") {
			out = append(out, h)
		}
	}
	return out
}

// Doctor lists the findings with their repairs, and doctor fix explains a
// finding with no repair.
func TestDoctorListsFindingsAndExplainsTheOnesWithoutRepair(t *testing.T) {
	m := newMachine(t)
	before := m.hash()
	id := m.ghostLink()
	before = m.hash()
	r := m.ok("doctor")
	for _, want := range []string{"enlace roto", id, "skillhub doctor fix <hallazgo>"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("doctor lacks %q:\n%s", want, r.stdout)
		}
	}
	if m.hash() != before {
		t.Fatal("doctor wrote something")
	}
	r = m.run("", "doctor", "fix", m.findingID("managed_by_gentle_ai"))
	if r.code != 2 || !strings.Contains(r.stderr, "gentle-ai") {
		t.Fatalf("exit %d, stderr %q; want 2 and why gentle-ai copies are only reported", r.code, r.stderr)
	}
}

// recover resolves a pending version 2 operation through the app, and its
// dry run, check and undo --dry-run tell the truth about it (A5 of L51).
func TestAPendingVersion2OperationIsToldAndRecovered(t *testing.T) {
	m := newMachine(t)
	m.pendingV2()
	before := m.hash()

	r := m.ok("recover", "--dry-run")
	if !strings.Contains(r.stdout, managedV2) || !strings.HasSuffix(r.stdout, dryRunLine) {
		t.Errorf("recover --dry-run:\n%s", r.stdout)
	}
	r = m.run("", "undo", "archify", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "hay una operación de gestión de skills sin terminar") {
		t.Errorf("undo --dry-run = exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r = m.run("", "check")
	if r.code != 2 || !strings.Contains(r.stderr, "«skillhub recover»") {
		t.Errorf("check = exit %d, stderr %q", r.code, r.stderr)
	}
	if m.hash() != before {
		t.Fatal("a dry run or a refusal changed something")
	}

	r = m.ok("recover")
	if !strings.Contains(r.stdout, "Se revirtió la operación de gestión interrumpida") {
		t.Errorf("recover:\n%s", r.stdout)
	}
	if _, err := os.Stat(engine.RecordFile(m.p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record is still there: %v", err)
	}
	if !m.exists(".claude/skills/archify") {
		t.Fatal("recover did not restore the link")
	}
	r = m.ok("recover", "--dry-run")
	if !strings.Contains(r.stdout, "No hay ninguna operación interrumpida.") {
		t.Errorf("recover --dry-run after recover:\n%s", r.stdout)
	}
}

// undo of a version 2 line asks like every other plan, and a line that is
// not there is refused.
func TestUndoOfAVersion2Line(t *testing.T) {
	m := newMachine(t)
	m.approve("skills", "disable", "agent-browser")
	snap := m.lastV2().SnapshotID

	r := m.run("", "undo", "v2:disable", "--to", snap)
	if r.code != 2 || !strings.Contains(r.stderr, needsTerminal) {
		t.Fatalf("undo without a terminal = exit %d, stderr %q", r.code, r.stderr)
	}
	m.interactive = true
	r = m.run("s\n", "undo", "v2:disable")
	if r.code != 0 || !m.exists(".agents/skills/agent-browser") {
		t.Fatalf("undo of the last disable = exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "Se desharía") {
		t.Errorf("undo does not show what it undoes:\n%s", r.stdout)
	}
	r = m.run("", "undo", "v2:disable", "--to", "nope", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "no hay ninguna operación v2:disable con el snapshot «nope»") {
		t.Fatalf("undo of a missing line = exit %d, stderr %q", r.code, r.stderr)
	}
}
