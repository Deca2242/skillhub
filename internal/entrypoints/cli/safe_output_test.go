package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// hostile is third-party text built to act on the terminal: \r returns to
// the start of the line, ESC[2K erases it and U+202E, the right-to-left
// override, reverses what follows. hostileShown is how it must print (A28).
var (
	rlo          = string(rune(0x202e))
	hostile      = "\r\x1b[2K" + rlo
	hostileShown = `\r\x1b[2K\` + "u202e"
)

// assertNothingActs fails when out holds a character of hostile as it is.
func assertNothingActs(t *testing.T, what, out string) {
	t.Helper()
	for _, raw := range []string{"\r", "\x1b", rlo} {
		if strings.Contains(out, raw) {
			t.Errorf("%s prints %q as it is:\n%q", what, raw, out)
		}
	}
}

// hostileUpdate installs alpha with a file whose name and content are
// hostile and publishes a version without that file and with a hostile
// description: its review holds a hostile file name, diff line and
// description, next to ordinary text.
func (f *fixture) hostileUpdate() {
	f.t.Helper()
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": {
		"SKILL.md":                "---\nname: alpha\ndescription: v1\n---\nbody v1\n",
		"notas" + hostile + ".md": "línea" + hostile + "oculta\n",
	}})
	f.publish("alpha", map[string]string{
		"SKILL.md": "---\nname: alpha\ndescription: Útil" + hostile + "falsa\n---\nbody v2: ñandú 🎉\tcon tab\n",
	})
	f.ok("check")
}

func TestUpdatesShowEscapesThirdPartyText(t *testing.T) {
	f := newFixture(t)
	f.hostileUpdate()

	out := f.ok("updates", "show", "alpha").stdout

	name := "notas" + hostileShown + ".md"
	for _, want := range []string{
		"  modificado  SKILL.md\n  eliminado   " + name + "\n",
		"--- a/" + name + "\n+++ b/" + name + "\n",
		"-línea" + hostileShown + "oculta\n",
		"+description: Útil" + hostileShown + "falsa\n",
		"+body v2: ñandú 🎉\tcon tab\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("updates show lacks %q:\n%s", want, out)
		}
	}
	assertNothingActs(t, "updates show", out)

	// Only the display escapes: the candidate and the JSON keep the text.
	if c := f.candidate("lock:alpha"); !hasPath(c.FileChanges, "notas"+hostile+".md") {
		t.Errorf("the persisted candidate lost the file name: %+v", c.FileChanges)
	}
	s := decodeShow(t, f.ok("updates", "show", "alpha", "--json").stdout)
	var unified string
	for _, d := range s.InstalledToUpstream {
		unified += d.Unified
	}
	if !strings.Contains(unified, "+description: Útil"+hostile+"falsa\n") || !strings.Contains(unified, "-línea"+hostile+"oculta\n") {
		t.Errorf("updates show --json changed the diff: %+v", s.InstalledToUpstream)
	}
}

// A file name with \n and \t stays on its header line of the diff, escaped
// like in the list of files: it cannot add lines of its own to the review.
func TestUpdatesShowKeepsAFileNameOnItsDiffHeader(t *testing.T) {
	f := newFixture(t)
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": {
		"SKILL.md":       "---\nname: alpha\ndescription: v1\n---\nbody v1\n",
		"nl\nname\tx.md": "x\n",
	}})
	f.publish("alpha", map[string]string{"SKILL.md": "---\nname: alpha\ndescription: v2\n---\nbody v2\n"})
	f.ok("check")

	out := f.ok("updates", "show", "alpha").stdout

	name := `nl\nname\tx.md`
	for _, want := range []string{
		"  eliminado   " + name + "\n",
		"\n--- a/" + name + "\n+++ b/" + name + "\n@@ -1 +0,0 @@\n-x\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("updates show lacks %q:\n%q", want, out)
		}
	}
	if strings.Contains(out, "\nname\t") {
		t.Errorf("the file name starts a line of its own:\n%q", out)
	}
}

func hasPath(changes []domain.FileChange, path string) bool {
	for _, fc := range changes {
		if fc.Path == path {
			return true
		}
	}
	return false
}

// The skill a candidate updates is named by its target, which carries the
// skill's name: the list, the plan of apply --all and its refusal of a
// blocked candidate print it escaped.
func TestUpdatesListAndApplyEscapeTheSkillName(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")
	st := f.persisted()
	target := "lock:evil" + hostile + "name"
	st.Candidates = append(st.Candidates, domain.UpdateCandidate{
		ID: "cand-hostile", Target: target, Skills: []string{target},
		FromSHA: "1111111111", InstalledSHAAtStage: "1111111111", ToCommit: "2222222222", ToSHA: "3333333333",
		FileChanges: []domain.FileChange{}, Labels: []domain.Label{domain.LabelTextOnly},
		State: domain.CandidateBlocked, BlockReason: domain.BlockDirtyWorktree, FirstSeenAt: t0,
	})
	if err := f.state().Save(st); err != nil {
		t.Fatal(err)
	}
	shownTarget := "lock:evil" + hostileShown + "name"

	list := f.ok("updates", "list").stdout
	if !strings.Contains(list, shownTarget+"  cand-hostile") {
		t.Errorf("updates list lacks %q:\n%s", shownTarget, list)
	}
	assertNothingActs(t, "updates list", list)

	plan := f.ok("updates", "apply", "--all", "--dry-run").stdout
	if !strings.Contains(plan, shownTarget) || !strings.Contains(plan, "lock:alpha") {
		t.Errorf("apply --all --dry-run lacks %q or lock:alpha:\n%s", shownTarget, plan)
	}
	assertNothingActs(t, "apply --all --dry-run", plan)
	if got := f.candidate(target); got.State != domain.CandidateBlocked {
		t.Errorf("the dry run changed the blocked candidate: %+v", got)
	}
}

// installNamedLock installs files in the store as the skill name and
// records it alone in the lock, every string encoded as JSON: installLock
// quotes the name the Go way, which a hostile name breaks.
func (f *fixture) installNamedLock(name string, files map[string]string) {
	f.t.Helper()
	dir := filepath.Join(f.home.Paths().StoreDir(), name)
	writeTree(f.t, dir, files)
	q := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			f.t.Fatal(err)
		}
		return string(b)
	}
	f.home.WriteFile(".agents/.skill-lock.json", fmt.Sprintf(
		"{\n  \"version\": 3,\n  \"skills\": {\n    %s: {\n      \"source\": %s,\n      \"sourceType\": \"github\",\n"+
			"      \"sourceUrl\": %s,\n      \"skillPath\": %s,\n      \"skillFolderHash\": %s,\n"+
			"      \"installedAt\": \"2026-01-01T00:00:00.000Z\",\n      \"updatedAt\": \"2026-01-02T00:00:00.000Z\"\n    }\n  }\n}\n",
		q(name), q(repo), q("https://github.com/"+repo+".git"), q("skills/"+name+"/SKILL.md"), q(hashOf(f.t, dir))))
}

// A skill whose own name is hostile is named escaped by every updates
// command that names it: show, the plan of apply, skip and its result.
func TestUpdatesCommandsEscapeAHostileSkillName(t *testing.T) {
	f := newFixture(t)
	name := "evil" + hostile + "name"
	f.installNamedLock(name, skillFiles("evil", "v1"))
	f.publish(name, skillFiles("evil", "v2"))
	f.ok("check")
	shownTarget := "lock:evil" + hostileShown + "name"

	runs := map[string][]string{
		"updates show":            {"updates", "show", name},
		"updates apply --dry-run": {"updates", "apply", name, "--dry-run"},
		"updates skip --dry-run":  {"updates", "skip", name, "--dry-run"},
		"updates skip":            {"updates", "skip", name},
	}
	for _, what := range []string{"updates show", "updates apply --dry-run", "updates skip --dry-run", "updates skip"} {
		out := f.ok(runs[what]...).stdout
		if !strings.Contains(out, shownTarget) {
			t.Errorf("%s lacks %q:\n%s", what, shownTarget, out)
		}
		assertNothingActs(t, what, out)
	}
}

func yamlQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScanEscapesThirdPartyTextAndKeepsJSON(t *testing.T) {
	f := newFixture(t)
	evilName := "evil" + hostile + "name"
	aliasName := "alias" + hostile + "link"
	f.installNamedLock(evilName, map[string]string{
		"SKILL.md": "---\nname: " + yamlQuote(t, evilName) + "\ndescription: " + yamlQuote(t, "desc"+hostile) + "\n---\nbody\n",
	})
	if err := os.Symlink(evilName, filepath.Join(f.home.Paths().StoreDir(), aliasName)); err != nil {
		t.Fatal(err)
	}

	out := f.ok("scan").stdout
	assertNothingActs(t, "scan", out)
	for _, want := range []string{
		"evil" + hostileShown + "name",
		"alias" + hostileShown + "link",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan lacks %q:\n%s", want, out)
		}
	}

	// JSON output must keep exact original unescaped structured data.
	j := f.ok("scan", "--json").stdout
	var skills []domain.Skill
	if err := json.Unmarshal([]byte(j), &skills); err != nil {
		t.Fatalf("scan --json: %v\n%s", err, j)
	}
	if len(skills) != 1 || skills[0].Name != evilName || skills[0].Description != "desc"+hostile {
		t.Errorf("scan --json altered third-party fields: %+v", skills)
	}
}

func TestCheckEscapesThirdPartyTextAndKeepsJSON(t *testing.T) {
	f := newFixture(t)
	evilName := "evil" + hostile + "name"
	f.installNamedLock(evilName, skillFiles(evilName, "v1"))
	f.publish(evilName, skillFiles(evilName, "v2"))

	// Human check + desktop notification escape target name.
	out := f.ok("check", "--notify").stdout
	assertNothingActs(t, "check", out)
	if !strings.Contains(out, "lock:evil"+hostileShown+"name") {
		t.Errorf("check lacks escaped target:\n%s", out)
	}
	sent := f.notifier.Sent()
	if len(sent) != 1 {
		t.Fatalf("notifier sent %d messages, want 1", len(sent))
	}
	assertNothingActs(t, "check notification", sent[0].Body)
	if !strings.Contains(sent[0].Body, "evil"+hostileShown+"name") {
		t.Errorf("notification lacks escaped name: %q", sent[0].Body)
	}

	// Failed check escapes error text on stdout while JSON preserves raw fields.
	f.gh.Fail = func(fakes.Call) error { return errors.New("boom" + hostile) }
	r := f.run(nil, "check")
	if r.code != 2 {
		t.Fatalf("failed check exit = %d, want 2", r.code)
	}
	assertNothingActs(t, "failed check", r.stdout)
	if !strings.Contains(r.stdout, "boom"+hostileShown) {
		t.Errorf("failed check lacks escaped error:\n%s", r.stdout)
	}

	rj := f.run(nil, "check", "--json")
	if rj.code != 2 {
		t.Fatalf("failed check --json exit = %d, want 2", rj.code)
	}
	var payload struct {
		Targets []struct {
			Target string `json:"target"`
			Error  string `json:"error"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &payload); err != nil {
		t.Fatalf("check --json: %v\n%s", err, rj.stdout)
	}
	if len(payload.Targets) != 1 || payload.Targets[0].Target != "lock:"+evilName || !strings.Contains(payload.Targets[0].Error, "boom"+hostile) {
		t.Errorf("check --json altered fields: %+v", payload.Targets)
	}
}

