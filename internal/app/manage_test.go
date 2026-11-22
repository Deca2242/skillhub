package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/manage/remove"
	"skillhub/internal/manage/repair"
	"skillhub/internal/manage/toggle"
	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

var bg = context.Background()

// Disable, enable, unlink, link, adopt OFF/, remove and its undo, each
// through the app, over the real adapters, and the home comes back.
func TestEveryToggleAndRemoveRunsThroughTheApp(t *testing.T) {
	m := newMachine(t)

	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	if p.Toggle == nil || p.PlanID == "" || p.Request.Action != app.ActionDisable || p.Request.Key != "agent-browser" {
		t.Fatalf("PlanDisable = %+v", p)
	}
	if !hasStep(p, "unlink", m.h.Path(".claude/skills/agent-browser")) || !hasStep(p, "lockPark", "agent-browser") {
		t.Fatalf("disable steps = %+v", p.Steps)
	}
	if !slices.Contains(p.Targets, "claude-code") || !slices.Contains(p.Reload, "claude-code") || !slices.Contains(p.Reload, "codex") {
		t.Errorf("targets %v, reload %v: want claude-code reached and every store reader reloading", p.Targets, p.Reload)
	}
	m.apply(p)
	if m.exists(".agents/skills/agent-browser") || m.exists(".claude/skills/agent-browser") {
		t.Fatal("disable left the folder or the link")
	}
	if _, ok := m.persisted().Parked["agent-browser"]; !ok {
		t.Fatal("disable did not park the skill")
	}

	p, err = m.app.PlanEnable(bg, "agent-browser")
	must(t, err)
	m.apply(p)
	if !m.exists(".agents/skills/agent-browser") || !m.exists(".claude/skills/agent-browser") {
		t.Fatal("enable did not bring the folder and the link back")
	}

	p, err = m.app.PlanUnlink(bg, "archify", "claude-code")
	must(t, err)
	if p.Toggle == nil || p.Toggle.Harness != "claude-code" {
		t.Fatalf("PlanUnlink preview = %+v", p.Toggle)
	}
	m.apply(p)
	if m.exists(".claude/skills/archify") {
		t.Fatal("unlink left the link")
	}
	p, err = m.app.PlanLink(bg, "archify", "claude-code")
	must(t, err)
	m.apply(p)
	if target, err := os.Readlink(m.h.Path(".claude/skills/archify")); err != nil || target != "../../.agents/skills/archify" {
		t.Fatalf("link = %q, %v", target, err)
	}

	p, err = m.app.PlanAdoptOff(bg)
	must(t, err)
	if len(p.Toggle.Skills) != 4 || !slices.Contains(p.Toggle.Kept, m.h.Path(".agents/skills/OFF/README.md")) {
		t.Fatalf("adopt-off preview = %+v", p.Toggle)
	}
	m.apply(p)
	if m.exists(".agents/skills/OFF/accessibility") || !m.exists(".agents/skills/OFF/README.md") {
		t.Fatal("adopt-off did not park OFF/ or touched its README")
	}

	p, err = m.app.PlanRemove(bg, "grill-me")
	must(t, err)
	if p.Remove == nil || p.Remove.Key != "grill-me" || p.Remove.LockEntry == nil {
		t.Fatalf("PlanRemove preview = %+v", p.Remove)
	}
	res := m.apply(p)
	if m.exists(".agents/skills/grill-me") || res.SnapshotID == "" {
		t.Fatalf("remove left the folder or no snapshot: %+v", res)
	}
	line := m.lastV2()
	if line.Target != "v2:remove" || line.SnapshotID != res.SnapshotID {
		t.Fatalf("journal line = %+v", line)
	}
	u, err := m.app.PlanUndo(bg, res.SnapshotID)
	must(t, err)
	if u.Undo == nil || u.Undo.Snapshot != res.SnapshotID || u.Undo.Of != "remove" {
		t.Fatalf("PlanUndo preview = %+v", u.Undo)
	}
	done, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: line.SnapshotID, PlanID: u.PlanID})
	must(t, err)
	if done.Version != 2 || done.V2.Proposal.PlanID != u.PlanID {
		t.Fatalf("UndoOperation = %+v", done)
	}
	if !m.exists(".agents/skills/grill-me") || !m.exists(".claude/skills/grill-me") {
		t.Fatal("undo did not restore the removed skill")
	}
}

