package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveFieldsRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)
	data := InstanceData{
		Title:      "TSA-1",
		Status:     Paused,
		Program:    "claude",
		IssueID:    "TSA-1",
		Archived:   true,
		ArchivedAt: at,
		InView:     true,
		Worktree:   GitWorktreeData{RepoPath: "/r", WorktreePath: "/w", BranchName: "agent/tsa-1"},
	}
	inst, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.True(t, inst.Archived)
	assert.True(t, inst.InView)
	assert.Equal(t, at, inst.ArchivedAt)
	assert.False(t, inst.Restoring)

	back := inst.ToInstanceData()
	assert.True(t, back.Archived)
	assert.True(t, back.InView)
	assert.Equal(t, at, back.ArchivedAt)
}
