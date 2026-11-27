package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"skillhub/internal/data"
	"skillhub/internal/doctor"
	"skillhub/internal/graph"
	"skillhub/internal/inventory"
	"skillhub/internal/manage/engine"
	"skillhub/internal/manage/remove"
	"skillhub/internal/manage/repair"
	"skillhub/internal/shared/domain"
)

// Read-only data the TUI's Skills panel shows next to the inventory
// (S13): which harnesses see each skill, and a readable name for each
// harness. Neither writes anything.

// Sighting kinds as exposed by Sighting.Kind, matching graph.Kind.
const (
	SightingDirect        = string(graph.Direct)
	SightingViaLink       = string(graph.ViaLink)
	SightingOwnCopy       = string(graph.OwnCopy)
	SightingIndeterminate = string(graph.Indeterminate)
)

// Visibility returns, for each skill of the graph by its inventory ID, the
// detected harnesses that see it, sorted and without repeats. A diagnostic
// profile is not a harness and is left out. A skill parked by hand in the
// store's OFF/ counts only the harnesses that see it directly or through a
// link, the ones Doctor's parked_but_visible finding names (L83): a harness
// that reads the store one level deep does not see it there. It only reads.
// Without the version 2 ports it returns ErrMissingDependency, and the
// caller keeps what the inventory's locations tell.
func (a *App) Visibility(ctx context.Context) (map[string][]string, error) {
	sightings, err := a.observeSightings(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(sightings))
	for id, sgs := range sightings {
		seen := make([]string, 0, len(sgs))
		for _, sg := range sgs {
			seen = append(seen, sg.Harness)
		}
		slices.Sort(seen)
		out[id] = slices.Compact(seen)
	}
	return out, nil
}

// Sightings returns, for each skill of the graph by its inventory ID, how each
// detected harness sees it and through which entry path. It applies the same
// visibility rules as Visibility and only reads.
func (a *App) Sightings(ctx context.Context) (map[string][]Sighting, error) {
	return a.observeSightings(ctx)
}

// IndeterminateSkill is a skill whose visibility for one or both compared
// agents is indeterminate, along with the agents and sightings explaining why.
type IndeterminateSkill struct {
	ID      string
	Agents  []string
	Reasons []Sighting
}

// AgentComparison is the pure comparison of what two agents see, computed from
// already-loaded Sightings data without reading disk.
type AgentComparison struct {
	First         string
	Second        string
	Common        []string
	OnlyFirst     []string
	OnlySecond    []string
	Indeterminate []IndeterminateSkill
}

