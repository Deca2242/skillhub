package tui_test

import (
	"cmp"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
)

// INV10 (S15 #9, A31): Salud groups Doctor's findings first by the skill they
// affect. Each skill is one line, listed once, and the group the cursor is in
// is open with its findings under it; a finding that names several skills is
// listed under each, and its repair stays one plan; the findings that name no
// skill keep a group of their kind, and gentle-ai's copies their folded line.
// Choosing a repair shows the same plan, identifier and confirmation as before.

// healthRow is a row of the Salud list the cursor stops at, as the oracle
// expects it: the finding, the skill it is listed under ("" for none), the
// line of its group and the words of its row.
type healthRow struct {
	finding app.Finding
	skill   string
	group   string
	words   string
}

// healthRowOrder are Doctor's findings in the order the cursor walks the
// Salud list, one per row it stops at: findingIndex walks the list with it.
func healthRowOrder(t *testing.T, a *app.App) []app.Finding {
	t.Helper()
	rows := healthOracle(t, a)
	out := make([]app.Finding, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.finding)
	}
	return out
}

// healthOracle builds the rows of the Salud list from the app, never from the
// TUI, so that it checks the list:
//   - gentle-ai's copies are left out: Salud folds them in one line;
//   - a finding is listed under each skill it names: its Skill, its Skills
//     (A31), and each skill one of whose broken links it is (the BrokenLinks
//     of SkillActions); one that names no skill stays in a group of its kind;
//   - the groups go by the rank, in text.TUIFindingOrder, of their most
//     urgent finding; at the same rank, a group without skill first, then the
//     skills of the inventory, then the others; then by name and by id;
//   - in a group, the findings go by kind in that order, then as Doctor
//     lists them;
//   - a skill is named as the inventory names it, else as its findings do,
//     else by its folder; when two groups share a name, one whose id is a
//     folder (dir: or git:) adds the folder that holds it, or its own folder
//     when that one is shared too.
func healthOracle(t *testing.T, a *app.App) []healthRow {
	t.Helper()
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actions, err := a.SkillActions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := a.Skills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]string{}
	for _, s := range inventory {
		known[s.ID] = s.Name
	}
	rank := func(kind string) int {
		if i := slices.Index(text.TUIFindingOrder, kind); i >= 0 {
			return i
		}
		return len(text.TUIFindingOrder)
	}
	type group struct {
		skill, name, line string
		rank, class       int
		findings          []app.Finding
	}
	byKey := map[string]*group{}
	var groups []*group
	get := func(key string, g group) *group {
		if byKey[key] == nil {
			g.rank = len(text.TUIFindingOrder) + 1
			byKey[key] = &g
			groups = append(groups, &g)
		}
		return byKey[key]
	}
	for _, f := range findings {
		if f.Kind == text.TUIGentleKind {
			continue
		}
		var skills []string
		if f.Skill != "" {
			skills = append(skills, f.Skill)
		}
		skills = append(skills, f.Skills...)
		for id, acts := range actions {
			for _, l := range acts.BrokenLinks {
				if l.Finding.ID == f.ID {
					skills = append(skills, id)
				}
			}
		}
		slices.Sort(skills)
		skills = slices.Compact(skills)
		if len(skills) == 0 {
			g := get("kind:"+f.Kind, group{name: f.Kind, line: text.TUIHealthNoSkill(f.Kind)})
			g.rank = min(g.rank, rank(f.Kind))
			g.findings = append(g.findings, f)
			continue
		}
		for _, id := range skills {
			class := 2
			if _, ok := known[id]; ok {
				class = 1
			}
			g := get("skill:"+id, group{skill: id, class: class})
			g.rank = min(g.rank, rank(f.Kind))
			g.findings = append(g.findings, f)
			if g.name == "" && (f.Skill == id || slices.Contains(f.Skills, id)) {
				g.name = f.Name
			}
		}
	}
	names := map[string]int{}
	for _, g := range groups {
		if g.skill == "" {
			continue
		}
		_, rel, _ := strings.Cut(g.skill, ":")
		switch {
		case known[g.skill] != "":
			g.name = known[g.skill]
		case g.name == "":
			g.name = path.Base(rel)
		}
		g.line = g.name
		names[g.name]++
	}
	folder := func(id string) (string, bool) {
		kind, rel, _ := strings.Cut(id, ":")
		return rel, kind == "dir" || kind == "git"
	}
	lines := map[string]int{}
	for _, g := range groups {
		if rel, ok := folder(g.skill); ok && names[g.name] > 1 {
			g.line = text.TUIHealthAt(g.name, "~/"+path.Dir(rel))
		}
		lines[g.line]++
	}
	for _, g := range groups {
		if rel, ok := folder(g.skill); ok && names[g.name] > 1 && lines[g.line] > 1 {
			g.line = text.TUIHealthAt(g.name, "~/"+rel)
		}
	}
	slices.SortFunc(groups, func(x, y *group) int {
		return cmp.Or(cmp.Compare(x.rank, y.rank), cmp.Compare(x.class, y.class),
			strings.Compare(x.name, y.name), strings.Compare(x.skill, y.skill))
	})
	var rows []healthRow
	for _, g := range groups {
		slices.SortStableFunc(g.findings, func(x, y app.Finding) int { return cmp.Compare(rank(x.Kind), rank(y.Kind)) })
		for _, f := range g.findings {
			words := text.TUIHealthRow(f.Kind)
			if g.skill == "" {
				// A finding without skill keeps the name it had: its own,
				// else its first path's folder, else its id.
				words = f.Name
				if words == "" && len(f.Paths) > 0 {
					words = path.Base(f.Paths[0])
				}
				words = cmp.Or(words, f.ID)
			}
			rows = append(rows, healthRow{finding: f, skill: g.skill, group: g.line, words: words})
		}
	}
	return rows
}

