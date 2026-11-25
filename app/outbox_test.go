package app

import (
	"claude-squad/session"
	"claude-squad/status"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueMessageRejectsBadNamesAndEmptyText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := QueueMessage("../x", "hi")
	assert.Error(t, err)
	_, err = QueueMessage("TSA-1", " \n ")
	assert.Error(t, err)
	path, err := QueueMessage("TSA-1", "first")
	require.NoError(t, err)
	_, err = QueueMessage("TSA-1", "second\nline")
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second line", flattenPrompt(string(raw)), "a newer message replaces the waiting one")
}

func TestDeliverOutboxWaitsForIdleSessionsAndCapacity(t *testing.T) {
	h := newPollHome(t)
	h.dispatcher.cfg.Linear.MaxConcurrent = 2
	idle := ticket("TSA-1", session.Ready, true)
	busy := ticket("TSA-2", session.Running, true)
	second := ticket("TSA-3", session.Ready, true)
	third := ticket("TSA-4", session.Ready, true)
	for _, inst := range []*session.Instance{idle, busy, second, third} {
		h.list.AddInstance(inst)
	}
	require.NoError(t, status.Write("TSA-1", status.Status{State: status.StateBlocked, Notification: "idle_prompt"}))
	require.NoError(t, status.Write("TSA-2", status.Status{State: status.StateRunning}))
	require.NoError(t, status.Write("TSA-3", status.Status{State: status.StateBlocked, Notification: "idle_prompt"}))
	require.NoError(t, status.Write("TSA-4", status.Status{State: status.StateBlocked, Notification: "idle_prompt"}))

	assert.Empty(t, h.deliverOutbox(), "nothing queued")

	var paths []string
	for _, name := range []string{"tsa-1", "TSA-2", "TSA-3", "TSA-4", "TSA-99"} {
		p, err := QueueMessage(name, "re-triage\nplease")
		require.NoError(t, err)
		paths = append(paths, p)
	}

	// One session is working and max_concurrent is 2, so one message goes out now.
	cmds := h.deliverOutbox()
	require.Len(t, cmds, 1)
	assert.True(t, h.delivering[idle], "matched by ticket id whatever the case")
	assert.False(t, h.delivering[second])
	assert.Empty(t, h.deliverOutbox(), "the delivery in flight uses the last free slot")

	// The session is not running here, so the input-box check fails and the message
	// stays queued for the next poll.
	msg := cmds[0]().(outboxSentMsg)
	assert.True(t, msg.skipped)
	h.handleOutboxSent(msg)
	assert.False(t, h.delivering[idle])
	for _, p := range paths {
		assert.FileExists(t, p)
	}

	// A delivered message is filed under sent and not offered again.
	h.delivering[idle] = true
	h.handleOutboxSent(outboxSentMsg{instance: idle, path: paths[0]})
	assert.NoFileExists(t, paths[0])
	sent, err := filepath.Glob(filepath.Join(filepath.Dir(paths[0]), outboxSentName, "tsa-1-*.md"))
	require.NoError(t, err)
	assert.Len(t, sent, 1)
	assert.False(t, idle.NewsAt.IsZero())

	cmds = h.deliverOutbox()
	require.Len(t, cmds, 1)
	assert.True(t, h.delivering[second], "the busy session's message waits; the next idle one goes")
	assert.FileExists(t, paths[1])
	assert.FileExists(t, paths[4], "a message for an unknown session waits")
}
