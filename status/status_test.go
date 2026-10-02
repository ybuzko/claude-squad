package status

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useTempConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func handle(t *testing.T, issueID, payload string) {
	t.Helper()
	require.NoError(t, HandleHook(strings.NewReader(payload), issueID, time.Unix(1700000000, 0)))
}

func TestHandleHookNoopWithoutIssueID(t *testing.T) {
	useTempConfigDir(t)
	handle(t, "", `{"hook_event_name":"Stop","session_id":"s"}`)
	s, err := Read("TSA-1")
	require.NoError(t, err)
	assert.Nil(t, s)
}

func TestHandleHookStateTransitions(t *testing.T) {
	useTempConfigDir(t)

	handle(t, "TSA-1", `{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-1"}`)
	s, err := Read("TSA-1")
	require.NoError(t, err)
	assert.Equal(t, StateRunning, s.State)
	assert.Equal(t, "sess-1", s.SessionID)
	assert.Equal(t, int64(1700000000), s.TS)

	handle(t, "TSA-1", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash","session_id":"sess-1"}`)
	s, _ = Read("TSA-1")
	assert.Equal(t, StateBlocked, s.State)
	assert.Equal(t, "Claude needs your permission to use Bash", s.Reason)

	handle(t, "TSA-1", `{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"sess-1"}`)
	s, _ = Read("TSA-1")
	assert.Equal(t, StateRunning, s.State)
	assert.Empty(t, s.Reason)

	handle(t, "TSA-1", `{"hook_event_name":"Stop","last_assistant_message":"Triage done.\n\nShall I proceed with the backfill (12,340 rows)?\n","session_id":"sess-1"}`)
	s, _ = Read("TSA-1")
	assert.Equal(t, StateIdle, s.State)
	assert.Equal(t, "Shall I proceed with the backfill (12,340 rows)?", s.Reason)

	handle(t, "TSA-1", `{"hook_event_name":"UserPromptSubmit","user_input":"yes","session_id":"sess-1"}`)
	s, _ = Read("TSA-1")
	assert.Equal(t, StateRunning, s.State)
}

func TestHandleHookCompactKeepsStateAndSessionID(t *testing.T) {
	useTempConfigDir(t)
	handle(t, "TSA-2", `{"hook_event_name":"Stop","last_assistant_message":"waiting","session_id":"sess-2"}`)
	handle(t, "TSA-2", `{"hook_event_name":"SessionStart","source":"compact","session_id":"sess-2"}`)
	s, err := Read("TSA-2")
	require.NoError(t, err)
	assert.Equal(t, StateIdle, s.State)
	assert.Equal(t, "waiting", s.Reason)
	assert.Equal(t, "sess-2", s.SessionID)
}

func TestHandleHookResumeIsIdle(t *testing.T) {
	useTempConfigDir(t)
	handle(t, "TSA-3", `{"hook_event_name":"SessionStart","source":"resume","session_id":"sess-3"}`)
	s, _ := Read("TSA-3")
	assert.Equal(t, StateIdle, s.State)
}

func TestHandleHookIgnoresUnknownEvents(t *testing.T) {
	useTempConfigDir(t)
	handle(t, "TSA-4", `{"hook_event_name":"PreToolUse","session_id":"sess-4"}`)
	s, err := Read("TSA-4")
	require.NoError(t, err)
	assert.Nil(t, s)
}

func TestRemove(t *testing.T) {
	useTempConfigDir(t)
	handle(t, "TSA-5", `{"hook_event_name":"Stop","session_id":"s"}`)
	require.NoError(t, Remove("TSA-5"))
	require.NoError(t, Remove("TSA-5"))
	s, err := Read("TSA-5")
	require.NoError(t, err)
	assert.Nil(t, s)
}