// healthFrames are the lines of each box of a screen, borders and padding
// out: at 80 columns, the Salud list and then its panel.
func healthFrames(s string) [][]string {
	var boxes [][]string
	var box []string
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "╭"):
			box = []string{}
		case strings.HasPrefix(l, "╰") && box != nil:
			for len(box) > 0 && box[len(box)-1] == "" {
				box = box[:len(box)-1]
			}
			boxes = append(boxes, box)
			box = nil
		case box != nil:
			l = strings.TrimSuffix(strings.TrimRight(l, " "), "│")
			box = append(box, strings.TrimSpace(strings.TrimPrefix(l, "│")))
		}
	}
	return boxes
}

// healthLines are the lines the Salud list should show with the cursor on
// row: every group once, by its line, and the cursor's open, with the words
// of each of its findings.
func healthLines(rows []healthRow, at int) []string {
	var lines []string
	for i, r := range rows {
		if i > 0 && rows[i-1].group == r.group && rows[i-1].skill == r.skill {
			continue
		}
		open := rows[at].group == r.group && rows[at].skill == r.skill
		if !open {
			lines = append(lines, text.TUIHealthClosed+" "+r.group)
			continue
		}
		lines = append(lines, text.TUIHealthOpen+" "+r.group)
		for _, o := range rows[i:] {
			if o.group != r.group || o.skill != r.skill {
				break
			}
			lines = append(lines, "· "+o.words)
		}
	}
	return lines
}

// skillRows counts the rows of a skill's group by the words of the row.
func skillRows(rows []healthRow, skill string) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		if r.skill == skill {
			out[r.words]++
		}
	}
	return out
}

// openUX3Health opens Salud on the machine of TestGoldenUX3, where the
// golden counts 26 problems, 36 findings and 19 broken links.
func openUX3Health(t *testing.T, width, height int) (*machine, *app.App, *tui.Model) {
	t.Helper()
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "archify"})
	m := openSized(t, a, width, height)
	pressRun(m, "4")
	return mc, a, m
}