// A skill the user ignores is still managed: the graph takes the inventory
// without the ignore list (L37).
func TestTheGraphIgnoresTheIgnoreList(t *testing.T) {
	m := newMachine(t)
	s, err := m.settings.Load()
	must(t, err)
	s.Ignore = []string{"lock:agent-browser", "agent-browser"}
	must(t, m.settings.Save(s))

	p, err := m.app.PlanDisable(bg, "agent-browser")
	if err != nil {
		t.Fatalf("PlanDisable of an ignored skill: %v", err)
	}
	// With the ignore list, the lock entry drops out of the graph and the
	// plan would leave it for npx skills update to reinstall.
	if !hasStep(p, "lockPark", "agent-browser") {
		t.Fatalf("disable of an ignored skill does not park its lock entry: %+v", p.Steps)
	}
}

// Install goes through the app: resolve and prepare, propose targets, plan,
// apply; the lock gains the entry.
func TestInstallRunsThroughTheApp(t *testing.T) {
	m := newMachine(t)
	files := map[string]string{"SKILL.md": "---\nname: tidy\ndescription: tidy up\n---\nbody\n"}
	m.gh.AddRepo("acme/tools", "main")
	writeTree(t, filepath.Join(m.upstream, "skills", "tidy"), files)
	m.gh.Commit(t, "acme/tools", "main", m.upstream)

	rev, err := m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	if rev.Key != "tidy" || rev.TreeSHA == "" || rev.Prepared.TreeSHA != rev.TreeSHA || len(rev.FileChanges) != 1 {
		t.Fatalf("ResolveInstall = %+v", rev)
	}
	targets, err := m.app.ProposeTargets(bg)
	must(t, err)
	if !slices.Contains(targets, "claude-code") {
		t.Fatalf("ProposeTargets = %v", targets)
	}
	p, err := m.app.PlanInstall(bg, rev, []string{"claude-code"})
	must(t, err)
	if p.Install == nil || p.Install.Key != "tidy" || p.Install.TreeSHA != rev.TreeSHA || p.Request.Install == nil {
		t.Fatalf("PlanInstall = %+v", p)
	}
	// The request travels as JSON between planning and approving.
	data, err := json.Marshal(p.Request)
	must(t, err)
	var back app.Request
	must(t, json.Unmarshal(data, &back))
	m.clock.Advance(3600e9)
	if _, err := m.app.ApplyPlan(bg, back, p.PlanID); err != nil {
		t.Fatalf("ApplyPlan of the serialized install: %v", err)
	}
	if !m.exists(".agents/skills/tidy/SKILL.md") || !m.exists(".claude/skills/tidy") {
		t.Fatal("install did not publish the skill and its link")
	}
	lock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if !strings.Contains(string(lock), `"tidy"`) || strings.Contains(string(lock), `"ref"`) {
		t.Fatalf("lock after install:\n%s", lock)
	}

	// Installing it again is refused with a typed error naming the folder.
	rev, err = m.app.ResolveInstall(bg, "acme/tools")
	must(t, err)
	_, err = m.app.PlanInstall(bg, rev, nil)
	var ref *app.RefusalError
	if !errors.Is(err, app.ErrKeyExists) || !errors.Is(err, install.ErrKeyExists) || !errors.As(err, &ref) ||
		!slices.Contains(ref.Related, m.h.Path(".agents/skills/tidy")) {
		t.Fatalf("second install = %v (%+v), want ErrKeyExists naming the store folder", err, ref)
	}
}

