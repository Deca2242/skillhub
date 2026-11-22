package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"skillhub/internal/app"
	"skillhub/internal/entrypoints/text"
	"skillhub/internal/shared/domain"
	"skillhub/internal/shared/ports"
	"skillhub/internal/updates"
)

// Screen identifies one of the TUI screens. Since UX3 four of them are
// the tabs (Skills, Actualizar, Instalar and Salud, which is Doctor), and
// Historial and Ajustes open from any tab with h and c.
type Screen int

const (
	ScreenSkills Screen = iota
	ScreenUpdates
	ScreenHistory
	ScreenSettings
	ScreenInstall
	ScreenDoctor
)

// SkillRow is the presenter output for one row of the Skills table.
type SkillRow struct {
	ID        string
	RawName   string
	Name      string
	Origin    string
	Harnesses string
	State     string
	Issues    string
	// ParkedKey is the key of a disabled skill's row: A enables it.
	ParkedKey string
	// Kind picks the row's icon and Short is its short state, as the
	// Skills list shows them (S13).
	Kind  RowKind
	Short string
	// Ignored marks a skill the ignore setting hides: only the «ignoradas»
	// filter shows it, and x stops ignoring it (UX4).
	Ignored bool
}

// PresentSkills formats skills and filters them by query (case-insensitive).
func PresentSkills(skills []domain.Skill, query string) []SkillRow {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]SkillRow, 0, len(skills))
	for _, s := range skills {
		name := s.Name
		if len(s.Aliases) > 0 {
			name = fmt.Sprintf("%s (%s)", s.Name, strings.Join(s.Aliases, ", "))
		}
		row := SkillRow{
			ID:        s.ID,
			RawName:   s.Name,
			Name:      name,
			Origin:    text.OriginKind(s.Origin.Kind),
			Harnesses: formatHarnesses(s.Locations),
			State:     text.State(s.State),
			Issues:    formatIssues(s.Issues),
		}
		row.Kind, row.Short = rowState(s)
		if q != "" && !matchesSkillQuery(row, q) {
			continue
		}
		out = append(out, row)
	}
	return out
}

// parkedIDPrefix marks the id of a disabled skill's row, which matches no
// skill of the inventory.
const parkedIDPrefix = "parked:"

