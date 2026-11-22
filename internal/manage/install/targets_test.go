package install_test

import (
	"reflect"
	"slices"
	"testing"

	"skillhub/internal/data"
	"skillhub/internal/manage/install"
	"skillhub/internal/shared/domain"
)

// TestProposeTargetsJoinsTheDetectedAdministrableHarnessesAndThePreferred:
// decision 1. The detected harnesses with a link directory are proposed, in
// table order, together with the preferred ones that skillhub can link in;
// a universal harness (it reads the store) and an unknown preference are
// not. Proposing changes nothing.
func TestProposeTargetsJoinsTheDetectedAdministrableHarnessesAndThePreferred(t *testing.T) {
	s := newSite(t, true)
	g := s.graph()
	byID := map[string]data.Harness{}
	for _, h := range s.harness {
		byID[h.ID] = h
	}
	var detectedLinked, detectedUniversal, undetectedLinked []string
	for _, h := range s.harness {
		detected := slices.Contains(g.Harnesses, h.ID)
		switch {
		case detected && h.LinkDir != "":
			detectedLinked = append(detectedLinked, h.ID)
		case detected:
			detectedUniversal = append(detectedUniversal, h.ID)
		case h.LinkDir != "":
			undetectedLinked = append(undetectedLinked, h.ID)
		}
	}
	if len(detectedLinked) < 2 || len(detectedUniversal) == 0 || len(undetectedLinked) == 0 {
		t.Fatalf("fixture: detected linked %v, detected universal %v, undetected linked %v",
			detectedLinked, detectedUniversal, undetectedLinked)
	}
	prefs := domain.Preferences{Targets: []string{
		undetectedLinked[0], detectedLinked[0], "no-such-harness", detectedUniversal[0],
	}}
	home0, state0 := s.everything(), s.state()

	got := install.ProposeTargets(g, s.harness, prefs)

	var want []string
	for _, h := range s.harness {
		if slices.Contains(detectedLinked, h.ID) || h.ID == undetectedLinked[0] {
			want = append(want, h.ID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProposeTargets = %v, want %v", got, want)
	}
	if got := install.ProposeTargets(g, s.harness, domain.Preferences{}); !reflect.DeepEqual(got, detectedLinked) {
		t.Errorf("ProposeTargets without preferences = %v, want %v", got, detectedLinked)
	}
	sameHome(t, "proposing", home0, s.everything())
	if !reflect.DeepEqual(s.state(), state0) {
		t.Error("proposing changed the state")
	}
}