// Doctor lists the findings, Repairs offers, PlanRepair plans and ApplyPlan
// runs a repair.
func TestARepairRunsThroughTheApp(t *testing.T) {
	m := newMachine(t)
	findings, err := m.app.Doctor(bg)
	must(t, err)
	i := slices.IndexFunc(findings, func(f app.Finding) bool {
		return f.Kind == "broken_link" && slices.Contains(f.Paths, m.h.Path(".kiro/skills/accessibility"))
	})
	if i < 0 {
		t.Fatalf("no broken link finding for kiro: %+v", findings)
	}
	f := findings[i]
	offer, err := m.app.Repairs(bg, f.ID)
	must(t, err)
	if !slices.Contains(offer.Repairs, string(repair.AdoptOff)) {
		t.Fatalf("Repairs(%s) = %+v", f.ID, offer)
	}

	// A lock entry without its folder offers reinstall, handed to install.
	must(t, os.RemoveAll(m.h.Path(".agents/skills/grill-me")))
	must(t, os.Remove(m.h.Path(".claude/skills/grill-me")))
	findings, err = m.app.Doctor(bg)
	must(t, err)
	i = slices.IndexFunc(findings, func(f app.Finding) bool { return f.Kind == "lock_entry_without_folder" && f.LockKey == "grill-me" })
	if i < 0 {
		t.Fatalf("no lock entry finding: %+v", findings)
	}
	p, err := m.app.PlanRepair(bg, findings[i].ID, string(repair.Reinstall), "")
	must(t, err)
	if p.PlanID != "" || p.Repair == nil || p.Repair.Reinstall == nil || p.Repair.Reinstall.Key != "grill-me" {
		t.Fatalf("reinstall proposal = %+v", p.Repair)
	}
	if p.Repair.Reinstall.Input == "" {
		t.Errorf("reinstall input is empty for a github entry: %+v", p.Repair.Reinstall)
	}
	if _, err := m.app.ApplyPlan(bg, p.Request, ""); !errors.Is(err, app.ErrNoPlan) {
		t.Fatalf("ApplyPlan of a reinstall = %v, want ErrNoPlan", err)
	}

	p, err = m.app.PlanRepair(bg, findings[i].ID, string(repair.ParkEntry), "")
	must(t, err)
	if p.Repair == nil || p.Repair.LockEntry != "grill-me" {
		t.Fatalf("park entry proposal = %+v", p.Repair)
	}
	m.apply(p)
	if _, ok := m.persisted().Parked["grill-me"]; !ok {
		t.Fatal("park_entry did not park the entry")
	}
}

// A link created between planning and approving changes the plan: nothing
// runs (A1 of W3 and W4).
func TestANewLinkBetweenPlanAndApplyChangesThePlan(t *testing.T) {
	for name, plan := range map[string]func(m *machine) (app.Proposal, error){
		"disable": func(m *machine) (app.Proposal, error) { return m.app.PlanDisable(bg, "agent-browser") },
		"remove":  func(m *machine) (app.Proposal, error) { return m.app.PlanRemove(bg, "agent-browser") },
	} {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			p, err := plan(m)
			must(t, err)
			m.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/agent-browser")
			before := m.hash()

			_, err = m.app.ApplyPlan(bg, p.Request, p.PlanID)

			if !errors.Is(err, app.ErrPlanChanged) {
				t.Fatalf("ApplyPlan = %v, want ErrPlanChanged", err)
			}
			if m.hash() != before {
				t.Fatal("a changed plan wrote something")
			}
		})
	}
}

// A lock another program changed between planning and approving changes the
// plan, and --yes with an identifier never approves another plan.
func TestALockChangedBetweenPlanAndApplyChangesThePlan(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "archify")
	must(t, err)
	lock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	must(t, os.WriteFile(m.p.SkillsLockFile(), []byte(strings.Replace(string(lock), `"dismissed": {}`, `"dismissed": {"x": true}`, 1)), 0o644))
	before := m.hash()

	if _, err := m.app.ApplyPlan(bg, p.Request, p.PlanID); !errors.Is(err, app.ErrPlanChanged) {
		t.Fatalf("ApplyPlan = %v, want ErrPlanChanged", err)
	}
	if _, err := m.app.ApplyPlan(bg, p.Request, "0000"); !errors.Is(err, app.ErrPlanChanged) {
		t.Fatalf("ApplyPlan with another id = %v, want ErrPlanChanged", err)
	}
	if m.hash() != before {
		t.Fatal("a changed plan wrote something")
	}
}