// PresentParked formats the disabled skills as rows of the Skills table and
// filters them by query, as PresentSkills does.
func PresentParked(parked []app.ParkedSkill, query string) []SkillRow {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]SkillRow, 0, len(parked))
	for _, p := range parked {
		issues := msgEmpty
		if p.LockEntry {
			issues = text.TUIParkedEntry
		}
		row := SkillRow{
			ID:        parkedIDPrefix + p.Key,
			RawName:   p.Key,
			Name:      p.Key,
			Origin:    text.TUIParkedOrigin,
			Harnesses: orEmpty(strings.Join(p.Harnesses, ", ")),
			State:     text.TUIParkedSince(FormatTime(p.Since)),
			Issues:    issues,
			ParkedKey: p.Key,
			Kind:      RowOff,
			Short:     text.TUIRowDisabled,
		}
		if q != "" && !matchesSkillQuery(row, q) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func matchesSkillQuery(r SkillRow, q string) bool {
	fields := []string{r.ID, r.Name, r.Origin, r.Harnesses, r.State, r.Issues}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// PresentSkillDetail formats a full detail view of a skill in Spanish.
func PresentSkillDetail(s domain.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill: %s\n", s.Name)
	fmt.Fprintf(&b, "ID: %s\n", s.ID)
	if len(s.Aliases) > 0 {
		fmt.Fprintf(&b, "Alias: %s\n", strings.Join(s.Aliases, ", "))
	}
	if s.Description != "" {
		fmt.Fprintf(&b, "Descripción: %s\n", s.Description)
	}
	fmt.Fprintf(&b, "Origen: %s\n", text.OriginKind(s.Origin.Kind))
	if s.Origin.Lock != nil {
		fmt.Fprintf(&b, "  Repositorio: %s\n", s.Origin.Lock.Source)
		fmt.Fprintf(&b, "  Ruta en repo: %s\n", s.Origin.Lock.SkillPath)
		fmt.Fprintf(&b, "  Hash del lock: %s\n", ShortSHA(s.Origin.Lock.FolderHash))
		if s.Origin.Lock.Ref != "" {
			fmt.Fprintf(&b, "  Ref: %s\n", s.Origin.Lock.Ref)
		}
	}
	if s.Origin.GitClone != nil {
		fmt.Fprintf(&b, "  Remoto: %s\n", s.Origin.GitClone.Remote)
		fmt.Fprintf(&b, "  Rama: %s\n", orEmpty(s.Origin.GitClone.Branch))
		fmt.Fprintf(&b, "  Seguimiento: %s\n", orEmpty(s.Origin.GitClone.Upstream))
		fmt.Fprintf(&b, "  HEAD: %s\n", ShortSHA(s.Origin.GitClone.Head))
	}
	fmt.Fprintf(&b, "Estado: %s\n", text.State(s.State))
	fmt.Fprintf(&b, "Problemas: %s\n", formatIssues(s.Issues))
	fmt.Fprintf(&b, "Ubicaciones (%d):\n", len(s.Locations))
	for _, loc := range s.Locations {
		fmt.Fprintf(&b, "  - [%s] %s (%s, sha %s)\n",
			loc.AgentID, text.LocationKind(loc.Kind), loc.Path, ShortSHA(loc.ContentTreeSHA))
	}
	return b.String()
}

// UpdateRow is the presenter output for one row of the Actualizaciones table.
type UpdateRow struct {
	CandidateID     string
	Target          string
	ToCommit        string
	ToSHA           string
	State           string
	Labels          string
	Blocked         bool
	BlockReason     string
	LocallyModified bool
	FileCount       int
}

// PresentUpdates formats update candidates for the Actualizaciones screen.
func PresentUpdates(cands []domain.UpdateCandidate) []UpdateRow {
	out := make([]UpdateRow, 0, len(cands))
	for _, c := range cands {
		state := text.CandidateState(c.State)
		var reason string
		if c.State == domain.CandidateBlocked {
			reason = text.BlockReason(c.BlockReason)
			state = fmt.Sprintf("%s: %s", state, reason)
		}
		out = append(out, UpdateRow{
			CandidateID:     c.ID,
			Target:          c.Target,
			ToCommit:        ShortSHA(c.ToCommit),
			ToSHA:           c.ToSHA,
			State:           state,
			Labels:          formatLabels(c.Labels),
			Blocked:         c.State == domain.CandidateBlocked,
			BlockReason:     reason,
			LocallyModified: isLocallyModified(c),
			FileCount:       len(c.FileChanges),
		})
	}
	return out
}

// ExcludedCandidate describes a candidate excluded from ApplyAll and why.
type ExcludedCandidate struct {
	Target string
	Reason string
}

// ApplyAllPlan holds what "Actualizar todas" will apply and what it leaves out.
type ApplyAllPlan struct {
	Included []UpdateRow
	Excluded []ExcludedCandidate
}

// Approvals returns the exact (CandidateID, ToSHA) pairs approved by this plan.
func (p ApplyAllPlan) Approvals() []updates.Approval {
	aps := make([]updates.Approval, 0, len(p.Included))
	for _, row := range p.Included {
		aps = append(aps, updates.Approval{
			CandidateID: row.CandidateID,
			ToSHA:       row.ToSHA,
		})
	}
	return aps
}

// BuildApplyAllPlan builds the confirmation plan for "Actualizar todas"
// (docs/design.md section 9 and A15).
func BuildApplyAllPlan(cands []domain.UpdateCandidate) ApplyAllPlan {
	var plan ApplyAllPlan
	for _, row := range PresentUpdates(cands) {
		switch {
		case row.Blocked:
			plan.Excluded = append(plan.Excluded, ExcludedCandidate{
				Target: row.Target,
				Reason: row.BlockReason,
			})
		case row.LocallyModified:
			plan.Excluded = append(plan.Excluded, ExcludedCandidate{
				Target: row.Target,
				Reason: msgExcludedLocal,
			})
		default:
			plan.Included = append(plan.Included, row)
		}
	}
	return plan
}

// PresentApplyAllPlan formats the confirmation modal content for ApplyAll.
func PresentApplyAllPlan(plan ApplyAllPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Actualizar todas (%d incluidas, %d excluidas):\n\n", len(plan.Included), len(plan.Excluded))
	if len(plan.Included) > 0 {
		b.WriteString("Se aplicarán:\n")
		for _, r := range plan.Included {
			fmt.Fprintf(&b, "  - %s (%s) — %d archivo(s) — etiquetas: %s\n",
				r.Target, r.CandidateID, r.FileCount, r.Labels)
		}
	}
	if len(plan.Excluded) > 0 {
		if len(plan.Included) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Quedan fuera:\n")
		for _, ex := range plan.Excluded {
			fmt.Fprintf(&b, "  - %s: %s\n", ex.Target, ex.Reason)
		}
	}
	b.WriteString("\n" + msgConfirmApplyAllPrompt)
	return b.String()
}

// PresentApplyAllResults formats per-target results after ApplyAll.
func PresentApplyAllResults(results []updates.ApplyResult) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		parts = append(parts, fmt.Sprintf("%s: %s", r.Target, outcomeName(r.Outcome)))
	}
	return strings.Join(parts, " | ")
}