// Each affected skill is one line of the list, listed once; walking the
// list, the group the cursor is in opens with its findings under it, and the
// panel is the finding under the cursor.
func TestHealthListsEachSkillOnceWithItsFindings(t *testing.T) {
	_, a, m := openUX3Health(t, 80, 200)
	rows := healthOracle(t, a)
	skills := map[string]bool{}
	findings := map[string]bool{}
	for _, r := range rows {
		if r.skill != "" {
			skills[r.skill] = true
		}
		findings[r.finding.ID] = true
	}
	if len(skills) != 21 || len(findings) != 36 || len(rows) != 45 {
		t.Fatalf("the machine has %d skills, %d findings and %d rows, want 21, 36 and 45", len(skills), len(findings), len(rows))
	}
	// The 19 broken links are under the four skills of OFF/ they lead to.
	links := map[string]int{}
	for link := range graphtest.BrokenLinks {
		links[path.Base(link)]++
	}
	for _, name := range graphtest.OffSkills {
		got := skillRows(rows, "lock:"+name)
		want := map[string]int{"enlace roto": links[name], "apagada pero visible": 1, "nombre repetido": 1}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("the rows of %s = %v, want %v", name, got, want)
		}
	}

	for i, r := range rows {
		if i > 0 {
			pressRun(m, "down")
		}
		boxes := healthFrames(screen(m))
		if len(boxes) < 2 {
			t.Fatalf("row %d: no list and panel:\n%s", i, screen(m))
		}
		list, panel := boxes[0], strings.Join(boxes[1], "\n")
		if list[0] != text.TUIHealthTitle(21, 36) {
			t.Errorf("row %d: the title is %q", i, list[0])
		}
		want := healthLines(rows, i)
		if len(list)-1 != len(want) {
			t.Fatalf("row %d (%s): %d lines, want %d:\n%s", i, r.finding.ID, len(list)-1, len(want), strings.Join(list, "\n"))
		}
		for j, w := range want {
			if !strings.HasPrefix(list[j+1], w) {
				t.Fatalf("row %d (%s): line %d is %q, want %q:\n%s", i, r.finding.ID, j+1, list[j+1], w, strings.Join(list, "\n"))
			}
		}
		if !strings.Contains(panel, "Hallazgo: "+r.finding.ID) {
			t.Fatalf("row %d: the panel is not %s:\n%s", i, r.finding.ID, panel)
		}
		if r.skill != "" && !strings.HasPrefix(panel, r.group) {
			t.Errorf("row %d: the panel does not name its skill %q:\n%s", i, r.group, panel)
		}
		if one := text.TUIHealthFindings(r.finding.Kind, 1); r.skill != "" && !strings.Contains(flat(panel), flat(one)) {
			t.Errorf("row %d: the panel does not tell its one finding as %q:\n%s", i, one, panel)
		}
	}
}

// A broken link's row names the agent whose folder holds it, from the
// skill's broken links (LinkFix), and so does the panel.
func TestABrokenLinkRowNamesItsAgent(t *testing.T) {
	_, a, m := openUX3Health(t, 80, 200)
	acts, err := a.SkillActions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fixes := acts["lock:accessibility"].BrokenLinks
	if len(fixes) != 6 {
		t.Fatalf("accessibility has %d broken links, want 6", len(fixes))
	}
	list := healthFrames(screen(m))[0]
	for i, fix := range fixes {
		var names []string
		for _, h := range fix.Harnesses {
			names = append(names, a.AgentName(h))
		}
		agents := text.JoinNames(names)
		if line := list[2+i]; agents == "" || !strings.HasPrefix(line, "· "+text.TUIHealthRow("broken_link")) || !strings.HasSuffix(line, agents) {
			t.Errorf("the row of %s is %q, want its agent %q", fix.Finding.ID, line, agents)
		}
	}
	if panel := strings.Join(healthFrames(screen(m))[1], " "); !strings.Contains(panel, text.TUIDoctorAgents+a.AgentName(fixes[0].Harnesses[0])) {
		t.Errorf("the panel does not name the agent of the link:\n%s", panel)
	}
}

// A finding that names two skills is listed under both (A31): the same
// finding, which tells where else it is, and the same answer from either.
func TestAFindingOfTwoSkillsIsListedUnderBoth(t *testing.T) {
	mc, a, m := openUX3Health(t, 80, 200)
	rows := healthOracle(t, a)
	const id = "homonyms:accessibility"
	var at []int
	for i, r := range rows {
		if r.finding.ID == id {
			at = append(at, i)
		}
	}
	if len(at) != 2 || rows[at[0]].skill != "lock:accessibility" || rows[at[1]].skill != "dir:.claude-omni/skills/accessibility" {
		t.Fatalf("%s is not under its two skills: rows %v", id, at)
	}
	other := map[int]string{at[0]: rows[at[1]].group, at[1]: rows[at[0]].group}
	before := mc.hash()
	pos := 0
	var answers []string
	for _, i := range at {
		for ; pos < i; pos++ {
			pressRun(m, "down")
		}
		boxes := healthFrames(screen(m))
		if len(boxes) < 2 {
			t.Fatalf("row %d: no list and panel:\n%s", i, screen(m))
		}
		if !slices.ContainsFunc(boxes[0], func(l string) bool { return strings.HasPrefix(l, text.TUIHealthOpen+" "+rows[i].group) }) {
			t.Errorf("row %d: %s is not open:\n%s", i, rows[i].group, strings.Join(boxes[0], "\n"))
		}
		panel := flat(strings.Join(boxes[1], "\n"))
		if !strings.Contains(panel, "Hallazgo: "+id) || !strings.Contains(panel, flat(text.TUIHealthAlsoIn([]string{other[i]}))) {
			t.Errorf("row %d: the panel does not say %s is also under %s:\n%s", i, id, other[i], panel)
		}
		pressRun(m, "enter")
		answers = append(answers, m.StatusForTest())
		if m.InPlanForTest() || !strings.Contains(m.StatusForTest(), "nunca une skills por su nombre") {
			t.Errorf("row %d: enter planned or did not explain: %q", i, m.StatusForTest())
		}
	}
	if answers[0] != answers[1] {
		t.Errorf("the two rows answer differently: %q and %q", answers[0], answers[1])
	}
	if mc.hash() != before {
		t.Fatal("reading a finding wrote something")
	}
}

