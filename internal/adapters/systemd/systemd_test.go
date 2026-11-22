package systemd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"skillhub/internal/adapters/fs"
	"skillhub/internal/adapters/proc"
	"skillhub/internal/adapters/systemd"
	"skillhub/internal/shared/ports"
	"skillhub/internal/testsupport"
	"skillhub/internal/testsupport/fakes"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func systemctl(args ...string) ports.Command {
	return ports.Command{Name: "systemctl", Args: append([]string{"--user"}, args...)}
}

var (
	reloadCmd    = systemctl("daemon-reload")
	enableCmd    = systemctl("enable", "--now", "skillhub-check.timer")
	disableCmd   = systemctl("disable", "--now", "skillhub-check.timer")
	isEnabledCmd = systemctl("is-enabled", "skillhub-check.timer")
	isActiveCmd  = systemctl("is-active", "skillhub-check.timer")
)

func ok(stdout string) ports.Result { return ports.Result{Stdout: []byte(stdout)} }

// env is one fixture home with the real FileOps and a fake systemctl.
type env struct {
	home    *testsupport.Home
	runner  *fakes.ProcessRunner
	service *systemd.Service
	unitDir string
}

func newEnv(t *testing.T) env {
	t.Helper()
	home := testsupport.NewHome(t)
	runner := fakes.NewProcessRunner()
	p := home.Paths()
	return env{
		home:    home,
		runner:  runner,
		service: systemd.New(p, runner, fs.NewFileOps(p.Guard())),
		unitDir: p.SystemdUserDir(),
	}
}

func (e env) unit(name string) string { return filepath.Join(e.unitDir, name) }

// binary creates an executable in the fixture home and returns its path.
func (e env) binary(rel string) string {
	return e.home.WriteFileMode(rel, "#!/bin/sh\nexit 0\n", 0o755)
}

func (e env) installUnits(t *testing.T) {
	t.Helper()
	e.home.WriteFile(".config/systemd/user/skillhub-check.service", "[Service]\n")
	e.home.WriteFile(".config/systemd/user/skillhub-check.timer", "[Timer]\n")
}

func commandLines(cmds []ports.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.TrimSpace(c.Name+" "+strings.Join(c.Args, " ")))
	}
	return out
}

func lines(cmds ...ports.Command) []string { return commandLines(cmds) }

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestEnableWritesBothUnitsAndStartsTheTimer(t *testing.T) {
	e := newEnv(t)
	bin := e.binary(".local/bin/skillhub")
	e.runner.Expect(reloadCmd, ok(""), nil)
	e.runner.Expect(enableCmd, ok(""), nil)

	if err := e.service.Enable(context.Background(), bin); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}

	wantService := `# Generado por skillhub con "skillhub background enable".
# "skillhub background disable" lo desactiva y lo borra.

[Unit]
Description=skillhub: revisión de actualizaciones de skills

[Service]
Type=oneshot
ExecStart="` + bin + `" check --if-due --notify
TimeoutStartSec=10min
`
	wantTimer := `# Generado por skillhub con "skillhub background enable".
# "skillhub background disable" lo desactiva y lo borra.

[Unit]
Description=skillhub: revisión periódica de actualizaciones de skills

[Timer]
OnActiveSec=2min
OnUnitActiveSec=15min
Unit=skillhub-check.service

[Install]
WantedBy=timers.target
`
	for name, want := range map[string]string{"skillhub-check.service": wantService, "skillhub-check.timer": wantTimer} {
		got, err := os.ReadFile(e.unit(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s =\n%s\nwant\n%s", name, got, want)
		}
		info, err := os.Stat(e.unit(name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, want 0644", name, info.Mode().Perm())
		}
	}
	if got, want := commandLines(e.runner.Commands()), lines(reloadCmd, enableCmd); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	for _, c := range e.runner.Commands() {
		if c.Dir != "" || len(c.Env) != 0 || len(c.Stdin) != 0 {
			t.Errorf("%s: Dir %q, Env %v, Stdin %q; want none", c.Name, c.Dir, c.Env, c.Stdin)
		}
	}
}

func TestEnableQuotesTheBinaryForSystemd(t *testing.T) {
	e := newEnv(t)
	bin := e.binary(`odd dir/100% ñandú/$HOME;x/skillhub`)
	e.runner.Expect(reloadCmd, ok(""), nil)
	e.runner.Expect(enableCmd, ok(""), nil)

	if err := e.service.Enable(context.Background(), bin); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}

	got, err := os.ReadFile(e.unit("skillhub-check.service"))
	if err != nil {
		t.Fatal(err)
	}
	// Only the specifier sign is doubled; systemd keeps $ in the executable.
	escaped := strings.ReplaceAll(bin, "%", "%%")
	if want := "\nExecStart=\"" + escaped + "\" check --if-due --notify\n"; !strings.Contains(string(got), want) {
		t.Errorf("service unit:\n%s\nwant the line %q", got, want)
	}
	verifyUnits(t, e.unitDir)
}

