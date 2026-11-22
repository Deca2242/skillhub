package text

import (
	"fmt"
	"strings"

	"skillhub/internal/shared/domain"
)

// The texts of UX3 (S13, L85): four tabs with counters, Historial and
// Ajustes as shortcuts, the Skills filters and summary, the agent check
// boxes, undoing the last operation with z, the risk traffic light and the
// panel that guides. Neutral Spanish (S11).

// The four tabs and the two shortcuts of the header.
const (
	TUITabSkills  = "1 Skills"
	TUITabUpdates = "2 Actualizar"
	TUITabInstall = "3 Instalar"
	TUITabHealth  = "4 Salud"
	// TUIHeaderShortcuts follows the tabs when the terminal has room.
	TUIHeaderShortcuts = "h historial • c ajustes"
	// TUITitleHistory and TUITitleSettings name the shortcut open in the
	// header, after the tabs.
	TUITitleHistory  = "Historial"
	TUITitleSettings = "Ajustes"
)

// TUITab is a tab's label with its counter; n < 0 shows none.
func TUITab(label string, n int) string {
	if n < 0 {
		return label
	}
	return fmt.Sprintf("%s (%d)", label, n)
}

// SkillFilter is a filter of the Skills list by state, in the order f
// goes through them.
type SkillFilter int

// The filters of the Skills list.
const (
	FilterAll SkillFilter = iota
	FilterActive
	FilterDisabled
	FilterProblems
	FilterUpdates
	// FilterIgnored shows only the skills the user ignored (UX4); every
	// other filter leaves them out.
	FilterIgnored
	filterCount
)

// Next is the filter f goes to after this one.
func (f SkillFilter) Next() SkillFilter { return (f + 1) % filterCount }

var filterNames = map[SkillFilter]string{
	FilterActive:   "activas",
	FilterDisabled: "desactivadas",
	FilterProblems: "con problemas",
	FilterUpdates:  "con actualización",
	FilterIgnored:  "ignoradas",
}

// Name is how the title of the Skills list names the filter; "" for all.
func (f SkillFilter) Name() string { return filterNames[f] }

// TUISkillsListTitle heads the Skills list: the agent whose view is shown,
// if any, the state filter, and the rows shown.
func TUISkillsListTitle(agent string, f SkillFilter, n int) string {
	title := "Skills"
	if agent != "" {
		title = "Qué ve " + agent
	}
	if name := f.Name(); name != "" {
		title += " · " + name
	}
	return fmt.Sprintf("%s (%d)", title, n)
}

// TUISkillsNoneShown replaces the list when the filters keep no row.
const TUISkillsNoneShown = "Ninguna skill cumple el filtro. Pulsa f o g para cambiarlo."

// SkillCounts are the numbers of the summary line above the Skills list.
type SkillCounts struct {
	Total, Active, Disabled, Updates, Problems int
	// Ignored are the skills the ignore setting hides; Total leaves them out.
	Ignored int
}

// TUISkillsSummary is the one line that tells, before the list, how the
// skills are.
func TUISkillsSummary(c SkillCounts) string {
	s := fmt.Sprintf("%d skills · %d activas · %d desactivadas · %d con actualización · %d con problemas",
		c.Total, c.Active, c.Disabled, c.Updates, c.Problems)
	if c.Ignored > 0 {
		s += fmt.Sprintf(" · %d ignoradas", c.Ignored)
	}
	return s
}

// The agent filter (g) and the agent check boxes (l).
const (
	TUIAgentFilterQuestion = "¿Qué agente quieres ver?"
	TUIAgentFilterAll      = "Todos los agentes"
	TUIAgentFilterHint     = "Muestra solo las skills que ese agente ve."
	TUIKeysAgentFilter     = "↑/↓ elegir • enter ver • esc volver"

	TUIKeysChecks = "↑/↓ elegir • espacio marcar • enter confirmar • esc cancelar"
	// TUIChecksFixed marks an agent the app cannot change now.
	TUIChecksFixed = "(ahora no se puede cambiar)"
	// TUIChecksNone answers enter on check boxes that changed nothing.
	TUIChecksNone = "No cambiaste ningún agente; no se hizo nada."
	// TUIChecksEmpty is the check boxes of a skill with no agent to link.
	TUIChecksEmpty = "No hay agentes donde haga falta enlazarla."
	// TUIChecksReading is shown while the choices are read.
	TUIChecksReading = "Leyendo en qué agentes está..."
	// TUIChainLeft follows a cancelled step of chained plans.
	TUIChainLeft = "Los demás cambios de agentes tampoco se aplicaron."
	// TUIChainStale follows a step that ran when the user had left.
	TUIChainStale = "Te fuiste de la pantalla: los demás cambios de agentes no se planificaron. Vuelve a elegirlos con [l]."

	TUIActionAgents = "[l] Elegir agentes"
	TUIKeyAgents    = "l elegir agentes"
	TUIKeyFind      = "/ buscar"
	TUIKeyStates    = "f filtrar"
	TUIKeyByAgent   = "g ver por agente"
)