// A repair is one plan whichever skill it is chosen from: adopting OFF/
// fixes findings of four skills, and from any of them it is the plan the
// app plans for that finding, with the same identifier. Approved once, after
// the reload none of those findings is left under any skill.
func TestARepairIsOnePlanWhicheverSkillItIsChosenFrom(t *testing.T) {
	// Tall enough that the plan's detail shows its identifier, after every
	// long temporary path of the fixture.
	mc, a, m := openUX3Health(t, 80, 600)
	rows := healthOracle(t, a)
	from := []string{"parked_but_visible:lock:accessibility", "broken_link:.kiro/skills/web-quality-audit"}
	before, journal := mc.hash(), len(mc.v2Lines())
	pos := 0
	var planID string
	for n, id := range from {
		i := slices.IndexFunc(rows, func(r healthRow) bool { return r.finding.ID == id })
		if i < pos {
			t.Fatalf("%s comes before the row already walked", id)
		}
		for ; pos < i; pos++ {
			pressRun(m, "down")
		}
		if panel := healthFrames(screen(m))[1]; !slices.Contains(panel, "Hallazgo: "+id) {
			t.Fatalf("the cursor is not on %s:\n%s", id, strings.Join(panel, "\n"))
		}
		pressRun(m, "enter")
		if got := screen(m); !strings.Contains(got, text.TUIRepairsQuestion) || !strings.Contains(got, text.TUIRepairPlain("adopt_off")) {
			t.Fatalf("%s does not offer adopting OFF/:\n%s", id, got)
		}
		pressRun(m, "enter")
		p, err := a.Plan(t.Context(), app.Request{Action: app.ActionRepair, Finding: id, Repair: "adopt_off"})
		if err != nil {
			t.Fatal(err)
		}
		pressRun(m, "v")
		if got := screen(m); !m.InPlanForTest() || !strings.Contains(squash(got), squash(text.TUIPlanID(p.PlanID))) {
			t.Fatalf("the plan from %s is not the app's %s:\n%s", id, p.PlanID, got)
		}
		if n == 0 {
			planID = p.PlanID
			pressRun(m, "n")
			if mc.hash() != before {
				t.Fatal("a plan not confirmed wrote something")
			}
			continue
		}
		if p.PlanID != planID {
			t.Errorf("the plan from %s is %s, from %s %s", id, p.PlanID, from[0], planID)
		}
		pressRun(m, "s")
	}
	if lines := mc.v2Lines(); len(lines) != journal+1 || lines[journal].Action != "applied" {
		t.Fatalf("the plan did not run exactly once: %+v", lines[journal:])
	}
	for _, r := range healthOracle(t, a) {
		if r.finding.Kind == "broken_link" || r.finding.Kind == "parked_but_visible" {
			t.Errorf("%s is still found under %q", r.finding.ID, r.skill)
		}
	}
	got := strings.Join(healthFrames(screen(m))[0], "\n")
	for _, words := range []string{text.TUIHealthRow("broken_link"), text.TUIHealthRow("parked_but_visible")} {
		if strings.Contains(got, "· "+words) {
			t.Errorf("the list still shows %q after the reload:\n%s", words, got)
		}
	}
}

// The findings that name no skill keep their place: a link to a skill that
// is nowhere stays in the group of its kind, before the skills of the same
// rank; gentle-ai's copies stay folded in one line.
func TestFindingsWithoutSkillKeepTheirPlace(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
	a := mc.newApp()
	const ghost = "broken_link:.claude/skills/ghost"
	if got := healthRowOrder(t, a)[0].ID; got != ghost {
		t.Fatalf("the first row is %s, want %s", got, ghost)
	}
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	gentle := 0
	for _, f := range findings {
		if f.Kind == text.TUIGentleKind {
			gentle++
		}
	}
	m := openSized(t, a, 80, 200)
	pressRun(m, "4")
	got := screen(m)
	if gentle == 0 || !strings.Contains(got, text.TUIGentleFolded(gentle)) {
		t.Errorf("gentle-ai's %d copies are not one folded line:\n%s", gentle, got)
	}
	boxes := healthFrames(got)
	list, panel := boxes[0], strings.Join(boxes[1], "\n")
	if want := []string{text.TUIHealthOpen + " " + text.TUIHealthNoSkill("broken_link"), "· ghost", text.TUIHealthClosed + " accessibility"}; !slices.Equal(firstWords(list[1:4]), want) {
		t.Errorf("the list starts with %q, want %q", list[1:4], want)
	}
	for _, want := range []string{text.TUIFindingGroupTitle("broken_link", 1), "Hallazgo: " + ghost, "~/.claude/skills/ghost"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel lacks %q:\n%s", want, panel)
		}
	}
	if strings.Contains(strings.Join(list, "\n"), "gentle") {
		t.Errorf("gentle-ai's copies are listed one by one:\n%s", strings.Join(list, "\n"))
	}
}

