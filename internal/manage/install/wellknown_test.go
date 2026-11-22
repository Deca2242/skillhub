package install_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/treehash"
	"skillhub/internal/testsupport/fakes"
)

// The site of these tests is uizze.sh as L84 found it: the old index,
// {skills:[{name, description, files[]}]}, at /.well-known/agent-skills/.
const (
	uizze       = "https://uizze.sh"
	uizzeIndex  = uizze + "/.well-known/agent-skills/index.json"
	uizzeSkill  = uizze + "/.well-known/agent-skills/ui-taste"
	tasteMD     = "---\nname: ui-taste\ndescription: Taste for UI.\n---\n\nUse taste.\n"
	paletteMD   = "# Palette\n"
	legacyIndex = `{"skills":[{"name":"ui-taste","description":"Taste for UI.","files":["SKILL.md","references/palette.md"]}]}`
	// tasteDigest is computeWellKnownSkillDigest of npx skills 1.9.0
	// (wellknown.ts:849-861) for SKILL.md and references/palette.md above,
	// computed by running that function's code with node (the oracle of
	// TestWebInstallWritesTheLockEntryNpxWrites).
	tasteDigest = "sha256:2d434533f48272256b0cc82ef373d04fb12060ed1a004997719336d6cf1cd814"
	// v2Schema is DISCOVERY_SCHEMA_V2 (wellknown.ts:10).
	v2Schema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"
)

// npxWebEntry is the entry npx skills 1.9.0 writes for npx skills add
// https://uizze.sh --skill ui-taste -g (add.ts:1406-1414, 1985-1997;
// skill-lock.ts:186-200), as node printed it for the fixture above.
const npxWebEntry = `{
  "source": "uizze.sh",
  "sourceType": "well-known",
  "sourceUrl": "https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md",
  "skillFolderHash": "",
  "sourceBaseUrl": "https://uizze.sh",
  "wellKnownDigest": "sha256:2d434533f48272256b0cc82ef373d04fb12060ed1a004997719336d6cf1cd814",
  "installedAt": "2026-11-22T10:00:00.000Z",
  "updatedAt": "2026-11-22T10:00:00.000Z"
}`

// uizzeWeb serves the ui-taste skill of uizze.sh.
func uizzeWeb() *fakes.Web {
	w := fakes.NewWeb()
	w.Serve(uizzeIndex, []byte(legacyIndex))
	w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD))
	w.Serve(uizzeSkill+"/references/palette.md", []byte(paletteMD))
	return w
}

func webResolve(t *testing.T, w *fakes.Web, input string) (install.Resolved, error) {
	t.Helper()
	src, err := install.Parse(input)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", input, err)
	}
	return install.WebResolver{Web: w}.Resolve(t.Context(), src)
}

