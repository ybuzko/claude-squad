package app

import (
	"claude-squad/session"
	"claude-squad/status"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sentNote struct{ title, body string }

func newNotifyHome(t *testing.T, idle bool) (*home, *[]sentNote) {
	h := newPollHome(t)
	var sent []sentNote
	h.notifier = &notifier{idle: idle, last: make(map[*session.Instance]hookSig),
		send: func(_ context.Context, title, body string) error {
			sent = append(sent, sentNote{title, body})
			return nil
		}}
	return h, &sent
}

func hook(ts int64, event, state, reason string) *status.Status {
	return &status.Status{TS: ts, Event: event, State: state, Reason: reason}
}

// flush runs the notifier's delayed flush and the send it starts.
func flush(t *testing.T, h *home) {
	t.Helper()
	if cmd := h.handleNotifyFlush(); cmd != nil {
		require.Nil(t, h.handleNotifySent(cmd().(notifySentMsg)))
	}
}

func TestNotifierTellsAboutBlockedAndFinishedSessions(t *testing.T) {
	h, sent := newNotifyHome(t, true)
	a := ticket("TSA-1", session.Running, true)
	a.IssueTitle = "Fix the feed"
	b := &session.Instance{Title: "scratch", Status: session.Running}
	h.list.AddInstance(a)
	h.list.AddInstance(b)

	// What was there when cs started is not news.
	assert.Nil(t, h.noteHookStatus(a, hook(1, "Stop", status.StateIdle, "old")))
	assert.Nil(t, h.noteHookStatus(b, hook(1, "PostToolUse", status.StateRunning, "")))
	assert.Nil(t, h.noteHookStatus(a, hook(1, "Stop", status.StateIdle, "old")), "unchanged")

	require.NotNil(t, h.noteHookStatus(a, hook(2, "Notification", status.StateBlocked, "Claude needs your permission to use Bash")))
	a.Status = session.Blocked
	flush(t, h)
	require.Len(t, *sent, 1)
	assert.Equal(t, sentNote{"TSA-1 needs you", "Claude needs your permission to use Bash\nFix the feed"}, (*sent)[0])

	// Two at once go out as one, blocked first.
	require.NotNil(t, h.noteHookStatus(b, hook(3, "Stop", status.StateIdle, "TL;DR: tests pass")))
	assert.Nil(t, h.noteHookStatus(a, hook(3, "Notification", status.StateBlocked, "Claude has a question")), "one flush pending")
	a.Status, b.Status = session.Blocked, session.Ready
	flush(t, h)
	require.Len(t, *sent, 2)
	assert.Equal(t, sentNote{"1 needs you, 1 finished", "TSA-1: Claude has a question\nscratch: TL;DR: tests pass"}, (*sent)[1])
}

func TestNotifierDropsEventsThatAreOver(t *testing.T) {
	h, sent := newNotifyHome(t, true)
	a := ticket("TSA-1", session.Running, true)
	h.list.AddInstance(a)
	h.noteHookStatus(a, hook(1, "PostToolUse", status.StateRunning, ""))

	// Finished, then handed more work (say by the PR watcher) before the flush.
	require.NotNil(t, h.noteHookStatus(a, hook(2, "Stop", status.StateIdle, "done")))
	a.Status = session.Running
	flush(t, h)
	assert.Empty(t, *sent)

	// A resumed session reports idle without finishing anything.
	assert.Nil(t, h.noteHookStatus(a, hook(3, "SessionStart", status.StateIdle, "")))
}

func TestNotifierBlockedOnly(t *testing.T) {
	h, sent := newNotifyHome(t, false)
	a := ticket("TSA-1", session.Running, true)
	h.list.AddInstance(a)
	h.noteHookStatus(a, hook(1, "PostToolUse", status.StateRunning, ""))
	assert.Nil(t, h.noteHookStatus(a, hook(2, "Stop", status.StateIdle, "done")))
	flush(t, h)
	assert.Empty(t, *sent)
}

func TestNotifierSkipsSessionsCsIsResumingOrArchived(t *testing.T) {
	h, sent := newNotifyHome(t, true)
	a := ticket("TSA-1", session.Running, true)
	h.list.AddInstance(a)
	h.noteHookStatus(a, hook(1, "PostToolUse", status.StateRunning, ""))
	a.Restoring = true
	assert.Nil(t, h.noteHookStatus(a, hook(2, "Notification", status.StateBlocked, "?")))
	a.Restoring = false

	require.NotNil(t, h.noteHookStatus(a, hook(3, "Notification", status.StateBlocked, "?")))
	a.Status = session.Blocked
	h.list.Remove(a)
	h.archive.AddInstance(a)
	flush(t, h)
	assert.Empty(t, *sent)
}
