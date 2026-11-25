package tui_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/tui"
)

// UX4 (S13, L90): the skills the user ignored can be ignored no more from
// the TUI: Ajustes lists them as check boxes, the Skills filter f has an
// «ignoradas» state where x stops ignoring a skill, and the summary counts
// them. Ignoring or not only ever changes settings.toml.

// ignore writes the ignore setting as «skillhub settings set ignore» does.
func (m *machine) ignore(a *app.App, list string) {
	m.t.Helper()
	if _, err := a.SetSetting(app.KeyIgnore, list, false); err != nil {
		m.t.Fatal(err)
	}
}

// ignored is the ignore setting as saved.
func ignored(t *testing.T, a *app.App) []string {
	t.Helper()
	s, err := a.Settings()
	if err != nil {
		t.Fatal(err)
	}
	return s.Ignore
}

// skillsUntouched fails unless the only file a step changed is
// settings.toml: put back as it was, every hash matches the one before.
func (m *machine) skillsUntouched(before string, settings []byte) {
	m.t.Helper()
	if err := os.WriteFile(m.p.SettingsFile(), settings, 0o644); err != nil {
		m.t.Fatal(err)
	}
	if m.hash() != before {
		m.t.Fatal("stopping to ignore wrote something besides settings.toml")
	}
}

// readSettings is settings.toml as saved now.
func (m *machine) readSettings() []byte {
	m.t.Helper()
	b, err := os.ReadFile(m.p.SettingsFile())
	if err != nil {
		m.t.Fatal(err)
	}
	return b
}

// openIgnored opens Ajustes with the cursor on the «Ignoradas» row.
func openIgnored(m *tui.Model) {
	pressRun(m, "c")
	pressRun(m, "down")
	pressRun(m, "down")
}

// In Ajustes the «Ignoradas» row is selected with the arrows; enter opens
// its check boxes, every ignored skill marked. Unmarking one and confirming
// stops ignoring it, through settings.toml only, and the status line tells
// it in plain words. The skill comes back to Skills.
func TestSettingsChecksStopIgnoringASkill(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs,domain-driven-design")
	m := openSized(t, a, 100, 30)
	if strings.Contains(screen(m), "grill-with-docs") {
		t.Fatalf("Skills shows an ignored skill:\n%s", screen(m))
	}
	openIgnored(m)
	if !strings.Contains(screen(m), "> Ignoradas:") {
		t.Fatalf("the cursor does not reach Ignoradas:\n%s", screen(m))
	}
	pressRun(m, "enter")
	got := screen(m)
	for _, want := range []string{"> [x] grill-with-docs", "  [x] domain-driven-design", "espacio marcar"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the check boxes lack %q:\n%s", want, got)
		}
	}
	before, saved := mc.hash(), mc.readSettings()
	pressRun(m, "space")
	if !strings.Contains(screen(m), "[ ] grill-with-docs") {
		t.Fatalf("space did not unmark grill-with-docs:\n%s", screen(m))
	}
	pressRun(m, "enter")
	if got := ignored(t, a); !slices.Equal(got, []string{"domain-driven-design"}) {
		t.Errorf("ignore = %v, want [domain-driven-design]", got)
	}
	if want := "Ya no se ignora grill-with-docs: vuelve a aparecer en Skills."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
	if !strings.Contains(screen(m), "Ignoradas:              domain-driven-design") {
		t.Errorf("Ajustes still names grill-with-docs:\n%s", screen(m))
	}
	mc.skillsUntouched(before, saved)
	pressRun(m, "1")
	selectSkill(t, m, "grill-with-docs")
}

// Every box can be unmarked at once: the status names them all.
func TestSettingsChecksStopIgnoringSeveral(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs,find-skills")
	m := openSized(t, a, 100, 30)
	openIgnored(m)
	pressRun(m, "enter")
	pressRun(m, "space")
	pressRun(m, "down")
	pressRun(m, "space")
	pressRun(m, "enter")
	if got := ignored(t, a); len(got) != 0 {
		t.Errorf("ignore = %v, want empty", got)
	}
	if want := "Ya no se ignoran grill-with-docs y find-skills: vuelven a aparecer en Skills."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
}

