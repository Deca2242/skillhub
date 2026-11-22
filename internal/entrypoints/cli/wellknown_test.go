package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"skillhub/internal/adapters/httpsget"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

// The site of these tests answers as uizze.sh did when L84 read it: the old
// index at /.well-known/agent-skills/, recorded as curl prints it.
const (
	webIndexURL = "https://uizze.sh/.well-known/agent-skills/index.json"
	webSkillURL = "https://uizze.sh/.well-known/agent-skills/ui-taste"
	webIndex    = `{"skills":[{"name":"ui-taste","description":"Taste for UI.","files":["SKILL.md","references/palette.md"]}]}`
	webSkillMD  = "---\nname: ui-taste\ndescription: Taste for UI.\n---\n\nUse taste.\n"
	// webEntry is the entry npx skills 1.9.0 writes for this index with
	// npx skills add https://uizze.sh --skill ui-taste -g (node ran its
	// globalWellKnownLockEntry and computeWellKnownSkillDigest), as the lock
	// indents it.
	webEntry = `    "ui-taste": {
      "source": "uizze.sh",
      "sourceType": "well-known",
      "sourceUrl": "https://uizze.sh/.well-known/agent-skills/ui-taste/SKILL.md",
      "skillFolderHash": "",
      "sourceBaseUrl": "https://uizze.sh",
      "wellKnownDigest": "sha256:2d434533f48272256b0cc82ef373d04fb12060ed1a004997719336d6cf1cd814",
      "installedAt": "2026-11-22T10:00:00.000Z",
      "updatedAt": "2026-11-22T10:00:00.000Z"
    }`
)

// curlGet is the command the curl adapter runs for url with limit.
func curlGet(url string, limit int64) ports.Command {
	return ports.Command{Name: "curl", Args: []string{
		"-q", "--silent", "--show-error", "--fail", "--max-time", "30", "--proto", "=https", "--location", "--max-redirs", "0",
		"--max-filesize", strconv.FormatInt(limit, 10), "--globoff", url,
	}}
}

// recordedSite is the real curl adapter over a fake runner that answers the
// recorded site: no test reaches the network.
func recordedSite(t *testing.T) *fakes.ProcessRunner {
	t.Helper()
	runner := fakes.NewProcessRunner()
	runner.ExpectAlways(curlGet(webIndexURL, 1<<20), ports.Result{Stdout: []byte(webIndex)}, nil)
	runner.ExpectAlways(curlGet(webSkillURL+"/SKILL.md", 8<<20), ports.Result{Stdout: []byte(webSkillMD)}, nil)
	// Each file gets the per-file limit while the total has room.
	runner.ExpectAlways(curlGet(webSkillURL+"/references/palette.md", 8<<20), ports.Result{Stdout: []byte("# Palette\n")}, nil)
	return runner
}

func (m *machine) withRecordedSite() *fakes.ProcessRunner {
	m.t.Helper()
	runner := recordedSite(m.t)
	web, err := httpsget.New(runner)
	must(m.t, err)
	m.web = web
	return runner
}

// skills install https://uizze.sh@ui-taste reviews, plans and applies a
// website skill as it does a GitHub one, writes the entry npx writes, and
// undo v2:install leaves the home as it was.
func TestInstallOfAWebsiteSkillThroughTheCommandLine(t *testing.T) {
	m := newMachine(t)
	runner := m.withRecordedSite()
	lock0, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)

	shown := m.ok("skills", "install", "https://uizze.sh@ui-taste", "--to", "claude-code", "--dry-run")
	for _, want := range []string{
		"Revisión de ui-taste (sitio web uizze.sh (no es GitHub), árbol",
		"references/palette.md", "+Use taste.",
		"Plan: Instalar ui-taste desde sitio web uizze.sh (no es GitHub)",
		"Origen: sitio web uizze.sh (no es GitHub), " + webSkillURL + "/SKILL.md",
		"origen web: la revisión de actualizaciones llegará después",
		`"sourceType": "well-known"`,
	} {
		if !strings.Contains(shown.stdout, want) {
			t.Errorf("the dry run lacks %q:\n%s", want, shown.stdout)
		}
	}
	if m.exists(".agents/skills/ui-taste") || m.exists(".agents/.skillhub/staging") {
		t.Fatal("the dry run installed or left a staged copy")
	}
	before := m.homeLines()

	m.ok("skills", "install", "https://uizze.sh@ui-taste", "--to", "claude-code", "--yes", "--plan", planID(t, shown.stdout))
	if !m.exists(".agents/skills/ui-taste/references/palette.md") || !m.exists(".claude/skills/ui-taste") {
		t.Fatal("the install did not publish the skill and its link")
	}
	lock, err := os.ReadFile(m.p.SkillsLockFile())
	must(t, err)
	if !strings.Contains(string(lock), webEntry) {
		t.Errorf("the lock lacks the entry npx writes:\n%s\nwant\n%s", lock, webEntry)
	}
	for _, c := range runner.Commands() {
		if c.Name != "curl" || !slices.Contains(c.Args, "--proto") {
			t.Errorf("ran %v", c)
		}
	}

	m.approve("undo", "v2:install", "--to", m.lastV2().SnapshotID)
	if got := m.homeLines(); !slices.Equal(got, before) {
		t.Errorf("undo v2:install did not leave the home as it was:\n%s", lineDiff(before, got))
	}
	if after, err := os.ReadFile(m.p.SkillsLockFile()); err != nil || string(after) != string(lock0) {
		t.Errorf("lock after undo:\n%s", after)
	}
}

