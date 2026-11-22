package repair

import (
	"fmt"

	"skillhub/internal/manage/engine"
)

// replay adds the steps of plan, which another planner built, to b, which
// must hold no step yet: an effect of plan that names a step by its index
// stays valid only then. The builder checks every step again against the disk
// and the steps before it, exactly as the planner's own builder did, so a
// composed plan never carries a step the engine would not plan.
//
// It is how a repair reuses a planner whose steps it needs inside a larger
// plan (enable, then link) without planning those steps a second way.
func replay(b *engine.Builder, plan engine.Plan) error {
	return replaySteps(b, plan.Action, plan.Steps, nil)
}

// replaySteps adds steps to b as replay does; override, when set, may add
// something else in place of a step and report that it did.
func replaySteps(b *engine.Builder, action string, steps []engine.Step, override func(engine.Step) (bool, error)) error {
	for i, s := range steps {
		if override != nil {
			done, err := override(s)
			if err != nil {
				return fmt.Errorf("replay %s: %w", action, err)
			}
			if done {
				continue
			}
		}
		var err error
		switch s.Kind {
		case engine.StepMove:
			err = b.Move(s.Src, s.Dst)
		case engine.StepLink:
			err = b.Link(s.Path, s.Target)
		case engine.StepUnlink:
			err = b.Unlink(s.Path, s.Target)
		case engine.StepRemoveTree:
			err = b.RemoveTree(s.Path)
		case engine.StepRestoreTree:
			err = b.RestoreTree(s.Path, s.Src, s.Post[0].TreeSHA)
		case engine.StepLockPark:
			err = b.LockPark(s.Name)
		case engine.StepLockUnpark:
			err = b.LockUnpark(*s.Entry)
		case engine.StepLockAdd:
			err = b.LockAdd(s.Name, s.Raw)
		case engine.StepRequire:
			err = b.Require(s.Pre[0])
		case engine.StepLockRemove:
			if s.Post[0].State == engine.StateNoLock {
				err = b.LockRemoveLast(s.Name, s.Raw)
			} else {
				err = b.LockRemove(s.Name, s.Raw)
			}
		default:
			err = fmt.Errorf("%w: step %d of %s is %q", engine.ErrInvalidStep, i, action, s.Kind)
		}
		if err != nil {
			return fmt.Errorf("replay %s: %w", action, err)
		}
	}
	return nil
}