func outcomeName(o updates.Outcome) string {
	switch o {
	case updates.OutcomeApplied:
		return msgOutcomeApplied
	case updates.OutcomeRejected:
		return msgOutcomeRejected
	case updates.OutcomeFailed:
		return msgOutcomeFailed
	case updates.OutcomeExcluded:
		return msgOutcomeExcluded
	default:
		return string(o)
	}
}

// DiffView picks which diff of a split review the viewport shows.
type DiffView int

const (
	// DiffViewUser shows the user's changes: registered → installed.
	DiffViewUser DiffView = iota
	// DiffViewAuthor shows the author's changes: registered → upstream.
	DiffViewAuthor
)

// Toggle returns the other view.
func (v DiffView) Toggle() DiffView {
	if v == DiffViewUser {
		return DiffViewAuthor
	}
	return DiffViewUser
}

// PresentDiff formats a candidate's review for the scrollable viewport. A
// split review shows view, with the other one named; any other review shows
// its single installed → upstream diff.
func PresentDiff(c domain.UpdateCandidate, r app.ReviewDiff, view DiffView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Destino: %s\n", c.Target)
	fmt.Fprintf(&b, "Candidato: %s\n", c.ID)
	fmt.Fprintf(&b, "De %s a commit %s (árbol %s)\n", ShortSHA(c.FromSHA), ShortSHA(c.ToCommit), ShortSHA(c.ToSHA))
	fmt.Fprintf(&b, "Etiquetas: %s\n", formatLabels(c.Labels))

	diffs := r.Installed
	switch {
	case r.Split() && view == DiffViewAuthor:
		diffs = r.Author
		fmt.Fprintf(&b, "Vista: %s\nv: %s\n", msgDiffAuthorLabel, msgDiffUserLabel)
	case r.Split():
		diffs = r.User
		fmt.Fprintf(&b, "Vista: %s\nv: %s\n", msgDiffUserLabel, msgDiffAuthorLabel)
	default:
		fmt.Fprintf(&b, "%s\n", msgDiffLabel)
	}

	if r.LocallyModified && !r.RegisteredAvailable {
		b.WriteString("\n" + msgDiffRegisteredGone + "\n")
	}
	if r.LocallyModified || isLocallyModified(c) {
		b.WriteString("\n" + msgDiffLocalNotice + "\n")
	}
	writeFileDiffs(&b, diffs)
	return b.String()
}

