package engine_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/shared/domain"
)

// world is everything an operation may change outside skillhub's own
// bookkeeping: the skill folders and links of the fixture harnesses, the
// lock's exact bytes or absence, and the managed and parked skills.
type world struct {
	agents, claude map[string]string
	lock           string
	// state is the managed and parked skills as JSON, with the fake home
	// written as <home> and snapshot ids (which hash the plan, paths
	// included) as op-<id>, so worlds of two fake homes compare.
	state string
}

var snapshotIDs = regexp.MustCompile(`op-[0-9A-Za-z._-]+`)

func observeWorld(t *testing.T, e *env) world {
	t.Helper()
	w := world{agents: tree(t, e.home.Path(".agents")), claude: tree(t, e.home.Path(".claude")), lock: "<none>"}
	if data, err := os.ReadFile(e.p.SkillsLockFile()); err == nil {
		w.lock = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	st, err := e.state.Load()
	must(t, err)
	data, err := json.Marshal([]any{st.Managed, st.Parked})
	must(t, err)
	w.state = snapshotIDs.ReplaceAllString(strings.ReplaceAll(string(data), e.home.Root(), "<home>"), "op-<id>")
	return w
}

func assertWorld(t *testing.T, what string, got, want world) {
	t.Helper()
	if d := diffKeys(want.agents, got.agents); len(d) != 0 {
		t.Errorf("%s: ~/.agents differs: %q", what, d)
	}
	if d := diffKeys(want.claude, got.claude); len(d) != 0 {
		t.Errorf("%s: ~/.claude differs: %q", what, d)
	}
	if got.lock != want.lock {
		t.Errorf("%s: the lock is\n%s\nwant\n%s", what, got.lock, want.lock)
	}
	if got.state != want.state {
		t.Errorf("%s: state is %s, want %s", what, got.state, want.state)
	}
}

// journal reads the journal lines.
func journal(t *testing.T, e *env) []domain.HistoryEntry {
	t.Helper()
	j, err := e.state.Journal()
	must(t, err)
	return j
}

// disableEffectPlan is disablePlan with the state change disabling alpha
// makes: alpha leaves managed and is parked with the entry step 2 takes.
func disableEffectPlan(t *testing.T, e *env, eng *engine.Engine, fp fixturePaths) engine.Plan {
	t.Helper()
	st, err := e.state.Load()
	must(t, err)
	st.Managed = map[string]domain.ManagedSkill{"alpha": {Key: "alpha", Targets: []string{"claude-code"}}}
	must(t, e.state.Save(st))
	b := eng.NewBuilder(engine.Meta{Action: "disable", Targets: []string{"claude-code"}})
	must(t, b.Unlink(fp.link, fp.linkTarget))
	must(t, b.Move(fp.alpha, fp.parked))
	must(t, b.LockPark("alpha"))
	must(t, b.RemoveTree(fp.beta))
	must(t, b.SetEffect(engine.Effect{
		DropManaged: []string{"alpha"},
		SetParked: []engine.ParkedEffect{{
			Skill:     domain.ParkedSkill{Key: "alpha", Folder: fp.parked, Links: []domain.LinkRecord{{Path: fp.link, Target: fp.linkTarget}}},
			EntryStep: 2,
		}},
	}))
	plan, err := b.Build()
	must(t, err)
	return plan
}

func TestApplyRunsEveryStepVerifiesAndCloses(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disableEffectPlan(t, e, eng, fp)
	alpha := tree(t, fp.alpha)
	var writes []string
	e.files.afterWrite = func(_ int, line string) { writes = append(writes, line) }

	out, err := eng.Apply(t.Context(), plan)
	must(t, err)

	if out.PlanID != plan.ID || out.SnapshotID == "" {
		t.Errorf("outcome = %+v", out)
	}
	if _, err := os.Lstat(fp.link); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the link is still there: %v", err)
	}
	if _, err := os.Lstat(fp.alpha); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("alpha is still in the store: %v", err)
	}
	if got := tree(t, fp.parked); !reflect.DeepEqual(got, alpha) {
		t.Errorf("the parked folder is not alpha: %v", diffKeys(alpha, got))
	}
	if _, err := os.Lstat(fp.beta); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("beta was not removed: %v", err)
	}
	lock, err := os.ReadFile(e.p.SkillsLockFile())
	must(t, err)
	if strings.Contains(string(lock), `"alpha"`) || !strings.Contains(string(lock), `"beta"`) {
		t.Errorf("lock after parking alpha:\n%s", lock)
	}
	if recordExists(t, e.p) {
		t.Error("the record is still there")
	}

	j := journal(t, e)
	if len(j) != 1 || j[0].Action != domain.ActionApplied || j[0].SnapshotID != out.SnapshotID ||
		j[0].Target != "v2:disable" || !strings.Contains(j[0].Detail, plan.ID) {
		t.Errorf("journal = %+v", j)
	}
	st, err := e.state.Load()
	must(t, err)
	if _, ok := st.Managed["alpha"]; ok {
		t.Error("alpha is still managed")
	}
	parked, ok := st.Parked["alpha"]
	if !ok || parked.Entry == nil || parked.Entry.Name != "alpha" || parked.Entry.After != "beta" ||
		parked.Entry.Before != "" || parked.SnapshotID != out.SnapshotID || parked.Folder != fp.parked || parked.At.IsZero() {
		t.Errorf("parked alpha = %+v (entry %+v)", parked, parked.Entry)
	}
	snap, err := e.clean().ReadSnapshot(out.SnapshotID)
	must(t, err)
	if !strings.Contains(string(snap.Entries[0].Raw), `"ref"`) || string(parked.Entry.Raw) != string(snap.Entries[0].Raw) {
		t.Errorf("the parked entry is not the entry with its ref: %s", parked.Entry.Raw)
	}

	// The journal line is durable before the record goes, and the state
	// change sits between them.
	at := func(prefix string) int {
		return slices.IndexFunc(writes, func(w string) bool { return strings.HasPrefix(w, prefix) })
	}
	journalAt, saveAt, removeAt := at("AppendJournal"), at("Save"), at("RemoveTree "+engine.RecordFile(e.p))
	if journalAt < 0 || saveAt < journalAt || removeAt < saveAt {
		t.Errorf("close order: journal %d, state %d, record removal %d in %q", journalAt, saveAt, removeAt, writes)
	}
}

