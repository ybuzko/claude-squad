package app

import (
	"claude-squad/linear"
	"claude-squad/session"
	"testing"

	"github.com/stretchr/testify/assert"
)

func ticket(id string, st session.Status, inView bool) *session.Instance {
	return &session.Instance{Title: id, IssueID: id, Status: st, InView: inView}
}

func issues(ids ...string) []linear.Issue {
	out := make([]linear.Issue, len(ids))
	for i, id := range ids {
		out[i] = linear.Issue{Identifier: id}
	}
	return out
}

func queuedIDs(p viewPlan) []string {
	var ids []string
	for _, is := range p.queue {
		ids = append(ids, is.Identifier)
	}
	return ids
}

func TestPlanViewQueuesNewTicketsInViewOrder(t *testing.T) {
	live := ticket("TSA-2", session.Ready, true)
	plan := planView(issues("TSA-3", "TSA-2", "TSA-1"), []*session.Instance{live}, nil)
	assert.Equal(t, []string{"TSA-3", "TSA-1"}, queuedIDs(plan))
	assert.Empty(t, plan.archive)
	assert.False(t, plan.changed)
}

func TestPlanViewArchivesTicketThatLeftTheView(t *testing.T) {
	idle := ticket("TSA-1", session.Ready, true)
	blocked := ticket("TSA-2", session.Blocked, true)
	plan := planView(issues(), []*session.Instance{idle, blocked}, nil)
	assert.ElementsMatch(t, []*session.Instance{idle, blocked}, plan.archive)
}

func TestPlanViewWaitsForBusySessionBeforeArchiving(t *testing.T) {
	running := ticket("TSA-1", session.Running, true)
	loading := ticket("TSA-2", session.Loading, true)
	restoring := ticket("TSA-3", session.Paused, true)
	restoring.Restoring = true

	plan := planView(issues(), []*session.Instance{running, loading, restoring}, nil)
	assert.Empty(t, plan.archive)
	// InView stays true so the next poll sees the same departure again.
	assert.True(t, running.InView)

	running.Status = session.Ready
	plan = planView(issues(), []*session.Instance{running}, nil)
	assert.Equal(t, []*session.Instance{running}, plan.archive)
}

func TestPlanViewRestoresArchivedTicketThatCameBack(t *testing.T) {
	archived := ticket("TSA-1", session.Paused, false)
	archived.Archived = true
	plan := planView(issues("TSA-1"), nil, []*session.Instance{archived})
	assert.Equal(t, []string{"TSA-1"}, queuedIDs(plan))
}

func TestPlanViewKeepsManuallyArchivedTicketUntilItLeavesAndReturns(t *testing.T) {
	// Archived by hand while still in the view: InView is still true.
	archived := ticket("TSA-1", session.Paused, true)
	archived.Archived = true

	plan := planView(issues("TSA-1"), nil, []*session.Instance{archived})
	assert.Empty(t, plan.queue, "still in the view: stays archived")

	plan = planView(issues(), nil, []*session.Instance{archived})
	assert.True(t, plan.changed)
	assert.False(t, archived.InView, "left the view")

	plan = planView(issues("TSA-1"), nil, []*session.Instance{archived})
	assert.Equal(t, []string{"TSA-1"}, queuedIDs(plan), "came back: restore")
}

func TestPlanViewMarksTicketsEnteringTheView(t *testing.T) {
	// Spawned with `t` from outside the view.
	manual := ticket("TSA-1", session.Ready, false)

	plan := planView(issues(), []*session.Instance{manual}, nil)
	assert.Empty(t, plan.archive, "never in the view: not the poller's to archive")
	assert.False(t, plan.changed)

	plan = planView(issues("TSA-1"), []*session.Instance{manual}, nil)
	assert.True(t, plan.changed)
	assert.True(t, manual.InView)
	assert.Empty(t, plan.queue)

	plan = planView(issues(), []*session.Instance{manual}, nil)
	assert.Equal(t, []*session.Instance{manual}, plan.archive, "entered then left: archived")
}

func TestPlanViewIgnoresPlainSessions(t *testing.T) {
	plain := &session.Instance{Title: "scratch", Status: session.Ready}
	plan := planView(issues("TSA-1"), []*session.Instance{plain}, []*session.Instance{plain})
	assert.Equal(t, []string{"TSA-1"}, queuedIDs(plan))
	assert.Empty(t, plan.archive)
	assert.False(t, plan.changed)
}

func TestPlanViewPrefersLiveInstanceOverArchivedDuplicate(t *testing.T) {
	live := ticket("TSA-1", session.Ready, true)
	stale := ticket("TSA-1", session.Paused, false)
	stale.Archived = true
	plan := planView(issues("TSA-1"), []*session.Instance{live}, []*session.Instance{stale})
	assert.Empty(t, plan.queue)
}
