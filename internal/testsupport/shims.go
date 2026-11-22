package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// shimScript makes a tool fail loudly and record the call in the sandbox log,
// so Main can fail the run even when the caller ignored the failure.
// Arguments: tool name, quoted log path, exit code.
const shimScript = `#!/bin/sh
printf '%%s\n' "%[1]s $*" >> %[2]s
echo "skillhub testsupport: the real '%[1]s' is blocked in tests; use the fake ProcessRunner (args: $*)" >&2
exit %[3]d
`

func writeShims(dir, logPath string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, tool := range blockedTools {
		if realTools[tool] {
			continue
		}
		script := fmt.Sprintf(shimScript, tool, shellQuote(logPath), shimExitCode)
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
