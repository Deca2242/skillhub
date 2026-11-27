package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"skillhub/internal/packs"
	"skillhub/internal/shared/domain"
)

// MemberPresence reports whether a pack member was observed in the current inventory.
type MemberPresence string

const (
	MemberObserved    MemberPresence = "observed"
	MemberNotObserved MemberPresence = "not_observed"
)

// MemberHealth summarizes the observed health of a pack member.
type MemberHealth string

const (
	MemberHealthHealthy         MemberHealth = "healthy"
	MemberHealthUpdateAvailable MemberHealth = "update_available"
	MemberHealthIssues          MemberHealth = "issues"
	MemberHealthNotObserved     MemberHealth = "not_observed"
)

// PackSummary describes a draft pack in lists and skill membership views.
type PackSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// PackMemberView is one member of a draft pack with its observed inventory health.
type PackMemberView struct {
	ID       string         `json:"id"`
	Name     string         `json:"name,omitempty"`
	Presence MemberPresence `json:"presence"`
	Health   MemberHealth   `json:"health"`
	State    domain.State   `json:"state,omitempty"`
	Issues   []domain.Issue `json:"issues,omitempty"`
}

// PackView is the detailed view of a draft pack and its members.
type PackView struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Members     []PackMemberView `json:"members"`
	CreatedAt   time.Time        `json:"created_at,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at,omitempty"`
}

func (a *App) requirePacks() (*packs.Service, error) {
	if a.packs == nil {
		return nil, fmt.Errorf("%w: Packs", ErrMissingDependency)
	}
	return a.packs, nil
}

// PackCreate creates a new draft pack.
func (a *App) PackCreate(ctx context.Context, name, description string) (PackSummary, error) {
	svc, err := a.requirePacks()
	if err != nil {
		return PackSummary{}, err
	}
	p, err := svc.Create(ctx, name, description)
	if err != nil {
		return PackSummary{}, err
	}
	return summarizePack(p), nil
}

// PackList lists all draft packs sorted by name.
func (a *App) PackList() ([]PackSummary, error) {
	svc, err := a.requirePacks()
	if err != nil {
		return nil, err
	}
	all, err := svc.List()
	if err != nil {
		return nil, err
	}
	out := make([]PackSummary, len(all))
	for i, p := range all {
		out[i] = summarizePack(p)
	}
	return out, nil
}

// PackShow returns the draft pack named name with each member's observed health.
func (a *App) PackShow(ctx context.Context, name string) (PackView, error) {
	svc, err := a.requirePacks()
	if err != nil {
		return PackView{}, err
	}
	p, err := svc.Get(name)
	if err != nil {
		return PackView{}, err
	}
	skills, err := a.Skills(ctx)
	if err != nil {
		return PackView{}, err
	}
	byID := make(map[string]domain.Skill, len(skills))
	for _, s := range skills {
		byID[s.ID] = s
	}

	members := make([]PackMemberView, 0, len(p.Members))
	for _, id := range p.Members {
		s, ok := byID[id]
		if !ok || !isObservedSkill(s) {
			members = append(members, PackMemberView{
				ID:       id,
				Presence: MemberNotObserved,
				Health:   MemberHealthNotObserved,
			})
			continue
		}
		members = append(members, PackMemberView{
			ID:       id,
			Name:     s.Name,
			Presence: MemberObserved,
			Health:   memberHealth(s),
			State:    s.State,
			Issues:   slices.Clone(s.Issues),
		})
	}
	return PackView{
		Name:        p.Name,
		Description: p.Description,
		Members:     members,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}, nil
}

// PackAdd adds an observed skill (by exact ID or unique name/alias) to packName.
func (a *App) PackAdd(ctx context.Context, packName, skillRef string) (PackSummary, error) {
	svc, err := a.requirePacks()
	if err != nil {
		return PackSummary{}, err
	}
	id, err := a.resolveObservedSkill(ctx, skillRef)
	if err != nil {
		return PackSummary{}, err
	}
	p, err := svc.AddMember(ctx, packName, id)
	if err != nil {
		return PackSummary{}, err
	}
	return summarizePack(p), nil
}

// PackRemove removes a member (by exact saved ID or unique observed name/alias) from packName.
func (a *App) PackRemove(ctx context.Context, packName, skillRef string) (PackSummary, error) {
	svc, err := a.requirePacks()
	if err != nil {
		return PackSummary{}, err
	}
	p, err := svc.Get(packName)
	if err != nil {
		return PackSummary{}, err
	}
	if slices.Contains(p.Members, skillRef) {
		updated, err := svc.RemoveMember(ctx, packName, skillRef)
		if err != nil {
			return PackSummary{}, err
		}
		return summarizePack(updated), nil
	}
	id, err := a.resolveObservedSkill(ctx, skillRef)
	if err != nil {
		// If not observed in inventory, still try removing the raw reference so
		// PackRemove returns ErrMemberNotFound cleanly.
		updated, remErr := svc.RemoveMember(ctx, packName, skillRef)
		if remErr != nil {
			return PackSummary{}, err
		}
		return summarizePack(updated), nil
	}
	updated, err := svc.RemoveMember(ctx, packName, id)
	if err != nil {
		return PackSummary{}, err
	}
	return summarizePack(updated), nil
}

// SkillPacks returns all draft packs that include skillID, sorted by name.
// When Packs is not wired in Deps, it returns an empty slice.
func (a *App) SkillPacks(skillID string) ([]PackSummary, error) {
	if a.packs == nil {
		return []PackSummary{}, nil
	}
	list, err := a.packs.ForSkill(skillID)
	if err != nil {
		return nil, err
	}
	out := make([]PackSummary, len(list))
	for i, p := range list {
		out[i] = summarizePack(p)
	}
	return out, nil
}

func (a *App) resolveObservedSkill(ctx context.Context, ref string) (string, error) {
	skills, err := a.Skills(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range skills {
		if !isObservedSkill(s) {
			continue
		}
		if s.ID == ref {
			return s.ID, nil
		}
	}
	var ids []string
	for _, s := range skills {
		if !isObservedSkill(s) {
			continue
		}
		if s.Name == ref || slices.Contains(s.Aliases, ref) {
			ids = append(ids, s.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("%q: %w", ref, ErrSkillNotFound)
	case 1:
		return ids[0], nil
	default:
		return "", &AmbiguousError{Ref: ref, IDs: ids}
	}
}

func isObservedSkill(s domain.Skill) bool {
	if s.State == domain.StateMissing {
		return false
	}
	return slices.ContainsFunc(s.Locations, func(loc domain.Location) bool {
		return loc.Kind != domain.LocationBrokenLink
	})
}

func memberHealth(s domain.Skill) MemberHealth {
	if len(s.Issues) > 0 || s.State == domain.StateCheckFailed {
		return MemberHealthIssues
	}
	if s.State == domain.StateUpdateAvailable {
		return MemberHealthUpdateAvailable
	}
	return MemberHealthHealthy
}

func summarizePack(p packs.Pack) PackSummary {
	return PackSummary{
		Name:        p.Name,
		Description: p.Description,
		MemberCount: len(p.Members),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}