// writeFileDiffs writes the list of files and then each file's diff.
func writeFileDiffs(b *strings.Builder, diffs []app.FileDiff) {
	b.WriteString("\nArchivos:\n")
	for _, d := range diffs {
		fmt.Fprintf(b, "  %s  %s\n", text.FileChangeKind(d.Change), d.Path)
	}
	if len(diffs) == 0 {
		b.WriteString("  " + msgDiffEmpty + "\n")
	}
	for _, d := range diffs {
		b.WriteString("\n")
		if d.Binary {
			fmt.Fprintf(b, "%s: %s\n", d.Path, msgDiffBinary)
			continue
		}
		if d.Unified == "" {
			fmt.Fprintf(b, "%s: %s\n", d.Path, msgDiffEmpty)
			continue
		}
		b.WriteString(d.Unified)
		if !strings.HasSuffix(d.Unified, "\n") {
			b.WriteString("\n")
		}
	}
}

// HistoryRow is the presenter output for one row of the Historial table.
type HistoryRow struct {
	At         string
	Action     string
	Target     string
	Versions   string
	SnapshotID string
	Detail     string
	CanUndo    bool

	// at and action are the entry's, for the line in plain words.
	at     time.Time
	action domain.HistoryAction
}

// PresentHistory formats journal entries newest-first for the Historial screen.
func PresentHistory(entries []domain.HistoryEntry) []HistoryRow {
	out := make([]HistoryRow, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		out = append(out, HistoryRow{
			At:         FormatTime(e.At),
			Action:     text.HistoryAction(e.Action),
			Target:     e.Target,
			Versions:   fmt.Sprintf("%s → %s", ShortSHA(e.FromSHA), ShortSHA(e.ToSHA)),
			SnapshotID: orEmpty(e.SnapshotID),
			Detail:     orEmpty(e.Detail),
			CanUndo:    e.Action == domain.ActionApplied && e.SnapshotID != "",
			at:         e.At,
			action:     e.Action,
		})
	}
	return out
}

// SettingsView is the presenter output for the Ajustes screen.
type SettingsView struct {
	CheckInterval       string
	Notifications       string
	Ignore              string
	BackgroundState     string
	BackgroundInstalled string
	BackgroundActive    string
	BackgroundCommand   string
}

// PresentSettings formats settings and background status for the Ajustes screen.
func PresentSettings(s domain.Settings, bg ports.BackgroundStatus) SettingsView {
	bgState := msgBgOff
	bgCmd := msgCmdEnable
	if bg.Enabled {
		bgState = msgBgOn
		bgCmd = msgCmdDisable
	}
	ignore := text.TUIIgnoredNone
	if len(s.Ignore) > 0 {
		ignore = strings.Join(s.Ignore, ", ")
	}
	return SettingsView{
		CheckInterval:       FormatDuration(s.CheckInterval),
		Notifications:       yesNo(s.Notifications),
		Ignore:              ignore,
		BackgroundState:     bgState,
		BackgroundInstalled: yesNo(bg.Installed),
		BackgroundActive:    yesNo(bg.Active),
		BackgroundCommand:   bgCmd,
	}
}

// PresentInterrupted formats the interrupted operation screen content.
func PresentInterrupted(op domain.Operation) string {
	var b strings.Builder
	b.WriteString(msgInterruptedTitle + "\n\n")
	fmt.Fprintf(&b, "Operación: %s\n", text.OperationKind(op.Kind))
	fmt.Fprintf(&b, "Destino:   %s\n", op.Target)
	fmt.Fprintf(&b, "Snapshot:  %s\n", orEmpty(op.RestoreSnapshotID))
	fmt.Fprintf(&b, "Iniciada:  %s\n\n", FormatTime(op.StartedAt))
	b.WriteString(msgInterruptedNote + "\n\n")
	b.WriteString(msgRecoverPrompt)
	return b.String()
}

// PresentManagedPending formats the recover screen of a pending version 2
// operation.
func PresentManagedPending() string {
	return msgInterruptedTitle + "\n\n" + text.TUIManagedPending + "\n\n" + text.TUIManagedRecoverPrompt
}

// ShortSHA returns up to the first 10 characters of a SHA, or msgEmpty.
func ShortSHA(sha string) string {
	if sha == "" {
		return msgEmpty
	}
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// FormatTime formats a timestamp deterministically in UTC.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return msgEmpty
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// FormatDuration formats a duration cleanly.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return msgEmpty
	}
	return d.String()
}

