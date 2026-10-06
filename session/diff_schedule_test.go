package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDiffScheduleIdleInstanceRefreshesOnlyWhenStale(t *testing.T) {
	i := &Instance{}
	t0 := time.Unix(1_000_000, 0)
	assert.True(t, i.DiffDue(t0, false), "never computed")
	i.MarkDiffComputed(t0, false)

	for _, d := range []time.Duration{time.Second, 5 * time.Second, 59 * time.Second} {
		i.NoteDiffActivity(false, 0)
		assert.False(t, i.DiffDue(t0.Add(d), false), "idle at %v", d)
	}
	assert.True(t, i.DiffDue(t0.Add(DiffMaxAge), false), "stale after DiffMaxAge")
}

func TestDiffScheduleActivityRefreshesAtMostEveryMinInterval(t *testing.T) {
	i := &Instance{}
	t0 := time.Unix(1_000_000, 0)
	i.MarkDiffComputed(t0, false)

	i.NoteDiffActivity(true, 0)
	assert.False(t, i.DiffDue(t0.Add(2*time.Second), false), "too soon")
	assert.True(t, i.DiffDue(t0.Add(DiffMinInterval), false))

	i.MarkDiffComputed(t0.Add(DiffMinInterval), false)
	assert.False(t, i.DiffDue(t0.Add(2*DiffMinInterval), false), "activity was consumed")
}

func TestDiffScheduleHookTimestampCountsAsActivityOnlyWhenItChanges(t *testing.T) {
	i := &Instance{}
	t0 := time.Unix(1_000_000, 0)
	i.NoteDiffActivity(false, 100)
	i.MarkDiffComputed(t0, false)

	i.NoteDiffActivity(false, 100)
	assert.False(t, i.DiffDue(t0.Add(DiffMinInterval), false), "same status file")

	i.NoteDiffActivity(false, 101)
	assert.True(t, i.DiffDue(t0.Add(DiffMinInterval), false), "status file rewritten")
}

func TestDiffScheduleSelectedInstance(t *testing.T) {
	i := &Instance{}
	t0 := time.Unix(1_000_000, 0)
	i.MarkDiffComputed(t0, false)

	assert.True(t, i.DiffDue(t0.Add(time.Second), true), "newly selected: needs full content now")
	i.MarkDiffComputed(t0.Add(time.Second), true)

	assert.False(t, i.DiffDue(t0.Add(3*time.Second), true))
	assert.True(t, i.DiffDue(t0.Add(time.Second+DiffMinInterval), true), "selected refreshes every DiffMinInterval")
}