// squash is a screen without its spaces, line ends and borders: a long
// identifier wrapped by a box reads in one piece.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '│' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// firstWords are the lines without the short column that ends them.
func firstWords(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if i := strings.Index(l, "   "); i > 0 {
			l = l[:i]
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// The Salud counter keeps its meaning: the problems it can repair, each
// once, not the skills nor the rows of the list.
func TestTheHealthCounterStillCountsRepairableProblems(t *testing.T) {
	_, a, m := openUX3Health(t, 100, 30)
	rows := healthOracle(t, a)
	skills := map[string]bool{}
	for _, r := range rows {
		if r.skill != "" {
			skills[r.skill] = true
		}
	}
	if n := health(t, a); n != 26 || n == len(rows) || n == len(skills) {
		t.Fatalf("the machine has %d problems to repair, %d rows and %d skills; want 26 problems", n, len(rows), len(skills))
	}
	if head := firstLine(m); !strings.Contains(head, "4 Salud (26)") {
		t.Errorf("the Salud counter changed:\n%s", head)
	}
}

// The golden files of the list grouped by skill, at 80 and 100 columns: a
// finding of two skills under the second one, and a group without skill.
func TestGoldenHealthGroups(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprintf("TwoSkills%d", width), func(t *testing.T) {
			mc, a, m := openUX3Health(t, width, 30)
			i := slices.IndexFunc(healthOracle(t, a), func(r healthRow) bool {
				return r.finding.ID == "homonyms:accessibility" && r.skill != "lock:accessibility"
			})
			for range i {
				pressRun(m, "down")
			}
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
		t.Run(fmt.Sprintf("NoSkill%d", width), func(t *testing.T) {
			mc := newMachine(t)
			mc.h.Symlink(".claude/skills/ghost", "../../.agents/skills/ghost")
			m := openSized(t, mc.newApp(), width, 30)
			pressRun(m, "4")
			teatest.RequireEqualOutput(t, stable(mc, screen(m)))
		})
	}
}

// The skills of the broken links arrive after the findings: Doctor answers
// before the overview that tells, by each skill's LinkFix, which skill each
// link leads to. These tests deliver the load's messages in that order, as
// the runtime may (B1 of INV10's verifier), instead of running them in the
// order the load asks for them.

// collect runs cmd, and each command of a batch, and returns their messages
// undelivered.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// deliver gives m one message and returns the command it produced, unrun.
func deliver(m *tui.Model, msg tea.Msg) tea.Cmd {
	next, cmd := m.Update(msg)
	*m = *next.(*tui.Model)
	return cmd
}

// takeMsg is the message of the load named kind among msgs.
func takeMsg(t *testing.T, msgs []tea.Msg, kind string) tea.Msg {
	t.Helper()
	for _, msg := range msgs {
		if tui.LoadMsgKindForTest(msg) == kind {
			return msg
		}
	}
	t.Fatalf("the load did not ask for %s", kind)
	return nil
}

// loadFindingsFirst loads m's data with Salud open and delivers its
// findings, holding what each skill admits, which it returns for the test to
// deliver later. early opens Salud before the load, which then asks for both
// at once; otherwise Salud is opened after the load, before the overview
// arrives.
func loadFindingsFirst(t *testing.T, m *tui.Model, early bool) (actions tea.Msg) {
	t.Helper()
	if early {
		press(m, "4")
	}
	msgs := collect(deliver(m, takeMsg(t, collect(m.Init()), "data")))
	actions = takeMsg(t, msgs, "actions")
	if early {
		runSyncCmd(m, deliver(m, takeMsg(t, msgs, "findings")))
	} else {
		pressRun(m, "4")
	}
	return actions
}

var panelIDPattern = regexp.MustCompile(`Hallazgo: (\S+)`)

// panelFinding is the finding the panel of a screen tells.
func panelFinding(s string) string {
	if mm := panelIDPattern.FindStringSubmatch(s); mm != nil {
		return strings.TrimRight(mm[1], "│")
	}
	return ""
}

// When what each skill admits arrives after the findings, the list puts the
// broken links under their skills; the cursor stays on the finding it was
// on, wherever that is now, and enter reads that finding's repairs.
func TestTheCursorKeepsItsFindingWhenTheSkillsArrive(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "archify"})
	for _, early := range []bool{true, false} {
		for _, k := range []int{0, 1, 3, 6, 7, 18} {
			m := tui.NewModel(a)
			deliver(m, tea.WindowSizeMsg{Width: 400, Height: 100})
			actions := loadFindingsFirst(t, m, early)
			for range k {
				pressRun(m, "down")
			}
			chosen := panelFinding(screen(m))
			runSyncCmd(m, deliver(m, actions))
			if got := panelFinding(screen(m)); chosen == "" || got != chosen {
				t.Errorf("early=%v, row %d: the cursor went from %q to %q when the skills arrived", early, k, chosen, got)
			}
			before := mc.hash()
			pressRun(m, "enter")
			if got, ok := m.RepairsForTest(); !ok || got != chosen {
				t.Errorf("early=%v, row %d: enter read the repairs of %q (shown: %v), want %s", early, k, got, ok, chosen)
			}
			if mc.hash() != before {
				t.Fatal("reading the repairs wrote something")
			}
		}
	}
}