// esc closes the boxes and enter with every box marked saves nothing.
func TestSettingsChecksWithoutChangesSaveNothing(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs")
	m := openSized(t, a, 100, 30)
	openIgnored(m)
	before := mc.hash()
	saved := mc.readSettings()

	pressRun(m, "enter")
	pressRun(m, "space")
	pressRun(m, "esc")
	if strings.Contains(screen(m), "[ ]") || !strings.Contains(screen(m), "> Ignoradas:") {
		t.Errorf("esc did not go back to Ajustes:\n%s", screen(m))
	}
	pressRun(m, "enter")
	pressRun(m, "enter")
	if want := "No desmarcaste ninguna; no se hizo nada."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
	if string(mc.readSettings()) != string(saved) || mc.hash() != before {
		t.Error("closing the boxes without a change wrote something")
	}
}

// With nothing ignored the row says «ninguna», and enter tells how to
// ignore a skill instead of opening empty boxes.
func TestSettingsWithNothingIgnoredSaysSo(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := openSized(t, a, 100, 30)
	before := mc.hash()
	openIgnored(m)
	if !strings.Contains(screen(m), "> Ignoradas:              ninguna") {
		t.Fatalf("the row does not say ninguna:\n%s", screen(m))
	}
	pressRun(m, "enter")
	if want := "No ignoras ninguna skill. Para ignorar una, selecciónala en Skills y pulsa x."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
	if strings.Contains(screen(m), "¿Qué skills sigues ignorando?") || !strings.Contains(screen(m), "> Ignoradas:") ||
		mc.hash() != before {
		t.Errorf("enter on no ignored skill opened boxes or wrote:\n%s", screen(m))
	}
}

// An entry that names no skill now, such as an old name, is listed too, so
// that it can be removed.
func TestSettingsListsAnEntryThatNamesNoSkill(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "old-name,grill-with-docs")
	m := openSized(t, a, 100, 30)
	openIgnored(m)
	pressRun(m, "enter")
	if !strings.Contains(screen(m), "> [x] old-name (ninguna skill tiene ese nombre)") {
		t.Fatalf("the boxes lack old-name:\n%s", screen(m))
	}
	pressRun(m, "space")
	pressRun(m, "enter")
	if got := ignored(t, a); !slices.Equal(got, []string{"grill-with-docs"}) {
		t.Errorf("ignore = %v, want [grill-with-docs]", got)
	}
	if want := "Se quitó old-name de la lista de ignoradas."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
}

// A skill named by two entries (its name and its id) is one box: unmarking
// it removes both, so the skill really comes back.
func TestSettingsOneBoxPerSkill(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs,lock:grill-with-docs")
	m := openSized(t, a, 100, 30)
	openIgnored(m)
	pressRun(m, "enter")
	if n := strings.Count(screen(m), "grill-with-docs"); n != 1 {
		t.Fatalf("grill-with-docs has %d boxes, want 1:\n%s", n, screen(m))
	}
	pressRun(m, "space")
	pressRun(m, "enter")
	if got := ignored(t, a); len(got) != 0 {
		t.Errorf("ignore = %v, want empty", got)
	}
}

