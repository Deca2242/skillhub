package text_test

import (
	"testing"

	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

// names collects, for one enum, the Spanish name of every value.
func names[T ~string](values []T, name func(T) string) map[string]string {
	out := map[string]string{}
	for _, v := range values {
		out[string(v)] = name(v)
	}
	return out
}

func TestEveryEnumValueHasASpanishName(t *testing.T) {
	enums := map[string]map[string]string{
		"State":          names(domain.States(), text.State),
		"Issue":          names(domain.Issues(), text.Issue),
		"Label":          names(domain.Labels(), text.Label),
		"CandidateState": names(domain.CandidateStates(), text.CandidateState),
		"OriginKind":     names(domain.OriginKinds(), text.OriginKind),
		"LocationKind":   names(domain.LocationKinds(), text.LocationKind),
		"OperationKind":  names(domain.OperationKinds(), text.OperationKind),
		"HistoryAction":  names(domain.HistoryActions(), text.HistoryAction),
		"SnapshotCause":  names(domain.SnapshotCauses(), text.SnapshotCause),
		"BlockReason":    names(domain.BlockReasons(), text.BlockReason),
		"FileChangeKind": names(domain.FileChangeKinds(), text.FileChangeKind),
	}
	for enum, values := range enums {
		t.Run(enum, func(t *testing.T) {
			seen := map[string]string{}
			for code, name := range values {
				if name == "" || name == code {
					t.Errorf("%s %q has no Spanish name (got %q)", enum, code, name)
				}
				if other, dup := seen[name]; dup {
					t.Errorf("%s %q and %q share the name %q", enum, code, other, name)
				}
				seen[name] = code
			}
		})
	}
}

func TestKnownNames(t *testing.T) {
	tests := []struct {
		got, want string
	}{
		{text.State(domain.StateUpdateAvailable), "actualización disponible"},
		{text.Issue(domain.IssueLocallyModified), "modificada localmente"},
		{text.Label(domain.LabelSuspiciousText), "texto sospechoso"},
		{text.CandidateState(domain.CandidateBlocked), "bloqueada"},
		{text.OriginKind(domain.OriginNone), "sin origen"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("name = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestAnUnknownValueShowsItsCode(t *testing.T) {
	if got := text.State(domain.State("raro")); got != "raro" {
		t.Fatalf("State(unknown) = %q, want the code itself", got)
	}
}
