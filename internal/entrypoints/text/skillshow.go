package text

import (
	"fmt"
	"strings"
	"time"

	"skillhub/internal/app"
	"skillhub/internal/shared/domain"
)

// Help of the skills show command, in neutral Spanish (S11).
const (
	SkillShowUse   = "show <skill> [--json]"
	SkillShowShort = "Muestra el detalle de una skill: estado, agentes, origen y actividad"
	SkillShowLong  = "Muestra el estado de una skill en skillhub (activa, desactivada o ignorada), qué agentes la ven y por qué, su origen y su actividad reciente en skillhub. Solo lee: no cambia nada.\n\n<skill> es el nombre de la skill, un alias o su id. Con --json devuelve un objeto con los datos sin escapar."
)

// SkillShowData carries all fields rendered by SkillShow.
type SkillShowData struct {
	Skill         domain.Skill
	SkillhubState string
	AvailableIn   string
	Sightings     []app.Sighting
	AgentName     func(string) string
	Activity      []app.ActivityEntry
	Now           time.Time
	// Parked is set for a skill disabled via skillhub (stored in v2 Parked).
	Parked   bool
	Since    time.Time
	ReturnTo []string
}

// SkillShow formats the full detail of a skill in neutral Spanish, escaping
// all third-party values with SafeLine (A28).
func SkillShow(d SkillShowData) string {
	var b strings.Builder
	s := d.Skill
	fmt.Fprintf(&b, "Skill: %s\n", SafeLine(s.Name))
	fmt.Fprintf(&b, "ID: %s\n", SafeLine(s.ID))
	if len(s.Aliases) > 0 {
		aliases := make([]string, len(s.Aliases))
		for i, a := range s.Aliases {
			aliases[i] = SafeLine(a)
		}
		fmt.Fprintf(&b, "Alias: %s\n", strings.Join(aliases, ", "))
	}
	if s.Description != "" {
		fmt.Fprintf(&b, "Descripción: %s\n", SafeLine(s.Description))
	}
	fmt.Fprintf(&b, "%s\n", TUIPanelSkillhubState(d.SkillhubState))
	if d.Parked {
		if !d.Since.IsZero() {
			fmt.Fprintf(&b, "%s\n", TUIPanelDisabledSince(d.Since.Local().Format("2006-01-02 15:04")))
		}
		if len(d.ReturnTo) > 0 {
			names := make([]string, len(d.ReturnTo))
			for i, id := range d.ReturnTo {
				names[i] = SafeLine(agentDisplayName(d.AgentName, id))
			}
			fmt.Fprintf(&b, "%s\n", TUIPanelReturnsTo(JoinNames(names)))
		}
		fmt.Fprintf(&b, "%s\n", TUIPanelAvailable(TUIPanelNowhere))
		fmt.Fprintf(&b, "%s\n", TUIPanelOrigin(TUIPanelDisabledBy))
		writeSkillActivity(&b, d.Activity, d.Now)
		return b.String()
	}

	avail := d.AvailableIn
	if avail == "" {
		avail = TUIPanelNowhere
	}
	fmt.Fprintf(&b, "%s\n", TUIPanelAvailable(avail))
	if len(d.Sightings) > 0 {
		fmt.Fprintf(&b, "%s\n", TUISkillDetailWhyHeader)
		for _, sg := range d.Sightings {
			name := SafeLine(agentDisplayName(d.AgentName, sg.Harness))
			fmt.Fprintf(&b, "  - %s: %s (%s)\n", name, TUISightingReason(sg.Kind), SafeLine(sg.Entry))
		}
	}
	writeSkillActivity(&b, d.Activity, d.Now)
	writeSkillOrigin(&b, s.Origin)
	fmt.Fprintf(&b, "Estado: %s\n", State(s.State))
	fmt.Fprintf(&b, "Problemas: %s\n", formatShowIssues(s.Issues))
	fmt.Fprintf(&b, "Ubicaciones (%d):\n", len(s.Locations))
	for _, loc := range s.Locations {
		fmt.Fprintf(&b, "  - [%s] %s (%s, sha %s)\n",
			SafeLine(loc.AgentID), LocationKind(loc.Kind), SafeLine(loc.Path), showShortSHA(loc.ContentTreeSHA))
	}
	return b.String()
}

func agentDisplayName(fn func(string) string, id string) string {
	if fn != nil {
		return fn(id)
	}
	return id
}

func writeSkillActivity(b *strings.Builder, activity []app.ActivityEntry, now time.Time) {
	if len(activity) == 0 {
		fmt.Fprintf(b, "%s\n", TUIActivityNone)
		return
	}
	fmt.Fprintf(b, "%s\n", TUIActivityHeader)
	for _, e := range activity {
		fmt.Fprintf(b, "%s\n", TUIActivityLine(e, now))
	}
}

func writeSkillOrigin(b *strings.Builder, o domain.Origin) {
	fmt.Fprintf(b, "Origen: %s\n", OriginKind(o.Kind))
	if o.Lock != nil {
		fmt.Fprintf(b, "  Repositorio: %s\n", SafeLine(o.Lock.Source))
		fmt.Fprintf(b, "  Ruta en repo: %s\n", SafeLine(o.Lock.SkillPath))
		fmt.Fprintf(b, "  Hash del lock: %s\n", showShortSHA(o.Lock.FolderHash))
		if o.Lock.Ref != "" {
			fmt.Fprintf(b, "  Ref: %s\n", SafeLine(o.Lock.Ref))
		}
	}
	if o.GitClone != nil {
		fmt.Fprintf(b, "  Remoto: %s\n", SafeLine(o.GitClone.Remote))
		fmt.Fprintf(b, "  Rama: %s\n", showOrNone(SafeLine(o.GitClone.Branch)))
		fmt.Fprintf(b, "  Seguimiento: %s\n", showOrNone(SafeLine(o.GitClone.Upstream)))
		fmt.Fprintf(b, "  HEAD: %s\n", showShortSHA(o.GitClone.Head))
	}
}

func formatShowIssues(issues []domain.Issue) string {
	if len(issues) == 0 {
		return "ninguno"
	}
	names := make([]string, len(issues))
	for i, iss := range issues {
		names[i] = Issue(iss)
	}
	return strings.Join(names, ", ")
}

func showShortSHA(sha string) string {
	if sha == "" {
		return "—"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func showOrNone(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