func TestEnableRefusesABadBinaryPath(t *testing.T) {
	bad := []string{
		"", "skillhub", "bin/skillhub", "/usr/bin/skill\nfolio", "/usr/bin/skill\x00folio", "/usr/bin/skill\xfffolio",
		// Characters systemd refuses in an executable path.
		`/opt/"x"/skillhub`, "/opt/x'y/skillhub", `/opt/x\y/skillhub`, "/opt/x*/skillhub", "/opt/x?/skillhub", "/opt/[x]/skillhub",
	}
	for _, bin := range bad {
		t.Run(bin, func(t *testing.T) {
			e := newEnv(t)

			if err := e.service.Enable(context.Background(), bin); err == nil {
				t.Fatalf("Enable(%q) succeeded, want an error", bin)
			}

			if exists(t, e.unitDir) {
				t.Errorf("Enable(%q) created %s", bin, e.unitDir)
			}
			if cmds := e.runner.Commands(); len(cmds) != 0 {
				t.Errorf("Enable(%q) ran %q, want nothing", bin, commandLines(cmds))
			}
		})
	}
}

func TestEnableFailures(t *testing.T) {
	boom := errors.New("cannot start")
	tests := []struct {
		name      string
		setup     func(*fakes.ProcessRunner)
		wantCmds  []string
		wantErr   error
		wantInErr string
	}{
		{
			name: "daemon-reload fails",
			setup: func(r *fakes.ProcessRunner) {
				r.Expect(reloadCmd, ports.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus: No medium found\n")}, nil)
			},
			wantCmds:  lines(reloadCmd),
			wantInErr: "Failed to connect to bus",
		},
		{
			name: "enable fails",
			setup: func(r *fakes.ProcessRunner) {
				r.Expect(reloadCmd, ok(""), nil)
				r.Expect(enableCmd, ports.Result{ExitCode: 1, Stderr: []byte("Failed to enable unit\n")}, nil)
			},
			wantCmds:  lines(reloadCmd, enableCmd),
			wantInErr: "Failed to enable unit",
		},
		{
			name:     "systemctl cannot start",
			setup:    func(r *fakes.ProcessRunner) { r.Expect(reloadCmd, ports.Result{}, boom) },
			wantCmds: lines(reloadCmd),
			wantErr:  boom,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e.runner)

			err := e.service.Enable(context.Background(), e.binary("bin/skillhub"))
			if err == nil {
				t.Fatal("Enable() succeeded, want an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("Enable() error = %v, want it to wrap %v", err, tt.wantErr)
			}
			if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("Enable() error = %v, want it to contain %q", err, tt.wantInErr)
			}
			if got := commandLines(e.runner.Commands()); !slices.Equal(got, tt.wantCmds) {
				t.Errorf("commands = %q, want %q", got, tt.wantCmds)
			}
		})
	}
}

func TestEnableStopsWhenAUnitCannotBeWritten(t *testing.T) {
	home := testsupport.NewHome(t)
	runner := fakes.NewProcessRunner()
	files := fakes.NewFileOps(home.Paths().Guard())
	boom := errors.New("disk full")
	files.Fail = func(c fakes.Call) error {
		if c.Op == "WriteFileAtomic" && strings.HasSuffix(c.Args[0], ".timer") {
			return boom
		}
		return nil
	}

	err := systemd.New(home.Paths(), runner, files).Enable(context.Background(), "/usr/bin/skillhub")
	if !errors.Is(err, boom) {
		t.Fatalf("Enable() error = %v, want it to wrap %v", err, boom)
	}
	if cmds := runner.Commands(); len(cmds) != 0 {
		t.Errorf("ran %q after a failed write, want nothing", commandLines(cmds))
	}
}