func sha256Digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestParseReadsAWebSource(t *testing.T) {
	tests := []struct {
		in   string
		want install.Source
	}{
		{"https://uizze.sh@ui-taste", install.Source{Site: "https://uizze.sh", Skill: "ui-taste"}},
		{"https://uizze.sh", install.Source{Site: "https://uizze.sh"}},
		{"https://uizze.sh/", install.Source{Site: "https://uizze.sh/"}},
		{"  https://uizze.sh@ui-taste ", install.Source{Site: "https://uizze.sh", Skill: "ui-taste"}},
		{"https://docs.example.com/guides@intro", install.Source{Site: "https://docs.example.com/guides", Skill: "intro"}},
		{"https://example.com/skills/alpha", install.Source{Site: "https://example.com/skills/alpha"}},
		{"https://skills.sh/owner/repo", install.Source{Site: "https://skills.sh/owner/repo"}},
		{"https://github.example.com/owner/repo", install.Source{Site: "https://github.example.com/owner/repo"}},
		{"https://www.mintlify.com:8443@docs", install.Source{Site: "https://www.mintlify.com:8443", Skill: "docs"}},
		// GitHub stays GitHub.
		{"https://github.com/owner/repo", install.Source{Repo: "owner/repo"}},
		{"owner/repo@skill", install.Source{Repo: "owner/repo", Skill: "skill"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := install.Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRefusesAnUnsafeWebSource(t *testing.T) {
	tests := []struct {
		in   string
		kind install.SourceKind
	}{
		{"http://uizze.sh", install.KindInsecure},
		{"http://uizze.sh@ui-taste", install.KindInsecure},
		{"HTTP://uizze.sh", install.KindInsecure},
		{"https://raw.githubusercontent.com/o/r/main/SKILL.md", install.KindURL},
		{"https://huggingface.co/spaces/x", install.KindURL},
		{"https://example.com/repo.git", install.KindURL},
		{"https://user@uizze.sh", install.KindMalformed},
		{"https://user:pw@uizze.sh@ui-taste", install.KindMalformed},
		{"https://uizze.sh@Bad_Name", install.KindMalformed},
		{"https://uizze.sh@", install.KindMalformed},
		{"https://uizze.sh?x=1", install.KindMalformed},
		{"https://uizze.sh#frag", install.KindMalformed},
		{"https://uizze.sh/a/../b", install.KindMalformed},
		{"https://uizze.sh/a//b", install.KindMalformed},
		{`https://uizze.sh/a\b`, install.KindMalformed},
		{"https://uizze.sh/a b", install.KindMalformed},
		{"https://uizze.sh/a\x01b", install.KindMalformed},
		{"https://", install.KindMalformed},
		{"https:///path", install.KindMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := install.Parse(tt.in)
			se, ok := errors.AsType[*install.SourceError](err)
			if !ok || !errors.Is(err, install.ErrUnsupportedSource) {
				t.Fatalf("Parse(%q) = %v, want a *SourceError", tt.in, err)
			}
			if se.Kind != tt.kind {
				t.Errorf("Parse(%q) kind = %s, want %s", tt.in, se.Kind, tt.kind)
			}
		})
	}
}

func TestResolveReadsTheOldIndexOfASite(t *testing.T) {
	w := uizzeWeb()
	res, err := webResolve(t, w, "https://uizze.sh@ui-taste")
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if res.Repo != "uizze.sh" || res.Site != uizze || res.Name != "ui-taste" || res.Key != "ui-taste" ||
		res.SourceURL != uizzeSkill+"/SKILL.md" || res.Digest != tasteDigest ||
		res.Commit != "" || res.Ref != "" || res.SkillPath != "" {
		t.Errorf("Resolve = %+v", res)
	}
	wantCalls := []string{uizzeIndex, uizzeSkill + "/SKILL.md", uizzeSkill + "/references/palette.md"}
	if got := w.URLs(); !slices.Equal(got, wantCalls) {
		t.Errorf("requests = %q, want %q", got, wantCalls)
	}
	for _, c := range w.Calls() {
		if c.MaxBytes <= 0 || c.MaxBytes > install.MaxWebFileBytes {
			t.Errorf("Get(%s) limit = %d, want within 1..%d", c.URL, c.MaxBytes, install.MaxWebFileBytes)
		}
	}
}

// The index is looked for as npx skills 1.9.0 looks for it
// (wellknown.ts:191-205, 623-656): agent-skills, then skills; for a site
// with a path, only below that path, never at the root.
func TestResolveLooksForTheIndexInNpxOrder(t *testing.T) {
	t.Run("skills/ when agent-skills/ is missing", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizze+"/.well-known/skills/index.json", []byte(legacyIndex))
		w.Serve(uizze+"/.well-known/skills/ui-taste/SKILL.md", []byte(tasteMD))
		w.Serve(uizze+"/.well-known/skills/ui-taste/references/palette.md", []byte(paletteMD))
		res, err := webResolve(t, w, "https://uizze.sh")
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if res.SourceURL != uizze+"/.well-known/skills/ui-taste/SKILL.md" {
			t.Errorf("SourceURL = %s", res.SourceURL)
		}
		if got := w.URLs()[:2]; !slices.Equal(got, []string{uizzeIndex, uizze + "/.well-known/skills/index.json"}) {
			t.Errorf("first requests = %q", got)
		}
	})
	t.Run("a path is a scope", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve("https://ex.com/.well-known/agent-skills/index.json", []byte(legacyIndex))
		w.Serve("https://ex.com/docs/.well-known/skills/index.json", []byte(legacyIndex))
		w.Serve("https://ex.com/docs/.well-known/skills/ui-taste/SKILL.md", []byte(tasteMD))
		w.Serve("https://ex.com/docs/.well-known/skills/ui-taste/references/palette.md", []byte(paletteMD))
		res, err := webResolve(t, w, "https://ex.com/docs/@ui-taste")
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if res.SourceURL != "https://ex.com/docs/.well-known/skills/ui-taste/SKILL.md" || res.Repo != "ex.com" ||
			res.Site != "https://ex.com/docs/" {
			t.Errorf("Resolve = %+v", res)
		}
		want := []string{"https://ex.com/docs/.well-known/agent-skills/index.json", "https://ex.com/docs/.well-known/skills/index.json"}
		if got := w.URLs()[:2]; !slices.Equal(got, want) {
			t.Errorf("first requests = %q, want %q", got, want)
		}
	})
	t.Run("a scope never widens to the root", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve("https://ex.com/.well-known/agent-skills/index.json", []byte(legacyIndex))
		_, err := webResolve(t, w, "https://ex.com/docs@ui-taste")
		if !errors.Is(err, install.ErrNoIndex) {
			t.Fatalf("Resolve = %v, want ErrNoIndex", err)
		}
		for _, u := range w.URLs() {
			if !strings.HasPrefix(u, "https://ex.com/docs/") {
				t.Errorf("requested %s outside the scope", u)
			}
		}
	})
	t.Run("no index", func(t *testing.T) {
		w := fakes.NewWeb()
		if _, err := webResolve(t, w, "https://uizze.sh"); !errors.Is(err, install.ErrNoIndex) {
			t.Fatalf("Resolve = %v, want ErrNoIndex", err)
		}
		want := []string{uizzeIndex, uizze + "/.well-known/skills/index.json"}
		if got := w.URLs(); !slices.Equal(got, want) {
			t.Errorf("requests = %q, want %q", got, want)
		}
	})
	t.Run("an unreachable site is not a missing index", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Fail(uizzeIndex, &fakes.WebError{URL: uizzeIndex, Reason: "offline"})
		_, err := webResolve(t, w, "https://uizze.sh")
		if err == nil || errors.Is(err, install.ErrNoIndex) {
			t.Fatalf("Resolve = %v, want the failure", err)
		}
		if _, ok := errors.AsType[*fakes.WebError](err); !ok {
			t.Errorf("Resolve = %v, want the fetcher's error kept", err)
		}
	})
}