func formatHarnesses(locs []domain.Location) string {
	var out []string
	for _, loc := range locs {
		if loc.AgentID != domain.StoreAgentID && !slices.Contains(out, loc.AgentID) {
			out = append(out, loc.AgentID)
		}
	}
	if len(out) == 0 {
		return msgEmpty
	}
	return strings.Join(out, ", ")
}

func formatIssues(issues []domain.Issue) string {
	if len(issues) == 0 {
		return msgEmpty
	}
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, text.Issue(is))
	}
	return strings.Join(out, ", ")
}

func formatLabels(labels []domain.Label) string {
	if len(labels) == 0 {
		return msgEmpty
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, text.Label(l))
	}
	return strings.Join(out, ", ")
}

func isLocallyModified(c domain.UpdateCandidate) bool {
	return slices.Contains(c.Labels, domain.LabelLocallyModified)
}

func yesNo(v bool) string {
	if v {
		return msgYes
	}
	return msgNo
}

func orEmpty(s string) string {
	if s == "" {
		return msgEmpty
	}
	return s
}

// sentence turns a message of the text package into a sentence: an upper
// case first letter and a final period.
func sentence(msg string) string {
	r, size := utf8.DecodeRuneInString(msg)
	return string(unicode.ToUpper(r)) + msg[size:] + "."
}

// ExplainError translates internal errors to neutral Spanish messages and
// never exposes raw English error strings.
func ExplainError(err error) string {
	if err == nil {
		return ""
	}
	var amb *app.AmbiguousError
	switch {
	case errors.As(err, &amb):
		return fmt.Sprintf(msgErrAmbiguous, amb.Ref, strings.Join(amb.IDs, ", "))
	case errors.Is(err, ports.ErrStateLocked):
		return msgErrStateLocked
	case errors.Is(err, updates.ErrInterrupted):
		return msgErrInterrupted
	case errors.Is(err, updates.ErrV2OperationPending):
		return sentence(text.V2OperationPending)
	case errors.Is(err, updates.ErrInstalledChanged):
		return msgErrInstalledChanged
	case errors.Is(err, updates.ErrStagedChanged):
		return msgErrStagedChanged
	case errors.Is(err, updates.ErrCandidateNotFound),
		errors.Is(err, updates.ErrNotPending),
		errors.Is(err, updates.ErrApprovalMismatch):
		return msgErrCandidateReplaced
	case errors.Is(err, updates.ErrRejected):
		return msgErrRejected
	case errors.Is(err, updates.ErrNothingToUndo):
		return msgErrNothingToUndo
	case errors.Is(err, updates.ErrSnapshotNotFound):
		return msgErrSnapshotNotFound
	case errors.Is(err, domain.ErrIntervalTooShort):
		return msgIntervalTooShort
	case errors.Is(err, app.ErrInvalidSettingValue):
		return msgInvalidInterval
	case errors.Is(err, app.ErrUnknownSetting):
		return msgErrUnknownSetting
	default:
		return msgErrGeneric
	}
}

// allocateColumns distributes totalWidth across columns, subtracting 2 chars
// of horizontal cell padding per column so the rendered table never overflows.
func allocateColumns(totalWidth int, mins, weights []int) []int {
	const cellPadding = 2
	n := len(mins)
	avail := max(n, totalWidth-n*cellPadding)
	widths := make([]int, n)
	used := 0
	for i := range n {
		widths[i] = mins[i]
		used += mins[i]
	}
	if used > avail {
		// Scale down proportionally when terminal is narrower than sum of mins.
		rem := avail
		for i := range n {
			if i == n-1 {
				widths[i] = max(1, rem)
				break
			}
			w := max(1, (mins[i]*avail)/used)
			if w > rem-(n-1-i) {
				w = max(1, rem-(n-1-i))
			}
			widths[i] = w
			rem -= w
		}
		return widths
	}
	extra := avail - used
	totalWeight := 0
	for _, w := range weights {
		totalWeight += w
	}
	if totalWeight == 0 || extra == 0 {
		return widths
	}
	distributed := 0
	for i := range n {
		add := (extra * weights[i]) / totalWeight
		widths[i] += add
		distributed += add
	}
	widths[n-1] += extra - distributed
	return widths
}