// f has an «ignoradas» state: it shows only the ignored skills, marked
// «ignorada», and x on one of them stops ignoring it, through settings.toml
// only. The footer, the panel and the help say what x does on each row.
func TestSkillsIgnoredFilterAndXStopsIgnoring(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := openSized(t, a, 100, 30)
	if strings.Contains(summaryOf(screen(m)), "ignoradas") {
		t.Errorf("the summary counts ignored skills when there are none:\n%s", summaryOf(screen(m)))
	}
	if got := screen(m); !strings.Contains(got, "x ignorar") || !strings.Contains(got, "[x] Ignorar") {
		t.Errorf("a skill's footer or panel lacks x ignorar:\n%s", got)
	}
	mc.ignore(a, "grill-with-docs,domain-driven-design")
	m = openSized(t, a, 100, 30)
	total := m.RowCountForTest()
	if sum := summaryOf(screen(m)); !strings.Contains(sum, "· 2 ignoradas") {
		t.Errorf("the summary does not count the ignored skills: %q", sum)
	}
	for range 5 {
		pressRun(m, "f")
	}
	got := screen(m)
	if !strings.Contains(got, "Skills · ignoradas (2)") || m.RowCountForTest() != 2 {
		t.Fatalf("the ignored filter does not show the two skills:\n%s", got)
	}
	for _, want := range []string{"grill-with-docs", "domain-driven-design", "ignorada", "x dejar de ignorar", "[x] Dejar de ignorar"} {
		if !strings.Contains(got, want) {
			t.Errorf("the ignored filter lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "x ignorar") {
		t.Errorf("an ignored row offers x ignorar:\n%s", got)
	}
	if id := m.SelectedIDForTest(); id != "lock:domain-driven-design" && id != "lock:grill-with-docs" {
		t.Fatalf("selected %q", id)
	}
	name := strings.TrimPrefix(m.SelectedIDForTest(), "lock:")
	other := "grill-with-docs"
	if name == other {
		other = "domain-driven-design"
	}
	before, saved := mc.hash(), mc.readSettings()
	pressRun(m, "x")
	if got := ignored(t, a); !slices.Equal(got, []string{other}) {
		t.Errorf("ignore = %v, want [%s]", got, other)
	}
	if want := fmt.Sprintf("Ya no se ignora %s: vuelve a aparecer en Skills.", name); m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
	if m.RowCountForTest() != 1 {
		t.Errorf("the ignored filter shows %d rows, want 1", m.RowCountForTest())
	}
	mc.skillsUntouched(before, saved)
	pressRun(m, "f")
	if m.RowCountForTest() != total+1 {
		t.Errorf("Skills shows %d rows, want %d", m.RowCountForTest(), total+1)
	}
	pressRun(m, "?")
	if !strings.Contains(flat(screen(m)), "x ignorar o dejar de ignorar") {
		t.Errorf("the help does not tell x both ways:\n%s", screen(m))
	}
}

// On an ignored row, non-removal write keys require x first; e still opens
// reversible removal confirmation while keeping the ignore preference.
func TestAnIgnoredRowOffersOnlyX(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs")
	m := openSized(t, a, 100, 30)
	for range 5 {
		pressRun(m, "f")
	}
	before := mc.hash()
	for _, k := range []string{"d", "l", "o", "p"} {
		pressRun(m, k)
		if m.InPlanForTest() || mc.hash() != before {
			t.Fatalf("%s on an ignored row planned or wrote:\n%s", k, screen(m))
		}
		if want := "grill-with-docs está ignorada: pulsa x para dejar de ignorarla."; m.StatusForTest() != want {
			t.Errorf("%s: status = %q, want %q", k, m.StatusForTest(), want)
		}
	}
	pressRun(m, "e")
	if !m.InPlanForTest() {
		t.Fatalf("e on an ignored row did not open removal confirmation; status %q", m.StatusForTest())
	}
	pressRun(m, "n")
	if mc.hash() != before {
		t.Fatal("cancelling removal on an ignored row wrote something")
	}
	pressRun(m, "enter")
	if !strings.Contains(screen(m), "Skill: grill-with-docs") {
		t.Errorf("enter does not show the ignored skill:\n%s", screen(m))
	}
}

// The golden files of UX4, at 80 and 100 columns: Ajustes with the cursor
// on Ignoradas, its check boxes with an entry that names no skill, and the
// Skills list with the «ignoradas» filter.
func TestGoldenUX4(t *testing.T) {
	for _, width := range []int{80, 100} {
		mc := newMachine(t)
		a := mc.newApp()
		mc.ignore(a, "grill-with-docs,domain-driven-design,old-name")
		m := openSized(t, a, width, 30)
		shot := func(name string) {
			t.Run(fmt.Sprintf("%s%d", name, width), func(t *testing.T) {
				teatest.RequireEqualOutput(t, stable(mc, screen(m)))
			})
		}
		openIgnored(m)
		shot("Settings")
		pressRun(m, "enter")
		shot("SettingsChecks")
		pressRun(m, "esc")
		pressRun(m, "1")
		for range 5 {
			pressRun(m, "f")
		}
		shot("SkillsIgnored")
	}
}

// A kept entry with a comma, written by hand in settings.toml, would be
// split when the list is saved: stopping to ignore another one is refused
// and nothing is written.
func TestSettingsRefusesKeptComma(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.ignore(a, "grill-with-docs")
	saved := strings.Replace(string(mc.readSettings()), `"grill-with-docs"`, `"grill-with-docs", "a,b"`, 1)
	if err := os.WriteFile(mc.p.SettingsFile(), []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ignored(t, a); !slices.Equal(got, []string{"grill-with-docs", "a,b"}) {
		t.Fatalf("the fixture's ignore = %v", got)
	}
	m := openSized(t, a, 100, 30)
	openIgnored(m)
	pressRun(m, "enter")
	pressRun(m, "space")
	pressRun(m, "enter")
	if want := "Una de las que sigues ignorando tiene una coma en el nombre; cambia la lista en settings.toml."; m.StatusForTest() != want {
		t.Errorf("status = %q, want %q", m.StatusForTest(), want)
	}
	if string(mc.readSettings()) != saved {
		t.Error("the list with a comma was rewritten")
	}
}
