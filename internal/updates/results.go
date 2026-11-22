package updates

import (
	"maps"
	"time"

	"skillhub/internal/shared/domain"
)

// recordResults returns results with the outcome of every checked target in
// targets, stamped at now. A target that was not checked keeps its previous
// result. The map is copied, never changed in place.
func recordResults(results map[string]domain.TargetResult, targets []TargetCheck, now time.Time) map[string]domain.TargetResult {
	out := results
	for _, t := range targets {
		r, ok := targetResult(t, now)
		if ok {
			out = withResult(out, t.Target, &r)
		}
	}
	return out
}

// targetResult maps one check outcome to the result the state keeps. A
// blocked candidate is still an update upstream; a skipped version leaves
// nothing to do.
func targetResult(t TargetCheck, now time.Time) (domain.TargetResult, bool) {
	r := domain.TargetResult{CheckedAt: now}
	switch t.Outcome {
	case CheckUpToDate:
		r.State, r.RemovedUpstream = domain.StateUpToDate, t.RemovedUpstream
	case CheckSkipped:
		r.State = domain.StateUpToDate
	case CheckUpdateAvailable, CheckBlocked:
		r.State = domain.StateUpdateAvailable
	case CheckFailed:
		r.State = domain.StateCheckFailed
		if t.Err != nil {
			r.Detail = t.Err.Error()
		}
	default:
		return domain.TargetResult{}, false
	}
	return r, true
}

// withResult returns a copy of results with tgt set to r, or without tgt
// when r is nil; an empty map is nil, so state.json omits it. The input is
// never changed, so a caller can put the previous map back on a revert.
func withResult(results map[string]domain.TargetResult, tgt string, r *domain.TargetResult) map[string]domain.TargetResult {
	out := maps.Clone(results)
	if r != nil {
		if out == nil {
			out = map[string]domain.TargetResult{}
		}
		out[tgt] = *r
	} else {
		delete(out, tgt)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