// An http:// website is refused, in Spanish, with exit 2 and nothing
// written or run.
func TestInstallRefusesAnHTTPWebsite(t *testing.T) {
	m := newMachine(t)
	runner := m.withRecordedSite()
	before := m.hash()
	r := m.run("", "skills", "install", "http://uizze.sh@ui-taste", "--to", "claude-code", "--dry-run")
	if r.code != 2 || !strings.Contains(r.stderr, "un sitio web se lee solo por https") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if m.hash() != before || len(runner.Commands()) != 0 {
		t.Errorf("the refusal wrote something or ran %v", runner.Commands())
	}
}

// check does not fail over a website skill: it says its updates are not
// reviewed yet, and --json marks it.
func TestCheckTellsAWebsiteSkillIsNotCheckedYet(t *testing.T) {
	m := newMachine(t)
	m.withRecordedSite()
	shown := m.ok("skills", "install", "https://uizze.sh@ui-taste", "--to", "claude-code", "--dry-run")
	m.ok("skills", "install", "https://uizze.sh@ui-taste", "--to", "claude-code", "--yes", "--plan", planID(t, shown.stdout))

	// The other skills of this machine come from GitHub, which the fake
	// does not serve: only the line of ui-taste is looked at.
	r := m.run("", "check")
	var line string
	for l := range strings.SplitSeq(r.stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "lock:ui-taste ") {
			line = l
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(line), "origen web: la revisión de actualizaciones llegará después") {
		t.Errorf("the line of ui-taste = %q\nstdout: %s\nstderr: %s", line, r.stdout, r.stderr)
	}
	j := m.run("", "check", "--json")
	var out struct {
		Targets []struct {
			Target    string `json:"target"`
			Outcome   string `json:"outcome"`
			WebSource bool   `json:"web_source"`
			Error     string `json:"error"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(j.stdout), &out); err != nil {
		t.Fatalf("check --json: %v\n%s", err, j.stdout)
	}
	i := slices.IndexFunc(out.Targets, func(tg struct {
		Target    string `json:"target"`
		Outcome   string `json:"outcome"`
		WebSource bool   `json:"web_source"`
		Error     string `json:"error"`
	}) bool {
		return tg.Target == "lock:ui-taste"
	})
	if i < 0 || out.Targets[i].Outcome != "not_checked" || !out.Targets[i].WebSource || out.Targets[i].Error != "" {
		t.Errorf("check --json targets = %+v", out.Targets)
	}
}

// homeLines lists every path, mode, file content hash and link content of
// the home, skillhub's state and cache left out, and the levels that only
// hold them: they keep the journal and the snapshots of what ran.
func (m *machine) homeLines() []string {
	m.t.Helper()
	var out []string
	skip := []string{m.p.StateDir(), m.p.CacheDir()}
	err := filepath.WalkDir(m.h.Root(), func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if slices.Contains(skip, p) {
			return filepath.SkipDir
		}
		if slices.ContainsFunc(skip, func(s string) bool { return strings.HasPrefix(s, p+string(filepath.Separator)) }) {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := p + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			line += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			line += " " + hex.EncodeToString(sum[:])
		}
		out = append(out, line)
		return nil
	})
	must(m.t, err)
	return out
}

// lineDiff is what differs between two listings.
func lineDiff(before, after []string) string {
	var b strings.Builder
	for _, l := range before {
		if !slices.Contains(after, l) {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range after {
		if !slices.Contains(before, l) {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
