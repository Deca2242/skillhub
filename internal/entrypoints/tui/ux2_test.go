package tui_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
)

// UX2 (S13, L79): the keys are lower case letters, Actualizaciones,
// Historial, Instalar, Doctor and Ajustes tell what they show in plain
// words, and the TUI never says harness.

// keyHint is a key the screen shows with its label.
type keyHint struct{ key, label string }

var (
	bracketKey = regexp.MustCompile(`\[([^\]\s]{1,8})\]`)
	pressKey   = regexp.MustCompile(`Pulsa (\S+)`)
	checkBox   = regexp.MustCompile(`^(\s*(?:> )?)\[[x ]\] `)
	borders    = strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ")
)

// visibleKeys are the keys a rendered screen names: "[k] Label" anywhere,
// "k label" in the segments of a line joined by " • ", and "Pulsa k".
func visibleKeys(screen string) [][]keyHint {
	var out [][]keyHint
	for _, raw := range strings.Split(screen, "\n") {
		// A check box, [x] or [ ], marks an agent: it is no key (UX3).
		line := checkBox.ReplaceAllString(borders.Replace(raw), "$1")
		var hints []keyHint
		for _, loc := range bracketKey.FindAllStringSubmatchIndex(line, -1) {
			key := line[loc[2]:loc[3]]
			rest := line[loc[1]:]
			label := ""
			if strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "  ") {
				label = rest[1:]
				if i := strings.Index(label, "  "); i >= 0 {
					label = label[:i]
				}
				if i := strings.Index(label, "["); i >= 0 {
					label = label[:i]
				}
			}
			hints = append(hints, keyHint{key, label})
		}
		if strings.Contains(line, " • ") {
			for i, seg := range strings.Split(strings.TrimSpace(line), " • ") {
				if i == 0 {
					if _, after, ok := strings.Cut(seg, ": "); ok {
						seg = after
					}
				}
				key, label, _ := strings.Cut(strings.TrimSpace(seg), " ")
				hints = append(hints, keyHint{key, label})
			}
		}
		for _, mt := range pressKey.FindAllStringSubmatch(line, -1) {
			hints = append(hints, keyHint{mt[1], ""})
		}
		out = append(out, hints)
	}
	return out
}

func isLetter(k string) bool {
	r, size := utf8.DecodeRuneInString(k)
	return size == len(k) && unicode.IsLetter(r)
}

// checkKeys fails when a letter shown is upper case or, within one screen
// (one line of the help, whose lines are its screens), names two actions.
func checkKeys(t *testing.T, name string, screen string, perLine bool) int {
	t.Helper()
	meaning := map[string]string{}
	seen := 0
	for _, line := range visibleKeys(screen) {
		if perLine {
			meaning = map[string]string{}
		}
		for _, h := range line {
			if !isLetter(h.key) {
				continue
			}
			seen++
			if strings.ToLower(h.key) != h.key {
				t.Errorf("%s: the key %q is shown in upper case:\n%s", name, h.key, screen)
			}
			label := strings.ToLower(strings.TrimRight(strings.TrimSpace(h.label), ".,"))
			if label == "" {
				continue
			}
			if prev, ok := meaning[h.key]; ok && prev != label {
				t.Errorf("%s: the key %q means both %q and %q:\n%s", name, h.key, prev, label, screen)
			}
			meaning[h.key] = label
		}
	}
	return seen
}

