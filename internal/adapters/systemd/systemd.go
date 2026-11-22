// Package systemd implements ports.BackgroundService with two systemd user
// units in paths.Paths.SystemdUserDir(): skillhub-check.service, which
// runs "skillhub check --if-due --notify", and skillhub-check.timer,
// which starts it shortly after the timer starts (at login, or when it is
// enabled) and every 15 minutes after that (docs/design.md section 8). The
// check interval the user picks lives in settings.toml, so changing it never
// rewrites a unit.
//
// The units are written through ports.FileOps and systemctl runs through
// ports.ProcessRunner. Enabling the timer is a user action: no test or other
// command calls Enable on the real machine.
package systemd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"skillhub/internal/shared/paths"
	"skillhub/internal/shared/ports"
)

var _ ports.BackgroundService = (*Service)(nil)

// Unit file names.
const (
	ServiceUnit = "skillhub-check.service"
	TimerUnit   = "skillhub-check.timer"
)

// unitPerm is the mode of both unit files.
const unitPerm = 0o644

// header opens both units, for whoever finds them in the systemd directory.
const header = `# Generado por skillhub con "skillhub background enable".
# "skillhub background disable" lo desactiva y lo borra.

`

// timerContent is the timer unit. OnActiveSec counts from the timer's own
// start, which is the login for an enabled timer and the moment of
// enable --now; OnUnitActiveSec counts from the service's last start.
const timerContent = header + `[Unit]
Description=skillhub: revisión periódica de actualizaciones de skills

[Timer]
OnActiveSec=2min
OnUnitActiveSec=15min
Unit=` + ServiceUnit + `

[Install]
WantedBy=timers.target
`

// Service manages the background check's user units.
type Service struct {
	dir    string
	runner ports.ProcessRunner
	files  ports.FileOps
}

// New builds a Service that keeps its units in p.SystemdUserDir(), writes
// them through files and runs systemctl through r.
func New(p paths.Paths, r ports.ProcessRunner, files ports.FileOps) *Service {
	return &Service{dir: p.SystemdUserDir(), runner: r, files: files}
}

func (s *Service) servicePath() string { return filepath.Join(s.dir, ServiceUnit) }
func (s *Service) timerPath() string   { return filepath.Join(s.dir, TimerUnit) }

// Enable implements ports.BackgroundService. binary must be an absolute
// path; it is what the service runs. Both units are written before systemctl
// runs, then the user manager is reloaded and the timer enabled and started.
func (s *Service) Enable(ctx context.Context, binary string) error {
	service, err := serviceContent(binary)
	if err != nil {
		return err
	}
	if err := s.files.WriteFileAtomic(s.servicePath(), []byte(service), unitPerm); err != nil {
		return fmt.Errorf("write %s: %w", ServiceUnit, err)
	}
	if err := s.files.WriteFileAtomic(s.timerPath(), []byte(timerContent), unitPerm); err != nil {
		return fmt.Errorf("write %s: %w", TimerUnit, err)
	}
	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return s.systemctl(ctx, "enable", "--now", TimerUnit)
}