// Each rule of isValidSkillEntryV1 (wellknown.ts:312-347) makes the whole old
// index invalid, as npx does (all or nothing, :288-301): the next place is
// tried, and with none left there is no index.
func TestResolveRefusesAnOldIndexNpxRefuses(t *testing.T) {
	good := `{"name":"ui-taste","description":"Taste for UI.","files":["SKILL.md"]}`
	tests := map[string]string{
		"a file with ..":         `{"name":"x","description":"d","files":["SKILL.md","a/../b"]}`,
		"a dotdot anywhere":      `{"name":"x","description":"d","files":["SKILL.md","a..b"]}`,
		"an absolute file":       `{"name":"x","description":"d","files":["SKILL.md","/etc/passwd"]}`,
		"a backslash-rooted":     `{"name":"x","description":"d","files":["SKILL.md","\\x"]}`,
		"a NUL":                  `{"name":"x","description":"d","files":["SKILL.md","a\u0000b"]}`,
		"no SKILL.md":            `{"name":"x","description":"d","files":["README.md"]}`,
		"no files":               `{"name":"x","description":"d","files":[]}`,
		"a file that is no text": `{"name":"x","description":"d","files":["SKILL.md",3]}`,
		"no description":         `{"name":"x","description":"","files":["SKILL.md"]}`,
		"an uppercase name":      `{"name":"Bad","description":"d","files":["SKILL.md"]}`,
		"a name with --":         `{"name":"a--b","description":"d","files":["SKILL.md"]}`,
		"a name ending in -":     `{"name":"ab-","description":"d","files":["SKILL.md"]}`,
		"a 65-character name":    `{"name":"` + strings.Repeat("a", 65) + `","description":"d","files":["SKILL.md"]}`,
	}
	for name, bad := range tests {
		t.Run(name, func(t *testing.T) {
			w := fakes.NewWeb()
			w.Serve(uizzeIndex, []byte(`{"skills":[`+good+`,`+bad+`]}`))
			w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD))
			_, err := webResolve(t, w, "https://uizze.sh@ui-taste")
			if !errors.Is(err, install.ErrNoIndex) {
				t.Fatalf("Resolve = %v, want ErrNoIndex", err)
			}
			if slices.Contains(w.URLs(), uizzeSkill+"/SKILL.md") {
				t.Error("a file of an invalid index was downloaded")
			}
		})
	}
	t.Run("not an object or no skills list", func(t *testing.T) {
		for _, body := range []string{`[]`, `{"skills":{}}`, `not json`, `{}`} {
			w := fakes.NewWeb()
			w.Serve(uizzeIndex, []byte(body))
			if _, err := webResolve(t, w, "https://uizze.sh"); !errors.Is(err, install.ErrNoIndex) {
				t.Errorf("index %s: Resolve = %v, want ErrNoIndex", body, err)
			}
		}
	})
}