// Every screen, modal and help shows its letters in lower case, and no
// letter means two things on the same screen.
func TestEveryScreenShowsLowerCaseLettersWithOneMeaning(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	// A broken link of archify: its row offers p next to the filters.
	mc.h.Symlink(".kiro/skills/archify", "../../.agents/skills/old-archify")
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	m := openSized(t, a, 220, 70)
	check := func(name string) { t.Helper(); checkKeys(t, name, screen(m), false) }

	// The parser finds the keys: the Skills screen names several letters.
	if n := checkKeys(t, "Skills", screen(m), false); n < 4 {
		t.Fatalf("only %d letters found on Skills:\n%s", n, screen(m))
	}
	selectSkill(t, m, "agent-browser")
	check("Skills, a disabled row")
	pressRun(m, "a")
	check("the enable confirmation")
	pressRun(m, "v")
	check("the enable confirmation with its detail")
	pressRun(m, "n")
	pressRun(m, "/")
	pressRun(m, "esc")
	selectSkill(t, m, "archify")
	check("Skills, a managed row")
	if !strings.Contains(screen(m), text.TUIKeyFixLink) {
		t.Fatalf("archify does not offer fixing its link:\n%s", screen(m))
	}
	// UX3: the check boxes, the filters, the agent filter, the result of
	// an operation with z, and the undo z opens.
	pressRun(m, "l")
	check("the agent check boxes")
	pressRun(m, "esc")
	for _, name := range []string{"active", "disabled", "with problems", "with an update", "ignored", "all"} {
		pressRun(m, "f")
		check("Skills, filter " + name)
	}
	pressRun(m, "g")
	check("the agent filter")
	pressRun(m, "esc")
	pressRun(m, "d")
	pressRun(m, "s")
	check("Skills, after an operation")
	pressRun(m, "z")
	check("the undo z opens")
	pressRun(m, "n")
	pressRun(m, "/")
	pressRun(m, "esc")

	pressRun(m, "?")
	checkKeys(t, "the help", screen(m), true)
	pressRun(m, "?")

	for _, s := range []struct{ key, name string }{{"h", "Historial"}, {"c", "Ajustes"}, {"3", "Instalar"}, {"4", "Salud"}} {
		pressRun(m, s.key)
		check(s.name)
	}
	pressRun(m, "c")
	pressRun(m, "enter")
	check("Ajustes, editing the interval")
	pressRun(m, "esc")

	pressRun(m, "3")
	pressRun(m, "enter")
	check("Instalar, step 1")
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	check("Instalar, step 2")
	pressRun(m, "enter")
	check("Instalar, step 3")
	pressRun(m, "enter")
	check("Instalar, step 4")
	pressRun(m, "n")

	pressRun(m, "4")
	pressRun(m, "enter")
	check("Doctor, the repairs")
	pressRun(m, "esc")

	u := openSized(t, setupGoldenFixture(t), 220, 70)
	checkU := func(name string) { t.Helper(); checkKeys(t, name, screen(u), false) }
	pressRun(u, "2")
	checkU("Actualizaciones")
	for range 3 {
		pressRun(u, "down")
		checkU("Actualizaciones, another row")
	}
	pressRun(u, "home")
	pressRun(u, "down")
	pressRun(u, "enter")
	checkU("the diff")
	pressRun(u, "a")
	checkU("the update confirmation")
	pressRun(u, "n")
	pressRun(u, "t")
	checkU("the confirmation of applying all")
	pressRun(u, "v")
	checkU("the confirmation of applying all, with its detail")
	pressRun(u, "n")

	r := newMachine(t)
	ra := r.newApp()
	r.pendingV2()
	checkKeys(t, "the recover screen", screen(open(t, ra)), false)
}

// A capital letter does what its lower case does, and is never shown; the
// text typed in a filter or a prompt keeps its capitals.
func TestACapitalLetterDoesWhatItsLowerCaseDoes(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := open(t, a)
	selectSkill(t, m, "agent-browser")
	before := mc.hash()
	pressRun(m, "D")
	if got := screen(m); !strings.Contains(got, "¿Desactivar agent-browser?") {
		t.Fatalf("D did not plan the disable, as d does:\n%s", got)
	}
	pressRun(m, "N")
	if got := screen(m); strings.Contains(got, "¿Desactivar agent-browser?") {
		t.Fatalf("N did not answer no, as n does:\n%s", got)
	}
	if mc.hash() != before {
		t.Fatal("answering N wrote something")
	}

	pressRun(m, "/")
	pressRun(m, "esc")
	pressRun(m, "/")
	typeText(m, "AGENT")
	if got := screen(m); !strings.Contains(got, "Filtro: AGENT") {
		t.Errorf("the filter lost its capitals:\n%s", got)
	}
	pressRun(m, "esc")

	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	ua := f.newApp()
	if _, err := ua.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	u := openSized(t, ua, 100, 30)
	pressRun(u, "2")
	pressRun(u, "O")
	if cands, err := ua.Candidates(); err != nil || len(cands) != 0 {
		t.Fatalf("O did not skip the version, as o does: %+v, %v", cands, err)
	}
}