// Planning and a dry run write nothing at all: no state lock, no record,
// no counter, no directory.
func TestPlanningAndADryRunWriteNothing(t *testing.T) {
	m := newMachine(t)
	before := m.hash()
	for name, plan := range map[string]func() (app.Proposal, error){
		"disable":   func() (app.Proposal, error) { return m.app.PlanDisable(bg, "agent-browser") },
		"unlink":    func() (app.Proposal, error) { return m.app.PlanUnlink(bg, "archify", "claude-code") },
		"adopt-off": func() (app.Proposal, error) { return m.app.PlanAdoptOff(bg) },
		"remove":    func() (app.Proposal, error) { return m.app.PlanRemove(bg, "grill-me") },
	} {
		p, err := plan()
		must(t, err)
		req := p.Request
		req.DryRun = true
		res, err := m.app.ApplyPlan(bg, req, p.PlanID)
		if err != nil || !res.DryRun || res.Proposal.PlanID != p.PlanID {
			t.Fatalf("%s: dry run = %+v, %v", name, res, err)
		}
		if got := m.hash(); got != before {
			t.Fatalf("%s: planning or the dry run wrote something", name)
		}
	}
	if _, err := m.app.Doctor(bg); err != nil {
		t.Fatal(err)
	}
	if m.hash() != before {
		t.Fatal("Doctor wrote something")
	}
}

// A dry run with a plan that changed is refused like a real one.
func TestADryRunOfAChangedPlanIsRefused(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	req := p.Request
	req.DryRun = true
	if _, err := m.app.ApplyPlan(bg, req, "0000"); !errors.Is(err, app.ErrPlanChanged) {
		t.Fatalf("dry run = %v, want ErrPlanChanged", err)
	}
}