// skillhub is stricter than npx with the paths it writes: a path npx would
// accept but that is not a plain relative path of safe characters is
// refused before anything is downloaded.
func TestResolveRefusesAFilePathItCannotWriteSafely(t *testing.T) {
	for _, file := range []string{"./x.md", "a//b.md", "a/./b.md", "dir/", ".git/hooks/x", "a b.md", "a\x01b", "a?b", "a#b", "a%2e%2e/b", "a\\b"} {
		t.Run(file, func(t *testing.T) {
			w := fakes.NewWeb()
			quoted, err := json.Marshal(file)
			must(t, err)
			index := `{"skills":[{"name":"ui-taste","description":"d","files":["SKILL.md",` + string(quoted) + `]}]}`
			w.Serve(uizzeIndex, []byte(index))
			w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD))
			_, err = webResolve(t, w, "https://uizze.sh@ui-taste")
			if !errors.Is(err, install.ErrUnsafeFile) {
				t.Fatalf("Resolve = %v, want ErrUnsafeFile", err)
			}
			if got := w.URLs(); !slices.Equal(got, []string{uizzeIndex}) {
				t.Errorf("requests = %q, want only the index", got)
			}
		})
	}
}

func TestResolveSelectsOneSkill(t *testing.T) {
	two := `{"skills":[{"name":"ui-taste","description":"d","files":["SKILL.md"]},{"name":"other","description":"d","files":["SKILL.md"]}]}`
	t.Run("several and none named", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(two))
		_, err := webResolve(t, w, "https://uizze.sh")
		ae, ok := errors.AsType[*install.AmbiguousSkillError](err)
		if !ok || !slices.Equal(ae.Folders, []string{"other", "ui-taste"}) {
			t.Fatalf("Resolve = %v, want the two skills listed", err)
		}
	})
	t.Run("a name it does not publish", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(two))
		if _, err := webResolve(t, w, "https://uizze.sh@missing"); !errors.Is(err, install.ErrNoSkill) {
			t.Fatalf("Resolve = %v, want ErrNoSkill", err)
		}
	})
	t.Run("a SKILL.md without name or description", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(two))
		w.Serve(uizzeSkill+"/SKILL.md", []byte("---\nname: ui-taste\n---\nNo description.\n"))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrNoSkill) {
			t.Fatalf("Resolve = %v, want ErrNoSkill", err)
		}
	})
	t.Run("a SKILL.md that is not UTF-8", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(two))
		w.Serve(uizzeSkill+"/SKILL.md", []byte("---\nname: ui-taste\ndescription: \xff\n---\n"))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrNoSkill) {
			t.Fatalf("Resolve = %v, want ErrNoSkill", err)
		}
	})
	t.Run("the name in any case, the BOM dropped as fetch's text() drops it", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(two))
		w.Serve(uizzeSkill+"/SKILL.md", []byte("\uFEFF"+tasteMD))
		res, err := webResolve(t, w, "https://uizze.sh@UI-Taste")
		if err != nil || res.Name != "ui-taste" {
			t.Fatalf("Resolve = %+v, %v", res, err)
		}
		if res.Digest != sha256Digest("SKILL.md\x00"+tasteMD+"\x00") {
			t.Errorf("Digest = %s, want the one of the text without its BOM", res.Digest)
		}
	})
}

