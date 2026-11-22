package background_test

import (
	"reflect"
	"testing"
	"time"

	"skillhub/internal/background"
	"skillhub/internal/shared/domain"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

func TestDue(t *testing.T) {
	tests := []struct {
		name   string
		checks domain.CheckTimes
		now    time.Time
		want   bool
	}{
		{name: "no check ever ran", checks: domain.CheckTimes{}, now: t0, want: true},
		{name: "right after a successful check", checks: domain.CheckTimes{LastRun: t0, LastSuccess: t0}, now: t0, want: false},
		{name: "one second before the interval", checks: domain.CheckTimes{LastRun: t0, LastSuccess: t0}, now: t0.Add(time.Hour - time.Second), want: false},
		{name: "exactly at the interval", checks: domain.CheckTimes{LastRun: t0, LastSuccess: t0}, now: t0.Add(time.Hour), want: true},
		{name: "past the interval", checks: domain.CheckTimes{LastRun: t0, LastSuccess: t0}, now: t0.Add(3 * time.Hour), want: true},
		{
			name:   "a later failed run does not reset the interval",
			checks: domain.CheckTimes{LastRun: t0.Add(50 * time.Minute), LastSuccess: t0, LastFailed: true},
			now:    t0.Add(time.Hour), want: true,
		},
		{
			name:   "only failures so far",
			checks: domain.CheckTimes{LastRun: t0, LastFailed: true},
			now:    t0.Add(time.Minute), want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := background.Due(tt.checks, time.Hour, tt.now); got != tt.want {
				t.Errorf("Due() = %v, want %v", got, tt.want)
			}
		})
	}
}

func cand(id string, state domain.CandidateState) domain.UpdateCandidate {
	return domain.UpdateCandidate{ID: id, Target: "lock:" + id, State: state}
}

func ids(cs []domain.UpdateCandidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestDecide(t *testing.T) {
	op := &domain.Operation{ID: "op1", Kind: domain.OperationApply, Target: "lock:a"}
	tests := []struct {
		name        string
		in          background.Input
		wantReasons []background.Reason
		wantNew     []string
		wantMarks   domain.NotifiedMarks
	}{
		{
			name:        "a new candidate is announced and marked",
			in:          background.Input{Enabled: true, Checked: true, Open: []domain.UpdateCandidate{cand("c1", domain.CandidatePending)}},
			wantReasons: []background.Reason{background.ReasonNewCandidates},
			wantNew:     []string{"c1"},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
		},
		{
			name: "an unchanged pending list is not announced again",
			in: background.Input{
				Enabled: true, Checked: true, Marks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
				Open: []domain.UpdateCandidate{cand("c1", domain.CandidatePending)},
			},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
		},
		{
			name: "a candidate that supersedes an announced one is new",
			in: background.Input{
				Enabled: true, Checked: true, Marks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
				Open: []domain.UpdateCandidate{cand("c2", domain.CandidatePending)},
			},
			wantReasons: []background.Reason{background.ReasonNewCandidates},
			wantNew:     []string{"c2"},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{"c2"}},
		},
		{
			name: "only the new one of several is announced",
			in: background.Input{
				Enabled: true, Checked: true, Marks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
				Open: []domain.UpdateCandidate{cand("c3", domain.CandidateBlocked), cand("c1", domain.CandidatePending)},
			},
			wantReasons: []background.Reason{background.ReasonNewCandidates},
			wantNew:     []string{"c3"},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{"c1", "c3"}},
		},
		{
			name:        "a failure after a success is announced",
			in:          background.Input{Enabled: true, Checked: true, CheckFailed: true},
			wantReasons: []background.Reason{background.ReasonCheckFailed},
			wantNew:     []string{},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{}, CheckFailure: true},
		},
		{
			name:      "a failure after an announced failure is not announced again",
			in:        background.Input{Enabled: true, Checked: true, CheckFailed: true, Marks: domain.NotifiedMarks{CheckFailure: true}},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{}, CheckFailure: true},
		},
		{
			name:      "a success clears the failure mark",
			in:        background.Input{Enabled: true, Checked: true, Marks: domain.NotifiedMarks{CheckFailure: true}},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{}},
		},
		{
			name:        "an interrupted operation not announced yet",
			in:          background.Input{Enabled: true, Checked: true, Operation: op},
			wantReasons: []background.Reason{background.ReasonInterrupted},
			wantNew:     []string{},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{}, OperationID: "op1"},
		},
		{
			name:      "an announced interrupted operation is not announced again",
			in:        background.Input{Enabled: true, Checked: true, Operation: op, Marks: domain.NotifiedMarks{OperationID: "op1"}},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{}, OperationID: "op1"},
		},
		{
			name:      "a resolved operation clears its mark",
			in:        background.Input{Enabled: true, Checked: true, Marks: domain.NotifiedMarks{OperationID: "op1"}},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{}},
		},
		{
			name: "all three cases make one notification",
			in: background.Input{
				Enabled: true, Checked: true, CheckFailed: true, Operation: op,
				Open: []domain.UpdateCandidate{cand("c1", domain.CandidatePending)},
			},
			wantReasons: []background.Reason{background.ReasonNewCandidates, background.ReasonCheckFailed, background.ReasonInterrupted},
			wantNew:     []string{"c1"},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{"c1"}, CheckFailure: true, OperationID: "op1"},
		},
		{
			name: "a skipped check announces only the interrupted operation and keeps the other marks",
			in: background.Input{
				Enabled: true, Checked: false, CheckFailed: false, Operation: op,
				Marks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}, CheckFailure: true},
				Open:  []domain.UpdateCandidate{cand("c2", domain.CandidatePending)},
			},
			wantReasons: []background.Reason{background.ReasonInterrupted},
			wantNew:     []string{},
			wantMarks:   domain.NotifiedMarks{CandidateIDs: []string{"c1"}, CheckFailure: true, OperationID: "op1"},
		},
		{
			name: "notifications off silence everything and mark nothing new",
			in: background.Input{
				Enabled: false, Checked: true, CheckFailed: true, Operation: op,
				Open: []domain.UpdateCandidate{cand("c1", domain.CandidatePending)},
			},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{}},
		},
		{
			name: "notifications off still forget what is gone",
			in: background.Input{
				Enabled: false, Checked: true,
				Marks: domain.NotifiedMarks{CandidateIDs: []string{"c1", "c9"}, CheckFailure: true, OperationID: "op1"},
				Open:  []domain.UpdateCandidate{cand("c1", domain.CandidatePending)},
			},
			wantNew:   []string{},
			wantMarks: domain.NotifiedMarks{CandidateIDs: []string{"c1"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := background.Decide(tt.in)
			if !reflect.DeepEqual(got.Reasons, tt.wantReasons) {
				t.Errorf("Reasons = %v, want %v", got.Reasons, tt.wantReasons)
			}
			if got.Notify() != (len(tt.wantReasons) > 0) {
				t.Errorf("Notify() = %v, want %v", got.Notify(), len(tt.wantReasons) > 0)
			}
			if gotNew := ids(got.NewCandidates); !reflect.DeepEqual(gotNew, tt.wantNew) {
				t.Errorf("NewCandidates = %v, want %v", gotNew, tt.wantNew)
			}
			if !reflect.DeepEqual(got.Marks, tt.wantMarks) {
				t.Errorf("Marks = %+v, want %+v", got.Marks, tt.wantMarks)
			}
		})
	}
}
