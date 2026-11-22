package engine

import (
	"bytes"
	"fmt"
	"slices"

	"skillhub/internal/shared/orderedjson"
)

// semantics checks what shape does not: that a step's images say what its
// kind does, so a record whose identifier was recomputed over an incoherent
// step (an unlink whose target is not its preimage's, a move whose
// postimage names another tree) is refused before anything runs, rolls back
// or recovers (W2a verifier, advisory A3).
func (s Step) semantics() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalidStep, s.Kind, fmt.Sprintf(format, args...))
	}
	for i := range s.Pre {
		if err := s.Pre[i].clean(); err != nil {
			return bad("preimage %d: %v", i, err)
		}
		if err := s.Post[i].clean(); err != nil {
			return bad("postimage %d: %v", i, err)
		}
	}
	if err := s.unusedFields(); err != nil {
		return bad("%v", err)
	}
	pre, post := s.Pre[0], s.Post[0]
	switch s.Kind {
	case StepMove:
		dstPre, dstPost := s.Pre[1], s.Post[1]
		if pre.State != StateTree || dstPre.State != StateAbsent || post.State != StateAbsent ||
			dstPost.State != StateTree || dstPost.TreeSHA != pre.TreeSHA {
			return bad("%s -> %s is not a folder moved to a free path", pre.describe(), dstPost.describe())
		}
	case StepLink:
		if pre.State != StateAbsent || post.State != StateLink || post.Target != s.Target {
			return bad("%s -> %s does not create a link to %q", pre.describe(), post.describe(), s.Target)
		}
	case StepUnlink:
		if pre.State != StateLink || pre.Target != s.Target || post.State != StateAbsent {
			return bad("%s -> %s does not remove a link to %q", pre.describe(), post.describe(), s.Target)
		}
	case StepRequire:
		if (pre.State != StateTree && pre.State != StateLink) || !pre.same(post) {
			return bad("%s -> %s does not keep a folder or a link as it is", pre.describe(), post.describe())
		}
	case StepRemoveTree:
		if pre.State != StateTree || post.State != StateAbsent {
			return bad("%s -> %s does not remove a folder", pre.describe(), post.describe())
		}
	case StepRestoreTree:
		if pre.State != StateAbsent || post.State != StateTree {
			return bad("%s -> %s does not restore a folder", pre.describe(), post.describe())
		}
	case StepLockPark:
		if pre.State != StateEntry || post.State != StateAbsent {
			return bad("%s -> %s does not take an entry out", pre.describe(), post.describe())
		}
	case StepLockUnpark:
		if err := entryPost(post, s.Entry.Raw); err != nil || pre.State != StateAbsent {
			return bad("%s -> %s does not put the parked entry back", pre.describe(), post.describe())
		}
	case StepLockAdd:
		if err := entryPost(post, s.Raw); err != nil || (pre.State != StateAbsent && pre.State != StateNoLock) {
			return bad("%s -> %s does not add the entry", pre.describe(), post.describe())
		}
	case StepLockReplace:
		if err := entryPost(post, s.Raw); err != nil || pre.State != StateEntry || !sameRef(pre.Raw, s.Raw) {
			return bad("%s -> %s does not replace the entry keeping its ref", pre.describe(), post.describe())
		}
	case StepLockRemove:
		if pre.State != StateEntry || !bytes.Equal(pre.Raw, s.Raw) ||
			(post.State != StateAbsent && post.State != StateNoLock) {
			return bad("%s -> %s does not remove the entry expected", pre.describe(), post.describe())
		}
		if post.State == StateNoLock && hasRef(s.Raw) {
			// Rolling back a removal that deleted the lock adds the entry
			// again, and an added entry never carries a ref (L32).
			return bad("an entry with a ref cannot leave no lock behind")
		}
	}
	return nil
}

// entryPost checks that post is the entry raw as the lock store writes it.
func entryPost(post Image, raw []byte) error {
	canon, err := canonicalEntry(raw)
	if err != nil {
		return err
	}
	if post.State != StateEntry || !bytes.Equal(post.Raw, canon) {
		return fmt.Errorf("%w: postimage is not the entry", ErrInvalidStep)
	}
	return nil
}

// hasRef reports whether an entry carries a ref; an unreadable entry counts
// as carrying one.
func hasRef(raw []byte) bool {
	val, err := orderedjson.ParseValue(raw)
	if err != nil || val.Kind() != orderedjson.KindObject {
		return true
	}
	_, ok := val.Get("ref")
	return ok
}

// sameRef reports whether two entries carry the same ref, or none: a
// malformed entry never does.
func sameRef(a, b []byte) bool {
	ref := func(raw []byte) (string, bool, bool) {
		val, err := orderedjson.ParseValue(raw)
		if err != nil || val.Kind() != orderedjson.KindObject {
			return "", false, false
		}
		r, ok := val.Get("ref")
		if !ok {
			return "", false, true
		}
		return string(r.Bytes()), true, true
	}
	ra, hasA, okA := ref(a)
	rb, hasB, okB := ref(b)
	return okA && okB && hasA == hasB && ra == rb
}

// clean checks that an image carries only the fields of its state, and a
// state its object can have.
func (i Image) clean() error {
	isEntry := i.Entry != ""
	var ok bool
	switch i.State {
	case StateAbsent:
		ok = i.TreeSHA == "" && i.Target == "" && i.Raw == nil
	case StateTree:
		ok = !isEntry && i.TreeSHA != "" && i.Target == "" && i.Raw == nil
	case StateLink:
		ok = !isEntry && i.TreeSHA == "" && validText(i.Target) && i.Raw == nil
	case StateEntry:
		ok = isEntry && i.TreeSHA == "" && i.Target == "" && len(i.Raw) > 0
	case StateNoLock:
		ok = isEntry && i.TreeSHA == "" && i.Target == "" && i.Raw == nil
	default:
		// StateOther is only ever observed, never planned.
		ok = false
	}
	if !ok {
		return fmt.Errorf("a %s image of %s with tree %q, target %q and %d raw bytes",
			i.State, i.object(), i.TreeSHA, i.Target, len(i.Raw))
	}
	return nil
}

// unusedFields refuses a step that sets a field its kind does not use, so
// nothing in a record is silently ignored.
func (s Step) unusedFields() error {
	used := map[StepKind][]string{
		StepMove:        {"src", "dst"},
		StepLink:        {"path", "target"},
		StepUnlink:      {"path", "target"},
		StepRemoveTree:  {"path"},
		StepRequire:     {"path"},
		StepRestoreTree: {"path", "src"},
		StepLockPark:    {"name"},
		StepLockUnpark:  {"entry"},
		StepLockAdd:     {"name", "raw"},
		StepLockRemove:  {"name", "raw"},
		StepLockReplace: {"name", "raw"},
	}[s.Kind]
	fields := []struct {
		name string
		set  bool
	}{
		{"src", s.Src != ""}, {"dst", s.Dst != ""}, {"path", s.Path != ""}, {"target", s.Target != ""},
		{"name", s.Name != ""}, {"raw", s.Raw != nil}, {"entry", s.Entry != nil},
	}
	for _, f := range fields {
		if f.set && !slices.Contains(used, f.name) {
			return fmt.Errorf("sets %s, which a %s does not use", f.name, s.Kind)
		}
	}
	return nil
}