func TestResolveKeepsWithinTheLimits(t *testing.T) {
	t.Run("too many files", func(t *testing.T) {
		files := []string{`"SKILL.md"`}
		for i := range install.MaxWebFiles {
			files = append(files, fmt.Sprintf(`"f%d.md"`, i))
		}
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(`{"skills":[{"name":"ui-taste","description":"d","files":[`+strings.Join(files, ",")+`]}]}`))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrWebLimits) {
			t.Fatalf("Resolve = %v, want ErrWebLimits", err)
		}
		if got := w.URLs(); len(got) != 1 {
			t.Errorf("requests = %d, want only the index", len(got))
		}
	})
	t.Run("one file too large", func(t *testing.T) {
		w := uizzeWeb()
		w.Serve(uizzeSkill+"/references/palette.md", bytes.Repeat([]byte("x"), int(install.MaxWebFileBytes)+1))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrWebLimits) {
			t.Fatalf("Resolve = %v, want ErrWebLimits", err)
		}
	})
	t.Run("too many bytes in all", func(t *testing.T) {
		var names []string
		w := fakes.NewWeb()
		w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD))
		n := int(install.MaxWebBytes/install.MaxWebFileBytes) + 1
		for i := range n {
			name := fmt.Sprintf("big%d.bin", i)
			names = append(names, `"`+name+`"`)
			w.Serve(uizzeSkill+"/"+name, bytes.Repeat([]byte("x"), int(install.MaxWebFileBytes)))
		}
		w.Serve(uizzeIndex, []byte(`{"skills":[{"name":"ui-taste","description":"d","files":["SKILL.md",`+strings.Join(names, ",")+`]}]}`))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrWebLimits) {
			t.Fatalf("Resolve = %v, want ErrWebLimits", err)
		}
		// Each download is bounded by what is left of the total.
		for _, c := range w.Calls()[1:] {
			if c.MaxBytes > install.MaxWebFileBytes {
				t.Errorf("Get(%s) limit = %d, over the per-file limit", c.URL, c.MaxBytes)
			}
		}
		if last := w.Calls()[len(w.Calls())-1]; last.MaxBytes >= install.MaxWebFileBytes {
			t.Errorf("last limit = %d, want what is left of %d", last.MaxBytes, install.MaxWebBytes)
		}
	})
	t.Run("an index too large", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, bytes.Repeat([]byte(" "), int(install.MaxIndexBytes)+1))
		if _, err := webResolve(t, w, "https://uizze.sh"); err == nil {
			t.Fatal("Resolve of an index over the limit succeeded")
		}
		if c := w.Calls()[0]; c.MaxBytes != install.MaxIndexBytes {
			t.Errorf("index limit = %d, want %d", c.MaxBytes, install.MaxIndexBytes)
		}
	})
}

