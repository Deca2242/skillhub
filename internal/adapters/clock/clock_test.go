package clock_test

import (
	"testing"
	"time"

	"skillhub/internal/adapters/clock"
	"skillhub/internal/testsupport"
)

func TestMain(m *testing.M) { testsupport.Main(m) }

func TestSystemNowReadsTheWallClock(t *testing.T) {
	before := time.Now()
	got := clock.System{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}
