// Package arch checks the architecture rules of docs/design.md section 5 by
// parsing every Go file of the module, test files and build-tagged files
// included. The tests in this package run it over the repository; they are
// the architecture, so a failure means the code must change, not the rule.
//
// Rules:
//   - process: os/exec, os.StartProcess and the syscall exec functions only
//     in internal/adapters/proc.
//   - tui: Bubble Tea, Bubbles, Lip Gloss and teatest only under
//     internal/entrypoints/tui.
//   - cobra: Cobra and pflag only under internal/entrypoints/cli.
//   - network: net, net/http and the other network packages nowhere.
//   - env: environment and home lookups only in cmd/skillhub,
//     internal/shared/paths, internal/adapters/proc and internal/testsupport.
//   - envmut: calls that change the process environment (os.Setenv,
//     os.Unsetenv, os.Clearenv, t.Setenv and b.Setenv or f.Setenv) only in
//     internal/testsupport. The check is syntactic: any selector named
//     Setenv, Unsetenv or Clearenv counts, whatever its receiver. A test that
//     changes HOME or PATH would switch the safety net off.
//   - testsupport: only _test.go files and internal/testsupport itself import
//     internal/testsupport.
//   - testmain: every package with tests has an untagged TestMain that calls
//     testsupport.Main.
//   - deps: internal/deps holds only files tagged "tools"; those files, and
//     no others, are exempt from the import rules.
//   - readonly: the non-test files of internal/graph and internal/doctor
//     import neither internal/manage (the engine and the planners) nor any
//     adapter: they only observe (docs/design-v2.md section 15, point 8).
//   - facade: the non-test files of internal/entrypoints import neither
//     internal/manage, internal/graph nor internal/doctor; the version 2
//     operations reach them only through internal/app (section 15, point 7).
package arch

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Violation is one broken rule. File is slash-separated and relative to the
// module root; for the testmain rule it is the package directory.
type Violation struct {
	Rule   string
	File   string
	Detail string
}

func (v Violation) String() string { return fmt.Sprintf("[%s] %s: %s", v.Rule, v.File, v.Detail) }

// Report is the result of a check. Packages lists every directory that holds
// Go files, slash-separated and relative to the root.
type Report struct {
	Packages   []string
	Violations []Violation
}

const depsDir = "internal/deps"

// importRule restricts a set of import paths to some directories.
type importRule struct {
	name     string
	prefixes []string // an import matches a prefix exactly or as prefix + "/"
	allowed  []string // directories (and their subdirectories) allowed to import
}

var importRules = []importRule{
	{name: "process", prefixes: []string{"os/exec"}, allowed: []string{"internal/adapters/proc"}},
	{name: "tui", prefixes: []string{
		"charm.land/bubbletea", "charm.land/bubbles", "charm.land/lipgloss",
		"github.com/charmbracelet/bubbletea", "github.com/charmbracelet/bubbles", "github.com/charmbracelet/lipgloss",
		"github.com/charmbracelet/x/exp/teatest",
	}, allowed: []string{"internal/entrypoints/tui"}},
	{name: "cobra", prefixes: []string{"github.com/spf13/cobra", "github.com/spf13/pflag"}, allowed: []string{"internal/entrypoints/cli"}},
	// crypto/tls, net/textproto and golang.org/x/net open sockets without
	// the importer naming net itself.
	{name: "network", prefixes: []string{"net/http", "net/rpc", "net/smtp", "net/textproto", "crypto/tls", "golang.org/x/net"}, allowed: nil},
	{name: "env", prefixes: []string{"os/user"}, allowed: envAllowed},
}

// boundaryRule forbids some packages of the module, given relative to the
// module root, in the non-test files of some directories.
type boundaryRule struct {
	name      string
	dirs      []string // directories (and their subdirectories) the rule binds
	forbidden []string // module-relative package prefixes
}