// The v0.2.0 index (wellknown.ts:257-279, 349-370, 507-543): an unknown
// $schema is refused, invalid entries are skipped, the artifact must match
// its digest, and only the skill-md type is installed.
func TestResolveReadsTheV2Index(t *testing.T) {
	entry := func(name, typ, url, digest string) string {
		return fmt.Sprintf(`{"name":%q,"type":%q,"description":"Taste.","url":%q,"digest":%q}`, name, typ, url, digest)
	}
	index := func(entries ...string) []byte {
		return []byte(`{"$schema":"` + v2Schema + `","skills":[` + strings.Join(entries, ",") + `]}`)
	}
	good := entry("ui-taste", "skill-md", "ui-taste/SKILL.md", sha256Digest(tasteMD))
	t.Run("a skill-md artifact", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, index(entry("bad", "skill-md", "x", "sha256:XYZ"), good))
		w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD))
		res, err := webResolve(t, w, "https://uizze.sh@ui-taste")
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if res.SourceURL != uizzeSkill+"/SKILL.md" || res.Digest != sha256Digest(tasteMD) {
			t.Errorf("Resolve = %+v", res)
		}
	})
	t.Run("an unknown schema", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, []byte(`{"$schema":"https://example.com/other.json","skills":[`+good+`]}`))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrNoIndex) {
			t.Fatalf("Resolve = %v, want ErrNoIndex", err)
		}
	})
	t.Run("an artifact that does not match its digest", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, index(good))
		w.Serve(uizzeSkill+"/SKILL.md", []byte(tasteMD+"tampered\n"))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrDigestMismatch) {
			t.Fatalf("Resolve = %v, want ErrDigestMismatch", err)
		}
	})
	t.Run("an archive", func(t *testing.T) {
		w := fakes.NewWeb()
		w.Serve(uizzeIndex, index(entry("ui-taste", "archive", "ui-taste.tar.gz", sha256Digest("x"))))
		if _, err := webResolve(t, w, "https://uizze.sh@ui-taste"); !errors.Is(err, install.ErrUnsupportedArtifact) {
			t.Fatalf("Resolve = %v, want ErrUnsupportedArtifact", err)
		}
		if len(w.URLs()) != 1 {
			t.Errorf("requests = %q, want only the index", w.URLs())
		}
	})
	for name, url := range map[string]string{
		"another host":           "https://evil.example/SKILL.md",
		"outside the base":       "https://ex.com/other/SKILL.md",
		"outside by dot-dot":     "../../../other/SKILL.md",
		"not https":              "http://ex.com/docs/SKILL.md",
		"another port":           "https://ex.com:8443/docs/SKILL.md",
		"credentials in the url": "https://u@ex.com/docs/SKILL.md",
	} {
		t.Run("an artifact on "+name, func(t *testing.T) {
			w := fakes.NewWeb()
			w.Serve("https://ex.com/docs/.well-known/agent-skills/index.json", index(entry("ui-taste", "skill-md", url, sha256Digest(tasteMD))))
			if _, err := webResolve(t, w, "https://ex.com/docs@ui-taste"); !errors.Is(err, install.ErrUnsafeFile) {
				t.Fatalf("Resolve = %v, want ErrUnsafeFile", err)
			}
			if len(w.URLs()) != 1 {
				t.Errorf("requests = %q, want only the index", w.URLs())
			}
		})
	}
}

// webSite is a site of the install tests whose web serves uizze.sh.
func (s *site) webReview(w *fakes.Web, input string) install.Review {
	s.t.Helper()
	res, err := webResolve(s.t, w, input)
	if err != nil {
		s.t.Fatalf("Resolve(%q) = %v", input, err)
	}
	rev, err := s.preparer().Prepare(s.t.Context(), res)
	if err != nil {
		s.t.Fatalf("Prepare(%q) = %v", input, err)
	}
	return rev
}