// CompareAgents compares which skills first and second see based on sightings.
// Skills whose visibility is indeterminate for either agent are placed apart in
// Indeterminate with their sightings as reason.
func CompareAgents(sightings map[string][]Sighting, first, second string) AgentComparison {
	out := AgentComparison{
		First:         first,
		Second:        second,
		Common:        []string{},
		OnlyFirst:     []string{},
		OnlySecond:    []string{},
		Indeterminate: []IndeterminateSkill{},
	}
	if first == "" || second == "" || len(sightings) == 0 {
		return out
	}

	for id, sgs := range sightings {
		var hasFirst, hasSecond bool
		var indetAgents []string
		var indetReasons []Sighting
		for _, sg := range sgs {
			if sg.Harness != first && sg.Harness != second {
				continue
			}
			if sg.Harness == first {
				hasFirst = true
			}
			if sg.Harness == second {
				hasSecond = true
			}
			if sg.Kind == SightingIndeterminate {
				if !slices.Contains(indetAgents, sg.Harness) {
					indetAgents = append(indetAgents, sg.Harness)
				}
				indetReasons = append(indetReasons, sg)
			}
		}

		if len(indetReasons) > 0 {
			slices.Sort(indetAgents)
			slices.SortFunc(indetReasons, func(x, y Sighting) int {
				if c := strings.Compare(x.Harness, y.Harness); c != 0 {
					return c
				}
				return strings.Compare(x.Entry, y.Entry)
			})
			out.Indeterminate = append(out.Indeterminate, IndeterminateSkill{
				ID:      id,
				Agents:  indetAgents,
				Reasons: indetReasons,
			})
			continue
		}

		switch {
		case hasFirst && hasSecond:
			out.Common = append(out.Common, id)
		case hasFirst:
			out.OnlyFirst = append(out.OnlyFirst, id)
		case hasSecond:
			out.OnlySecond = append(out.OnlySecond, id)
		}
	}

	slices.Sort(out.Common)
	slices.Sort(out.OnlyFirst)
	slices.Sort(out.OnlySecond)
	slices.SortFunc(out.Indeterminate, func(a, b IndeterminateSkill) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// AgentPresence summarizes which known harnesses are detected on the machine
// and which are known in the agents table without being detected.
type AgentPresence struct {
	Detected   []string
	Undetected []string
}

// DetectedAgents returns the detected harness IDs (from the graph, in table
// order) and the known harness IDs from the agents table that were not
// detected. It only reads.
func (a *App) DetectedAgents(ctx context.Context) (AgentPresence, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return AgentPresence{}, err
	}
	g, err := m.observe()
	if err != nil {
		return AgentPresence{}, err
	}
	out := AgentPresence{
		Detected: slices.Clone(g.Harnesses),
	}
	for _, h := range m.harnesses {
		if !slices.Contains(g.Harnesses, h.ID) {
			out.Undetected = append(out.Undetected, h.ID)
		}
	}
	return out, nil
}

func (a *App) observeSightings(ctx context.Context) (map[string][]Sighting, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return nil, err
	}
	g, err := m.observe()
	if err != nil {
		return nil, err
	}
	entries := make(map[string]string, len(g.Folders))
	for _, f := range g.Folders {
		entries[f.Path] = f.Entry
	}
	off := filepath.Join(g.Store, inventory.OffDirName)
	out := make(map[string][]Sighting, len(g.Skills))
	for _, s := range g.Skills {
		inOff := s.Canonical != "" && strings.HasPrefix(entries[s.Canonical], off+string(filepath.Separator))
		var sgs []Sighting
		for _, v := range s.Visibility {
			if inOff && v.Kind != graph.Direct && v.Kind != graph.ViaLink {
				continue
			}
			if slices.Contains(g.Harnesses, v.Harness) {
				sgs = append(sgs, Sighting{
					Harness: v.Harness,
					Kind:    string(v.Kind),
					Entry:   v.Entry,
				})
			}
		}
		if len(sgs) == 0 {
			continue
		}
		slices.SortFunc(sgs, func(x, y Sighting) int {
			if c := strings.Compare(x.Harness, y.Harness); c != 0 {
				return c
			}
			if c := strings.Compare(x.Kind, y.Kind); c != 0 {
				return c
			}
			return strings.Compare(x.Entry, y.Entry)
		})
		out[s.ID] = slices.Compact(sgs)
	}
	return out, nil
}

// Confirmed external provider identifiers for SkillActions.Provider.
const (
	ProviderClaudePlugin = "claude-plugin"
	ProviderClaudeSynced = "claude-synced"
	ProviderGentleAI     = "gentle-ai"
	ProviderCursor       = "cursor"
	ProviderCodex        = "codex"
)

// SkillActions is what the app can plan now for one skill of the graph,
// decided by the planners themselves on one observation: an action is
// offered only when its planner, given the same graph, returns a plan.
type SkillActions struct {
	// Key is the store key the planners take; "" when the skill is not in
	// the store.
	Key string
	// RemoveKey is the exact removal target passed to ActionRemove when
	// Remove is true (a store key, dir:<home-rel>, lock:<name> or parked:<key>).
	RemoveKey string
	// RemoveGroup is true when the skill has multiple attributed global
	// folders and at least one is eligible for complete global removal.
	RemoveGroup bool
	// RemoveGroupID is the scanner-observed graph Skill ID passed to
	// ActionRemove with Group=true when RemoveGroup is true.
	RemoveGroupID string
	Disable       bool
	Remove        bool
	// Manual is true when the skill is a confirmed independent manual
	// installation: no recorded origin, no foreign zone, unambiguous claim,
	// and every observed folder confirmed free of .git.
	Manual bool
	// Provider is the confirmed external provider or plugin manager that owns
	// the skill when it lies in a foreign provider/plugin zone; empty when
	// ownership is not proven.
	Provider string
	// Unlink are the detected harnesses whose link to the skill the app can
	// remove.
	Unlink []string
	// LinkIn is the first detected harness, in table order, where the app
	// can link the skill; "" when there is none. Only the first is looked
	// for: planning a link in every harness costs more than the panel needs.
	LinkIn string
	// Off is set for a skill parked by hand in the store's OFF/ that the
	// adoption of OFF/ takes. That adoption is one plan for every skill in
	// OFF/: OffKeys are all of them, sorted, and OffKey is this one.
	Off     bool
	OffKey  string
	OffKeys []string
	// BrokenLinks are the broken links that lead to the skill, as Doctor
	// finds them, each with the repair the row offers.
	BrokenLinks []LinkFix
}