// Disable implements ports.BackgroundService. It disables and stops the
// timer, removes both units and reloads the user manager. Without units it
// does nothing; when the timer cannot be disabled, both units stay.
func (s *Service) Disable(ctx context.Context) error {
	hasService, err := s.exists(s.servicePath())
	if err != nil {
		return err
	}
	hasTimer, err := s.exists(s.timerPath())
	if err != nil {
		return err
	}
	if !hasService && !hasTimer {
		return nil
	}
	if hasTimer {
		if err := s.systemctl(ctx, "disable", "--now", TimerUnit); err != nil {
			return err
		}
	}
	for _, path := range []string{s.timerPath(), s.servicePath()} {
		if err := s.files.RemoveTree(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return s.systemctl(ctx, "daemon-reload")
}

// Status implements ports.BackgroundService. Installed needs both units;
// without them systemctl is not asked.
func (s *Service) Status(ctx context.Context) (ports.BackgroundStatus, error) {
	hasService, err := s.exists(s.servicePath())
	if err != nil {
		return ports.BackgroundStatus{}, err
	}
	hasTimer, err := s.exists(s.timerPath())
	if err != nil {
		return ports.BackgroundStatus{}, err
	}
	if !hasService || !hasTimer {
		return ports.BackgroundStatus{}, nil
	}
	enabled, err := s.query(ctx, "is-enabled", enabledStates)
	if err != nil {
		return ports.BackgroundStatus{}, err
	}
	active, err := s.query(ctx, "is-active", activeStates)
	if err != nil {
		return ports.BackgroundStatus{}, err
	}
	return ports.BackgroundStatus{Installed: true, Enabled: enabled, Active: active}, nil
}

// enabledStates and activeStates map every state systemctl is-enabled and
// is-active print to whether it counts as enabled or active.
var (
	enabledStates = map[string]bool{
		"enabled": true, "enabled-runtime": true,
		"linked": false, "linked-runtime": false, "alias": false, "masked": false, "masked-runtime": false,
		"static": false, "indirect": false, "disabled": false, "generated": false, "transient": false,
		"bad": false, "not-found": false,
	}
	activeStates = map[string]bool{
		"active": true, "reloading": true, "refreshing": true,
		"inactive": false, "failed": false, "activating": false, "deactivating": false, "maintenance": false,
	}
)

// query runs systemctl --user verb on the timer. Both verbs exit non-zero
// for a negative answer, so a known state on stdout is the answer whatever
// the exit code; anything else is an error.
func (s *Service) query(ctx context.Context, verb string, states map[string]bool) (bool, error) {
	res, err := s.run(ctx, verb, TimerUnit)
	if err != nil {
		return false, err
	}
	state := strings.TrimSpace(string(res.Stdout))
	if value, known := states[state]; known {
		return value, nil
	}
	return false, exitError(verb, res)
}

func (s *Service) systemctl(ctx context.Context, args ...string) error {
	res, err := s.run(ctx, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return exitError(strings.Join(args, " "), res)
	}
	return nil
}

func (s *Service) run(ctx context.Context, args ...string) (ports.Result, error) {
	res, err := s.runner.Run(ctx, ports.Command{Name: "systemctl", Args: append([]string{"--user"}, args...)})
	if err != nil {
		return ports.Result{}, fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return res, nil
}

func (s *Service) exists(path string) (bool, error) {
	ok, err := s.files.Exists(path)
	if err != nil {
		return false, fmt.Errorf("check %s: %w", path, err)
	}
	return ok, nil
}

func exitError(what string, res ports.Result) error {
	msg := strings.TrimSpace(string(res.Stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(res.Stdout))
	}
	return fmt.Errorf("systemctl --user %s exited %d: %s", what, res.ExitCode, msg)
}

// serviceContent is the service unit that runs binary.
func serviceContent(binary string) (string, error) {
	quoted, err := quoteExecPath(binary)
	if err != nil {
		return "", err
	}
	return header + `[Unit]
Description=skillhub: revisión de actualizaciones de skills

[Service]
Type=oneshot
ExecStart=` + quoted + ` check --if-due --notify
TimeoutStartSec=10min
`, nil
}

// unsafeExecChars are the characters systemd refuses in an executable path
// ("Executable path contains special characters", checked with
// systemd-analyze verify of systemd 261).
const unsafeExecChars = `"'\*?[]`

// quoteExecPath quotes an absolute path for ExecStart. Inside double quotes
// a space is kept and the specifier sign % is doubled; systemd does not
// expand $ in the executable. A path systemd would refuse, or that a unit
// line cannot carry, is refused here.
func quoteExecPath(binary string) (string, error) {
	if !filepath.IsAbs(binary) {
		return "", fmt.Errorf("systemd: binary %q is not an absolute path", binary)
	}
	if !utf8.ValidString(binary) || strings.ContainsFunc(binary, unicode.IsControl) {
		return "", fmt.Errorf("systemd: binary path %q has a character a unit cannot carry", binary)
	}
	if strings.ContainsAny(binary, unsafeExecChars) {
		return "", fmt.Errorf("systemd: binary path %q has a character systemd refuses in an executable (one of %s)", binary, unsafeExecChars)
	}
	return `"` + strings.ReplaceAll(binary, "%", "%%") + `"`, nil
}
