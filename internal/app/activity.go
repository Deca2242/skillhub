package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"skillhub/internal/shared/domain"
)

// ActivityEntry is one journal event that touched a skill, ordered newest
// first.
type ActivityEntry struct {
	At        time.Time
	Action    domain.HistoryAction
	Target    string
	Operation OperationView
	V2        bool
}

// SkillActivity returns the journal events that touched s, ordered from newest
// to oldest. It is pure and performs no I/O. Version 2 operations match by
// the store keys OperationView.Keys carries from effectKeys.
func SkillActivity(history []domain.HistoryEntry, operations map[string]OperationView, s domain.Skill) []ActivityEntry {
	keys := skillStoreKeys(s)
	out := []ActivityEntry{}
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		v2 := strings.HasPrefix(h.Target, "v2:")
		var op OperationView
		if v2 {
			if len(keys) == 0 {
				continue
			}
			op = operations[h.SnapshotID]
			if !touchesAnyKey(op.Keys, keys) {
				continue
			}
		} else if !matchesV1Target(h.Target, s.ID) {
			continue
		}
		out = append(out, ActivityEntry{
			At:        h.At,
			Action:    h.Action,
			Target:    h.Target,
			Operation: op,
			V2:        v2,
		})
	}
	slices.SortStableFunc(out, func(a, b ActivityEntry) int {
		return b.At.Compare(a.At)
	})
	return out
}

func skillStoreKeys(s domain.Skill) []string {
	var keys []string
	add := func(k string) {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, loc := range s.Locations {
		if loc.AgentID == domain.StoreAgentID && loc.Path != "" {
			add(filepath.Base(loc.Path))
		}
	}
	if key, ok := strings.CutPrefix(s.ID, "lock:"); ok {
		add(key)
	}
	return keys
}

func touchesAnyKey(opKeys, skillKeys []string) bool {
	for _, k := range opKeys {
		if slices.Contains(skillKeys, k) {
			return true
		}
	}
	return false
}

func matchesV1Target(target, skillID string) bool {
	if target == "" || skillID == "" {
		return false
	}
	if target == skillID {
		return true
	}
	return strings.HasPrefix(target, "git:") && strings.HasPrefix(skillID, target+"/")
}

// Operations reads what each version 2 journal line did. A snapshot that cannot
// be read is omitted.
func (a *App) Operations(ctx context.Context, hist []domain.HistoryEntry) map[string]OperationView {
	out := map[string]OperationView{}
	for _, h := range hist {
		if !strings.HasPrefix(h.Target, "v2:") || h.SnapshotID == "" {
			continue
		}
		if _, done := out[h.SnapshotID]; done {
			continue
		}
		if op, err := a.Operation(ctx, h.SnapshotID); err == nil {
			out[h.SnapshotID] = op
		}
	}
	return out
}