func TestPrepareStagesAWebSkillAndReviewsIt(t *testing.T) {
	s := newSite(t, false)
	rev := s.webReview(uizzeWeb(), "https://uizze.sh@ui-taste")
	if rev.StagedPath != filepath.Join(install.StagingRoot(s.p), "ui-taste-"+rev.TreeSHA) {
		t.Errorf("StagedPath = %s", rev.StagedPath)
	}
	got, err := treehash.Dir(rev.StagedPath)
	if err != nil || got.SHA != rev.TreeSHA || rev.TreeSHA != rev.Resolved.FolderSHA {
		t.Fatalf("staged tree = %s, %v; review %s, resolved %s", got.SHA, err, rev.TreeSHA, rev.Resolved.FolderSHA)
	}
	for rel, want := range map[string]string{"SKILL.md": tasteMD, "references/palette.md": paletteMD} {
		data, err := os.ReadFile(filepath.Join(rev.StagedPath, rel))
		if err != nil || string(data) != want {
			t.Errorf("staged %s = %q, %v", rel, data, err)
		}
		if info, err := os.Lstat(filepath.Join(rev.StagedPath, rel)); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("staged %s mode = %v, %v", rel, info, err)
		}
	}
	var paths []string
	for _, fc := range rev.FileChanges {
		paths = append(paths, fc.Path)
	}
	if !slices.Equal(paths, []string{"SKILL.md", "references/palette.md"}) {
		t.Errorf("FileChanges = %v", paths)
	}
	if !slices.Equal(rev.FrontmatterDelta.Changed, []string{"description", "name"}) {
		t.Errorf("FrontmatterDelta = %v", rev.FrontmatterDelta.Changed)
	}
	// Only the staged copy is left in the staging root.
	entries, err := os.ReadDir(install.StagingRoot(s.p))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(rev.StagedPath) {
		t.Errorf("staging root holds %v, %v", entries, err)
	}
}

// TestWebInstallWritesTheLockEntryNpxWrites compares the entry with the one
// npx skills 1.9.0 writes for the same index, then undoes the install: the
// home and the lock are as before, byte for byte.
func TestWebInstallWritesTheLockEntryNpxWrites(t *testing.T) {
	for _, machine := range []bool{true, false} {
		t.Run(map[bool]string{true: "lock", false: "no lock"}[machine], func(t *testing.T) {
			s := newSite(t, machine)
			rev := s.webReview(uizzeWeb(), "https://uizze.sh@ui-taste")
			home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

			pr, err := s.planner().Install(rev, []string{"claude-code"})
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			if string(pr.Preview.LockEntry) != npxWebEntry {
				t.Errorf("lock entry =\n%s\nwant (npx 1.9.0)\n%s", pr.Preview.LockEntry, npxWebEntry)
			}
			if pr.Preview.Source != "uizze.sh" || pr.Preview.Site != uizze || pr.Preview.FollowsDefaultBranch ||
				pr.Preview.Commit != "" || pr.Preview.SourceURL != uizzeSkill+"/SKILL.md" {
				t.Errorf("Preview = %+v", pr.Preview)
			}
			out, err := s.engine().Apply(t.Context(), pr.Plan)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			store := filepath.Join(s.p.StoreDir(), "ui-taste")
			if got, err := treehash.Dir(store); err != nil || got.SHA != rev.TreeSHA {
				t.Errorf("store tree = %s, %v; want %s", got.SHA, err, rev.TreeSHA)
			}
			lock := s.lockBytes()
			if got := jsStringify(t, lock); got != string(lock) {
				t.Errorf("the lock is not what JSON.stringify writes:\n%s", lock)
			}
			if !strings.Contains(string(lock), `"ui-taste": `+indented(npxWebEntry)) {
				t.Errorf("lock after install:\n%s", lock)
			}
			if strings.Contains(string(lock), `"ref"`) {
				t.Error("the install wrote a ref")
			}
			if _, err := s.engine().Undo(t.Context(), out.SnapshotID); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			sameHome(t, "web install then undo", home0, s.everything())
			if got := s.lockBytes(); !bytes.Equal(got, lock0) {
				t.Errorf("lock after undo:\n%s\nwant:\n%s", got, lock0)
			}
			if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) {
				t.Errorf("managed after undo: %v", got.Managed)
			}
		})
	}
}