// TUIChecksQuestion heads the check boxes of the skill key.
func TUIChecksQuestion(key string) string { return fmt.Sprintf("¿En qué agentes está %s?", key) }

// TUIChecksInstall heads the check boxes of an install, its step 3.
const TUIChecksInstall = "Marca los agentes donde quedará disponible. Se proponen los detectados en tu equipo: es una propuesta, nunca un permiso."

// TUIChecksReaders tells, in one line, why the agents that read the store
// are not in the list.
func TUIChecksReaders(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%s no aparece: lee las skills directamente del almacén, así que no necesita enlace.", names[0])
	}
	return fmt.Sprintf("%s no aparecen: leen las skills directamente del almacén, así que no necesitan enlace.", JoinNames(names))
}

// TUIChainStep heads the confirmation of step i of n chained plans.
func TUIChainStep(i, n int) string { return fmt.Sprintf("Cambio %d de %d", i, n) }

// Undoing the last operation with z.
const (
	TUIUndoKey = "[z] deshacer"
	// TUIZNothing is z before anything was applied in this session.
	TUIZNothing = "Todavía no aplicaste nada en esta sesión que se pueda deshacer con z. Lo anterior está en el Historial (h)."
	// TUIZUndone is z after the last operation was an undo.
	TUIZUndone = "Lo último fue deshacer, y un deshacer no se puede deshacer. Puedes repetir la operación."
	// TUIZApplyAll is z after applying every update at once.
	TUIZApplyAll = "Aplicar todas hizo varias operaciones: deshaz cada una desde el Historial (h)."
)

// TUIWithUndo adds the z hint to the first line of a done message.
func TUIWithUndo(done string) string {
	first, rest, found := strings.Cut(done, "\n")
	first += " " + TUIUndoKey
	if found {
		return first + "\n" + rest
	}
	return first
}

// TUIUndoUpdateAsk starts the confirmation of undoing, with z, a version 1
// update.
func TUIUndoUpdateAsk(name string) Summary {
	return Summary{
		Question: fmt.Sprintf("¿Deshacer la actualización de %s?", name),
		Lines: []string{
			"Vuelve a la versión que tenías antes de actualizarla.",
			"La versión nueva se puede volver a aplicar desde Actualizar (2) cuando la revises otra vez.",
		},
	}
}

// Risk is a level of the traffic light of an update or an install.
type Risk int

// The levels, lowest first.
const (
	RiskLow Risk = iota
	RiskMedium
	RiskReview
)

var riskNames = map[Risk]string{RiskLow: "● bajo", RiskMedium: "▲ medio", RiskReview: "◆ revisar"}

// Icon and word of the level, as the list shows it.
func (r Risk) String() string { return riskNames[r] }

// Icon is the level's icon alone.
func (r Risk) Icon() string { icon, _, _ := strings.Cut(riskNames[r], " "); return icon }

// Word is the level's word alone.
func (r Risk) Word() string { _, word, _ := strings.Cut(riskNames[r], " "); return word }

// riskOf is the level of each label, and its consequence.
var riskOf = map[domain.Label]struct {
	level Risk
	why   string
}{
	domain.LabelTextOnly:        {RiskLow, "cambia las instrucciones que lee el agente"},
	domain.LabelTouchesScripts:  {RiskMedium, "cambia un script que el agente podría ejecutar"},
	domain.LabelNonTextFiles:    {RiskMedium, "trae binarios u otros archivos que no son texto y no se pueden leer aquí"},
	domain.LabelLocallyModified: {RiskMedium, "reemplaza una edición tuya, que queda guardada en una copia"},
	domain.LabelModeChange:      {RiskReview, "cambia qué archivos se pueden ejecutar"},
	domain.LabelNewPermissions:  {RiskReview, "le da al agente permisos nuevos para ejecutar herramientas"},
	domain.LabelSuspiciousText:  {RiskReview, "trae texto sospechoso o direcciones web que conviene leer antes"},
}