// After a remove applies, under the state lock, each open candidate of the
// skill is superseded; a dry run or a refused apply leaves them open.
func TestRemoveSupersedesTheOpenCandidates(t *testing.T) {
	setup := func(t *testing.T) (*machine, app.Proposal) {
		m := newMachine(t)
		st := m.persisted()
		st.Candidates = append(st.Candidates,
			domain.UpdateCandidate{ID: "c1", Target: "lock:agent-browser", Skills: []string{"lock:agent-browser"}, State: domain.CandidatePending},
			domain.UpdateCandidate{ID: "c2", Target: "lock:agent-browser", Skills: []string{"lock:agent-browser"}, State: domain.CandidateBlocked, BlockReason: domain.BlockDirtyWorktree},
			domain.UpdateCandidate{ID: "c3", Target: "lock:agent-browser", Skills: []string{"lock:agent-browser"}, State: domain.CandidateSkipped},
			domain.UpdateCandidate{ID: "c4", Target: "lock:archify", Skills: []string{"lock:archify"}, State: domain.CandidatePending},
		)
		must(t, m.state.Save(st))
		p, err := m.app.PlanRemove(bg, "agent-browser")
		must(t, err)
		if want := []string{"c1", "c2"}; !slices.Equal(p.Remove.PendingCandidates, want) {
			t.Fatalf("pending candidates = %v, want %v", p.Remove.PendingCandidates, want)
		}
		return m, p
	}
	states := func(m *machine) map[string]domain.CandidateState {
		out := map[string]domain.CandidateState{}
		for _, c := range m.persisted().Candidates {
			out[c.ID] = c.State
		}
		return out
	}

	t.Run("applied", func(t *testing.T) {
		m, p := setup(t)
		res := m.apply(p)
		got := states(m)
		if got["c1"] != domain.CandidateSuperseded || got["c2"] != domain.CandidateSuperseded ||
			got["c3"] != domain.CandidateSkipped || got["c4"] != domain.CandidatePending {
			t.Fatalf("candidates = %v", got)
		}
		if !slices.Equal(res.Superseded, []string{"c1", "c2"}) {
			t.Fatalf("Result.Superseded = %v", res.Superseded)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		m, p := setup(t)
		req := p.Request
		req.DryRun = true
		if _, err := m.app.ApplyPlan(bg, req, p.PlanID); err != nil {
			t.Fatal(err)
		}
		if got := states(m); got["c1"] != domain.CandidatePending || got["c2"] != domain.CandidateBlocked {
			t.Fatalf("a dry run changed the candidates: %v", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		m, p := setup(t)
		m.h.Symlink(".kiro/skills/agent-browser", "../../.agents/skills/agent-browser")
		if _, err := m.app.ApplyPlan(bg, p.Request, p.PlanID); err == nil {
			t.Fatal("ApplyPlan of a changed plan succeeded")
		}
		if got := states(m); got["c1"] != domain.CandidatePending || got["c2"] != domain.CandidateBlocked {
			t.Fatalf("a refused apply changed the candidates: %v", got)
		}
	})
}

// Recover goes to the engine while operation-v2.json exists, and to the
// version 1 flow otherwise.
func TestRecoverDispatchesOnTheRecord(t *testing.T) {
	t.Run("version 2", func(t *testing.T) {
		m := newMachine(t)
		p, err := m.app.PlanDisable(bg, "agent-browser")
		must(t, err)
		crashAfterBegin(t, m, p)
		if _, err := os.Stat(engine.RecordFile(m.p)); err != nil {
			t.Fatalf("no pending record: %v", err)
		}
		rec, err := m.app.RecoverOperation(bg)
		must(t, err)
		if rec.Version != 2 || !rec.V2.Found || !rec.V2.RolledBack || rec.V2.PlanID != p.PlanID {
			t.Fatalf("RecoverOperation = %+v", rec)
		}
		if _, err := os.Stat(engine.RecordFile(m.p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the record is still there: %v", err)
		}
		if !m.exists(".agents/skills/agent-browser") {
			t.Fatal("recover did not bring the skill back")
		}
	})
	t.Run("version 1", func(t *testing.T) {
		m := newMachine(t)
		rec, err := m.app.RecoverOperation(bg)
		if !errors.Is(err, updates.ErrNoOperation) || rec.Version != 1 {
			t.Fatalf("RecoverOperation = %+v, %v; want the version 1 flow's ErrNoOperation", rec, err)
		}
	})
}

// crashAfterBegin leaves p half done: begun and one step run, as a crash
// would, through an engine over the same home.
func crashAfterBegin(t *testing.T, m *machine, p app.Proposal) {
	t.Helper()
	e := newEngine(m)
	plan, err := m.app.EnginePlanForTest(p)
	must(t, err)
	run, err := e.Begin(bg, plan)
	must(t, err)
	must(t, run.Release())
}

// Undo dispatches on the journal line: a v2: target goes to the engine,
// any other to the version 1 flow.
func TestUndoDispatchesOnTheJournalLine(t *testing.T) {
	m := newMachine(t)
	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)
	m.apply(p)
	line := m.lastV2()
	u, err := m.app.PlanUndo(bg, line.SnapshotID)
	must(t, err)

	if _, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: line.SnapshotID, PlanID: "0000"}); !errors.Is(err, app.ErrPlanChanged) {
		t.Fatalf("undo with another plan id = %v, want ErrPlanChanged", err)
	}
	done, err := m.app.UndoOperation(bg, app.UndoLine{Target: line.Target, SnapshotID: line.SnapshotID, PlanID: u.PlanID})
	must(t, err)
	if done.Version != 2 || !m.exists(".agents/skills/agent-browser") {
		t.Fatalf("v2 undo = %+v", done)
	}

	done, err = m.app.UndoOperation(bg, app.UndoLine{Target: "lock:archify"})
	if done.Version != 1 || !errors.Is(err, updates.ErrNothingToUndo) {
		t.Fatalf("v1 undo = %+v, %v; want the version 1 flow's ErrNothingToUndo", done, err)
	}
}

// The plan lists the harness processes running now, read from /proc
// through FileOps, and never blocks.
func TestTheProposalListsRunningHarnesses(t *testing.T) {
	m := newMachine(t)
	proc := func(pid, comm, cmdline string) {
		dir := filepath.Join(m.proc, pid)
		must(t, os.MkdirAll(dir, 0o755))
		must(t, os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644))
	}
	proc("101", "claude", "claude\x00--resume\x00")
	proc("202", "node", "node\x00/usr/lib/node_modules/@google/gemini-cli/bin/gemini\x00")
	proc("303", "bash", "bash\x00")
	proc("404", "opencode", "opencode\x00serve\x00")
	must(t, os.MkdirAll(filepath.Join(m.proc, "self"), 0o755))
	must(t, os.WriteFile(filepath.Join(m.proc, "uptime"), []byte("1 2\n"), 0o644))

	p, err := m.app.PlanDisable(bg, "agent-browser")
	must(t, err)

	want := []app.Process{{PID: 101, Harness: "claude-code", Command: "claude"},
		{PID: 202, Harness: "gemini-cli", Command: "gemini"}, {PID: 404, Harness: "opencode", Command: "opencode"}}
	if !slices.Equal(p.Running, want) {
		t.Fatalf("Running = %+v, want %+v", p.Running, want)
	}
}

