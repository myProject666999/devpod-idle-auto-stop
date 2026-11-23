package inactivity

import (
	"testing"
	"time"

	"gotest.tools/assert"
)

func TestRetryGate(t *testing.T) {
	clock := &fakeClock{t: base}
	gate := NewRetryGate(clock, time.Minute)

	// first attempt is always allowed
	assert.Equal(t, gate.Allow(), true)
	gate.Record()

	// a second attempt within the interval is blocked
	clock.t = base.Add(30 * time.Second)
	assert.Equal(t, gate.Allow(), false)

	// exactly at the interval boundary it is allowed again
	clock.t = base.Add(time.Minute)
	assert.Equal(t, gate.Allow(), true)
	gate.Record()

	// and blocked again right after
	clock.t = base.Add(time.Minute + time.Second)
	assert.Equal(t, gate.Allow(), false)
}

func TestRetryGateDefaults(t *testing.T) {
	clock := &fakeClock{t: base}
	gate := NewRetryGate(clock, 0)

	gate.Record()
	clock.t = base.Add(DefaultShutdownRetryInterval - time.Second)
	assert.Equal(t, gate.Allow(), false)
	clock.t = base.Add(DefaultShutdownRetryInterval)
	assert.Equal(t, gate.Allow(), true)
}
