package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const (
	userSection   = "Tus cambios (registrada → instalada)"
	authorSection = "Cambios del autor (registrada → upstream)"
	singleSection = "instalada → upstream"
	goneNotice    = "La versión registrada ya no está en upstream"
	replaceNotice = "aplicar la reemplaza por la versión del autor"
)

// edited installs alpha at v1, adds a line of the user's to its SKILL.md
// and publishes v2. Unless publishV1, upstream never held v1.
func (f *fixture) edited(publishV1 bool) {
	f.t.Helper()
	v1 := skillFiles("alpha", "v1")
	if publishV1 {
		f.publish("alpha", v1)
	}
	f.installLock([]string{"alpha"}, map[string]map[string]string{"alpha": v1})
	writeTree(f.t, filepath.Join(f.home.Paths().StoreDir(), "alpha"),
		map[string]string{"SKILL.md": v1["SKILL.md"] + "mine\n"})
	f.publish("alpha", skillFiles("alpha", "v2"))
	f.ok("check")
}

// showJSON is what updates show --json prints.
type showJSON struct {
	Candidate struct {
		ID string `json:"id"`
	} `json:"candidate"`
	LocallyModified       bool           `json:"locally_modified"`
	RegisteredAvailable   bool           `json:"registered_available"`
	InstalledToUpstream   []fileDiffJSON `json:"installed_to_upstream"`
	RegisteredToInstalled []fileDiffJSON `json:"registered_to_installed"`
	RegisteredToUpstream  []fileDiffJSON `json:"registered_to_upstream"`
}

type fileDiffJSON struct {
	Path    string `json:"path"`
	Change  string `json:"change"`
	Unified string `json:"unified"`
	Binary  bool   `json:"binary"`
}

func decodeShow(t *testing.T, out string) showJSON {
	t.Helper()
	var s showJSON
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("updates show --json: %v\n%s", err, out)
	}
	return s
}

func TestUpdatesShowSplitsTheUserAndAuthorChanges(t *testing.T) {
	f := newFixture(t)
	f.edited(true)
	cand := f.candidate("lock:alpha")

	r := f.ok("updates", "show", "alpha")
	out := r.stdout
	for _, want := range []string{cand.ID, replaceNotice, userSection, authorSection, "+mine", "-body v1", "+body v2"} {
		if !strings.Contains(out, want) {
			t.Errorf("updates show lacks %q:\n%s", want, out)
		}
	}
	user, author := strings.Index(out, userSection), strings.Index(out, authorSection)
	if user < 0 || author < user {
		t.Fatalf("want the user's section before the author's:\n%s", out)
	}
	if userPart := out[user:author]; strings.Contains(userPart, "+body v2") || !strings.Contains(userPart, "+mine") {
		t.Errorf("user's section has the wrong diff:\n%s", userPart)
	}
	if authorPart := out[author:]; strings.Contains(authorPart, "mine") || !strings.Contains(authorPart, "+body v2") {
		t.Errorf("author's section has the wrong diff:\n%s", authorPart)
	}
	if strings.Contains(out, goneNotice) {
		t.Errorf("updates show says the registered version is gone:\n%s", out)
	}

	s := decodeShow(t, f.ok("updates", "show", "alpha", "--json").stdout)
	if s.Candidate.ID != cand.ID || !s.LocallyModified || !s.RegisteredAvailable || s.InstalledToUpstream != nil {
		t.Errorf("--json = %+v, want a split review of %s", s, cand.ID)
	}
	if len(s.RegisteredToInstalled) != 1 || s.RegisteredToInstalled[0].Path != "SKILL.md" ||
		s.RegisteredToInstalled[0].Change != "modified" || !strings.Contains(s.RegisteredToInstalled[0].Unified, "+mine") {
		t.Errorf("registered_to_installed = %+v", s.RegisteredToInstalled)
	}
	if len(s.RegisteredToUpstream) != 1 || !strings.Contains(s.RegisteredToUpstream[0].Unified, "+body v2") ||
		strings.Contains(s.RegisteredToUpstream[0].Unified, "mine") {
		t.Errorf("registered_to_upstream = %+v", s.RegisteredToUpstream)
	}
}

func TestUpdatesShowWithoutTheRegisteredVersion(t *testing.T) {
	f := newFixture(t)
	f.edited(false)

	out := f.ok("updates", "show", "alpha").stdout
	for _, want := range []string{replaceNotice, goneNotice, singleSection, "-mine", "+body v2"} {
		if !strings.Contains(out, want) {
			t.Errorf("updates show lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{userSection, authorSection} {
		if strings.Contains(out, unwanted) {
			t.Errorf("updates show has %q:\n%s", unwanted, out)
		}
	}

	s := decodeShow(t, f.ok("updates", "show", "alpha", "--json").stdout)
	if !s.LocallyModified || s.RegisteredAvailable || s.RegisteredToInstalled != nil || s.RegisteredToUpstream != nil {
		t.Errorf("--json = %+v, want one diff without the registered version", s)
	}
	if len(s.InstalledToUpstream) != 1 || !strings.Contains(s.InstalledToUpstream[0].Unified, "-mine") {
		t.Errorf("installed_to_upstream = %+v", s.InstalledToUpstream)
	}
}

func TestUpdatesShowJSONOfAnUneditedSkill(t *testing.T) {
	f := newFixture(t)
	f.standard("alpha")
	f.ok("check")

	text := f.ok("updates", "show", "alpha").stdout
	for _, unwanted := range []string{userSection, authorSection, goneNotice, replaceNotice} {
		if strings.Contains(text, unwanted) {
			t.Errorf("updates show of an unedited skill has %q:\n%s", unwanted, text)
		}
	}
	s := decodeShow(t, f.ok("updates", "show", "alpha", "--json").stdout)
	if s.LocallyModified || s.RegisteredAvailable || s.RegisteredToInstalled != nil || s.RegisteredToUpstream != nil ||
		len(s.InstalledToUpstream) != 1 || !strings.Contains(s.InstalledToUpstream[0].Unified, "+body v2") {
		t.Errorf("--json = %+v, want only the usual diff", s)
	}
}
