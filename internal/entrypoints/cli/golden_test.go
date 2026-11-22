package cli_test

import (
	"testing"
)

// The previews of the version 2 commands, as the user reads them, pinned by
// golden files (testdata/*.golden; rewrite them with go test -update).
// The home is ~ and plan identifiers are <plan>.
func TestGoldenPreviews(t *testing.T) {
	tests := []struct {
		name  string
		setup func(m *machine)
		args  []string
		code  int
	}{
		{name: "disable", args: []string{"skills", "disable", "i-have-adhd", "--dry-run"}},
		{name: "unlink", args: []string{"skills", "unlink", "archify", "--from", "claude-code", "--dry-run"}},
		{name: "remove", args: []string{"skills", "remove", "orchestration", "--dry-run"}},
		{name: "adopt-off", args: []string{"skills", "adopt-off", "--dry-run"}},
		{
			name:  "install",
			setup: func(m *machine) { m.publishTool() },
			args:  []string{"skills", "install", "acme/tools", "--to", "claude-code", "--dry-run"},
		},
		{
			name: "enable-neighbour",
			setup: func(m *machine) {
				m.approve("skills", "disable", "agent-browser")
				m.approve("skills", "disable", "archify")
			},
			args: []string{"skills", "enable", "agent-browser", "--dry-run"},
			code: 2,
		},
		{
			name:  "doctor-fix",
			setup: func(m *machine) { m.ghostLink() },
			args:  []string{"doctor", "fix", "broken_link:.claude/skills/ghost", "--dry-run"},
		},
		{name: "recover-pending", setup: func(m *machine) { m.pendingV2() }, args: []string{"recover", "--dry-run"}},
		{name: "yes-without-plan", args: []string{"skills", "remove", "grill-me", "--yes"}, code: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			if tt.setup != nil {
				tt.setup(m)
			}
			r := m.run("", tt.args...)
			if r.code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, tt.code, r.stdout, r.stderr)
			}
			golden(t, "preview-"+tt.name, m.normalize("$ skillhub "+join(tt.args)+"\n"+r.stdout+"--- stderr ---\n"+r.stderr))
		})
	}
}

func join(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