// Every refusal of a planner is a typed app error that keeps its cause.
func TestRefusalsAreTypedAndKeepTheirCause(t *testing.T) {
	m := newMachine(t)
	cases := []struct {
		name  string
		err   func() error
		kind  error
		cause error
	}{
		{"disable unknown", func() error { _, err := m.app.PlanDisable(bg, "nope"); return err }, app.ErrNotFound, toggle.ErrNotFound},
		{"remove unknown", func() error { _, err := m.app.PlanRemove(bg, "nope"); return err }, app.ErrNotFound, remove.ErrNotFound},
		{"enable active", func() error { _, err := m.app.PlanEnable(bg, "archify"); return err }, app.ErrNotDisabled, toggle.ErrNotParked},
		{"unlink unknown harness", func() error { _, err := m.app.PlanUnlink(bg, "archify", "nope"); return err }, app.ErrUnknownHarness, toggle.ErrUnknownHarness},
		{"disable bad key", func() error { _, err := m.app.PlanDisable(bg, "../x"); return err }, app.ErrInvalidKey, toggle.ErrInvalidKey},
		{"remove a gentle skill", func() error { _, err := m.app.PlanRemove(bg, "go-testing"); return err }, app.ErrForeignZone, remove.ErrForeignZone},
		{"repair a finding that is gone", func() error { _, err := m.app.PlanRepair(bg, "broken_link:nope", "delete_link", ""); return err }, app.ErrStale, repair.ErrStale},
		{"unsupported source", func() error { _, err := m.app.ResolveInstall(bg, "git@gitlab.com:a/b.git"); return err }, app.ErrUnsupportedSource, install.ErrUnsupportedSource},
	}
	for _, c := range cases {
		err := c.err()
		if !errors.Is(err, c.kind) || !errors.Is(err, c.cause) {
			t.Errorf("%s: %v; want both %v and %v", c.name, err, c.kind, c.cause)
		}
	}
	var ref *app.RefusalError
	if err := cases[0].err(); !errors.As(err, &ref) || ref.Kind != app.ErrNotFound {
		t.Errorf("errors.As(RefusalError) = %+v", ref)
	}
}

// Without the version 2 ports, the version 2 calls refuse and version 1 is
// untouched.
func TestVersion2NeedsItsPorts(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.PlanDisable(bg, "alpha"); !errors.Is(err, app.ErrMissingDependency) ||
		!strings.Contains(err.Error(), "Links, ManagedLock") {
		t.Fatalf("PlanDisable without the version 2 ports = %v", err)
	}
	if _, err := f.app.RecoverOperation(bg); !errors.Is(err, updates.ErrNoOperation) {
		t.Fatalf("RecoverOperation without a version 2 record = %v, want the version 1 flow", err)
	}
}