// LinkFix is a broken link of a skill and the Doctor repair that fixes it.
// Repair is "" when Doctor offers none. Harnesses are the harnesses whose
// folder holds the link, in table order (UX3: the panel names them).
type LinkFix struct {
	Finding   Finding
	Repair    string
	Harnesses []string
}

// SkillActions returns, for each skill of the graph by its inventory ID,
// what the app can plan for it now. It only reads.
func (a *App) SkillActions(ctx context.Context) (map[string]SkillActions, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return nil, err
	}
	g, err := m.observe()
	if err != nil {
		return nil, err
	}
	return m.skillActions(g, doctor.Detect(g))
}

// Overview is what the TUI reads once per load (UX3): what each skill
// admits, Doctor's findings, and which of them Doctor offers a repair for
// now. All three come from one observation.
type Overview struct {
	Actions  map[string]SkillActions
	Findings []Finding
	// Repairable holds, by finding ID, the findings with a repair offered.
	Repairable map[string]bool
}

// Overview returns SkillActions, Doctor and the repairable findings on one
// observation, so that a load observes the machine once. It only reads.
func (a *App) Overview(ctx context.Context) (Overview, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return Overview{}, err
	}
	g, err := m.observe()
	if err != nil {
		return Overview{}, err
	}
	found := doctor.Detect(g)
	acts, err := m.skillActions(g, found)
	if err != nil {
		return Overview{}, err
	}
	ov := Overview{Actions: acts, Repairable: map[string]bool{}}
	for _, f := range found {
		ov.Findings = append(ov.Findings, findingView(f))
		o, err := m.repair.Repairs(g, f)
		if err != nil {
			return Overview{}, refusal(err)
		}
		if len(o.Repairs) > 0 {
			ov.Repairable[f.ID] = true
		}
	}
	return ov, nil
}

