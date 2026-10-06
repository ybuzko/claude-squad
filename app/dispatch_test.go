package app

import (
	"claude-squad/linear"
	"claude-squad/session"
	"claude-squad/status"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTicketBranchNameFollowsRepoConvention(t *testing.T) {
	issue := linear.Issue{Identifier: "TSA-81", Title: "Aritzia prod requires INTERNAL_ORDER for cost allocation!"}
	assert.Equal(t, "agent/tsa-81-aritzia-prod-requires-internal-order-for-cost-allo",
		ticketBranchName("agent/", issue))
}

func TestTicketBranchNameWithoutTitle(t *testing.T) {
	assert.Equal(t, "agent/tsa-1", ticketBranchName("agent/", linear.Issue{Identifier: "TSA-1", Title: "  ---  "}))
}

func TestApplyHookStatus(t *testing.T) {
	inst := &session.Instance{Title: "TSA-1", IssueID: "TSA-1", Status: session.Running}

	persist := applyHookStatus(inst, &status.Status{State: status.StateBlocked, Reason: "needs permission", SessionID: "s1"})
	assert.True(t, persist)
	assert.Equal(t, session.Blocked, inst.Status)
	assert.Equal(t, "needs permission", inst.HookReason)
	assert.Equal(t, "s1", inst.ClaudeSessionID)

	persist = applyHookStatus(inst, &status.Status{State: status.StateIdle, SessionID: "s1"})
	assert.False(t, persist)
	assert.Equal(t, session.Ready, inst.Status)

	persist = applyHookStatus(inst, &status.Status{State: status.StateRunning, SessionID: "s2"})
	assert.True(t, persist)
	assert.Equal(t, session.Running, inst.Status)
	assert.Equal(t, "s2", inst.ClaudeSessionID)
}

func TestResumeProgram(t *testing.T) {
	manual := &session.Instance{Title: "scratch", Program: "/usr/local/bin/claude"}
	assert.Equal(t, "/usr/local/bin/claude", manual.ResumeProgram())

	ticket := &session.Instance{Title: "TSA-1", IssueID: "TSA-1", Program: `/usr/local/bin/claude "/triage TSA-1"`}
	assert.Equal(t, "/usr/local/bin/claude -c", ticket.ResumeProgram())

	ticket.ClaudeSessionID = "abc-123"
	assert.Equal(t, "/usr/local/bin/claude --resume abc-123", ticket.ResumeProgram())
}

func TestRefreshTicketDetails(t *testing.T) {
	active := &session.Instance{Title: "TSA-1", IssueID: "TSA-1", IssueTitle: "Old"}
	archived := &session.Instance{Title: "TSA-2", IssueID: "TSA-2", Archived: true}
	plain := &session.Instance{Title: "scratch"}
	view := []linear.Issue{
		{Identifier: "TSA-1", Title: "Renamed", DueDate: "2026-10-05"},
		{Identifier: "TSA-2", Title: "Second"},
	}

	assert.True(t, refreshTicketDetails(view, []*session.Instance{active, plain}, []*session.Instance{archived}))
	assert.Equal(t, "Renamed", active.IssueTitle)
	assert.Equal(t, "2026-10-05", active.IssueDueDate)
	assert.Equal(t, "Second", archived.IssueTitle)
	assert.Empty(t, plain.IssueTitle)

	assert.False(t, refreshTicketDetails(view, []*session.Instance{active}, []*session.Instance{archived}), "no change")

	view[0].DueDate = ""
	assert.True(t, refreshTicketDetails(view, []*session.Instance{active}))
	assert.Empty(t, active.IssueDueDate, "a cleared due date is cleared here too")
}

func TestApplyHookStatusUsesHookTimestampAsStatusTime(t *testing.T) {
	inst := &session.Instance{Title: "TSA-1", IssueID: "TSA-1", Status: session.Running}
	applyHookStatus(inst, &status.Status{State: status.StateIdle, TS: 1_791_000_000})
	assert.Equal(t, int64(1_791_000_000), inst.StatusChangedAt.Unix())
}