// A reinstall rewrites a GitHub entry; a web review never replaces one.
func TestReinstallRefusesAWebReview(t *testing.T) {
	s := newSite(t, true)
	rev := s.webReview(uizzeWeb(), "https://uizze.sh@ui-taste")
	if _, err := s.planner().Reinstall(rev, []string{"claude-code"}); !errors.Is(err, install.ErrSourceMismatch) {
		t.Fatalf("Reinstall = %v, want ErrSourceMismatch", err)
	}
}

// TestACrashDuringAWebInstallIsRecovered: the process dies after each write
// of the web install's plan; recover leaves the home, the lock and the state
// as they were.
func TestACrashDuringAWebInstallIsRecovered(t *testing.T) {
	for at := 1; at <= 2; at++ {
		for _, machine := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%d", map[bool]string{true: "lock", false: "no lock"}[machine], at), func(t *testing.T) {
				s := newSite(t, machine)
				pr, err := s.planner().Install(s.webReview(uizzeWeb(), "https://uizze.sh@ui-taste"), []string{"claude-code"})
				must(t, err)
				home0, lock0, state0 := s.everything(), s.lockBytes(), s.state()

				c := &crash{at: at}
				s.wrap = c.wrap
				if _, err := s.engine().Apply(t.Context(), pr.Plan); !errors.Is(err, errCrash) {
					t.Fatalf("Apply() error = %v, want the crash", err)
				}
				s.wrap = nil
				if !c.dead {
					t.Fatal("the crash point was never reached")
				}
				res, err := s.clean().Recover(t.Context())
				if err != nil || !res.Found || !res.RolledBack {
					t.Fatalf("Recover() = %+v, %v; want a rollback", res, err)
				}
				sameHome(t, "crash then recover", home0, s.everything())
				if !bytes.Equal(s.lockBytes(), lock0) {
					t.Errorf("lock after recover:\n%s", s.lockBytes())
				}
				if got := s.state(); !reflect.DeepEqual(got.Managed, state0.Managed) {
					t.Errorf("managed after recover: %v", got.Managed)
				}
				if exists(t, engine.RecordFile(s.p)) {
					t.Error("the record is still there")
				}
			})
		}
	}
}

// Preparing a web skill writes nothing outside the staging root, even when
// it fails half way.
func TestPrepareOfAWebSkillWritesOnlyItsStaging(t *testing.T) {
	s := newSite(t, false)
	before := s.everything()
	res, err := webResolve(t, uizzeWeb(), "https://uizze.sh@ui-taste")
	must(t, err)
	failing := s.preparer()
	failing.HashDir = func(string) (treehash.Tree, error) { return treehash.Tree{}, errors.New("disk gone") }
	if _, err := failing.Prepare(t.Context(), res); err == nil {
		t.Fatal("Prepare with a failing hash succeeded")
	}
	sameHome(t, "a failed web prepare", before, withoutStaging(t, s, s.everything()))
	if entries, _ := os.ReadDir(install.StagingRoot(s.p)); len(entries) != 0 {
		t.Errorf("staging root keeps %v", entries)
	}
}

// withoutStaging drops the empty staging root, and its parent, that
// preparing creates, from a listing of everything.
func withoutStaging(t *testing.T, s *site, all map[string]string) map[string]string {
	t.Helper()
	root, err := filepath.Rel(s.p.Home(), install.StagingRoot(s.p))
	must(t, err)
	out := map[string]string{}
	for k, v := range all {
		if k == root || k == filepath.Dir(root) {
			continue
		}
		out[k] = v
	}
	return out
}
