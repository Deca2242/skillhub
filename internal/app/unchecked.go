package app

import (
	"context"
	"fmt"
	"time"

	"skillhub/internal/shared/domain"
	"skillhub/internal/updates"
)

// UncheckedReason explains why an active skill was not or could not be
// checked for updates.
type UncheckedReason string

const (
	// UncheckedWebSource is a skill installed from a website's well-known index.
	UncheckedWebSource UncheckedReason = "web_source"
	// UncheckedUntrackedOrigin is a lock entry or git clone whose source is not
	// a tracked GitHub repository.
	UncheckedUntrackedOrigin UncheckedReason = "untracked_origin"
	// UncheckedNoOrigin is a manual folder without a registered origin.
	UncheckedNoOrigin UncheckedReason = "no_origin"
	// UncheckedCheckFailed is a skill whose last update check failed.
	UncheckedCheckFailed UncheckedReason = "check_failed"
)

// UncheckedSkill is one active skill that cannot be checked or failed its
// last check.
type UncheckedSkill struct {
	ID        string
	Name      string
	Reason    UncheckedReason
	CheckedAt time.Time
}

// UncheckedSkills returns active skills that update checking does not track
// or whose last check failed, in inventory order.
func (a *App) UncheckedSkills(ctx context.Context) ([]UncheckedSkill, error) {
	skills, err := a.Skills(ctx)
	if err != nil {
		return nil, err
	}
	lock, err := a.d.Lock.Read()
	if err != nil {
		return nil, fmt.Errorf("read the skills lock: %w", err)
	}
	st, err := a.d.State.Load()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	lockByID := make(map[string]domain.LockEntry, len(lock.Entries))
	for _, e := range lock.Entries {
		lockByID[domain.LockID(e.Name)] = e
	}

	out := []UncheckedSkill{}
	for _, s := range skills {
		if s.State == domain.StateDisabled || s.State == domain.StateMissing {
			continue
		}
		if item, ok := classifyUnchecked(s, lockByID, st.Results[a.cloneTarget(s)]); ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func classifyUnchecked(
	s domain.Skill,
	lockByID map[string]domain.LockEntry,
	lastResult domain.TargetResult,
) (UncheckedSkill, bool) {
	switch s.Origin.Kind {
	case domain.OriginNone:
		return UncheckedSkill{ID: s.ID, Name: s.Name, Reason: UncheckedNoOrigin}, true
	case domain.OriginLock:
		if e, ok := lockByID[s.ID]; ok {
			if checkable, web := updates.LockEntryCheckable(e); !checkable {
				reason := UncheckedUntrackedOrigin
				if web {
					reason = UncheckedWebSource
				}
				return UncheckedSkill{ID: s.ID, Name: s.Name, Reason: reason}, true
			}
		}
	case domain.OriginGitClone:
		if s.Origin.GitClone == nil || !updates.CloneCheckable(s.Origin.GitClone.Remote) {
			return UncheckedSkill{ID: s.ID, Name: s.Name, Reason: UncheckedUntrackedOrigin}, true
		}
	}

	if lastResult.State == domain.StateCheckFailed || s.State == domain.StateCheckFailed {
		return UncheckedSkill{
			ID:        s.ID,
			Name:      s.Name,
			Reason:    UncheckedCheckFailed,
			CheckedAt: lastResult.CheckedAt,
		}, true
	}
	return UncheckedSkill{}, false
}
