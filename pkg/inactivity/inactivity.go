// Package inactivity defines the verifiable rules that decide when a DevPod
// machine or container is considered idle and should auto-stop.
//
// The rules are implemented as pure functions over in-memory workspace
// snapshots with an injectable clock, so they can be tested without real
// machines or containers. The file-based adapters in file.go build the
// snapshots from the agent home folder (workspace config modtimes and busy
// files), reusing agent.HasWorkspaceBusyFile and
// agent.DefaultInactivityTimeout.
//
// The decided semantics are:
//
//   - A workspace's activity timestamp is the LATER of its config file
//     modtime and any refresh record (RefreshedAt).
//   - Any active connection means the machine is active, no matter what the
//     timestamps say.
//   - A present busy flag pauses the idle timer for that workspace (it is
//     treated as active while busy) instead of adding a fixed grace period
//     to its activity timestamp.
//   - A timeout of zero or less disables auto-stop for that workspace; it
//     is never parsed as "immediately timed out".
//   - An empty timeout uses agent.DefaultInactivityTimeout. An unparseable
//     timeout also falls back to agent.DefaultInactivityTimeout, and the
//     fallback is reported via WorkspaceStatus.TimeoutSource and
//     WorkspaceStatus.TimeoutErr.
//   - Timestamps in the future (clock skew) simply count as "not yet idle".
//   - Workspaces without a shutdown command never participate in the
//     decision. If none participate, the decision is a no-op.
//
// Two aggregation rules are provided for machines with multiple workspaces:
//
//   - RuleLatestActivity (legacy, current behaviour): the workspace with
//     the latest activity determines idleness and its own timeout is used.
//     A shorter timeout of any other workspace is ignored.
//   - RuleAllIdle (new, opt-in): the machine stops only when EVERY
//     participating workspace has been idle for longer than its own
//     timeout. A workspace with auto-stop disabled blocks stopping.
package inactivity

import (
	"time"

	"github.com/loft-sh/devpod/pkg/agent"
)

// Never is used for StopIn / ExpiresIn when there is no finite time at
// which the target would stop (auto-stop disabled, busy flag set, active
// connections, or nothing to evaluate).
const Never = time.Duration(-1)

// Rule selects how multiple workspaces on one machine are aggregated.
type Rule int

const (
	// RuleLatestActivity is the legacy rule: the most recently active
	// workspace decides, with its own timeout.
	RuleLatestActivity Rule = iota
	// RuleAllIdle is the new rule: the machine is idle only when every
	// participating workspace is idle beyond its own timeout.
	RuleAllIdle
)

// TimeoutSource describes where the effective timeout of a workspace came
// from.
type TimeoutSource string

const (
	// TimeoutSourceConfigured means a valid, positive configured timeout.
	TimeoutSourceConfigured TimeoutSource = "configured"
	// TimeoutSourceDefault means the timeout was empty or unparseable and
	// the default was used.
	TimeoutSourceDefault TimeoutSource = "default"
	// TimeoutSourceDisabled means the configured timeout was zero or
	// negative and auto-stop is disabled for this workspace.
	TimeoutSourceDisabled TimeoutSource = "disabled"
)

// Clock abstracts the current time so tests can inject a fake.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RealClock returns a Clock backed by time.Now.
func RealClock() Clock { return realClock{} }

// Workspace is an in-memory snapshot of everything the idle rules need to
// know about a single workspace.
type Workspace struct {
	// ID identifies the workspace.
	ID string
	// ModTime is the modification time of the workspace config file.
	ModTime time.Time
	// RefreshedAt is an optional extra activity record (zero if none).
	// The effective activity is the later of ModTime and RefreshedAt.
	RefreshedAt time.Time
	// Timeout is the raw configured inactivity timeout (e.g. "30m").
	Timeout string
	// HasShutdownCommand reports whether a shutdown command is configured.
	// Workspaces without one never participate in the decision.
	HasShutdownCommand bool
	// Busy reports whether the busy flag (workspace.lock) is present.
	Busy bool
}

