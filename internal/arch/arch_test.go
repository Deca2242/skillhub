package arch_test

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"skillhub/internal/arch"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// TestRepositoryHonorsTheArchitecture is the architecture: keep it green by
// fixing the code, never by changing the rules.
func TestRepositoryHonorsTheArchitecture(t *testing.T) {
	report, err := arch.Check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, pkg := range []string{"cmd/skillhub", "internal/adapters/proc", "internal/testsupport", "internal/arch"} {
		if !slices.Contains(report.Packages, pkg) {
			t.Fatalf("Check did not scan %s; scanned %v", pkg, report.Packages)
		}
	}
	for _, v := range report.Violations {
		t.Errorf("%s", v)
	}
}

// cleanModule is a module that honors every rule, including the allowed
// exceptions, plus a planted violation under testdata that must be ignored.
var cleanModule = map[string]string{
	"go.mod": "module skillhub\n\ngo 1.27\n",
	"cmd/skillhub/main.go": `package main

import "os"

func main() { _ = os.Getenv("HOME") }
`,
	"internal/shared/paths/paths.go": `package paths

import (
	"os"
	"os/user"
)

var _, _ = os.UserHomeDir, user.Current
`,
	"internal/adapters/proc/proc.go": `package proc

import (
	"os"
	"os/exec"
)

var _, _ = exec.Command, os.Environ
`,
	"internal/entrypoints/tui/tui.go": `package tui

import tea "charm.land/bubbletea/v2"

var _ tea.Model
`,
	"internal/entrypoints/tui/tui_test.go": `package tui

import (
	"testing"

	_ "github.com/charmbracelet/x/exp/teatest/v2"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }
`,
	"internal/entrypoints/cli/cli.go": `package cli

import "github.com/spf13/cobra"

var _ cobra.Command
`,
	"internal/inventory/inventory.go": `package inventory

import "os"

var _ = os.ReadFile
`,
	"internal/inventory/inventory_test.go": `package inventory_test

import (
	"testing"

	ts "skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { ts.Main(m) }

func TestSomething(t *testing.T) {}
`,
	"internal/testsupport/main.go": `package testsupport

import (
	"os"
	"os/user"
	"testing"
)

func Main(m *testing.M) {
	_, _ = user.Current()
	_ = os.Getenv("HOME")
	os.Exit(m.Run())
}
`,
	"internal/testsupport/main_test.go": `package testsupport

import "testing"

func TestMain(m *testing.M) { Main(m) }

func TestChangesItsEnvironment(t *testing.T) { t.Setenv("PATH", "x") }
`,
	"internal/testsupport/sandbox.go": `package testsupport

import "os"

func reset() {
	_ = os.Setenv("HOME", "x")
	_ = os.Unsetenv("XDG_X")
	os.Clearenv()
}
`,
	"internal/deps/tools.go": `//go:build tools

package deps

import (
	_ "charm.land/bubbletea/v2"
	_ "github.com/spf13/cobra"
)
`,
	"internal/arch/testdata/planted/planted.go": `package planted

import "os/exec"

var _ = exec.Command
`,
}