func TestApplyRevalidatesEachStepAndNeverReplaces(t *testing.T) {
	e, fp := newEnv(t)
	eng := e.engine()
	plan := disablePlan(t, eng, fp)
	// The parking spot appears between Begin and the move: the move must
	// not replace it, and must not even try.
	e.files.afterWrite = func(n int, line string) {
		if strings.HasPrefix(line, "RemoveLink") {
			must(t, os.MkdirAll(fp.parked, 0o755))
			must(t, os.WriteFile(fp.parked+"/theirs.txt", []byte("theirs\n"), 0o644))
		}
	}
	_, err := eng.Apply(t.Context(), plan)
	if !errors.Is(err, engine.ErrForeignState) {
		t.Fatalf("error = %v, want ErrForeignState", err)
	}
	if data, err := os.ReadFile(fp.parked + "/theirs.txt"); err != nil || string(data) != "theirs\n" {
		t.Errorf("their folder was touched: %q, %v", data, err)
	}
	if _, err := os.Lstat(fp.alpha); err != nil {
		t.Errorf("alpha moved anyway: %v", err)
	}
	if slices.ContainsFunc(e.files.writeLog(), func(w string) bool { return strings.HasPrefix(w, "RenameNoReplace") }) {
		t.Error("the move was attempted over foreign state")
	}
}

// failingWrites runs action once without a fault and returns its write log.
func failingWrites(t *testing.T, setup func(t *testing.T) (*env, func(eng *engine.Engine) error)) []string {
	t.Helper()
	e, action := setup(t)
	e.files.arm(-1, failOnce)
	must(t, action(e.engine()))
	return e.files.writeLog()
}

// TestAFailingWriteRollsBackByteForByte fails every write an Apply makes
// after Begin, as a plain failure and as a write that happened but reported
// a failure, and checks that the operation is rolled back to exactly what
// was there, journaled as failed, with no record left.
func TestAFailingWriteRollsBackByteForByte(t *testing.T) {
	for name, sc := range scenarios() {
		t.Run(name, func(t *testing.T) {
			writes := failingWrites(t, sc.setup)
			first := sc.firstStepWrite(writes)
			last := slices.IndexFunc(writes, func(w string) bool { return strings.HasPrefix(w, "AppendJournal") })
			for n := first; n < last; n++ {
				for _, mode := range []faultMode{failOnce, doneButFailed} {
					t.Run(writes[n]+" "+mode.String(), func(t *testing.T) {
						e, action := sc.setup(t)
						before := observeWorld(t, e)
						e.files.arm(n, mode)
						if err := action(e.engine()); !errors.Is(err, errInjected) {
							t.Fatalf("error = %v, want the injected failure", err)
						}
						assertWorld(t, "after the rollback", observeWorld(t, e), before)
						if recordExists(t, e.p) {
							t.Error("a rolled back operation left its record")
						}
						j := journal(t, e)
						if len(j) == 0 || j[len(j)-1].Action != domain.ActionFailed {
							t.Errorf("journal = %+v, want a failed line last", j)
						}
					})
				}
			}
		})
	}
}

