package tui_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	fsadapter "skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/settings"
	"skillhub/internal/adapters/skillslock"
	"skillhub/internal/adapters/statefile"
	"skillhub/internal/app"
	"skillhub/internal/data"
	"skillhub/internal/entrypoints/tui"
	"skillhub/internal/graph/graphtest"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport/fakes"
)

func catalogApp(t *testing.T, mc *machine, cat ports.Catalog) *app.App {
	t.Helper()
	files := fsadapter.NewFileOps(mc.p.Guard())
	agents, err := data.LoadAgents(mc.p, files)
	if err != nil {
		t.Fatal(err)
	}
	lock := skillslock.New(mc.p, files)
	git := fakes.NewGit()
	clone, err := filepath.EvalSymlinks(mc.h.Path(graphtest.KarpathyClone))
	if err != nil {
		t.Fatal(err)
	}
	git.AddClone(clone, ports.CloneStatus{
		Remote: "https://github.com/multica-ai/andrej-karpathy-skills.git", Branch: "main",
		Upstream: "origin/main", Head: "1111111111111111111111111111111111111111", Clean: true,
	})
	web := fakes.NewWeb()
	const site = "https://example.com/.well-known/agent-skills/"
	web.Serve(site+"index.json", []byte(`{"skills":[{"name":"ui-taste","description":"Taste for UI.","files":["SKILL.md"]}]}`))
	web.Serve(site+"ui-taste/SKILL.md", []byte("---\nname: ui-taste\ndescription: Taste for UI.\n---\n\nUse taste.\n"))
	a, err := app.New(app.Deps{
		Paths: mc.p, Agents: agents, Files: files, Dirs: fsadapter.NewScanner(mc.p.Guard()),
		Lock: lock, ManagedLock: lock, Links: fsadapter.NewLinkOps(mc.p.Guard()),
		State: statefile.New(mc.p), Settings: settings.New(mc.p), GitHub: mc.gh, Git: git,
		Notifier: fakes.NewNotifier(), Background: fakes.NewBackgroundService(), Clock: mc.clock,
		ProcDir: mc.proc, Catalog: cat, Web: web,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func catalogSkill(source, id, name string, installs int) ports.CatalogSkill {
	return ports.CatalogSkill{ID: source + "/" + id, Source: source, SkillID: id, Name: name, Installs: installs}
}

func wantCatalogView(t *testing.T, m *tui.Model, wants ...string) {
	t.Helper()
	got := screen(m)
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
}

func catalogSearch(t *testing.T, m *tui.Model, q string) {
	t.Helper()
	press(m, "/")
	wantCatalogView(t, m, "Buscar en skills.sh: ")
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typeText(m, q)
}

func TestCatalogLazyNavigation(t *testing.T) {
	for _, route := range [][]string{{"3"}, {"2", "tab"}, {"3", "h", "esc"}, {"3", "c", "esc"}} {
		t.Run(strings.Join(route, "-"), func(t *testing.T) {
			mc := newMachine(t)
			cat := fakes.NewCatalog()
			cat.Set("react", catalogSkill("acme/tools", "tidy", "tidy", 1200))
			a := catalogApp(t, mc, cat)
			m := openSized(t, a, 100, 30)
			if got := cat.Calls(); len(got) != 0 {
				t.Fatalf("startup catalog calls = %v", got)
			}
			for _, k := range route {
				pressRun(m, k)
			}
			if got := cat.Queries(); !slices.Equal(got, app.PopularTopics) {
				t.Errorf("catalog calls = %v, want %v", got, app.PopularTopics)
			}
			wantCatalogView(t, m, "Populares en skills.sh (1)", "tidy", "1200", "[enter] Instalar")
			pressRun(m, "1")
			pressRun(m, "3")
			if got := len(cat.Calls()); got != len(app.PopularTopics) {
				t.Errorf("reopening made catalog calls: %d", got)
			}
		})
	}
}

func TestCatalogSearchRefreshAndReturn(t *testing.T) {
	mc := newMachine(t)
	cat := fakes.NewCatalog()
	cat.Set("úi", catalogSkill("example.com", "ui-taste", "Taste", 12345))
	m := openSized(t, catalogApp(t, mc, cat), 100, 30)
	pressRun(m, "3")
	n := len(cat.Calls())
	catalogSearch(t, m, " ñ ")
	if cmd := press(m, "enter"); cmd != nil {
		t.Error("a one-rune query returned a command")
	}
	wantCatalogView(t, m, "la búsqueda necesita al menos dos caracteres")
	if len(cat.Calls()) != n {
		t.Fatal("short search contacted the catalog")
	}
	catalogSearch(t, m, " úi ")
	pressRun(m, "enter")
	wantCatalogView(t, m, "Búsqueda «úi» en skills.sh (1)", "Taste", "12 345", "sitio web", "https://example.com@ui-taste")
	calls := cat.Calls()
	if len(calls) != n+1 || calls[n] != (fakes.CatalogCall{Query: "úi", Limit: app.DefaultCatalogLimit}) {
		t.Fatalf("search calls = %v", calls)
	}
	catalogSearch(t, m, "other")
	pressRun(m, "esc")
	wantCatalogView(t, m, "Búsqueda «úi»")
	if len(cat.Calls()) != n+1 {
		t.Fatal("cancelling search contacted the catalog")
	}
	cat.Set("úi", catalogSkill("example.com", "ui-taste", "Renamed", 55))
	pressRun(m, "u")
	wantCatalogView(t, m, "Renamed")
	if len(cat.Calls()) != n+2 {
		t.Fatalf("refresh calls = %v", cat.Calls())
	}
	pressRun(m, "esc")
	wantCatalogView(t, m, "Populares en skills.sh (0)", "[o] Escribir origen")
	catalogSearch(t, m, "missing")
	pressRun(m, "enter")
	wantCatalogView(t, m, "Búsqueda «missing» en skills.sh (0)", "[esc]", "[o] Escribir origen")
	catalogSearch(t, m, "   ")
	pressRun(m, "enter")
	wantCatalogView(t, m, "Populares en skills.sh (0)")
	if len(cat.Calls()) != n+3 {
		t.Fatalf("return to popular bypassed cache: %v", cat.Calls())
	}
}

func TestCatalogManualOriginUnblockedAndResponsesAreIndependent(t *testing.T) {
	mc := newMachine(t)
	mc.publishTool()
	cat := fakes.NewCatalog()
	cat.Set("react", catalogSkill("acme/other", "old", "Old result", 10))
	cat.Set("tidy", catalogSkill("acme/tools", "tidy", "New result", 20))
	m := openSized(t, catalogApp(t, mc, cat), 100, 30)
	old := press(m, "3")
	if old == nil {
		t.Fatal("entering Instalar did not start a catalog load")
	}
	wantCatalogView(t, m, "[o] Escribir origen")
	if press(m, "enter") != nil {
		t.Fatal("loading catalog tried installation")
	}
	pressRun(m, "o")
	wantCatalogView(t, m, "Origen (GitHub o sitio web):")
	pressRun(m, "esc")
	catalogSearch(t, m, "tidy")
	pressRun(m, "enter")
	wantCatalogView(t, m, "New result")
	resolve := press(m, "enter")
	if resolve == nil {
		t.Fatal("the catalog selection did not request a review")
	}
	runSyncCmd(m, old)
	wantCatalogView(t, m, "New result")
	if press(m, "enter") != nil {
		t.Fatal("a stale catalog response cleared the install busy state")
	}
	runSyncCmd(m, resolve)
	wantCatalogView(t, m, "Revisión de tidy")
	pressRun(m, "esc")
}

func TestCatalogInstallApprovalAndBadges(t *testing.T) {
	for _, tt := range []struct {
		name, source, id, review string
		duplicate, apply         bool
	}{
		{name: "GitHub", source: "acme/tools", id: "tidy", review: "+second line", apply: true},
		{name: "Website", source: "example.com", id: "ui-taste", review: "+Use taste."},
		{name: "Duplicate", source: "acme/tools", id: "agent-browser", review: "+A different skill.", duplicate: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMachine(t)
			if tt.duplicate {
				mc.gh.AddRepo(tt.source, "main")
				writeTree(t, filepath.Join(mc.upstream, "skills", tt.id), map[string]string{
					"SKILL.md": "---\nname: agent-browser\ndescription: Different origin.\n---\nA different skill.\n",
				})
				mc.gh.Commit(t, tt.source, "main", mc.upstream)
			} else {
				mc.publishTool()
			}
			cat := fakes.NewCatalog()
			cat.Set("react", catalogSkill(tt.source, tt.id, tt.id, 42))
			m := openSized(t, catalogApp(t, mc, cat), 160, 44)
			pressRun(m, "3")
			if tt.duplicate {
				wantCatalogView(t, m, "posible duplicado", "owner/agent-browser")
			}
			pressRun(m, "enter")
			wantCatalogView(t, m, "Revisión de "+tt.id, "SKILL.md", tt.review)
			pressRun(m, "enter")
			wantCatalogView(t, m, "[x]")
			if tt.duplicate {
				// The existing skill may occupy paths the plan would need;
				// the catalog must still pass the different origin to review.
				pressRun(m, "esc")
				wantCatalogView(t, m, "posible duplicado", "owner/agent-browser")
				return
			}
			pressRun(m, "enter")
			wantCatalogView(t, m, "¿Instalar "+tt.id+"?")
			installed := func() bool { return mc.exists(".agents/skills/" + tt.id + "/SKILL.md") }
			lockHasSkill := func() bool {
				b, err := os.ReadFile(mc.p.SkillsLockFile())
				if err != nil {
					t.Fatal(err)
				}
				return strings.Contains(string(b), `"`+tt.id+`"`)
			}
			if installed() || lockHasSkill() {
				t.Fatal("catalog installed before approval")
			}
			if !tt.apply {
				pressRun(m, "n")
				if installed() || lockHasSkill() {
					t.Fatal("declined install made changes")
				}
				return
			}
			pressRun(m, "s")
			if !installed() || !lockHasSkill() {
				t.Fatal("approval did not install skill and update lock")
			}
			wantCatalogView(t, m, "instalada")
			if len(cat.Calls()) != len(app.PopularTopics) {
				t.Fatalf("install reread the remote catalog: %v", cat.Calls())
			}
			pressRun(m, "z")
			pressRun(m, "s")
			if installed() || lockHasSkill() {
				t.Fatal("undo left the installed skill or lock entry")
			}
			if strings.Contains(screen(m), "instalada") {
				t.Errorf("undo left the installed badge:\n%s", screen(m))
			}
			if len(cat.Calls()) != len(app.PopularTopics) {
				t.Fatalf("undo reread the remote catalog: %v", cat.Calls())
			}
		})
	}
}

func TestCatalogUninstallableRowsDoNotResolve(t *testing.T) {
	mc := newMachine(t)
	cat := fakes.NewCatalog()
	cat.Set("react", catalogSkill("owner/agent-browser", "agent-browser", "agent-browser", 20), catalogSkill("invalid", "broken", "broken", 10))
	m := openSized(t, catalogApp(t, mc, cat), 100, 30)
	pressRun(m, "3")
	if press(m, "enter") != nil {
		t.Error("installed row returned an install command")
	}
	wantCatalogView(t, m, "owner/agent-browser", "Skills (1)", "Actualizar (2)")
	pressRun(m, "end")
	if press(m, "enter") != nil {
		t.Error("unsupported row returned an install command")
	}
	wantCatalogView(t, m, "no instalable", "no se puede instalar con skillhub")
	pressRun(m, "home")
	if press(m, "enter") != nil {
		t.Error("home did not return to the installed row")
	}
	wantCatalogView(t, m, "Skills (1)")
}

func TestCatalogErrorStaleAndSafeOutput(t *testing.T) {
	mc := newMachine(t)
	cat := fakes.NewCatalog()
	const bad = "\x1b[31m\r\u202e"
	q := "fi\u202end"
	cat.Fail("react", errors.New("broken"+bad))
	m := openSized(t, catalogApp(t, mc, cat), 100, 30)
	pressRun(m, "3")
	wantCatalogView(t, m, `broken\x1b[31m\r\u202e`, "[o] Escribir origen", "[u] Recargar catálogo")
	pressRun(m, "o")
	wantCatalogView(t, m, "Origen (GitHub o sitio web):")
	pressRun(m, "esc")
	cat.Set("react")
	pressRun(m, "u")
	wantCatalogView(t, m, "Populares en skills.sh (0)")
	cat.Set(q, catalogSkill("acme/tools", "tidy", "evil"+bad+"\nline", 2), catalogSkill("evil"+bad, "\tname", "broken", 1))
	catalogSearch(t, m, q)
	wantCatalogView(t, m, `fi\u202end`)
	if strings.Contains(m.View().Content, "\u202e") {
		t.Fatal("raw bidi reached the search prompt")
	}
	pressRun(m, "enter")
	wantCatalogView(t, m, `Búsqueda «fi\u202end»`, `evil\x1b[31m\r\u202e\nline`)
	pressRun(m, "end")
	wantCatalogView(t, m, `Origen: evil\x1b[31m\r\u202e`)
	cat.Fail(q, errors.New("again"+bad))
	pressRun(m, "u")
	wantCatalogView(t, m, `again\x1b[31m\r\u202e`, "Se muestran resultados guardados", "Búsqueda «fi")
	got := m.View().Content
	if strings.Contains(got, "\x1b[31m") || strings.ContainsAny(got, "\r\u202e") {
		t.Errorf("raw catalog controls reached the view: %q", got)
	}
}

func TestCatalog(t *testing.T) {
	t.Run("help without catalog", func(t *testing.T) {
		m := openSized(t, newMachine(t).newApp(), 80, 24)
		pressRun(m, "?")
		got := plainCatalogLines(screen(m))
		const legacy = "3 Instalar: enter empezar o continuar • espacio marcar un agente • esc cancelar"
		_, install, _ := strings.Cut(got, "3 Instalar: ")
		install, _, _ = strings.Cut(install, "4 Salud:")
		if !strings.Contains(got, legacy) || strings.Contains(install, "recargar catálogo") || strings.Contains(install, "/ buscar") {
			t.Errorf("help without catalog should show only the manual install keys:\n%s", screen(m))
		}
	})
	t.Run("help with catalog", func(t *testing.T) {
		m := openSized(t, catalogApp(t, newMachine(t), fakes.NewCatalog()), 80, 24)
		pressRun(m, "?")
		got := screen(m)
		const catalogHelp = "3 Instalar: / buscar • o escribir origen • u recargar catálogo • enter instalar • esc cancelar"
		if !strings.Contains(plainCatalogLines(got), catalogHelp) {
			t.Errorf("help lacks catalog keys:\n%s", got)
		}
		if strings.Count(got, "\n")+1 > 24 || strings.Contains(got, "desplazar la ayuda") {
			t.Errorf("catalog help does not fit 80x24 without scrolling:\n%s", got)
		}
		checkKeys(t, "catalog help", got, true)
	})
	for _, size := range [][2]int{{80, 24}, {100, 24}, {100, 30}} {
		width, height := size[0], size[1]
		t.Run(fmt.Sprintf("duplicate evidence and footer %dx%d", width, height), func(t *testing.T) {
			cat := fakes.NewCatalog()
			cat.Set("react",
				catalogSkill("acme/tools", "agent-browser", "agent-browser", 30),
				catalogSkill("owner/agent-browser", "agent-browser", "agent-browser", 20),
				catalogSkill("unsupported", "broken", "broken", 10),
			)
			m := openSized(t, catalogApp(t, newMachine(t), cat), width, height)
			pressRun(m, "3")
			got := screen(m)
			if !strings.Contains(plainCatalogLines(got), "posible duplicado (agent-browser, de owner/agent-browser)") {
				t.Errorf("view lacks complete duplicate evidence:\n%s", got)
			}
			wantCatalogInstallFooter(t, m, true)
			pressRun(m, "down")
			wantCatalogView(t, m, "instalada")
			wantCatalogInstallFooter(t, m, false)
			pressRun(m, "end")
			wantCatalogView(t, m, "no instalable")
			wantCatalogInstallFooter(t, m, false)
			pressRun(m, "home")
			wantCatalogInstallFooter(t, m, true)
		})
	}
}

// Read a help line or the right-hand panel even when it wraps beside the list.
func plainCatalogLines(view string) string {
	var lines []string
	for _, line := range strings.Split(view, "\n") {
		line = strings.Trim(line, " │")
		if _, panel, ok := strings.Cut(line, "││"); ok {
			line = panel
		}
		lines = append(lines, line)
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

func wantCatalogInstallFooter(t *testing.T, m *tui.Model, want bool) {
	t.Helper()
	view := screen(m)
	footer := view[strings.LastIndex(view, "╯")+len("╯"):]
	if got := strings.Contains(strings.Join(strings.Fields(footer), " "), "enter instalar"); got != want {
		t.Errorf("footer advertises enter instalar: got %t, want %t: %q", got, want, footer)
	}
}

func TestGoldenCatalog(t *testing.T) {
	for _, width := range []int{80, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			mc := newMachine(t)
			cat := fakes.NewCatalog()
			cat.Set("react", catalogSkill("acme/tools", "agent-browser", "agent-browser", 12345), catalogSkill("example.com", "ui-taste", "ui-taste", 4389), catalogSkill("owner/agent-browser", "agent-browser", "agent-browser", 500), catalogSkill("unknown", "unsupported", "unsupported", 50))
			m := openSized(t, catalogApp(t, mc, cat), width, 30)
			pressRun(m, "3")
			teatest.RequireEqualOutput(t, []byte(screen(m)))
		})
	}
}