var boundaryRules = []boundaryRule{
	{name: "readonly", dirs: []string{"internal/graph", "internal/doctor"}, forbidden: []string{"internal/manage", "internal/adapters"}},
	{name: "facade", dirs: []string{"internal/entrypoints"}, forbidden: []string{"internal/manage", "internal/graph", "internal/doctor"}},
}

// exactNetwork is matched exactly: net/url and net/netip parse values and
// reach nothing.
const exactNetwork = "net"

var envAllowed = []string{"cmd/skillhub", "internal/shared/paths", "internal/adapters/proc", "internal/testsupport"}

// testsupportDir is the only place allowed to change the process environment
// and the only non-test code allowed to import the test harness.
const testsupportDir = "internal/testsupport"

// envMutators are the selector names of the envmut rule.
var envMutators = []string{"Setenv", "Unsetenv", "Clearenv"}

// selectorRule restricts package-level identifiers to some directories.
type selectorRule struct {
	name    string
	pkg     string
	names   []string
	allowed []string
}

var selectorRules = []selectorRule{
	{name: "process", pkg: "os", names: []string{"StartProcess"}, allowed: []string{"internal/adapters/proc"}},
	{name: "process", pkg: "syscall", names: []string{"Exec", "ForkExec", "StartProcess"}, allowed: []string{"internal/adapters/proc"}},
	{name: "env", pkg: "os", names: []string{"Getenv", "LookupEnv", "Environ", "ExpandEnv", "UserHomeDir", "UserConfigDir", "UserCacheDir"}, allowed: envAllowed},
	{name: "env", pkg: "syscall", names: []string{"Getenv", "Environ"}, allowed: envAllowed},
}

// Check checks the module rooted at root, which must hold a go.mod.
func Check(root string) (Report, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return Report{}, err
	}
	testsupportPath := module + "/internal/testsupport"

	var report Report
	tests := map[string]*testInfo{} // by package directory
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if !slices.Contains(report.Packages, dir) {
			report.Packages = append(report.Packages, dir)
		}
		f, err := parseFile(path, rel, dir)
		if err != nil {
			return err
		}
		report.Violations = append(report.Violations, f.check(module, testsupportPath)...)
		if f.isTest {
			info := tests[dir]
			if info == nil {
				info = &testInfo{}
				tests[dir] = info
			}
			info.hasTests = true
			info.hasMain = info.hasMain || f.callsTestsupportMain(testsupportPath)
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	for _, dir := range report.Packages {
		if info := tests[dir]; info != nil && info.hasTests && !info.hasMain {
			report.Violations = append(report.Violations, Violation{
				Rule: "testmain", File: dir,
				Detail: "package has tests but no untagged TestMain that calls testsupport.Main",
			})
		}
	}
	return report, nil
}

type testInfo struct{ hasTests, hasMain bool }