// A dangling link named after a skill of the store is that skill's once what
// each skill admits arrives. Chosen before, it is still the link enter
// repairs and the one its plan deletes; the other dangling links stay.
func TestTheLinkChosenIsTheOneRepairedWhenTheSkillsArrive(t *testing.T) {
	mc := newMachine(t)
	mc.h.Symlink(".claude/skills/aaa-ghost", "../../.agents/skills/aaa-ghost")
	mc.h.Symlink(".codex/skills/grill-me", "../../.agents/skills/grill-me-v1")
	mc.h.Symlink(".codex/skills/zzz-ghost", "../../.agents/skills/zzz-ghost")
	a := mc.newApp()
	mc.approve(a, app.Request{Action: app.ActionDisable, Key: "archify"})
	const chosen = "broken_link:.codex/skills/grill-me"
	offer, err := a.Repairs(t.Context(), chosen)
	if err != nil {
		t.Fatal(err)
	}
	del := slices.Index(offer.Repairs, "delete_link")
	if del < 0 {
		t.Fatalf("%s offers no delete_link: %v", chosen, offer.Repairs)
	}

	m := tui.NewModel(a)
	deliver(m, tea.WindowSizeMsg{Width: 400, Height: 100})
	actions := loadFindingsFirst(t, m, true)
	for i := 0; i < 100 && panelFinding(screen(m)) != chosen; i++ {
		pressRun(m, "down")
	}
	if got := panelFinding(screen(m)); got != chosen {
		t.Fatalf("the cursor never reached %s before the skills arrived; it is on %q", chosen, got)
	}
	runSyncCmd(m, deliver(m, actions))
	if got := panelFinding(screen(m)); got != chosen {
		t.Fatalf("the cursor went from %s to %q when the skills arrived", chosen, got)
	}
	pressRun(m, "enter")
	if got, ok := m.RepairsForTest(); !ok || got != chosen {
		t.Fatalf("enter read the repairs of %q (shown: %v), want %s", got, ok, chosen)
	}
	for range del {
		pressRun(m, "down")
	}
	pressRun(m, "enter")
	if !m.InPlanForTest() || m.PlanFindingForTest() != chosen {
		t.Fatalf("the plan is for %q (in plan: %v), want %s: %q", m.PlanFindingForTest(), m.InPlanForTest(), chosen, m.StatusForTest())
	}
	pressRun(m, "s")
	if mc.exists(".codex/skills/grill-me") || !mc.exists(".codex/skills/zzz-ghost") || !mc.exists(".claude/skills/aaa-ghost") {
		t.Errorf("after deleting %s: grill-me %v, zzz-ghost %v, aaa-ghost %v; want only grill-me gone", chosen,
			mc.exists(".codex/skills/grill-me"), mc.exists(".codex/skills/zzz-ghost"), mc.exists(".claude/skills/aaa-ghost"))
	}
}