// TestACrashAtAnyWriteRecoversByteForByte crashes an operation at every
// write, before or after it, and runs Recover in a new process: the result
// is exactly the world before the operation, or exactly the world after it
// when the crash came after the journal line that commits it. A second
// Recover changes nothing and adds no journal line.
func TestACrashAtAnyWriteRecoversByteForByte(t *testing.T) {
	for name, sc := range scenarios() {
		t.Run(name, func(t *testing.T) {
			writes := failingWrites(t, sc.setup)
			e0, action0 := sc.setup(t)
			must(t, action0(e0.engine()))
			after := observeWorld(t, e0)
			for n := sc.firstStepWrite(writes); n < len(writes); n++ {
				for _, mode := range []faultMode{crashBefore, crashAfter} {
					t.Run(writes[n]+" "+mode.String(), func(t *testing.T) {
						e, action := sc.setup(t)
						before := observeWorld(t, e)
						e.files.arm(n, mode)
						if err := action(e.engine()); !errors.Is(err, errInjected) {
							t.Fatalf("error = %v, want the injected failure", err)
						}
						commit := slices.Index(writes, "AppendJournal "+sc.closeAction)
						committed := commit >= 0 && (commit < n || (commit == n && mode == crashAfter))
						hadRecord := recordExists(t, e.p)
						res, err := e.clean().Recover(t.Context())
						must(t, err)
						want := before
						if committed {
							want = after
						}
						assertWorld(t, "after recover", observeWorld(t, e), want)
						if recordExists(t, e.p) {
							t.Error("recover left the record")
						}
						if res.Found != hadRecord || (hadRecord && res.Committed != committed) {
							t.Errorf("result = %+v, a record was pending: %t, committed: %t", res, hadRecord, committed)
						}
						lines := len(journal(t, e))
						res2, err := e.clean().Recover(t.Context())
						must(t, err)
						if res2.Found || len(journal(t, e)) != lines {
							t.Errorf("a second recover found %+v and wrote %d lines", res2, len(journal(t, e))-lines)
						}
						assertWorld(t, "after a second recover", observeWorld(t, e), want)
					})
				}
			}
		})
	}
}

// TestACrashInsideRecoverIsRecoveredAgain interrupts an operation after its
// second step, then interrupts Recover at every write it makes; a last
// Recover always ends at the world before the operation, with one recovered
// line in the journal.
func TestACrashInsideRecoverIsRecoveredAgain(t *testing.T) {
	for name, sc := range scenarios() {
		t.Run(name, func(t *testing.T) {
			writes := failingWrites(t, sc.setup)
			// The crash point: the second step done, its progress not recorded.
			crashAt := sc.firstStepWrite(writes) + 2
			interrupt := func(t *testing.T) (*env, world) {
				e, action := sc.setup(t)
				before := observeWorld(t, e)
				e.files.arm(crashAt, crashAfter)
				if err := action(e.engine()); !errors.Is(err, errInjected) {
					t.Fatalf("error = %v, want the injected failure", err)
				}
				return e, before
			}
			probe, _ := interrupt(t)
			probe.files.arm(-1, failOnce)
			_, err := probe.engine().Recover(t.Context())
			must(t, err)
			recoverWrites := probe.files.writeLog()
			if len(recoverWrites) < 3 {
				t.Fatalf("recover made only %q", recoverWrites)
			}
			for m := range recoverWrites {
				for _, mode := range []faultMode{crashBefore, crashAfter} {
					t.Run(recoverWrites[m]+" "+mode.String(), func(t *testing.T) {
						e, before := interrupt(t)
						e.files.arm(m, mode)
						if _, err := e.engine().Recover(t.Context()); !errors.Is(err, errInjected) {
							t.Fatalf("recover error = %v, want the injected failure", err)
						}
						_, err := e.clean().Recover(t.Context())
						must(t, err)
						assertWorld(t, "after the second recover", observeWorld(t, e), before)
						recovered := 0
						for _, l := range journal(t, e) {
							if l.Action == domain.ActionRecovered {
								recovered++
							}
						}
						if recovered != 1 || recordExists(t, e.p) {
							t.Errorf("%d recovered lines, record %t", recovered, recordExists(t, e.p))
						}
					})
				}
			}
		})
	}
}

// pendingRecord reads the record on disk.
func pendingRecord(t *testing.T, e *env) engine.Record {
	t.Helper()
	data, err := os.ReadFile(engine.RecordFile(e.p))
	must(t, err)
	var rec engine.Record
	must(t, json.Unmarshal(data, &rec))
	return rec
}
