package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A ticket whose first Claude never saved a conversation starts over: `claude -c`
// would exit at once and take the tmux session with it.
func TestResumeProgramStartsOverWithoutAConversation(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	dir := "/home/u/.claude-squad/worktrees/agent/tsa-1_18ab"
	inst := &Instance{Title: "TSA-1", IssueID: "TSA-1", Program: `claude "/triage TSA-1"`}

	assert.Equal(t, `claude "/triage TSA-1"`, inst.resumeProgramIn(dir))
	assert.Equal(t, "claude -c", inst.resumeProgramIn(""), "unknown worktree: keep continuing")

	project := filepath.Join(configDir, "projects", "-home-u--claude-squad-worktrees-agent-tsa-1-18ab")
	require.NoError(t, os.MkdirAll(project, 0o755))
	assert.Equal(t, `claude "/triage TSA-1"`, inst.resumeProgramIn(dir), "empty project dir")
	require.NoError(t, os.WriteFile(filepath.Join(project, "abc.jsonl"), []byte("{}\n"), 0o644))
	assert.Equal(t, "claude -c", inst.resumeProgramIn(dir))

	inst.ClaudeSessionID = "abc"
	assert.Equal(t, "claude --resume abc", inst.resumeProgramIn(dir))
}
