package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
)

// writeJSON writes v as indented JSON with a final newline.
func writeJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// table writes tab-separated rows aligned in columns.
func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0) }

// shortSHA is the first seven characters of a SHA, as git shows it.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return msgNone
	}
	return sha
}

// formatTime shows a moment in the local time zone.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return msgNone
	}
	return t.Local().Format("2006-01-02 15:04")
}

// formatDuration drops the zero units Go prints, so 1h0m0s reads 1h.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return msgYes
	}
	return msgNo
}

func orNone(s string) string {
	if s == "" {
		return msgNone
	}
	return s
}

func opKind(k domain.OperationKind) string { return text.OperationKind(k) }

func labelNames(labels []domain.Label) string {
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, text.Label(l))
	}
	return orNone(strings.Join(names, ", "))
}

func issueNames(issues []domain.Issue) string {
	names := make([]string, 0, len(issues))
	for _, i := range issues {
		names = append(names, text.Issue(i))
	}
	return orNone(strings.Join(names, ", "))
}

// harnesses lists where a skill is present, the store first, without
// repeating a harness.
func harnesses(locs []domain.Location) string {
	var out []string
	for _, l := range locs {
		name := l.AgentID
		if name == domain.StoreAgentID {
			name = msgStore
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return orNone(strings.Join(out, ", "))
}

// isLocallyModified reports a candidate whose installed folder has the
// user's own edits.
func isLocallyModified(c domain.UpdateCandidate) bool {
	return slices.Contains(c.Labels, domain.LabelLocallyModified)
}