// skipDir mirrors the directories the go tool ignores.
func skipDir(name string) bool {
	return name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func modulePath(goMod string) (string, error) {
	file, err := os.Open(goMod)
	if err != nil {
		return "", fmt.Errorf("arch: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("arch: go.mod has no module line")
}

type goFile struct {
	rel, dir   string
	isTest     bool
	constraint string // the //go:build expression, or ""
	ast        *ast.File
	imports    []importRef // every import, in source order
}

// importRef is one import of a file. Imports are kept as a list because
// several can share a local name: every blank import is "_", every dot import
// is ".", and an unaliased import is named after its last path element, which
// is only a guess at the package name.
type importRef struct {
	local, path string
}

// refersTo reports whether the file imports path under the local name local.
func (f *goFile) refersTo(local, path string) bool {
	return slices.Contains(f.imports, importRef{local: local, path: path})
}

func parseFile(path, rel, dir string) (*goFile, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("arch: %w", err)
	}
	f := &goFile{
		rel: rel, dir: dir, ast: parsed,
		isTest: strings.HasSuffix(rel, "_test.go"),
	}
	for _, group := range parsed.Comments {
		if group.Pos() >= parsed.Package {
			break
		}
		for _, c := range group.List {
			if constraint.IsGoBuild(c.Text) {
				if expr, err := constraint.Parse(c.Text); err == nil {
					f.constraint = expr.String()
				}
			}
		}
	}
	for _, spec := range parsed.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		f.imports = append(f.imports, importRef{local: name, path: importPath})
	}
	return f, nil
}

func within(dir string, allowed []string) bool {
	for _, a := range allowed {
		if dir == a || strings.HasPrefix(dir, a+"/") {
			return true
		}
	}
	return false
}

func matchesPrefix(importPath, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}

func (f *goFile) violation(rule, format string, args ...any) Violation {
	return Violation{Rule: rule, File: f.rel, Detail: fmt.Sprintf(format, args...)}
}

func (f *goFile) check(module, testsupportPath string) []Violation {
	var out []Violation
	inDeps := within(f.dir, []string{depsDir})
	if inDeps && f.constraint != "tools" {
		out = append(out, f.violation("deps", "files in %s must carry exactly //go:build tools", depsDir))
	}
	// The tools file pins dependencies and never enters a normal build.
	exempt := inDeps && f.constraint == "tools"

	for _, imp := range f.imports {
		local, importPath := imp.local, imp.path
		if !exempt {
			if importPath == exactNetwork {
				out = append(out, f.violation("network", "imports %q; every network call goes through gh", importPath))
			}
			for _, rule := range importRules {
				for _, prefix := range rule.prefixes {
					if matchesPrefix(importPath, prefix) && !within(f.dir, rule.allowed) {
						out = append(out, f.violation(rule.name, "imports %q, allowed only in %v", importPath, rule.allowed))
					}
				}
			}
			out = append(out, f.boundaries(module, importPath)...)
		}
		if local == "." && (importPath == "os" || importPath == "syscall") && !within(f.dir, envAllowed) {
			out = append(out, f.violation("env", "dot import of %q hides environment and process calls", importPath))
		}
		if !f.isTest && matchesPrefix(importPath, testsupportPath) && !within(f.dir, []string{testsupportDir}) {
			out = append(out, f.violation("testsupport", "non-test code imports %q; only _test.go files may", importPath))
		}
	}

	ast.Inspect(f.ast, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if slices.Contains(envMutators, sel.Sel.Name) && !within(f.dir, []string{testsupportDir}) {
			out = append(out, f.violation("envmut", "calls %s, which changes the process environment; allowed only in %s", sel.Sel.Name, testsupportDir))
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		for _, rule := range selectorRules {
			if f.refersTo(ident.Name, rule.pkg) && slices.Contains(rule.names, sel.Sel.Name) && !within(f.dir, rule.allowed) {
				out = append(out, f.violation(rule.name, "uses %s.%s, allowed only in %v", rule.pkg, sel.Sel.Name, rule.allowed))
			}
		}
		return true
	})
	return out
}

// boundaries checks one import of a non-test file against boundaryRules.
func (f *goFile) boundaries(module, importPath string) []Violation {
	if f.isTest {
		return nil
	}
	var out []Violation
	for _, rule := range boundaryRules {
		if !within(f.dir, rule.dirs) {
			continue
		}
		for _, pkg := range rule.forbidden {
			if matchesPrefix(importPath, module+"/"+pkg) {
				out = append(out, f.violation(rule.name, "imports %q, which %v may not import", importPath, rule.dirs))
			}
		}
	}
	return out
}

// callsTestsupportMain reports whether f is an untagged test file whose
// TestMain calls Main from the testsupport package (or, inside testsupport's
// own internal tests, the bare Main).
func (f *goFile) callsTestsupportMain(testsupportPath string) bool {
	if !f.isTest || f.constraint != "" {
		return false
	}
	inTestsupport := f.ast.Name.Name == "testsupport" && strings.HasSuffix(testsupportPath, "/"+f.dir)
	for _, decl := range f.ast.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return !found
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if ident, ok := fun.X.(*ast.Ident); ok && fun.Sel.Name == "Main" && f.refersTo(ident.Name, testsupportPath) {
					found = true
				}
			case *ast.Ident:
				if inTestsupport && fun.Name == "Main" {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