// While the load still reads what each skill admits, the list may still
// move: enter reads no repairs and says why, as the write keys of Skills do,
// and works once the skills arrive, on the finding the cursor was on.
func TestSaludWaitsForTheSkillsBeforeReadingRepairs(t *testing.T) {
	mc := newMachine(t)
	a := mc.newApp()
	m := tui.NewModel(a)
	deliver(m, tea.WindowSizeMsg{Width: 400, Height: 100})
	actions := loadFindingsFirst(t, m, true)
	chosen := panelFinding(screen(m))
	before := mc.hash()
	pressRun(m, "enter")
	if got, ok := m.RepairsForTest(); ok || m.StatusForTest() != text.TUIReadingActions {
		t.Fatalf("enter before the skills arrived read the repairs of %q (shown: %v), status %q", got, ok, m.StatusForTest())
	}
	if got := flat(screen(m)); !strings.Contains(got, flat(text.TUIReadingActions)) {
		t.Errorf("Salud does not say it is reading the skills:\n%s", screen(m))
	}
	if mc.hash() != before {
		t.Fatal("enter before the skills arrived wrote something")
	}
	runSyncCmd(m, deliver(m, actions))
	pressRun(m, "enter")
	if got, ok := m.RepairsForTest(); !ok || chosen == "" || got != chosen {
		t.Errorf("enter after the skills arrived read the repairs of %q (shown: %v), want %q", got, ok, chosen)
	}
}

// When Salud looks for its findings again, the rows may change around the
// cursor: it stays on its finding wherever that is listed now, and once the
// finding is gone, on the row at its place.
func TestTheCursorKeepsItsFindingWhenTheFindingsChange(t *testing.T) {
	mc, a, m := openUX3Health(t, 400, 100)
	const k = 3
	chosen := healthRowOrder(t, a)[k]
	if chosen.Kind != "broken_link" {
		t.Fatalf("row %d is %s, want a broken link", k, chosen.ID)
	}
	for range k {
		pressRun(m, "down")
	}
	// A link to a skill that is nowhere comes first, in a group of its own.
	mc.h.Symlink(".claude/skills/aaa-ghost", "../../.agents/skills/aaa-ghost")
	pressRun(m, "1")
	pressRun(m, "4")
	rows := healthRowOrder(t, a)
	if rows[0].ID != "broken_link:.claude/skills/aaa-ghost" || rows[k+1].ID != chosen.ID {
		t.Fatalf("the new link is not the row before the others: %s, then %s at %d", rows[0].ID, rows[k+1].ID, k+1)
	}
	if got := panelFinding(screen(m)); got != chosen.ID {
		t.Errorf("the cursor went from %s to %q when a row came before it", chosen.ID, got)
	}

	// Its link deleted elsewhere, the finding is gone: the cursor stays at
	// its place, on the row that follows.
	if err := os.Remove(mc.h.Path(strings.TrimPrefix(chosen.ID, "broken_link:"))); err != nil {
		t.Fatal(err)
	}
	pressRun(m, "1")
	pressRun(m, "4")
	rows = healthRowOrder(t, a)
	if slices.ContainsFunc(rows, func(f app.Finding) bool { return f.ID == chosen.ID }) {
		t.Fatalf("%s is still found", chosen.ID)
	}
	if got := panelFinding(screen(m)); got != rows[k+1].ID {
		t.Errorf("with %s gone, the cursor is on %q, want the row at its place, %s", chosen.ID, got, rows[k+1].ID)
	}
}

var healthHeader = regexp.MustCompile(`^[▾▸] (.*?)\s{2,}(\d+ hallazgos?)$`)