func TestEachRuleCatchesAPlantedViolation(t *testing.T) {
	tests := []struct {
		name  string
		plant map[string]string
		want  []string // "rule file"
	}{
		{
			name: "clean module",
			want: nil,
		},
		{
			name:  "os/exec outside the proc adapter",
			plant: map[string]string{"internal/inventory/run.go": "package inventory\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n"},
			want:  []string{"process internal/inventory/run.go"},
		},
		{
			name:  "os/exec in a test file",
			plant: map[string]string{"internal/inventory/run_test.go": "package inventory_test\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n"},
			want:  []string{"process internal/inventory/run_test.go"},
		},
		{
			name:  "os.StartProcess outside the proc adapter",
			plant: map[string]string{"internal/updates/start.go": "package updates\n\nimport \"os\"\n\nvar _ = os.StartProcess\n"},
			want:  []string{"process internal/updates/start.go"},
		},
		{
			name:  "syscall.Exec outside the proc adapter",
			plant: map[string]string{"internal/updates/start.go": "package updates\n\nimport \"syscall\"\n\nvar _ = syscall.Exec\n"},
			want:  []string{"process internal/updates/start.go"},
		},
		{
			name:  "Bubble Tea outside the TUI",
			plant: map[string]string{"internal/app/app.go": "package app\n\nimport tea \"charm.land/bubbletea/v2\"\n\nvar _ tea.Model\n"},
			want:  []string{"tui internal/app/app.go"},
		},
		{
			name:  "Lip Gloss in the CLI",
			plant: map[string]string{"internal/entrypoints/cli/style.go": "package cli\n\nimport _ \"charm.land/lipgloss/v2\"\n"},
			want:  []string{"tui internal/entrypoints/cli/style.go"},
		},
		{
			name:  "Bubbles outside the TUI",
			plant: map[string]string{"internal/app/table.go": "package app\n\nimport _ \"charm.land/bubbles/v2/table\"\n"},
			want:  []string{"tui internal/app/table.go"},
		},
		{
			name:  "teatest outside the TUI",
			plant: map[string]string{"internal/inventory/flow_test.go": "package inventory_test\n\nimport _ \"github.com/charmbracelet/x/exp/teatest/v2\"\n"},
			want:  []string{"tui internal/inventory/flow_test.go"},
		},
		{
			name:  "Cobra in the TUI",
			plant: map[string]string{"internal/entrypoints/tui/cmd.go": "package tui\n\nimport _ \"github.com/spf13/cobra\"\n"},
			want:  []string{"cobra internal/entrypoints/tui/cmd.go"},
		},
		{
			name:  "Cobra in the composition root",
			plant: map[string]string{"cmd/skillhub/root.go": "package main\n\nimport _ \"github.com/spf13/cobra\"\n"},
			want:  []string{"cobra cmd/skillhub/root.go"},
		},
		{
			name:  "net/http anywhere",
			plant: map[string]string{"internal/adapters/github/client.go": "package github\n\nimport _ \"net/http\"\n"},
			want:  []string{"network internal/adapters/github/client.go"},
		},
		{
			name:  "net even in the proc adapter",
			plant: map[string]string{"internal/adapters/proc/dial.go": "package proc\n\nimport _ \"net\"\n"},
			want:  []string{"network internal/adapters/proc/dial.go"},
		},
		{
			name:  "os.Getenv outside the allowed packages",
			plant: map[string]string{"internal/inventory/env.go": "package inventory\n\nimport \"os\"\n\nvar _ = os.Getenv(\"HOME\")\n"},
			want:  []string{"env internal/inventory/env.go"},
		},
		{
			name:  "os.LookupEnv through an import alias",
			plant: map[string]string{"internal/updates/env.go": "package updates\n\nimport sys \"os\"\n\nvar _, _ = sys.LookupEnv(\"HOME\")\n"},
			want:  []string{"env internal/updates/env.go"},
		},
		{
			name:  "os.UserHomeDir in the fs adapter",
			plant: map[string]string{"internal/adapters/fs/home.go": "package fs\n\nimport \"os\"\n\nvar _, _ = os.UserHomeDir()\n"},
			want:  []string{"env internal/adapters/fs/home.go"},
		},
		{
			name:  "os.UserConfigDir and os.UserCacheDir",
			plant: map[string]string{"internal/updates/dirs.go": "package updates\n\nimport \"os\"\n\nvar _, _ = os.UserConfigDir, os.UserCacheDir\n"},
			want:  []string{"env internal/updates/dirs.go", "env internal/updates/dirs.go"},
		},
		{
			name:  "os.Environ as a value in a test file",
			plant: map[string]string{"internal/inventory/env_test.go": "package inventory_test\n\nimport \"os\"\n\nvar environ = os.Environ\n"},
			want:  []string{"env internal/inventory/env_test.go"},
		},
		{
			name:  "os.ExpandEnv",
			plant: map[string]string{"internal/updates/expand.go": "package updates\n\nimport \"os\"\n\nvar _ = os.ExpandEnv(\"$HOME\")\n"},
			want:  []string{"env internal/updates/expand.go"},
		},
		{
			name:  "syscall.Getenv",
			plant: map[string]string{"internal/updates/sys.go": "package updates\n\nimport \"syscall\"\n\nvar _, _ = syscall.Getenv(\"HOME\")\n"},
			want:  []string{"env internal/updates/sys.go"},
		},
		{
			name:  "the os/user package",
			plant: map[string]string{"internal/updates/who.go": "package updates\n\nimport \"os/user\"\n\nvar _ = user.Current\n"},
			want:  []string{"env internal/updates/who.go"},
		},
		{
			name:  "a dot import of os hides lookups",
			plant: map[string]string{"internal/updates/dot.go": "package updates\n\nimport . \"os\"\n\nvar _ = Getenv(\"HOME\")\n"},
			want:  []string{"env internal/updates/dot.go"},
		},
		{
			name: "a test package without TestMain",
			plant: map[string]string{
				"internal/updates/updates.go":      "package updates\n",
				"internal/updates/updates_test.go": "package updates\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			},
			want: []string{"testmain internal/updates"},
		},
		{
			name: "a TestMain that does not call testsupport.Main",
			plant: map[string]string{
				"internal/updates/main_test.go": "package updates\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestMain(m *testing.M) { os.Exit(m.Run()) }\n",
			},
			want: []string{"testmain internal/updates"},
		},
		{
			name: "a Main from another package named testsupport",
			plant: map[string]string{
				"internal/updates/main_test.go": "package updates\n\nimport (\n\t\"testing\"\n\n\ttestsupport \"example.com/other\"\n)\n\nfunc TestMain(m *testing.M) { testsupport.Main(m) }\n",
			},
			want: []string{"testmain internal/updates"},
		},
		{
			name: "a TestMain only in a build-tagged file",
			plant: map[string]string{
				"internal/updates/main_test.go": "//go:build network\n\npackage updates\n\nimport (\n\t\"testing\"\n\n\t\"skillhub/internal/testsupport\"\n)\n\nfunc TestMain(m *testing.M) { testsupport.Main(m) }\n",
				"internal/updates/x_test.go":    "package updates\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			},
			want: []string{"testmain internal/updates"},
		},
		{
			name:  "an untagged file in internal/deps",
			plant: map[string]string{"internal/deps/extra.go": "package deps\n\nimport _ \"charm.land/bubbletea/v2\"\n"},
			want:  []string{"deps internal/deps/extra.go", "tui internal/deps/extra.go"},
		},
		{
			name:  "non-test code importing testsupport",
			plant: map[string]string{"internal/inventory/fixture.go": "package inventory\n\nimport _ \"skillhub/internal/testsupport\"\n"},
			want:  []string{"testsupport internal/inventory/fixture.go"},
		},
		{
			name:  "the composition root importing testsupport",
			plant: map[string]string{"cmd/skillhub/fake.go": "package main\n\nimport _ \"skillhub/internal/testsupport\"\n"},
			want:  []string{"testsupport cmd/skillhub/fake.go"},
		},
		{
			name:  "t.Setenv in a test outside testsupport",
			plant: map[string]string{"internal/inventory/env_test.go": "package inventory_test\n\nimport \"testing\"\n\nfunc TestHome(t *testing.T) { t.Setenv(\"HOME\", \"/x\") }\n"},
			want:  []string{"envmut internal/inventory/env_test.go"},
		},
		{
			name:  "b.Setenv in a benchmark",
			plant: map[string]string{"internal/inventory/bench_test.go": "package inventory_test\n\nimport \"testing\"\n\nfunc BenchmarkX(b *testing.B) { b.Setenv(\"PATH\", \"/x\") }\n"},
			want:  []string{"envmut internal/inventory/bench_test.go"},
		},
		{
			name:  "os.Setenv, os.Unsetenv and os.Clearenv outside testsupport",
			plant: map[string]string{"internal/updates/mut.go": "package updates\n\nimport \"os\"\n\nfunc f() {\n\t_ = os.Setenv(\"HOME\", \"/x\")\n\t_ = os.Unsetenv(\"PATH\")\n\tos.Clearenv()\n}\n"},
			want:  []string{"envmut internal/updates/mut.go", "envmut internal/updates/mut.go", "envmut internal/updates/mut.go"},
		},
		{
			name:  "os.Setenv even in an env-allowed package",
			plant: map[string]string{"internal/shared/paths/set.go": "package paths\n\nimport \"os\"\n\nvar _ = os.Setenv(\"HOME\", \"/x\")\n"},
			want:  []string{"envmut internal/shared/paths/set.go"},
		},
		{
			name:  "a tools-tagged file outside internal/deps gets no exemption",
			plant: map[string]string{"internal/app/tools.go": "//go:build tools\n\npackage app\n\nimport _ \"github.com/spf13/cobra\"\n"},
			want:  []string{"cobra internal/app/tools.go"},
		},
		{
			name:  "packages that open sockets without importing net",
			plant: map[string]string{"internal/adapters/github/dial.go": "package github\n\nimport (\n\t_ \"crypto/tls\"\n\t_ \"golang.org/x/net/http2\"\n\t_ \"net/textproto\"\n)\n"},
			want:  []string{"network internal/adapters/github/dial.go", "network internal/adapters/github/dial.go", "network internal/adapters/github/dial.go"},
		},
		{
			name:  "forbidden blank imports followed by another blank import",
			plant: map[string]string{"internal/inventory/embed.go": "package inventory\n\nimport (\n\t_ \"net/http\"\n\t_ \"os/exec\"\n\t_ \"embed\"\n)\n"},
			want:  []string{"network internal/inventory/embed.go", "process internal/inventory/embed.go"},
		},
		{
			name:  "a forbidden import followed by one with the same last path element",
			plant: map[string]string{"internal/app/style.go": "package app\n\nimport (\n\t\"charm.land/lipgloss/v2\"\n\t\"math/rand/v2\"\n)\n\nvar _, _ = lipgloss.NewStyle, rand.Int\n"},
			want:  []string{"tui internal/app/style.go"},
		},
		{
			name:  "a dot import of os followed by another dot import",
			plant: map[string]string{"internal/updates/dot.go": "package updates\n\nimport (\n\t. \"os\"\n\t. \"strings\"\n)\n\nvar _, _ = Getpid, ToUpper\n"},
			want:  []string{"env internal/updates/dot.go"},
		},
		{
			name:  "os.Getenv next to a later import whose last path element is os",
			plant: map[string]string{"internal/updates/env.go": "package updates\n\nimport (\n\t\"os\"\n\n\t\"example.com/shadow/os\"\n)\n\nvar _ = os.Getenv\n"},
			want:  []string{"env internal/updates/env.go"},
		},
		{
			name: "graph or doctor importing the engine or an adapter",
			plant: map[string]string{
				"internal/graph/run.go":      "package graph\n\nimport _ \"skillhub/internal/manage/engine\"\n",
				"internal/doctor/files.go":   "package doctor\n\nimport _ \"skillhub/internal/adapters/fs\"\n",
				"internal/graph/sub/lock.go": "package sub\n\nimport _ \"skillhub/internal/adapters/skillslock\"\n",
			},
			want: []string{"readonly internal/doctor/files.go", "readonly internal/graph/run.go", "readonly internal/graph/sub/lock.go"},
		},
		{
			name: "an entry point importing the operations, the graph or doctor",
			plant: map[string]string{
				"internal/entrypoints/cli/remove.go": "package cli\n\nimport _ \"skillhub/internal/manage/remove\"\n",
				"internal/entrypoints/tui/graph.go":  "package tui\n\nimport (\n\t_ \"skillhub/internal/graph\"\n\t_ \"skillhub/internal/doctor\"\n)\n",
				"internal/entrypoints/text/eng.go":   "package text\n\nimport _ \"skillhub/internal/manage/engine\"\n",
			},
			want: []string{"facade internal/entrypoints/cli/remove.go", "facade internal/entrypoints/text/eng.go",
				"facade internal/entrypoints/tui/graph.go", "facade internal/entrypoints/tui/graph.go"},
		},
		{
			name: "tests and look-alike packages stay outside the new boundaries",
			plant: map[string]string{
				"internal/entrypoints/cli/fixture_test.go": testWithMain("cli_test", "skillhub/internal/graph/graphtest"),
				"internal/graph/graph_test.go":             testWithMain("graph_test", "skillhub/internal/adapters/fs"),
				"internal/entrypoints/cli/app.go":          "package cli\n\nimport _ \"skillhub/internal/app\"\n",
				"internal/graphite/run.go":                 "package graphite\n\nimport _ \"skillhub/internal/manage/engine\"\n",
				"internal/doctor/read.go":                  "package doctor\n\nimport _ \"skillhub/internal/managed\"\n",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, cleanModule)
			writeTree(t, root, tt.plant)

			report, err := arch.Check(root)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			var got []string
			for _, v := range report.Violations {
				got = append(got, v.Rule+" "+v.File)
			}
			sort.Strings(got)
			want := slices.Clone(tt.want)
			sort.Strings(want)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("violations:\n%s\nwant:\n%s\nfull report: %v", strings.Join(got, "\n"), strings.Join(want, "\n"), report.Violations)
			}
		})
	}
}

func TestCheckRefusesADirectoryWithoutAModule(t *testing.T) {
	if _, err := arch.Check(t.TempDir()); err == nil {
		t.Fatal("Check of a directory without go.mod returned no error")
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// testWithMain is a test file of package pkg that blank-imports imp and has
// the TestMain the testmain rule asks for.
func testWithMain(pkg, imp string) string {
	return "package " + pkg + "\n\nimport (\n\t\"testing\"\n\n\t_ \"" + imp + "\"\n\t\"skillhub/internal/testsupport\"\n)\n\n" +
		"func TestMain(m *testing.M) { testsupport.Main(m) }\n"
}