func TestDisable(t *testing.T) {
	e := newEnv(t)
	e.installUnits(t)
	e.runner.Expect(disableCmd, ok(""), nil)
	e.runner.Expect(reloadCmd, ok(""), nil)

	if err := e.service.Disable(context.Background()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}

	for _, name := range []string{"skillhub-check.service", "skillhub-check.timer"} {
		if exists(t, e.unit(name)) {
			t.Errorf("%s still exists after Disable", name)
		}
	}
	if got, want := commandLines(e.runner.Commands()), lines(disableCmd, reloadCmd); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestDisableWithoutUnitsDoesNothing(t *testing.T) {
	e := newEnv(t)

	if err := e.service.Disable(context.Background()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if cmds := e.runner.Commands(); len(cmds) != 0 {
		t.Errorf("ran %q, want nothing", commandLines(cmds))
	}
}

func TestDisableRemovesALeftoverServiceWithoutTimer(t *testing.T) {
	e := newEnv(t)
	e.home.WriteFile(".config/systemd/user/skillhub-check.service", "[Service]\n")
	e.runner.Expect(reloadCmd, ok(""), nil)

	if err := e.service.Disable(context.Background()); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if exists(t, e.unit("skillhub-check.service")) {
		t.Error("the leftover service unit still exists")
	}
	if got, want := commandLines(e.runner.Commands()), lines(reloadCmd); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestDisableKeepsTheUnitsWhenSystemctlFails(t *testing.T) {
	e := newEnv(t)
	e.installUnits(t)
	e.runner.Expect(disableCmd, ports.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus\n")}, nil)

	err := e.service.Disable(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("Disable() error = %v, want the systemctl failure", err)
	}
	for _, name := range []string{"skillhub-check.service", "skillhub-check.timer"} {
		if !exists(t, e.unit(name)) {
			t.Errorf("%s was removed although the timer could not be disabled", name)
		}
	}
	if got, want := commandLines(e.runner.Commands()), lines(disableCmd); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name      string
		enabled   ports.Result
		active    ports.Result
		want      ports.BackgroundStatus
		wantInErr string
	}{
		{"enabled and active", ok("enabled\n"), ok("active\n"), ports.BackgroundStatus{Installed: true, Enabled: true, Active: true}, ""},
		{"disabled and inactive", ports.Result{ExitCode: 1, Stdout: []byte("disabled\n")}, ports.Result{ExitCode: 3, Stdout: []byte("inactive\n")}, ports.BackgroundStatus{Installed: true}, ""},
		{"enabled at runtime, failed", ok("enabled-runtime\n"), ports.Result{ExitCode: 3, Stdout: []byte("failed\n")}, ports.BackgroundStatus{Installed: true, Enabled: true}, ""},
		{"masked", ports.Result{ExitCode: 1, Stdout: []byte("masked\n")}, ports.Result{ExitCode: 3, Stdout: []byte("inactive\n")}, ports.BackgroundStatus{Installed: true}, ""},
		{"no user bus", ports.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus: No medium found\n")}, ok("active\n"), ports.BackgroundStatus{}, "Failed to connect to bus"},
		{"is-active fails", ok("enabled\n"), ports.Result{ExitCode: 1, Stderr: []byte("Failed to get unit\n")}, ports.BackgroundStatus{}, "Failed to get unit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.installUnits(t)
			e.runner.ExpectAlways(isEnabledCmd, tt.enabled, nil)
			e.runner.ExpectAlways(isActiveCmd, tt.active, nil)

			got, err := e.service.Status(context.Background())
			if tt.wantInErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
					t.Fatalf("Status() = %+v, %v; want an error containing %q", got, err, tt.wantInErr)
				}
			} else if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Status() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestStatusKnowsEveryState checks every state systemctl documents for
// is-enabled and is-active: each is an answer, not an error.
func TestStatusKnowsEveryState(t *testing.T) {
	enabled := map[string]bool{
		"enabled": true, "enabled-runtime": true, "linked": false, "linked-runtime": false, "alias": false,
		"masked": false, "masked-runtime": false, "static": false, "indirect": false, "disabled": false,
		"generated": false, "transient": false, "bad": false, "not-found": false,
	}
	active := map[string]bool{
		"active": true, "reloading": true, "refreshing": true, "inactive": false, "failed": false,
		"activating": false, "deactivating": false, "maintenance": false,
	}
	for state, want := range enabled {
		e := newEnv(t)
		e.installUnits(t)
		e.runner.Expect(isEnabledCmd, ports.Result{ExitCode: 1, Stdout: []byte(state + "\n")}, nil)
		e.runner.Expect(isActiveCmd, ports.Result{ExitCode: 3, Stdout: []byte("inactive\n")}, nil)
		got, err := e.service.Status(context.Background())
		if err != nil || got.Enabled != want {
			t.Errorf("is-enabled %q: Status() = %+v, %v; want Enabled %v", state, got, err, want)
		}
	}
	for state, want := range active {
		e := newEnv(t)
		e.installUnits(t)
		e.runner.Expect(isEnabledCmd, ok("enabled\n"), nil)
		e.runner.Expect(isActiveCmd, ports.Result{ExitCode: 3, Stdout: []byte(state + "\n")}, nil)
		got, err := e.service.Status(context.Background())
		if err != nil || got.Active != want {
			t.Errorf("is-active %q: Status() = %+v, %v; want Active %v", state, got, err, want)
		}
	}
}

func TestStatusWithoutUnitsRunsNothing(t *testing.T) {
	for _, only := range []string{"", "skillhub-check.service", "skillhub-check.timer"} {
		t.Run("only "+only, func(t *testing.T) {
			e := newEnv(t)
			if only != "" {
				e.home.WriteFile(".config/systemd/user/"+only, "x\n")
			}

			got, err := e.service.Status(context.Background())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got != (ports.BackgroundStatus{}) {
				t.Errorf("Status() = %+v, want not installed", got)
			}
			if cmds := e.runner.Commands(); len(cmds) != 0 {
				t.Errorf("ran %q, want nothing", commandLines(cmds))
			}
		})
	}
}

func TestServiceIsABackgroundService(t *testing.T) {
	e := newEnv(t)
	var _ ports.BackgroundService = e.service
}

// TestUnitsPassSystemdAnalyzeVerify writes the units with Enable into a
// fixture home and checks them with the read-only systemd-analyze verify.
func TestUnitsPassSystemdAnalyzeVerify(t *testing.T) {
	e := newEnv(t)
	bin := e.binary("opt/skillhub 1.0/skillhub")
	e.runner.Expect(reloadCmd, ok(""), nil)
	e.runner.Expect(enableCmd, ok(""), nil)
	if err := e.service.Enable(context.Background(), bin); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}

	verifyUnits(t, e.unitDir)
}

// verifyUnits runs systemd-analyze --user verify on both units in dir and
// fails on a non-zero exit or on any message about them; systemd reports a
// bad setting as a warning and still exits 0. It skips when systemd-analyze
// is not installed.
func verifyUnits(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"skillhub-check.service", "skillhub-check.timer"} {
		if !exists(t, filepath.Join(dir, name)) {
			t.Fatalf("%s was not written in %s", name, dir)
		}
	}
	runtime := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := proc.New().Run(context.Background(), ports.Command{
		Name: "systemd-analyze",
		// The ./ makes systemd-analyze read the files rather than look the
		// names up.
		Args: []string{"--user", "verify", "--man=no", "./skillhub-check.service", "./skillhub-check.timer"},
		Dir:  dir,
		Env:  []string{"XDG_RUNTIME_DIR=" + runtime},
	})
	if err != nil && strings.Contains(err.Error(), "executable file not found") {
		t.Skipf("systemd-analyze is not installed: %v", err)
	}
	if err != nil {
		t.Fatalf("run systemd-analyze: %v", err)
	}
	output := string(res.Stdout) + string(res.Stderr)
	if res.ExitCode != 0 {
		t.Fatalf("systemd-analyze verify exited %d:\n%s", res.ExitCode, output)
	}
	// Other lines are about the host's own units.
	for line := range strings.Lines(output) {
		if strings.Contains(line, "skillhub-check.") {
			t.Errorf("systemd-analyze verify: %s", strings.TrimSpace(line))
		}
	}
}
