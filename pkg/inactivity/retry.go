package inactivity

import "time"

// DefaultShutdownRetryInterval is the default minimal interval between two
// attempts of a failing shutdown command, so a failing command is not
// re-executed every patrol cycle.
const DefaultShutdownRetryInterval = 5 * time.Minute

// RetryGate throttles repeated attempts of an action (e.g. a failing
// shutdown command) to at most one per interval. It is not safe for
// concurrent use; the patrol loop is single goroutine.
type RetryGate struct {
	clock       Clock
	interval    time.Duration
	lastAttempt time.Time
	attempted   bool
}

// NewRetryGate creates a RetryGate. A nil clock uses the real clock, an
// interval <= 0 uses DefaultShutdownRetryInterval.
func NewRetryGate(clock Clock, interval time.Duration) *RetryGate {
	if clock == nil {
		clock = RealClock()
	}
	if interval <= 0 {
		interval = DefaultShutdownRetryInterval
	}
	return &RetryGate{clock: clock, interval: interval}
}

// Allow reports whether an attempt may run now: always the first time,
// afterwards only once interval has elapsed since the last recorded
// attempt.
func (g *RetryGate) Allow() bool {
	if !g.attempted {
		return true
	}
	return !g.lastAttempt.Add(g.interval).After(g.clock.Now())
}

// Record marks an attempt at the current time.
func (g *RetryGate) Record() {
	g.lastAttempt = g.clock.Now()
	g.attempted = true
}
