package app_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// byPath indexes file diffs by path.
func byPath(diffs []app.FileDiff) map[string]app.FileDiff {
	out := map[string]app.FileDiff{}
	for _, d := range diffs {
		out[d.Path] = d
	}
	return out
}

// editedCandidate installs alpha at v1 with img.bin, edits the installed
// folder, publishes v2 and returns alpha's candidate after a check.
func editedCandidate(t *testing.T, f *fixture, publishV1 bool) domain.UpdateCandidate {
	t.Helper()
	reg := map[string]string{"SKILL.md": "a\nb\n", "old.md": "gone\n", "img.bin": "x\x00y"}
	if publishV1 {
		f.publish("alpha", reg)
	}
	f.installLock(map[string]map[string]string{"alpha": reg})
	dir := f.home.Path(".agents/skills/alpha")
	writeTree(t, dir, map[string]string{"SKILL.md": "a\nb\nmine\n", "notes.md": "my notes\n", "img.bin": "x\x00mine"})
	f.publish("alpha", map[string]string{"SKILL.md": "a\nc\n", "new.md": "hello\n", "img.bin": "x\x00y"})
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := f.app.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(c.Labels, domain.LabelLocallyModified) {
		t.Fatalf("candidate labels = %v, want locally_modified", c.Labels)
	}
	return c
}

func TestDiffForReviewSplitsUserAndAuthorChanges(t *testing.T) {
	f := newFixture(t)
	c := editedCandidate(t, f, true)
	before := f.homeTree()

	r, err := f.app.DiffForReview(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.LocallyModified || !r.RegisteredAvailable || !r.Split() {
		t.Fatalf("review = %+v, want locally modified with the registered version available", r)
	}
	if r.Installed != nil {
		t.Errorf("Installed = %+v, want none when the diff is split", r.Installed)
	}

	user := byPath(r.User)
	if len(r.User) != 3 {
		t.Errorf("user diffs = %+v, want SKILL.md, notes.md and img.bin", r.User)
	}
	if d := user["SKILL.md"]; d.Change != domain.ChangeModified || !strings.Contains(d.Unified, "+mine\n") ||
		strings.Contains(d.Unified, "+c\n") {
		t.Errorf("user SKILL.md = %+v, want only the user's line", d)
	}
	if d := user["notes.md"]; d.Change != domain.ChangeAdded || !strings.Contains(d.Unified, "+my notes\n") {
		t.Errorf("user notes.md = %+v", d)
	}
	if d := user["img.bin"]; !d.Binary || d.Unified != "" || d.Change != domain.ChangeModified {
		t.Errorf("user img.bin = %+v, want a binary change without text", d)
	}

	author := byPath(r.Author)
	if len(r.Author) != 3 {
		t.Errorf("author diffs = %+v, want SKILL.md, new.md and old.md", r.Author)
	}
	if d := author["SKILL.md"]; d.Change != domain.ChangeModified || !strings.Contains(d.Unified, "-b\n") ||
		!strings.Contains(d.Unified, "+c\n") || strings.Contains(d.Unified, "mine") {
		t.Errorf("author SKILL.md = %+v, want only the author's change", d)
	}
	if d := author["new.md"]; d.Change != domain.ChangeAdded || !strings.Contains(d.Unified, "+hello\n") {
		t.Errorf("author new.md = %+v", d)
	}
	if d := author["old.md"]; d.Change != domain.ChangeRemoved || !strings.Contains(d.Unified, "-gone\n") {
		t.Errorf("author old.md = %+v", d)
	}
	if _, ok := author["img.bin"]; ok {
		t.Error("author diff lists img.bin, which the author did not change")
	}

	reads := 0
	for _, call := range f.gh.Calls() {
		if call.Op == "TreeBySHA" {
			reads++
			if call.Args[0] != repo || call.Args[1] != c.FromSHA {
				t.Errorf("TreeBySHA(%v), want (%s, %s)", call.Args, repo, c.FromSHA)
			}
		}
	}
	if reads != 1 {
		t.Errorf("TreeBySHA calls = %d, want 1", reads)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("DiffForReview changed the home")
	}
}

func TestDiffForReviewFallsBackWhenTheRegisteredVersionIsGone(t *testing.T) {
	f := newFixture(t)
	c := editedCandidate(t, f, false)
	want, err := f.app.Diff(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	before := f.homeTree()

	r, err := f.app.DiffForReview(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.LocallyModified || r.RegisteredAvailable || r.Split() {
		t.Fatalf("review = %+v, want locally modified without the registered version", r)
	}
	if !slices.Equal(r.Installed, want) {
		t.Errorf("Installed = %+v, want the installed → upstream diff %+v", r.Installed, want)
	}
	if r.User != nil || r.Author != nil {
		t.Errorf("User = %+v, Author = %+v, want none", r.User, r.Author)
	}
	if after := f.homeTree(); !maps.Equal(before, after) {
		t.Error("DiffForReview changed the home")
	}
}

func TestDiffForReviewOfAnUneditedSkillIsTheUsualDiff(t *testing.T) {
	f := newFixture(t)
	f.standard()
	if _, err := f.app.Check(ctx, app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := f.app.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.app.Diff(ctx, c)
	if err != nil {
		t.Fatal(err)
	}

	r, err := f.app.DiffForReview(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.LocallyModified || r.Split() || !slices.Equal(r.Installed, want) || r.User != nil || r.Author != nil {
		t.Errorf("review = %+v, want only the usual diff %+v", r, want)
	}
	for _, call := range f.gh.Calls() {
		if call.Op == "TreeBySHA" {
			t.Errorf("an unedited skill read the registered tree: %v", call)
		}
	}
}

func TestDiffForReviewReportsAGitHubFailure(t *testing.T) {
	f := newFixture(t)
	c := editedCandidate(t, f, true)
	f.gh.Fail = func(call fakes.Call) error {
		if call.Op == "TreeBySHA" {
			return ports.ErrRateLimited
		}
		return nil
	}

	if _, err := f.app.DiffForReview(ctx, c); !errors.Is(err, ports.ErrRateLimited) {
		t.Errorf("DiffForReview error = %v, want ErrRateLimited", err)
	}
}

func TestDiffForReviewReportsABlobFailure(t *testing.T) {
	f := newFixture(t)
	c := editedCandidate(t, f, true)
	f.gh.Fail = func(call fakes.Call) error {
		if call.Op == "Blob" {
			return ports.ErrRateLimited
		}
		return nil
	}

	if _, err := f.app.DiffForReview(ctx, c); !errors.Is(err, ports.ErrRateLimited) {
		t.Errorf("DiffForReview error = %v, want ErrRateLimited", err)
	}
}