// activity returns the effective last-activity timestamp: the later of the
// config modtime and the refresh record.
func (w Workspace) activity() time.Time {
	if w.RefreshedAt.After(w.ModTime) {
		return w.RefreshedAt
	}
	return w.ModTime
}

// Options configures Evaluate.
type Options struct {
	// Rule selects the multi-workspace aggregation rule. The zero value is
	// RuleLatestActivity (legacy behaviour).
	Rule Rule
	// DefaultTimeout is used when a workspace has no (valid) configured
	// timeout. Zero means agent.DefaultInactivityTimeout.
	DefaultTimeout time.Duration
}

func (o Options) defaultTimeout() time.Duration {
	if o.DefaultTimeout != 0 {
		return o.DefaultTimeout
	}
	return agent.DefaultInactivityTimeout
}

// WorkspaceStatus is the per-workspace breakdown of an evaluation.
type WorkspaceStatus struct {
	ID string
	// Participates is false for workspaces without a shutdown command;
	// they are excluded from the decision.
	Participates bool
	// Activity is the effective last-activity timestamp.
	Activity time.Time
	// Timeout is the effective timeout (meaningless when disabled).
	Timeout time.Duration
	// TimeoutSource describes where Timeout came from.
	TimeoutSource TimeoutSource
	// TimeoutErr is set when an unparseable configured timeout fell back
	// to the default.
	TimeoutErr error
	// Busy mirrors Workspace.Busy.
	Busy bool
	// IdleFor is how long the workspace has been idle (negative when the
	// activity timestamp is in the future). Zero while busy.
	IdleFor time.Duration
	// ExpiresIn is the remaining time until this workspace's own timeout
	// is exceeded (<= 0 when expired, Never when disabled or busy).
	ExpiresIn time.Duration
	// Expired reports whether the workspace has been idle past its own
	// timeout.
	Expired bool
}

// Decision is the outcome of an evaluation.
type Decision struct {
	// Idle is true when the machine/container should stop now.
	Idle bool
	// StopIn is the remaining time until the decision becomes idle
	// (0 when Idle, Never when there is no finite stop time).
	StopIn time.Duration
	// Basis is the ID of the workspace the decision is based on
	// ("" when no workspace participated).
	Basis string
	// Reason is a human-readable explanation, mainly for logging.
	Reason string
	// Workspaces holds the per-workspace breakdown.
	Workspaces []WorkspaceStatus
}

// parseTimeout resolves a raw configured timeout.
func parseTimeout(raw string, def time.Duration) (time.Duration, TimeoutSource, error) {
	if raw == "" {
		return def, TimeoutSourceDefault, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def, TimeoutSourceDefault, err
	}
	if d <= 0 {
		return 0, TimeoutSourceDisabled, nil
	}
	return d, TimeoutSourceConfigured, nil
}

// Evaluate applies the idle rules to a snapshot of workspaces.
//
// now is the current time (inject a fake clock in tests), workspaces are
// the in-memory workspace snapshots and activeConnections is the number of
// currently active connections to the machine/container.
func Evaluate(now time.Time, workspaces []Workspace, activeConnections int, opts Options) Decision {
	decision := Decision{
		StopIn:     Never,
		Workspaces: make([]WorkspaceStatus, 0, len(workspaces)),
	}

	// evaluate every workspace individually
	for _, w := range workspaces {
		status := WorkspaceStatus{
			ID:           w.ID,
			Participates: w.HasShutdownCommand,
			Activity:     w.activity(),
			Busy:         w.Busy,
			ExpiresIn:    Never,
		}
		if !status.Participates {
			decision.Workspaces = append(decision.Workspaces, status)
			continue
		}

		status.Timeout, status.TimeoutSource, status.TimeoutErr = parseTimeout(w.Timeout, opts.defaultTimeout())
		switch {
		case status.TimeoutSource == TimeoutSourceDisabled:
			// auto-stop disabled: never expires
		case w.Busy:
			// busy pauses the idle timer: treated as active
		default:
			status.IdleFor = now.Sub(status.Activity)
			status.ExpiresIn = status.Timeout - status.IdleFor
			status.Expired = status.ExpiresIn <= 0
		}
		decision.Workspaces = append(decision.Workspaces, status)
	}

	// any active connection means active, regardless of timestamps
	if activeConnections > 0 {
		decision.Reason = "active connections"
		return decision
	}

	participants := participating(decision.Workspaces)
	if len(participants) == 0 {
		decision.Reason = "no workspaces with a shutdown command"
		return decision
	}

	if opts.Rule == RuleAllIdle {
		return evaluateAllIdle(decision, participants)
	}
	return evaluateLatestActivity(decision, participants)
}