// The lists no longer move half a page with d and u: on Actualizaciones
// they do nothing, and the selected row stays.
func TestTheListsDoNotMoveHalfAPageWithDOrU(t *testing.T) {
	u := openSized(t, setupGoldenFixture(t), 100, 30)
	pressRun(u, "2")
	before := screen(u)
	for _, k := range []string{"d", "u", "D", "U"} {
		if cmd := press(u, k); cmd != nil {
			t.Errorf("%s on Actualizaciones returned a command", k)
		}
		if got := screen(u); got != before {
			t.Errorf("%s changed Actualizaciones:\n%s", k, got)
		}
	}
	pressRun(u, "pgdown")
	if screen(u) == before {
		t.Error("PgDn no longer moves the list")
	}
}

// Actualizaciones: a applies the selected update after its confirmation,
// t applies them all after theirs, o skips this version, and s no longer
// skips anything.
func TestTheUpdateKeys(t *testing.T) {
	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v2\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m := openSized(t, a, 100, 30)
	pressRun(m, "2")
	if cmd := press(m, "s"); cmd != nil {
		t.Fatal("s on Actualizaciones still does something")
	}
	if c, err := a.CandidateFor("lock:alpha"); err != nil || c.State != "pending" {
		t.Fatalf("after s the update is not pending: %+v, %v", c, err)
	}
	pressRun(m, "t")
	if got := screen(m); !strings.Contains(got, "¿Aplicar todas las actualizaciones?") || !strings.Contains(got, text.TUIConfirmKeys) {
		t.Fatalf("t did not ask to apply them all:\n%s", got)
	}
	pressRun(m, "n")
	pressRun(m, "a")
	if got := screen(m); !strings.Contains(got, "¿Actualizar alpha?") {
		t.Fatalf("a did not ask to apply this one:\n%s", got)
	}
	pressRun(m, "n")
	pressRun(m, "o")
	if got := screen(m); !strings.Contains(got, "Actualización omitida.") {
		t.Errorf("o does not tell the skip:\n%s", got)
	}
	if cands, err := a.Candidates(); err != nil || len(cands) != 0 {
		t.Fatalf("o did not skip the version: %+v, %v", cands, err)
	}
}

// Each update row has its risk traffic light (UX3): ● low for only text,
// ▲ medium for scripts, binaries or the user's own edit, ◆ review for
// execution permissions or suspicious text; a blocked one says so.
func TestEachUpdateRowShowsItsRisk(t *testing.T) {
	m := openSized(t, setupGoldenFixture(t), 100, 30)
	pressRun(m, "2")
	got := screen(m)
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`● myclone +bloqueada`),
		regexp.MustCompile(`● alpha +bajo`),
		regexp.MustCompile(`▲ gamma +medio`),
	} {
		if !want.MatchString(got) {
			t.Errorf("the list lacks %v:\n%s", want, got)
		}
	}

	f := newFixture(t)
	f.setupSkillWithUpdate("alpha", "# Alpha v1\n", "# Alpha v1\nVisit https://example.com/install now.\n")
	a := f.newApp()
	if _, err := a.Check(t.Context(), app.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m = openSized(t, a, 100, 30)
	pressRun(m, "2")
	if got := screen(m); !regexp.MustCompile(`◆ alpha +revisar`).MatchString(got) {
		t.Errorf("a suspicious update lacks ◆:\n%s", got)
	}
}

// The panel tells in plain words what changes: the files, the labels in
// words and from which version to which, with the actions and their keys.
// A blocked update tells why and offers no apply.
func TestTheUpdatePanelTellsWhatChanges(t *testing.T) {
	a := setupGoldenFixture(t)
	cand, err := a.CandidateFor("lock:alpha")
	if err != nil {
		t.Fatal(err)
	}
	m := openSized(t, a, 100, 40)
	pressRun(m, "2")
	pressRun(m, "down")
	got := screen(m)
	for _, want := range []string{
		"cambia solo texto",
		fmt.Sprintf("De la versión %s a la %s.", tui.ShortSHA(cand.FromSHA), tui.ShortSHA(cand.ToCommit)),
		"SKILL.md",
		"[enter] Ver cambios", "[a] Aplicar esta", "[o] Omitir esta versión", "[t] Aplicar todas",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel of alpha lacks %q:\n%s", want, got)
		}
	}

	pressRun(m, "up")
	got = screen(m)
	if !strings.Contains(got, "No se puede aplicar:") || strings.Contains(got, "[a] Aplicar esta") {
		t.Errorf("the panel of a blocked update does not tell why, or offers to apply it:\n%s", got)
	}
}

