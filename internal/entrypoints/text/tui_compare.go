package text

import (
	"fmt"
	"strings"

	"skillhub/internal/app"
)

// Texts for the read-only agent comparison view (INV9, S15 #7). Neutral Spanish (S11).
const (
	TUIKeysCompareAgents    = "↑/↓ desplazar • esc volver al filtro"
	TUICompareCommon        = "En común"
	TUICompareIndeterminate = "Visibilidad indeterminada"
	TUICompareEmptyList     = "(ninguna)"
	TUICompareSingleAgent   = "Solo hay un agente detectado en este equipo; hace falta otro para comparar."
	TUICompareSameAgent     = "Elegiste el mismo agente dos veces; todas sus skills aparecen en común."

	// Texts for the read-only homonym skill comparison view (INV11, S15 #14).
	TUIKeyCompareSkills       = "m comparar"
	TUIActionCompare          = "[m] Comparar"
	TUIKeysCompareSkills      = "↑/↓ desplazar • PgUp/PgDn página • esc volver"
	TUIKeysSkillDetailCompare = TUIKeysSkillDetail + " • " + TUIKeyCompareSkills

	TUISkillComparePaths      = "  Rutas:"
	TUISkillCompareNoPaths    = "  Rutas: (ninguna)"
	TUISkillCompareDiffHeader = "Archivos que difieren:"
	TUISkillCompareIdentical  = "  Las dos copias tienen el mismo contenido."
	TUISkillCompareNoAgents   = "ningún agente"
	TUISkillCompareFileBinary = "archivo binario (no se muestra como texto)"
	TUISkillCompareFileEmpty  = "sin cambios de texto"
)

// TUISkillCompareTitle heads the homonym skill comparison view.
func TUISkillCompareTitle(name string) string {
	return fmt.Sprintf("Comparar homónimas: %s", SafeLine(name))
}

// TUISkillCompareSide labels one compared skill with its letter and ID.
func TUISkillCompareSide(label, id string) string {
	return fmt.Sprintf("%s: %s", label, SafeLine(id))
}

// TUISkillCompareAgents formats the readable agents that see one compared skill.
func TUISkillCompareAgents(agents string) string {
	if agents == "" {
		agents = TUISkillCompareNoAgents
	}
	return "  Agentes que la ven: " + SafeLine(agents)
}

// TUISkillCompareDiffs formats the file diffs of a SkillComparison with every
// third-party path and diff line escaped against terminal control sequences (A28).
func TUISkillCompareDiffs(diffs []app.FileDiff) string {
	if len(diffs) == 0 {
		return TUISkillCompareIdentical + "\n"
	}
	var b strings.Builder
	for _, d := range diffs {
		fmt.Fprintf(&b, "  %s  %s\n", FileChangeKind(d.Change), SafeLine(d.Path))
	}
	for _, d := range diffs {
		b.WriteString("\n")
		if d.Binary {
			fmt.Fprintf(&b, "%s: %s\n", SafeLine(d.Path), TUISkillCompareFileBinary)
			continue
		}
		if d.Unified == "" {
			fmt.Fprintf(&b, "%s: %s\n", SafeLine(d.Path), TUISkillCompareFileEmpty)
			continue
		}
		unified := SafeDiff(d.Path, d.Unified)
		b.WriteString(unified)
		if !strings.HasSuffix(unified, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TUICompareTitle heads the comparison view for two agents.
func TUICompareTitle(first, second string) string {
	return fmt.Sprintf("Comparar: %s y %s", first, second)
}

// TUICompareOnly labels the list of skills seen exclusively by one agent.
func TUICompareOnly(agent string) string {
	return fmt.Sprintf("Solo en %s", agent)
}

// TUICompareSection formats a comparison list heading with its item count.
func TUICompareSection(label string, count int) string {
	return fmt.Sprintf("%s (%d)", label, count)
}

// TUICompareFirstMarked shows which agent is marked first while choosing the second.
func TUICompareFirstMarked(agent string) string {
	return fmt.Sprintf("1.º para comparar: %s (elige el segundo con m)", agent)
}

// TUICompareScrollHint tells which lines of a long comparison are visible.
func TUICompareScrollHint(from, to, total int) string {
	return fmt.Sprintf("Mostrando %d–%d de %d líneas (↑/↓ para desplazar)", from, to, total)
}

// TUICompareIndeterminateItem formats an indeterminate skill with its affected agents and reason.
func TUICompareIndeterminateItem(skillName string, agents []string, reason string) string {
	return fmt.Sprintf("%s (%s: %s)", skillName, JoinNames(agents), reason)
}
