package text

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Management state labels shown in the Skills panel under "En skillhub:".
const (
	TUIStateActive       = "activa"
	TUIStateDisabled     = "desactivada"
	TUIStateDisabledHand = "desactivada (a mano en OFF/)"
	TUIStateIgnored      = "ignorada"
)

// TUISkillsExclusions names in plain language what the inventory leaves out (S1).
const TUISkillsExclusions = "Fuera: skills de proveedor, plugins de Claude Code, gentle-ai y de proyecto."

// TUIPanelSkillhubState formats the management state of a skill in skillhub.
func TUIPanelSkillhubState(state string) string {
	return "En skillhub: " + state
}

// SkillVisibilityCounts are the visibility counts of the Skills summary line.
type SkillVisibilityCounts struct {
	Visible         int
	Hidden          int
	ActiveHidden    int
	DisabledVisible int
}

// TUISkillsVisibilitySummary formats the visibility summary line of the Skills tab,
// separate from the management state summary, keeping within 80 columns.
func TUISkillsVisibilitySummary(c SkillVisibilityCounts) string {
	base := fmt.Sprintf("Visibles: %d · sin agente: %d", c.Visible, c.Hidden)
	var notes []string
	if c.DisabledVisible > 0 {
		if c.DisabledVisible == 1 {
			notes = append(notes, "1 desactivada visible")
		} else {
			notes = append(notes, fmt.Sprintf("%d desactivadas visibles", c.DisabledVisible))
		}
	}
	if c.ActiveHidden > 0 {
		if c.ActiveHidden == 1 {
			notes = append(notes, "1 activa sin agente")
		} else {
			notes = append(notes, fmt.Sprintf("%d activas sin agente", c.ActiveHidden))
		}
	}
	if len(notes) > 0 {
		full := base + " (" + strings.Join(notes, ", ") + ")"
		if utf8.RuneCountInString(full) <= 80 {
			return full
		}
	}
	return base
}

// TUISkillDetailWhyHeader heads the per-agent visibility explanation in the full skill view.
const TUISkillDetailWhyHeader = "Por qué la ven los agentes:"

// TUISightingReason explains in plain Spanish why an agent can see a skill.
func TUISightingReason(kind string) string {
	switch kind {
	case "direct":
		return "directamente, sin enlace"
	case "link":
		return "por un enlace"
	case "copy":
		return "por una copia propia"
	case "indeterminate":
		return "indeterminado (carpeta o enlaces sin verificar)"
	default:
		return kind
	}
}

// TUIPanelWhyVisible formats one grouped reason line under "Disponible en:" in the Skills panel.
func TUIPanelWhyVisible(agents, reason string) string {
	return "  " + agents + ": " + reason
}