// The groups are the skills each finding is about, as the finding and the
// fixture tell, not as the TUI's rules do (healthOracle copies those, and so
// cannot catch a mistake in them): a finding is about the skills it names
// (Skill, Skills), and each dangling link of graphtest's machine about the
// skill of OFF/ whose name it has. Walking the list, each skill's group holds
// exactly its findings, under the name the inventory gives it, and each
// group without skill the findings of its kind that are about none.
func TestHealthGroupsAreTheSkillsOfEachFinding(t *testing.T) {
	_, a, m := openUX3Health(t, 80, 200)
	findings, err := a.Doctor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := a.Skills(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	invName := map[string]string{}
	for _, s := range inventory {
		invName[s.ID] = s.Name
	}
	expect := map[string][]string{}  // skill -> its findings
	noSkill := map[string][]string{} // kind -> its findings about no skill
	named := map[string]string{}     // skill -> the name its findings give it
	total, walk := 0, 0
	for _, f := range findings {
		if f.Kind == text.TUIGentleKind {
			continue
		}
		total++
		about := slices.Clone(f.Skills)
		if f.Skill != "" {
			about = append(about, f.Skill)
		}
		for _, s := range about {
			named[s] = f.Name
		}
		if f.Kind == "broken_link" && len(f.Paths) > 0 && slices.Contains(graphtest.OffSkills, path.Base(f.Paths[0])) {
			about = append(about, "lock:"+path.Base(f.Paths[0]))
		}
		slices.Sort(about)
		about = slices.Compact(about)
		if len(about) == 0 {
			noSkill[f.Kind] = append(noSkill[f.Kind], f.ID)
			walk++
			continue
		}
		for _, s := range about {
			expect[s] = append(expect[s], f.ID)
			walk++
		}
	}

	shown := map[string][]string{} // group line -> the findings walked in it
	counts := map[string]string{}
	var groups []string
	for i := range walk {
		if i > 0 {
			pressRun(m, "down")
		}
		boxes := healthFrames(screen(m))
		if len(boxes) < 2 {
			t.Fatalf("row %d: no list and panel:\n%s", i, screen(m))
		}
		list, panel := boxes[0], boxes[1]
		if want := text.TUIHealthTitle(len(expect), total); list[0] != want {
			t.Errorf("row %d: the title is %q, want %q", i, list[0], want)
		}
		open, at := "", -1
		listed := map[string]bool{}
		for j, l := range list {
			mm := healthHeader.FindStringSubmatch(l)
			if mm == nil {
				continue
			}
			if listed[mm[1]] {
				t.Errorf("row %d: %q is listed twice", i, mm[1])
			}
			listed[mm[1]], counts[mm[1]] = true, mm[2]
			if strings.HasPrefix(l, text.TUIHealthOpen) {
				if open != "" {
					t.Errorf("row %d: %q and %q are both open", i, open, mm[1])
				}
				open, at = mm[1], j
			}
		}
		// The cursor walks a group's rows in order: its row is the next one.
		row := at + 1 + len(shown[open])
		if at < 0 || row >= len(list) || !strings.HasPrefix(list[row], "· ") {
			t.Fatalf("row %d: no row of the open group %q under the cursor:\n%s", i, open, strings.Join(list, "\n"))
		}
		if !slices.Contains(groups, open) {
			groups = append(groups, open)
		}
		shown[open] = append(shown[open], panelFinding(strings.Join(panel, "\n")))
		if !strings.HasSuffix(open, " sin skill") && panel[0] != open {
			t.Errorf("row %d: the panel is headed %q, the open group is %q", i, panel[0], open)
		}
	}
	last := panelFinding(screen(m))
	pressRun(m, "down")
	if panelFinding(screen(m)) != last {
		t.Errorf("the list has more than the %d rows of its findings", walk)
	}

	used := map[string]bool{}
	for _, g := range groups {
		ids := slices.Sorted(slices.Values(shown[g]))
		if counts[g] != text.TUIHealthCount(len(ids)) || len(slices.Compact(slices.Clone(ids))) != len(ids) {
			t.Errorf("group %q says %q and holds %v", g, counts[g], ids)
		}
		if strings.HasSuffix(g, " sin skill") {
			kind := ""
			for k := range noSkill {
				if text.TUIHealthNoSkill(k) == g {
					kind = k
				}
			}
			if want := slices.Sorted(slices.Values(noSkill[kind])); kind == "" || !slices.Equal(ids, want) {
				t.Errorf("group %q holds %v, want the findings of its kind about no skill %v", g, ids, want)
			}
			used["kind:"+kind] = true
			continue
		}
		var match []string
		for s, want := range expect {
			_, rel, _ := strings.Cut(s, ":")
			name := cmp.Or(invName[s], named[s], path.Base(rel))
			same := slices.Equal(slices.Sorted(slices.Values(want)), ids)
			if !used[s] && same && (g == name || strings.HasPrefix(g, name+" (")) {
				match = append(match, s)
			}
		}
		// A folder after the name is the skill's own, or the one holding it;
		// without one, a skill that is a folder goes only when no other does.
		_, folder, withFolder := strings.Cut(g, " (~/")
		folder = strings.TrimSuffix(folder, ")")
		several := len(match) > 1
		match = slices.DeleteFunc(match, func(s string) bool {
			kind, rel, _ := strings.Cut(s, ":")
			isFolder := kind == "dir" || kind == "git"
			if !withFolder {
				return isFolder && several
			}
			return !isFolder || (rel != folder && path.Dir(rel) != folder)
		})
		if len(match) != 1 {
			t.Errorf("group %q holds %v: the findings of skills %v", g, ids, match)
			continue
		}
		used[match[0]] = true
	}
	for s, ids := range expect {
		if !used[s] {
			t.Errorf("skill %s, with %v, has no group", s, ids)
		}
	}
	for k, ids := range noSkill {
		if !used["kind:"+k] {
			t.Errorf("the findings of kind %s about no skill, %v, have no group", k, ids)
		}
	}
}