// skillActions decides what each skill of g admits; found are Doctor's
// findings on g.
func (m *manager) skillActions(g graph.Graph, found []doctor.Finding) (map[string]SkillActions, error) {
	a := m.a
	// Every planner sees this one observation: the remover observes through
	// its own function, so it gets one that returns g.
	m.remove = remove.New(remove.Deps{
		Paths: a.d.Paths, Planner: m.engine, Lock: a.d.ManagedLock, State: a.d.State,
		Observe: func() (graph.Graph, error) { return g, nil }, Harnesses: m.harnesses, Profiles: m.profiles,
		Dirs: a.d.Dirs, Links: a.d.Links, Files: a.d.Files, HashDir: a.d.HashDir,
		ProjectDir: a.projectDir, Project: a.d.Project,
	})

	var offKeys []string
	if tp, err := m.toggle.AdoptOff(g); err == nil {
		for _, s := range tp.Preview.Skills {
			offKeys = append(offKeys, s.Key)
		}
		slices.Sort(offKeys)
	}
	folders := make(map[string]graph.Folder, len(g.Folders))
	for _, f := range g.Folders {
		folders[f.Path] = f
	}
	off := filepath.Join(g.Store, inventory.OffDirName)

	out := make(map[string]SkillActions, len(g.Skills))
	for _, s := range g.Skills {
		var acts SkillActions
		entry := folders[s.Canonical].Entry
		switch dir, base := filepath.Dir(entry), filepath.Base(entry); {
		case s.Canonical == "":
			allowProvider := s.Zone == graph.ZoneGentle || s.Zone == graph.ZonePlugin || s.Zone == graph.ZoneProvider
			if len(s.Folders) == 1 && (s.Zone == graph.ZoneNone || s.Zone == graph.ZoneProfile || allowProvider) {
				if rel, ok := a.d.Paths.HomeRel(folders[s.Folders[0]].Entry); ok {
					target := domain.DirID(rel)
					if _, err := m.remove.RemoveWithOptions(target, remove.Options{AllowProvider: allowProvider}); err == nil {
						acts.Remove = true
						acts.RemoveKey = target
					}
				}
			} else if len(s.Folders) > 1 && (s.Zone == graph.ZoneNone || s.Zone == graph.ZoneProfile || allowProvider) {
				if _, err := m.remove.RemoveGroupWithOptions(s.ID, remove.Options{AllowProvider: allowProvider}); err == nil {
					acts.RemoveGroup = true
					acts.RemoveGroupID = s.ID
				}
			}
		case dir == g.Store:
			acts = m.storeActions(g, base, entry)
			allowProvider := s.Zone == graph.ZoneGentle || s.Zone == graph.ZonePlugin || s.Zone == graph.ZoneProvider
			if allowProvider && !acts.Remove && len(s.Folders) <= 1 {
				target := base
				if s.Origin == domain.OriginGitClone {
					target = s.ID
				} else if s.LockKey != "" {
					target = domain.LockID(s.LockKey)
				} else if rel, ok := a.d.Paths.HomeRel(entry); ok {
					target = domain.DirID(rel)
				}
				if _, err := m.remove.RemoveWithOptions(target, remove.Options{AllowProvider: true}); err == nil {
					acts.Remove = true
					acts.RemoveKey = target
				}
			}
			if len(s.Folders) > 1 {
				if _, err := m.remove.RemoveGroupWithOptions(s.ID, remove.Options{AllowProvider: allowProvider}); err == nil {
					acts.RemoveGroup = true
					acts.RemoveGroupID = s.ID
				}
			}
		case dir == off:
			if slices.Contains(offKeys, base) {
				acts.Off, acts.OffKey, acts.OffKeys = true, base, slices.Clone(offKeys)
			}
			allowProvider := s.Zone == graph.ZoneGentle || s.Zone == graph.ZonePlugin || s.Zone == graph.ZoneProvider
			if allowProvider && s.Origin == domain.OriginGitClone {
				if _, err := m.remove.RemoveWithOptions(s.ID, remove.Options{AllowProvider: true}); err == nil {
					acts.Remove = true
					acts.RemoveKey = s.ID
				}
			} else if rel, ok := a.d.Paths.HomeRel(entry); ok {
				target := domain.DirID(rel)
				if _, err := m.remove.RemoveWithOptions(target, remove.Options{AllowProvider: allowProvider}); err == nil {
					acts.Remove = true
					acts.RemoveKey = target
				}
			}
			if len(s.Folders) > 1 {
				if _, err := m.remove.RemoveGroupWithOptions(s.ID, remove.Options{AllowProvider: allowProvider}); err == nil {
					acts.RemoveGroup = true
					acts.RemoveGroupID = s.ID
				}
			}
		case s.Zone == graph.ZoneGentle || s.Zone == graph.ZonePlugin || s.Zone == graph.ZoneProvider:
			if len(s.Folders) > 1 {
				if _, err := m.remove.RemoveGroupWithOptions(s.ID, remove.Options{AllowProvider: true}); err == nil {
					acts.RemoveGroup = true
					acts.RemoveGroupID = s.ID
				}
			} else if s.Origin == domain.OriginGitClone {
				if _, err := m.remove.RemoveWithOptions(s.ID, remove.Options{AllowProvider: true}); err == nil {
					acts.Remove = true
					acts.RemoveKey = s.ID
				}
			} else if rel, ok := a.d.Paths.HomeRel(entry); ok {
				target := domain.DirID(rel)
				if _, err := m.remove.RemoveWithOptions(target, remove.Options{AllowProvider: true}); err == nil {
					acts.Remove = true
					acts.RemoveKey = target
				}
			}
		}
		acts.Manual = isManualSkill(s, folders)
		acts.Provider = a.providerOf(s, entry)
		fixes, err := m.linkFixes(g, found, entry)
		if err != nil {
			return nil, err
		}
		acts.BrokenLinks = fixes
		out[s.ID] = acts
		if s.Canonical == "" && len(s.Folders) > 1 {
			// Grouped folders outside the store have no canonical ID; inventory.Scan
			// may pick any copy's dir: ID, so expose the same actions under each.
			for _, p := range s.Folders {
				if rel, ok := a.d.Paths.HomeRel(folders[p].Entry); ok {
					out[domain.DirID(rel)] = acts
				}
			}
		}
	}
	for _, b := range g.Boundary {
		if b.Zone != graph.ZoneGentle && b.Zone != graph.ZonePlugin && b.Zone != graph.ZoneProvider {
			continue
		}
		rel, ok := a.d.Paths.HomeRel(b.Path)
		if !ok {
			continue
		}
		id := domain.DirID(rel)
		acts := out[id]
		if acts.Provider != "" && acts.Remove {
			continue
		}
		if acts.Provider == "" {
			acts.Provider = a.providerForZone(b.Zone, b.Path)
		}
		if !acts.Remove {
			if _, err := m.remove.RemoveWithOptions(id, remove.Options{AllowProvider: true}); err == nil {
				acts.Remove = true
				acts.RemoveKey = id
			}
		}
		out[id] = acts
	}
	return out, nil
}