// Each Historial line reads in plain words; the identifiers and snapshots
// are only in the detail panel.
func TestEachHistoryLineReadsInPlainWords(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
	snap := mc.v2Lines()[0].SnapshotID

	m := openSized(t, a, 100, 40)
	pressRun(m, "h")
	got := screen(m)
	want := "Desactivaste agent-browser — hace un momento — se puede deshacer [u]"
	if !strings.Contains(got, want) {
		t.Fatalf("Historial lacks %q:\n%s", want, got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "Desactivaste") && (strings.Contains(l, snap) || strings.Contains(l, "v2:")) {
			t.Errorf("the line names an identifier: %q", l)
		}
	}
	if !strings.Contains(got, snap) || !strings.Contains(got, "[u] Deshacer") {
		t.Errorf("the detail panel lacks the snapshot or the undo key:\n%s", got)
	}

	mc.clock.Advance(2 * time.Hour)
	m = openSized(t, mc.newApp(), 100, 40)
	pressRun(m, "h")
	if got := screen(m); !strings.Contains(got, "Desactivaste agent-browser — hace 2 horas") {
		t.Errorf("two hours later the line does not say so:\n%s", got)
	}

	u := openSized(t, setupGoldenFixture(t), 100, 40)
	pressRun(u, "h")
	if got := screen(u); !strings.Contains(got, "Actualizaste alpha — ") {
		t.Errorf("a version 1 update is not told in plain words:\n%s", got)
	}
}

// Instalar explains its four steps, and each step says which it is.
func TestInstallExplainsEachStep(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	m := openSized(t, mc.newApp(), 120, 60)
	pressRun(m, "3")
	got := screen(m)
	for _, want := range []string{"(1) De dónde la traigo", "(2) Revisa lo que trae", "(3) Elige los agentes", "(4) Confirma"} {
		if !strings.Contains(got, want) {
			t.Errorf("Instalar lacks %q:\n%s", want, got)
		}
	}
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Paso 1 de 4") {
		t.Errorf("the source prompt does not say its step:\n%s", got)
	}
	typeText(m, "acme/tools")
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Paso 2 de 4") || !strings.Contains(got, "Lo que trae: ") {
		t.Errorf("the review does not say its step or what it brings:\n%s", got)
	}
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Paso 3 de 4") {
		t.Errorf("the targets prompt does not say its step:\n%s", got)
	}
	pressRun(m, "enter")
	if got := screen(m); !strings.Contains(got, "Paso 4 de 4") || !strings.Contains(got, "¿Instalar tidy?") {
		t.Errorf("the plan does not say its step:\n%s", got)
	}
}

// Doctor groups its findings, by skill since INV10 (health_groups_test.go):
// the first row's panel tells its finding in a plain line; gentle-ai's
// copies are one folded line; the repairs are told in plain words.
func TestDoctorGroupsItsFindings(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, f := range findings {
		count[f.Kind]++
	}
	if count["broken_link"] < 2 || count["managed_by_gentle_ai"] < 2 {
		t.Fatalf("the fixture lacks findings to group: %v", count)
	}
	first := healthRowOrder(t, a)[0]
	if first.Kind != "broken_link" {
		t.Fatalf("the first row is %s, want a broken link", first.ID)
	}
	m := openSized(t, a, 220, 80)
	pressRun(m, "4")
	got := screen(m)
	for _, want := range []string{
		"1 enlace roto: apunta a una skill que ya no existe. Arreglo sugerido: borrar el enlace.",
		fmt.Sprintf("%d skills de gentle-ai: las gestiona gentle-ai, skillhub no las toca.", count["managed_by_gentle_ai"]),
		"Hallazgo: " + first.ID,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Doctor lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, text.FindingKind("managed_by_gentle_ai")) {
		t.Errorf("gentle-ai's copies are listed one by one:\n%s", got)
	}
	offer, err := a.Repairs(t.Context(), first.ID)
	if err != nil || len(offer.Repairs) == 0 {
		t.Fatalf("the first broken link offers no repair: %+v, %v", offer, err)
	}
	pressRun(m, "enter")
	got = screen(m)
	if !strings.Contains(got, text.TUIRepairPlain(offer.Repairs[0])) || strings.Contains(got, offer.Repairs[0]) {
		t.Errorf("the repairs of %s are not told in plain words:\n%s", first.ID, got)
	}
}

