package app

import (
	"bytes"
	"path/filepath"
	"slices"
	"strconv"

	"skillhub/internal/shared/ports"
)

// harnessCommands maps the program name of a harness process to its
// harness. Node programs show up as node; their script's name is the second
// argument.
var harnessCommands = map[string]string{
	"claude":       "claude-code",
	"codex":        "codex",
	"pi":           "pi",
	"opencode":     "opencode",
	"gemini":       "gemini-cli",
	"qwen":         "qwen-code",
	"cursor-agent": "cursor",
}

// running lists the harness processes running now, reading the process
// directory through Dirs and Files: no process is started (docs/design-v2.md
// section 9). It is a warning only, so an unreadable directory or process
// is left out instead of failing the plan.
func (m *manager) running() []Process {
	dir := m.a.d.ProcDir
	if dir == "" {
		dir = "/proc"
	}
	entries, err := m.a.d.Dirs.Scan(dir)
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, h := range m.harnesses {
		known[h.ID] = true
	}
	var out []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name)
		if err != nil || pid <= 0 || e.Kind != ports.EntryDir {
			continue
		}
		cmd, harness := m.harnessOf(filepath.Join(dir, e.Name))
		if harness != "" && known[harness] {
			out = append(out, Process{PID: pid, Harness: harness, Command: cmd})
		}
	}
	slices.SortFunc(out, func(a, b Process) int { return a.PID - b.PID })
	return out
}

// harnessOf recognizes a process by its comm, then by the base name of its
// first two arguments.
func (m *manager) harnessOf(dir string) (cmd, harness string) {
	var names []string
	if comm, err := m.a.d.Files.ReadFile(filepath.Join(dir, "comm")); err == nil {
		names = append(names, string(bytes.TrimSpace(comm)))
	}
	if line, err := m.a.d.Files.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		args := bytes.Split(bytes.TrimRight(line, "\x00"), []byte{0})
		for i := 0; i < len(args) && i < 2; i++ {
			names = append(names, filepath.Base(string(args[i])))
		}
	}
	for _, n := range names {
		if h, ok := harnessCommands[n]; ok {
			return n, h
		}
	}
	return "", ""
}