func TestHistoryUndoAndRecoverEscapeThirdPartyTextAndKeepJSON(t *testing.T) {
	f := newFixture(t)
	evilName := "evil" + hostile + "name"
	v1SHA := hashOfDirSetup(t, f, evilName)
	f.publish(evilName, skillFiles(evilName, "v2"))
	f.ok("check")
	f.ok("updates", "apply", evilName, "--yes")

	// Append a journal line with hostile detail too.
	if err := f.state().AppendJournal(domain.HistoryEntry{
		At: t0, Action: domain.ActionFailed, Target: "lock:" + evilName,
		FromSHA: "111" + hostile, ToSHA: "222" + hostile,
		SnapshotID: "snap" + hostile, Detail: "detail" + hostile + "\nfake",
	}); err != nil {
		t.Fatal(err)
	}

	histOut := f.ok("history").stdout
	assertNothingActs(t, "history", histOut)
	if !strings.Contains(histOut, "lock:evil"+hostileShown+"name") || !strings.Contains(histOut, "detail"+hostileShown+`\nfake`) {
		t.Errorf("history lacks escaped fields:\n%s", histOut)
	}

	var entries []domain.HistoryEntry
	if err := json.Unmarshal([]byte(f.ok("history", "--json").stdout), &entries); err != nil {
		t.Fatalf("history --json: %v", err)
	}
	if len(entries) < 2 || entries[len(entries)-1].Target != "lock:"+evilName || entries[len(entries)-1].Detail != "detail"+hostile+"\nfake" {
		t.Errorf("history --json altered fields: %+v", entries)
	}

	// undo --dry-run and real undo escape target while restoring the exact hostile-named skill.
	dryOut := f.ok("undo", evilName, "--dry-run").stdout
	assertNothingActs(t, "undo --dry-run", dryOut)
	if !strings.Contains(dryOut, "lock:evil"+hostileShown+"name") {
		t.Errorf("undo --dry-run lacks escaped target:\n%s", dryOut)
	}

	undoOut := f.ok("undo", evilName).stdout
	assertNothingActs(t, "undo", undoOut)
	if !strings.Contains(undoOut, "lock:evil"+hostileShown+"name") {
		t.Errorf("undo lacks escaped target:\n%s", undoOut)
	}
	if got := f.installedSHA(evilName); got != v1SHA {
		t.Errorf("undo did not restore exact target: got %s, want %s", got, v1SHA)
	}

	// recover --dry-run and recover on an interrupted v1 operation escape target and snapshot.
	if err := f.state().RecordOperation(domain.Operation{
		ID:                "op-hostile",
		Kind:              domain.OperationApply,
		Target:            "lock:" + evilName,
		RestoreSnapshotID: "snap" + hostile,
		StartedAt:         t0,
	}); err != nil {
		t.Fatal(err)
	}
	recDry := f.ok("recover", "--dry-run").stdout
	assertNothingActs(t, "recover --dry-run", recDry)
	if !strings.Contains(recDry, "lock:evil"+hostileShown+"name") || !strings.Contains(recDry, "snap"+hostileShown) {
		t.Errorf("recover --dry-run lacks escaped op fields:\n%s", recDry)
	}

	recRun := f.ok("recover").stdout
	assertNothingActs(t, "recover", recRun)
	if !strings.Contains(recRun, "lock:evil"+hostileShown+"name") {
		t.Errorf("recover lacks escaped target:\n%s", recRun)
	}
}

