// Package clock is the wall-clock adapter.
package clock

import "time"

// System reads the wall clock.
type System struct{}

// Now returns the current time.
func (System) Now() time.Time { return time.Now() }