// isManualSkill reports whether s is a confirmed independent manual
// installation: no recorded origin, no foreign zone, unambiguous claim, and
// every observed folder confirmed free of .git.
func isManualSkill(s graph.Skill, folders map[string]graph.Folder) bool {
	if s.Origin != domain.OriginNone || s.Zone != graph.ZoneNone || len(s.SharedWith) > 0 || len(s.Folders) == 0 {
		return false
	}
	for _, path := range s.Folders {
		f, ok := folders[path]
		if !ok || f.Git != graph.GitNone {
			return false
		}
	}
	return true
}

// providerOf returns the confirmed external provider identifier when s lies in
// a proven foreign provider or plugin zone, or "" when ownership is unproven.
func (a *App) providerOf(s graph.Skill, entry string) string {
	if s.Canonical == "" {
		return ""
	}
	if p := a.providerForZone(s.Zone, entry); p != "" {
		return p
	}
	return a.providerForZone(s.Zone, s.Canonical)
}

func (a *App) providerForZone(zone graph.Zone, entry string) string {
	switch zone {
	case graph.ZonePlugin:
		return ProviderClaudePlugin
	case graph.ZoneGentle, graph.ZoneShared:
		return ProviderGentleAI
	case graph.ZoneProvider:
		rel, ok := a.d.Paths.HomeRel(entry)
		if !ok {
			return ""
		}
		switch {
		case rel == ".claude/plugins" || strings.HasPrefix(rel, ".claude/plugins/"):
			return ProviderClaudePlugin
		case rel == ".claude/skills/synced" || strings.HasPrefix(rel, ".claude/skills/synced/"):
			return ProviderClaudeSynced
		case rel == ".cursor/skills-cursor" || strings.HasPrefix(rel, ".cursor/skills-cursor/"):
			return ProviderCursor
		case rel == ".codex/skills/.system" || strings.HasPrefix(rel, ".codex/skills/.system/") ||
			rel == ".codex/plugins" || strings.HasPrefix(rel, ".codex/plugins/") ||
			rel == ".codex/.tmp" || strings.HasPrefix(rel, ".codex/.tmp/"):
			return ProviderCodex
		}
	}
	return ""
}

// storeActions asks each planner about the store skill key on g.
func (m *manager) storeActions(g graph.Graph, key, entry string) SkillActions {
	acts := SkillActions{Key: key}
	_, err := m.toggle.Disable(g, key)
	acts.Disable = err == nil
	if _, err := m.remove.Remove(key); err == nil {
		acts.Remove = true
		acts.RemoveKey = key
	} else if rel, ok := m.a.d.Paths.HomeRel(entry); ok {
		target := domain.DirID(rel)
		if _, err := m.remove.Remove(target); err == nil {
			acts.Remove = true
			acts.RemoveKey = target
		}
	}
	for _, h := range g.Harnesses {
		if _, err := m.toggle.Unlink(g, key, h); err == nil {
			acts.Unlink = append(acts.Unlink, h)
		}
		if acts.LinkIn == "" {
			if _, err := m.toggle.Link(g, key, h); err == nil {
				acts.LinkIn = h
			}
		}
	}
	return acts
}

// repairOrder is the repair a skill's row offers for a broken link, the
// first Doctor offers: the one that makes the link work again comes first.
var repairOrder = []repair.Kind{repair.RepointLink, repair.EnableAndLink, repair.AdoptOff, repair.DeleteLink}

