// Package fakes holds an in-memory double of every port of
// internal/shared/ports. Only tests import it.
//
// Every double records its calls and accepts an injected failure: set Fail
// to a function that returns a non-nil error for the call that must fail.
// A failing call changes nothing. Doubles are safe for concurrent use.
package fakes

import (
	"slices"
	"sync"
)

// Call is one recorded call: the port method and its string arguments.
type Call struct {
	Op   string
	Args []string
}

// Faults records calls and injects failures. Doubles embed it.
type Faults struct {
	// Fail, when set, is asked before every call; a non-nil error makes the
	// call fail with it and change nothing.
	Fail func(Call) error

	mu    sync.Mutex
	calls []Call
}

// Calls returns the calls made so far, oldest first.
func (f *Faults) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// enter records a call and returns the injected failure, if any.
func (f *Faults) enter(op string, args ...string) error {
	c := Call{Op: op, Args: slices.Clone(args)}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	fail := f.Fail
	f.mu.Unlock()
	if fail != nil {
		return fail(c)
	}
	return nil
}
