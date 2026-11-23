package inactivity

import (
	"testing"
	"time"

	"github.com/loft-sh/devpod/pkg/agent"
	"gotest.tools/assert"
)

var base = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// ws builds a workspace whose last activity was `ago` before base.
func ws(id string, ago time.Duration, timeout string) Workspace {
	return Workspace{
		ID:                 id,
		ModTime:            base.Add(-ago),
		Timeout:            timeout,
		HasShutdownCommand: true,
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		workspaces  []Workspace
		connections int
		rule        Rule
		wantIdle    bool
		wantStopIn  time.Duration
		wantBasis   string
		check       func(t *testing.T, d Decision)
	}{
		{
			name:       "exactly at timeout is idle",
			workspaces: []Workspace{ws("a", 20*time.Minute, "20m")},
			wantIdle:   true,
			wantStopIn: 0,
			wantBasis:  "a",
		},
		{
			name:       "one second before timeout is active",
			workspaces: []Workspace{ws("a", 20*time.Minute-time.Second, "20m")},
			wantIdle:   false,
			wantStopIn: time.Second,
			wantBasis:  "a",
		},
		{
			name:       "future timestamp is active (clock skew)",
			workspaces: []Workspace{ws("a", -5*time.Minute, "20m")},
			wantIdle:   false,
			wantStopIn: 25 * time.Minute,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].IdleFor, -5*time.Minute)
			},
		},
		{
			name:        "active connections are always active",
			workspaces:  []Workspace{ws("a", time.Hour, "20m")},
			connections: 1,
			wantIdle:    false,
			wantStopIn:  Never,
			wantBasis:   "",
		},
		{
			name: "busy flag pauses the idle timer",
			workspaces: []Workspace{func() Workspace {
				w := ws("a", time.Hour, "20m")
				w.Busy = true
				return w
			}()},
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].Expired, false)
				assert.Equal(t, d.Workspaces[0].ExpiresIn, Never)
			},
		},
		{
			name:       "after busy flag disappears idle counts from last activity",
			workspaces: []Workspace{ws("a", 30*time.Minute, "20m")},
			wantIdle:   true,
			wantStopIn: 0,
			wantBasis:  "a",
		},
		{
			name:       "zero timeout disables auto-stop",
			workspaces: []Workspace{ws("a", 24*time.Hour, "0s")},
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].TimeoutSource, TimeoutSourceDisabled)
			},
		},
		{
			name:       "negative timeout disables auto-stop",
			workspaces: []Workspace{ws("a", 24*time.Hour, "-5m")},
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].TimeoutSource, TimeoutSourceDisabled)
			},
		},
		{
			name:       "unparseable timeout falls back to default and is marked",
			workspaces: []Workspace{ws("a", agent.DefaultInactivityTimeout, "not-a-duration")},
			wantIdle:   true,
			wantStopIn: 0,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].TimeoutSource, TimeoutSourceDefault)
				assert.Equal(t, d.Workspaces[0].Timeout, agent.DefaultInactivityTimeout)
				assert.Assert(t, d.Workspaces[0].TimeoutErr != nil)
			},
		},
		{
			name:       "empty timeout uses default",
			workspaces: []Workspace{ws("a", agent.DefaultInactivityTimeout-time.Minute, "")},
			wantIdle:   false,
			wantStopIn: time.Minute,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].TimeoutSource, TimeoutSourceDefault)
				assert.Assert(t, d.Workspaces[0].TimeoutErr == nil)
			},
		},
		{
			name: "refresh record later than modtime wins",
			workspaces: []Workspace{func() Workspace {
				w := ws("a", time.Hour, "20m")
				w.RefreshedAt = base.Add(-10 * time.Minute)
				return w
			}()},
			wantIdle:   false,
			wantStopIn: 10 * time.Minute,
			wantBasis:  "a",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, d.Workspaces[0].Activity, base.Add(-10*time.Minute))
			},
		},
		{
			name: "modtime later than refresh record wins",
			workspaces: []Workspace{func() Workspace {
				w := ws("a", 10*time.Minute, "20m")
				w.RefreshedAt = base.Add(-time.Hour)
				return w
			}()},
			wantIdle:   false,
			wantStopIn: 10 * time.Minute,
			wantBasis:  "a",
		},
		{
			name: "legacy rule: latest activity decides, shorter timeout of other workspace ignored",
			workspaces: []Workspace{
				ws("short", time.Hour, "1m"),     // expired long ago, but not latest
				ws("long", 10*time.Minute, "2h"), // latest activity, long timeout
			},
			rule:       RuleLatestActivity,
			wantIdle:   false,
			wantStopIn: 110 * time.Minute,
			wantBasis:  "long",
		},
		{
			name: "legacy rule: latest activity expired means idle",
			workspaces: []Workspace{
				ws("short", time.Hour, "1m"),
				ws("long", 30*time.Minute, "20m"),
			},
			rule:       RuleLatestActivity,
			wantIdle:   true,
			wantStopIn: 0,
			wantBasis:  "long",
		},
		{
			name: "all-idle rule: one workspace within its timeout keeps machine up",
			workspaces: []Workspace{
				ws("short", time.Hour, "1m"),
				ws("long", 10*time.Minute, "2h"),
			},
			rule:       RuleAllIdle,
			wantIdle:   false,
			wantStopIn: 110 * time.Minute,
			wantBasis:  "long",
		},
		{
			name: "all-idle rule: idle only when every workspace exceeded its timeout",
			workspaces: []Workspace{
				ws("short", time.Hour, "1m"),
				ws("long", 30*time.Minute, "20m"),
			},
			rule:       RuleAllIdle,
			wantIdle:   true,
			wantStopIn: 0,
			wantBasis:  "long", // expired last: long 10m ago, short 59m ago
		},
		{
			name: "all-idle rule: disabled workspace blocks stopping",
			workspaces: []Workspace{
				ws("off", time.Hour, "0s"),
				ws("expired", time.Hour, "1m"),
			},
			rule:       RuleAllIdle,
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "off",
		},
		{
			name: "all-idle rule: busy workspace blocks stopping",
			workspaces: []Workspace{
				func() Workspace { w := ws("busy", time.Hour, "1m"); w.Busy = true; return w }(),
				ws("expired", time.Hour, "1m"),
			},
			rule:       RuleAllIdle,
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "busy",
		},
		{
			name: "workspaces without shutdown command do not participate",
			workspaces: []Workspace{
				{ID: "no-shutdown", ModTime: base.Add(-time.Hour), Timeout: "1m"},
			},
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "",
			check: func(t *testing.T, d Decision) {
				assert.Equal(t, len(d.Workspaces), 1)
				assert.Equal(t, d.Workspaces[0].Participates, false)
			},
		},
		{
			name:       "no workspaces at all is a no-op",
			workspaces: nil,
			wantIdle:   false,
			wantStopIn: Never,
			wantBasis:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := base
			if !tc.now.IsZero() {
				now = tc.now
			}
			d := Evaluate(now, tc.workspaces, tc.connections, Options{Rule: tc.rule})
			assert.Equal(t, d.Idle, tc.wantIdle, "reason: %s", d.Reason)
			assert.Equal(t, d.StopIn, tc.wantStopIn, "reason: %s", d.Reason)
			assert.Equal(t, d.Basis, tc.wantBasis, "reason: %s", d.Reason)
			if tc.check != nil {
				tc.check(t, d)
			}
		})
	}
}