// RiskOf is the traffic light of labels: the highest level among them,
// and the consequence of each label at that level, in one sentence. A
// change with no label is told at the low level: it still changes what the
// agent reads.
func RiskOf(labels []domain.Label) (Risk, string) {
	level := RiskLow
	for _, l := range labels {
		if r, ok := riskOf[l]; ok && r.level > level {
			level = r.level
		}
	}
	var whys []string
	for _, l := range labels {
		if r, ok := riskOf[l]; ok && r.level == level && !contains(whys, r.why) {
			whys = append(whys, r.why)
		}
	}
	if len(whys) == 0 {
		whys = []string{riskOf[domain.LabelTextOnly].why}
	}
	return level, JoinNames(whys)
}

// TUIInstallRiskLine is the traffic light of an install: every install
// asks new permissions, since the whole skill is new, so that label alone
// raises nothing (L80).
func TUIInstallRiskLine(labels []domain.Label) string {
	var rest []domain.Label
	for _, l := range labels {
		if l != domain.LabelNewPermissions {
			rest = append(rest, l)
		}
	}
	return TUIRiskLine(rest)
}

// TUIRiskLine is the panel's line of the traffic light.
func TUIRiskLine(labels []domain.Label) string {
	r, why := RiskOf(labels)
	return fmt.Sprintf("Riesgo: %s. %s.", r, upperFirst(why))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// The panel that guides: the situation, then the recommended next step.
const (
	// TUIGuideRecommended introduces the recommended action.
	TUIGuideRecommended = "Recomendado: "
	TUIGuideUpdate      = "Hay una versión nueva de su autor."
	TUIGuideUnmgd       = "skillhub no la gestiona todavía."
	TUIGuideProbs       = "Tiene un problema que revisar."

	TUIActionSeeUpdate = "[2] Ver la actualización"
	TUIActionSeeHealth = "[4] Verla en Salud"
	TUIKeySeeUpdate    = "2 ver la actualización"
	TUIKeySeeHealth    = "4 verla en Salud"
)

// TUIGuideBrokenLinks is the situation of a skill with n broken links, in
// the agents named.
func TUIGuideBrokenLinks(n int, agents []string) string {
	where := ""
	if len(agents) > 0 {
		where = " en " + JoinNames(agents)
	}
	if n == 1 {
		return "Tiene un enlace roto" + where + "."
	}
	return fmt.Sprintf("Tiene %d enlaces rotos%s.", n, where)
}

// The texts of UX4 (S13, L90): stopping to ignore a skill, from the check
// boxes of Ajustes or with x under the «ignoradas» filter of Skills.
const (
	// TUIIgnoredNone is the Ignoradas row of Ajustes with nothing ignored.
	TUIIgnoredNone = "ninguna"
	// TUIIgnoredEmpty answers enter on that row.
	TUIIgnoredEmpty = "No ignoras ninguna skill. Para ignorar una, selecciónala en Skills y pulsa x."
	// TUIIgnoredQuestion and TUIIgnoredHint head the check boxes.
	TUIIgnoredQuestion = "¿Qué skills sigues ignorando?"
	TUIIgnoredHint     = "Desmarca las que quieras volver a ver en Skills. Solo cambian tus ajustes: ningún archivo de las skills se toca."
	// TUIIgnoredNoSkill follows an entry of the setting that names no skill.
	TUIIgnoredNoSkill = "(ninguna skill tiene ese nombre)"
	// TUIIgnoredNoChange answers enter on boxes that changed nothing.
	TUIIgnoredNoChange = "No desmarcaste ninguna; no se hizo nada."
	// TUIIgnoredKeptComma refuses to rewrite a list whose kept entry has a
	// comma, which «settings set ignore» would split.
	TUIIgnoredKeptComma = "Una de las que sigues ignorando tiene una coma en el nombre; cambia la lista en settings.toml."
)

// TUIUnignored tells, in plain words, what stopping to ignore did: the
// skills that come back to Skills, and the entries that named no skill.
func TUIUnignored(skills, entries []string) string {
	var parts []string
	switch len(skills) {
	case 0:
	case 1:
		parts = append(parts, fmt.Sprintf("Ya no se ignora %s: vuelve a aparecer en Skills.", skills[0]))
	default:
		parts = append(parts, fmt.Sprintf("Ya no se ignoran %s: vuelven a aparecer en Skills.", JoinNames(skills)))
	}
	if len(entries) > 0 {
		parts = append(parts, fmt.Sprintf("Se quitó %s de la lista de ignoradas.", JoinNames(entries)))
	}
	return strings.Join(parts, " ")
}

// TUIIgnoredRow tells why a write key does nothing on an ignored skill.
func TUIIgnoredRow(name string) string {
	return name + " está ignorada: pulsa x para dejar de ignorarla."
}