// linkFixes are the broken links that lead to s, whose canonical folder is
// listed at entry: the ones Doctor relates to that folder (a skill moved to
// OFF/), and the ones named after its store key. Each gets the repair Doctor
// offers for it, in repairOrder. found are Doctor's findings on g.
func (m *manager) linkFixes(g graph.Graph, found []doctor.Finding, entry string) ([]LinkFix, error) {
	if entry == "" {
		return nil, nil
	}
	var out []LinkFix
	for _, f := range found {
		if f.Kind != doctor.BrokenLink || len(f.Paths) == 0 {
			continue
		}
		mine := slices.Contains(f.Related, entry) ||
			len(f.Related) == 0 && filepath.Dir(entry) == g.Store && filepath.Base(f.Paths[0]) == filepath.Base(entry)
		if !mine {
			continue
		}
		fix := LinkFix{Finding: findingView(f), Harnesses: m.harnessesIn(g, filepath.Dir(f.Paths[0]))}
		o, err := m.repair.Repairs(g, f)
		if err != nil {
			return nil, refusal(err)
		}
		for _, k := range repairOrder {
			if slices.Contains(o.Repairs, k) {
				fix.Repair = string(k)
				break
			}
		}
		out = append(out, fix)
	}
	return out, nil
}

// AgentName returns a readable name for the harness id: its words with an
// upper case first letter, and "cli" as CLI ("gemini-cli" is "Gemini CLI").
// The agents table keeps each display name, but the data package does not
// export it yet; until it does, the name is derived from the id.
func (a *App) AgentName(id string) string {
	if name := agentNames()[id]; name != "" {
		return name
	}
	words := strings.Split(id, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		if w == "cli" {
			words[i] = "CLI"
			continue
		}
		r, size := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[size:]
	}
	return strings.Join(words, " ")
}

// agentNames is the embedded table's display names, read once. An agent the
// table does not name, such as one from the user's overlay, falls back to a
// name derived from its id.
var agentNames = sync.OnceValue(func() map[string]string {
	names, err := data.AgentNames()
	if err != nil {
		return nil
	}
	return names
})

// OperationView is what a version 2 journal line did, for the TUI's
// Historial: the plan's action, the skill keys its effect touched, an optional
// path label when no store/parked key was touched, and whether it undid that
// action.
type OperationView struct {
	Action     string
	Keys       []string
	Label      string
	ProjectDir string
	Undo       bool
}

// Operation returns what the operation of the snapshot did. It only reads.
func (a *App) Operation(ctx context.Context, snapshot string) (OperationView, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return OperationView{}, err
	}
	snap, err := m.engine.ReadSnapshot(snapshot)
	if err != nil {
		return OperationView{}, err
	}
	v := OperationView{Action: snap.Plan.Action, Keys: effectKeys(snap.Plan.Effect)}
	plan := snap.Plan
	if snap.Plan.Undoes != "" {
		v.Undo = true
		orig, err := m.engine.ReadSnapshot(snap.Plan.Undoes)
		if err != nil {
			return OperationView{}, err
		}
		plan = orig.Plan
		v.Action = orig.Plan.Action
		if len(v.Keys) == 0 {
			v.Keys = effectKeys(orig.Plan.Effect)
		}
	}
	if plan.HasProjectScope() {
		v.ProjectDir = plan.Project.ProjectDir
	}
	if len(v.Keys) == 0 {
		v.Label = a.planPathLabel(plan)
	}
	return v, nil
}

// planPathLabel describes the folder a key-less operation (such as removing an
// independent global skill) acted on, for Historial display without polluting
// store-key activity matching.
func (a *App) planPathLabel(p engine.Plan) string {
	if p.HasProjectScope() {
		return filepath.ToSlash(filepath.Join(string(p.Project.Root), p.Project.SkillName))
	}
	for _, s := range p.Steps {
		if s.Kind == engine.StepRemoveTree && s.Path != "" {
			if rel, ok := a.d.Paths.HomeRel(s.Path); ok {
				return "~/" + rel
			}
			return filepath.Base(s.Path)
		}
	}
	return ""
}