func participating(statuses []WorkspaceStatus) []WorkspaceStatus {
	var ret []WorkspaceStatus
	for _, s := range statuses {
		if s.Participates {
			ret = append(ret, s)
		}
	}
	return ret
}

// evaluateLatestActivity implements the legacy rule: the workspace with the
// latest activity decides, with its own timeout.
func evaluateLatestActivity(decision Decision, participants []WorkspaceStatus) Decision {
	latest := participants[0]
	for _, s := range participants[1:] {
		if s.Activity.After(latest.Activity) {
			latest = s
		}
	}

	decision.Basis = latest.ID
	switch {
	case latest.TimeoutSource == TimeoutSourceDisabled:
		decision.StopIn = Never
		decision.Reason = "auto-stop disabled for most recently active workspace"
	case latest.Busy:
		decision.StopIn = Never
		decision.Reason = "most recently active workspace is busy"
	case latest.Expired:
		decision.Idle = true
		decision.StopIn = 0
		decision.Reason = "most recently active workspace exceeded its timeout"
	default:
		decision.StopIn = latest.ExpiresIn
		decision.Reason = "most recently active workspace still within its timeout"
	}
	return decision
}

// evaluateAllIdle implements the new rule: the machine is idle only when
// every participating workspace exceeded its own timeout.
func evaluateAllIdle(decision Decision, participants []WorkspaceStatus) Decision {
	// the workspace that expires last determines when the machine stops
	last := participants[0]
	for _, s := range participants[1:] {
		if laterExpires(s, last) {
			last = s
		}
	}

	decision.Basis = last.ID
	switch {
	case last.TimeoutSource == TimeoutSourceDisabled:
		decision.StopIn = Never
		decision.Reason = "auto-stop disabled for workspace " + last.ID
	case last.Busy:
		decision.StopIn = Never
		decision.Reason = "workspace " + last.ID + " is busy"
	case !last.Expired:
		decision.StopIn = last.ExpiresIn
		decision.Reason = "workspace " + last.ID + " still within its timeout"
	default:
		decision.Idle = true
		decision.StopIn = 0
		decision.Reason = "all workspaces exceeded their timeouts"
	}
	return decision
}

// laterExpires reports whether a expires later than b, where Never counts
// as "later than any finite time".
func laterExpires(a, b WorkspaceStatus) bool {
	if a.ExpiresIn == Never {
		return b.ExpiresIn != Never
	}
	if b.ExpiresIn == Never {
		return false
	}
	return a.ExpiresIn > b.ExpiresIn
}

// Evaluator bundles a clock and options for repeated evaluations.
type Evaluator struct {
	Clock   Clock
	Options Options
}

// Evaluate evaluates the given snapshot at the evaluator's current time.
func (e Evaluator) Evaluate(workspaces []Workspace, activeConnections int) Decision {
	clock := e.Clock
	if clock == nil {
		clock = RealClock()
	}
	return Evaluate(clock.Now(), workspaces, activeConnections, e.Options)
}