func hashOfDirSetup(t *testing.T, f *fixture, name string) string {
	t.Helper()
	f.installNamedLock(name, skillFiles(name, "v1"))
	return f.installedSHA(name)
}

func TestCatalogCommandsEscapeThirdPartyTextAndKeepJSON(t *testing.T) {
	f := newFixture(t)
	h := hostile
	catalog := fakes.NewCatalog()
	entry := ports.CatalogSkill{
		ID:       "acme/skills" + h + "/evil" + h,
		Source:   "acme/skills" + h,
		SkillID:  "evil" + h,
		Name:     "evil" + h + "\tcol\nrow",
		Installs: 15000,
	}
	catalog.Set("react", entry)
	catalog.Set("agent", entry)

	// skills search escapes table and install hint.
	r := f.runCatalog(catalog, "skills", "search", "react")
	if r.code != 0 {
		t.Fatalf("skills search = exit %d, stderr %q", r.code, r.stderr)
	}
	assertNothingActs(t, "skills search", r.stdout)
	if !strings.Contains(r.stdout, "evil"+hostileShown+`\tcol\nrow`) {
		t.Errorf("skills search lacks escaped name:\n%s", r.stdout)
	}

	// skills search --json preserves original unescaped structured data.
	rj := f.runCatalog(catalog, "skills", "search", "react", "--json")
	if rj.code != 0 {
		t.Fatalf("skills search --json = exit %d, stderr %q", rj.code, rj.stderr)
	}
	var got []app.CatalogEntry
	if err := json.Unmarshal([]byte(rj.stdout), &got); err != nil {
		t.Fatalf("skills search --json: %v\n%s", err, rj.stdout)
	}
	if len(got) != 1 || got[0].Name != entry.Name || got[0].Source != entry.Source {
		t.Errorf("skills search --json altered fields: %+v", got)
	}

	// skills search with hostile query and no results escapes the query.
	empty := f.runCatalog(fakes.NewCatalog(), "skills", "search", "q"+h)
	if empty.code != 0 {
		t.Fatalf("skills search empty = exit %d, stderr %q", empty.code, empty.stderr)
	}
	assertNothingActs(t, "skills search empty", empty.stdout)
	if !strings.Contains(empty.stdout, "q"+hostileShown) {
		t.Errorf("skills search empty lacks escaped query: %q", empty.stdout)
	}

	// skills popular escapes output while --json preserves raw fields.
	pop := f.runCatalog(catalog, "skills", "popular")
	if pop.code != 0 {
		t.Fatalf("skills popular = exit %d, stderr %q", pop.code, pop.stderr)
	}
	assertNothingActs(t, "skills popular", pop.stdout)
	popJSON := f.runCatalog(catalog, "skills", "popular", "--json")
	if popJSON.code != 0 {
		t.Fatalf("skills popular --json = exit %d, stderr %q", popJSON.code, popJSON.stderr)
	}
	var popEntries []app.CatalogEntry
	if err := json.Unmarshal([]byte(popJSON.stdout), &popEntries); err != nil {
		t.Fatalf("skills popular --json: %v", err)
	}
	if len(popEntries) != 1 || popEntries[0].Name != entry.Name {
		t.Errorf("skills popular --json altered fields: %+v", popEntries)
	}
}