// Each setting has a line that tells what it does.
func TestEachSettingTellsWhatItDoes(t *testing.T) {
	m := openSized(t, newMachine(t).newApp(), 100, 40)
	pressRun(m, "c")
	got := screen(m)
	for _, want := range []string{
		text.TUISettingInterval, text.TUISettingNotifications, text.TUISettingBackground,
		"oculta la skill de skillhub; tus agentes la siguen usando",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Ajustes lacks %q:\n%s", want, got)
		}
	}
}

// A refusal the TUI shares with the CLI says agente, never harness.
func TestTheTUINeverSaysHarness(t *testing.T) {
	mc := newMachine(t)
	m := open(t, mc.newApp())
	selectSkill(t, m, "agent-browser")
	// The agents are chosen with check boxes (UX3): no id is typed, and
	// neither the boxes nor the plan say harness.
	pressRun(m, "l")
	got := screen(m)
	pressRun(m, " ")
	pressRun(m, "enter")
	got += screen(m)
	if !m.InPlanForTest() {
		t.Fatalf("no plan after the boxes:\n%s", got)
	}
	if regexp.MustCompile(`(?i)harness`).MatchString(got) {
		t.Errorf("the TUI says harness:\n%s", got)
	}
	if msg, _ := text.TUIRefusal(&app.RefusalError{Kind: app.ErrUnknownHarness}, app.Request{Harness: "nope"}); msg != "«nope» no es un agente conocido" {
		t.Errorf("the refusal is not told: %q", msg)
	}
}

var (
	snapshotID  = regexp.MustCompile(`op-[0-9T.]+Z-[0-9a-f]+`)
	snapshotSHA = regexp.MustCompile(`snapshot_sha=\S+`)
)

// stable puts a screen with a history line in a golden file: its snapshot
// identifier has a random suffix and its hash covers temporary paths.
func stable(mc *machine, s string) []byte {
	s = snapshotSHA.ReplaceAllString(snapshotID.ReplaceAllString(s, "<snapshot>"), "snapshot_sha=<sha>")
	return mc.normalize(s)
}

// The golden files of the screens of UX2, at 80 and 100 columns. The
// screens that only read share one model per width, so that the machine is
// observed once (UX3: what each skill admits costs about a second a load).
func TestGoldenUX2Screens(t *testing.T) {
	for _, width := range []int{80, 100} {
		u := openSized(t, setupGoldenFixture(t), width, 30)
		t.Run(fmt.Sprintf("Updates%d", width), func(t *testing.T) {
			pressRun(u, "2")
			pressRun(u, "down")
			teatest.RequireEqualOutput(t, []byte(screen(u)))
		})
		t.Run(fmt.Sprintf("ApplyAll%d", width), func(t *testing.T) {
			pressRun(u, "t")
			teatest.RequireEqualOutput(t, []byte(screen(u)))
			pressRun(u, "n")
		})

		mc := newMachine(t)
		a := mc.newApp()
		mc.approve(a, app.Request{Action: app.ActionDisable, Key: "agent-browser"})
		m := openSized(t, a, width, 30)
		for _, sc := range []struct {
			name string
			keys []string
		}{
			{"History", []string{"h"}},
			{"Settings", []string{"esc", "c"}},
			{"Install", []string{"esc", "3"}},
			{"Doctor", []string{"4"}},
			{"DoctorRepairs", []string{"enter"}},
		} {
			t.Run(fmt.Sprintf("%s%d", sc.name, width), func(t *testing.T) {
				for _, k := range sc.keys {
					pressRun(m, k)
				}
				teatest.RequireEqualOutput(t, stable(mc, screen(m)))
			})
		}
		pressRun(m, "esc")
		t.Run(fmt.Sprintf("Help%d", width), func(t *testing.T) {
			h := openSized(t, a, width, 40)
			pressRun(h, "?")
			teatest.RequireEqualOutput(t, stable(mc, screen(h)))
		})
		t.Run(fmt.Sprintf("Recover%d", width), func(t *testing.T) {
			r := newMachine(t)
			ra := r.newApp()
			r.pendingV2()
			teatest.RequireEqualOutput(t, r.normalize(screen(openSized(t, ra, width, 30))))
		})
	}
}
