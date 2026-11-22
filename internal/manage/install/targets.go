package install

import (
	"slices"

	"skillhub/internal/data"
	"skillhub/internal/graph"
	"skillhub/internal/shared/domain"
)

// ProposeTargets proposes install targets (S7, decision 1): the harnesses
// the graph detected that have a link directory, joined with the preferred
// ones that are in the table and have one too, in table order. It only
// proposes: it never saves the preference, and Install uses the targets its
// caller passes, never these.
func ProposeTargets(g graph.Graph, harnesses []data.Harness, prefs domain.Preferences) []string {
	out := []string{}
	for _, h := range harnesses {
		if h.LinkDir == "" {
			continue
		}
		if slices.Contains(g.Harnesses, h.ID) || slices.Contains(prefs.Targets, h.ID) {
			out = append(out, h.ID)
		}
	}
	return out
}