// effectKeys are the skill keys an effect touches, in order, without
// repeats.
func effectKeys(f engine.Effect) []string {
	var keys []string
	add := func(k string) {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, s := range f.SetManaged {
		add(s.Key)
	}
	for _, k := range f.DropManaged {
		add(k)
	}
	for _, p := range f.SetParked {
		add(p.Skill.Key)
	}
	for _, k := range f.DropParked {
		add(k)
	}
	return keys
}

// Now is the app's clock, which the TUI tells times against.
func (a *App) Now() time.Time { return a.d.Clock.Now() }

// harnessesIn are the harnesses that link or read in dir, in table order.
func (m *manager) harnessesIn(g graph.Graph, dir string) []string {
	var out []string
	for _, h := range m.harnesses {
		dirs := []string{h.LinkDir}
		for _, r := range h.ReadRoots {
			dirs = append(dirs, r.Dir)
		}
		for _, rel := range dirs {
			if rel != "" && filepath.Join(g.Home, filepath.FromSlash(rel)) == dir {
				out = append(out, h.ID)
				break
			}
		}
	}
	return out
}

// AgentChoice is an agent a skill can be linked in: whether it is linked
// there now, and whether the app can plan changing that.
type AgentChoice struct {
	Harness    string
	Linked     bool
	Changeable bool
}

// AgentChoices are the agents the TUI's check boxes offer for a skill
// (UX3): the detected agents with a link folder, in table order, and apart
// the detected agents that read the store and so need no link.
type AgentChoices struct {
	Key       string
	Agents    []AgentChoice
	ReadStore []string
}

// AgentChoices returns the agent choices of the store skill key: each one
// is changeable when its planner, link or unlink, plans it now. With an
// empty key it returns an install's choices: every detected agent with a
// link folder, unlinked and changeable. It only reads.
func (a *App) AgentChoices(ctx context.Context, key string) (AgentChoices, error) {
	m, err := a.v2(ctx)
	if err != nil {
		return AgentChoices{}, err
	}
	g, err := m.observe()
	if err != nil {
		return AgentChoices{}, err
	}
	out := AgentChoices{Key: key}
	for _, h := range m.harnesses {
		if !slices.Contains(g.Harnesses, h.ID) {
			continue
		}
		if h.LinkDir == "" {
			if h.ReadsStore == data.Yes {
				out.ReadStore = append(out.ReadStore, h.ID)
			}
			continue
		}
		c := AgentChoice{Harness: h.ID}
		switch {
		case key == "":
			c.Changeable = true
		default:
			if _, err := m.toggle.Unlink(g, key, h.ID); err == nil {
				c.Linked, c.Changeable = true, true
			} else if _, err := m.toggle.Link(g, key, h.ID); err == nil {
				c.Changeable = true
			} else {
				c.Linked = linkedIn(g, key, h.ID)
			}
		}
		out.Agents = append(out.Agents, c)
	}
	return out, nil
}

// linkedIn reports whether the store skill key is seen by harness through a
// link, for a choice the app cannot change.
func linkedIn(g graph.Graph, key, harness string) bool {
	canonical := filepath.Join(g.Store, key)
	for _, s := range g.Skills {
		if s.Canonical != canonical {
			continue
		}
		for _, v := range s.Visibility {
			if v.Harness == harness && v.Kind == graph.ViaLink {
				return true
			}
		}
	}
	return false
}

// IgnoredSkill is a skill the ignore setting leaves out of the inventory,
// with the entries of the setting that name it, in the setting's order.
type IgnoredSkill struct {
	Skill   domain.Skill
	Entries []string
}

// Ignored lists the skills the ignore setting leaves out, as the inventory
// finds them without the setting, for the TUI to show and to stop
// ignoring (UX4). An entry that names no skill names nothing here. It only
// reads: Skills, scan and the CLI keep leaving those skills out.
func (a *App) Ignored(ctx context.Context) ([]IgnoredSkill, error) {
	settings, err := a.d.Settings.Load()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if len(settings.Ignore) == 0 {
		return nil, nil
	}
	all, err := inventory.Scan(ctx, inventory.Deps{
		Paths: a.d.Paths, Agents: a.d.Agents, Dirs: a.d.Dirs, Files: a.d.Files,
		Lock: a.d.Lock, Git: a.d.Git, HashDir: a.d.HashDir,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("scan the skills: %w", err)
	}
	var out []IgnoredSkill
	for _, s := range all {
		// The same names inventory.Scan matches: the id, the name or an alias.
		var entries []string
		for _, e := range settings.Ignore {
			if e == s.ID || e == s.Name || slices.Contains(s.Aliases, e) {
				entries = append(entries, e)
			}
		}
		if len(entries) > 0 {
			out = append(out, IgnoredSkill{Skill: s, Entries: entries})
		}
	}
	return out, nil
}