// TestEvaluateBusyTransition pins the busy-pause semantics across two
// evaluations: while busy the workspace never expires, and once the busy
// flag disappears the idle time is measured from the last activity again.
func TestEvaluateBusyTransition(t *testing.T) {
	w := ws("a", 25*time.Minute, "20m")
	w.Busy = true

	d := Evaluate(base, []Workspace{w}, 0, Options{})
	assert.Equal(t, d.Idle, false)
	assert.Equal(t, d.StopIn, Never)

	// busy flag removed: 25m idle > 20m timeout -> idle
	w.Busy = false
	d = Evaluate(base, []Workspace{w}, 0, Options{})
	assert.Equal(t, d.Idle, true)
}

// TestEvaluatorUsesInjectedClock verifies the evaluator reads the time from
// the injected clock.
func TestEvaluatorUsesInjectedClock(t *testing.T) {
	clock := &fakeClock{t: base}
	e := Evaluator{Clock: clock}

	d := e.Evaluate([]Workspace{ws("a", 10*time.Minute, "20m")}, 0)
	assert.Equal(t, d.Idle, false)
	assert.Equal(t, d.StopIn, 10*time.Minute)

	clock.t = base.Add(15 * time.Minute)
	d = e.Evaluate([]Workspace{ws("a", 10*time.Minute, "20m")}, 0)
	assert.Equal(t, d.Idle, true)
}

type fakeClock struct {
	t time.Time
}

func (f *fakeClock) Now() time.Time { return f.t }
