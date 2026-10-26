package app

import (
	"claude-squad/linear"
	"claude-squad/session"
	"claude-squad/status"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func watchedTicket(t *testing.T, h *home, id string, st session.Status, seen time.Time) *session.Instance {
	t.Helper()
	inst := ticket(id, st, true)
	inst.IssueUUID = "uuid-" + id
	inst.LinearSeenAt = seen
	h.list.AddInstance(inst)
	return inst
}

func comment(id string, author string, me bool, at time.Time) linear.Comment {
	return linear.Comment{IssueID: "uuid-" + id, Author: author, IsMe: me, CreatedAt: at}
}

func TestWatchedTicketsStartsUnseenSessionsNow(t *testing.T) {
	h := newPollHome(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	old := watchedTicket(t, h, "TSA-1", session.Ready, now.Add(-time.Hour))
	fresh := watchedTicket(t, h, "TSA-2", session.Ready, time.Time{})
	h.list.AddInstance(&session.Instance{Title: "manual"})

	ids, _, _ := h.watchedTickets(now)
	assert.Empty(t, ids, "off unless linear.watch_updates is set")

	h.dispatcher.cfg.Linear.WatchUpdates = true
	ids, since, seeded := h.watchedTickets(now)
	assert.ElementsMatch(t, []string{"uuid-TSA-1", "uuid-TSA-2"}, ids)
	assert.Equal(t, old.LinearSeenAt, since)
	assert.True(t, seeded)
	assert.Equal(t, now, fresh.LinearSeenAt, "no replay of comments from before the watcher")

	old.LinearSeenAt = now.Add(-60 * 24 * time.Hour)
	_, since, _ = h.watchedTickets(now)
	assert.Equal(t, now.Add(-updateLookback), since)
}

func TestDeliverTicketUpdatesWaitsForAnIdleSession(t *testing.T) {
	h := newPollHome(t)
	seen := time.Now().Add(-time.Hour)
	idle := watchedTicket(t, h, "TSA-1", session.Ready, seen)
	watchedTicket(t, h, "TSA-2", session.Running, seen)
	watchedTicket(t, h, "TSA-3", session.Blocked, seen)
	require.NoError(t, status.Write("TSA-1", status.Status{State: status.StateBlocked, Notification: "idle_prompt"}))
	require.NoError(t, status.Write("TSA-2", status.Status{State: status.StateRunning}))
	require.NoError(t, status.Write("TSA-3", status.Status{State: status.StateBlocked, Notification: "permission_prompt"}))

	at := seen.Add(30 * time.Minute)
	cmds := h.deliverTicketUpdates([]linear.Comment{
		comment("TSA-1", "Old News", false, seen.Add(-time.Minute)),
		comment("TSA-1", "Yaroslav Buzko", true, at),
		comment("TSA-2", "Kristin Cella", false, at),
		comment("TSA-3", "Kristin Cella", false, at),
	})
	assert.Empty(t, cmds, "own and already-seen comments are not updates; busy and asking sessions wait")

	cmds = h.deliverTicketUpdates([]linear.Comment{comment("TSA-1", "Kristin Cella", false, at)})
	require.Len(t, cmds, 1)
	assert.True(t, h.dispatcher.delivering[idle])
	assert.Empty(t, h.deliverTicketUpdates([]linear.Comment{comment("TSA-1", "Kristin Cella", false, at)}),
		"one delivery at a time")

	// The session is not running here, so the input-box check fails and the update
	// waits for the next poll.
	msg := cmds[0]().(ticketUpdateSentMsg)
	assert.True(t, msg.skipped)
	h.handleTicketUpdateSent(msg)
	assert.False(t, h.dispatcher.delivering[idle])
	assert.Equal(t, seen, idle.LinearSeenAt)

	h.handleTicketUpdateSent(ticketUpdateSentMsg{instance: idle, seen: at})
	assert.Equal(t, at, idle.LinearSeenAt)
	assert.False(t, idle.LinearUpdateAt.IsZero(), "the row shows that an update went in")
}

func TestDeliverTicketUpdatesResumesPausedSession(t *testing.T) {
	h := newPollHome(t)
	seen := time.Now().Add(-time.Hour)
	paused := watchedTicket(t, h, "TSA-1", session.Paused, seen)

	at := seen.Add(time.Minute)
	cmds := h.deliverTicketUpdates([]linear.Comment{
		comment("TSA-1", "Kristin Cella", false, at),
		comment("TSA-1", "", false, at.Add(time.Minute)),
	})
	require.Len(t, cmds, 1)
	assert.True(t, paused.Restoring)
	assert.Equal(t, at.Add(time.Minute), paused.LinearSeenAt)
	assert.False(t, paused.LinearUpdateAt.IsZero())
}

func TestUpdatePromptListsTheNewComments(t *testing.T) {
	h := newPollHome(t)
	h.dispatcher.cfg.Linear.UpdatePrompt = "{ISSUE_ID}: {UPDATES}"
	at := time.Date(2026, 10, 22, 23, 8, 0, 0, time.Local)
	got := h.updatePrompt("TSA-163", []linear.Comment{
		{Author: "Kristin Cella", CreatedAt: at},
		{CreatedAt: at.Add(time.Hour)},
	})
	assert.Equal(t, "TSA-163: Kristin Cella (Oct 22 23:08), someone (Oct 23 00:08)", got)
}
