package session

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func prLink(n int) string {
	num := strconv.Itoa(n)
	return `{"type":"pr-link","sessionId":"s","prNumber":` + num + `,"prUrl":"https://github.com/o/r/pull/` + num +
		`","prRepository":"o/r","timestamp":"2026-11-13T03:38:36.484Z"}` + "\n"
}

func TestPRScannerReadsNewTranscriptLinesOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	worktree := "/home/me/.claude-squad/worktrees/agent/tsa-1_abc"
	dir, err := ClaudeProjectDir(worktree)
	require.NoError(t, err)
	assert.Equal(t, "-home-me--claude-squad-worktrees-agent-tsa-1-abc", filepath.Base(dir))
	require.NoError(t, os.MkdirAll(dir, 0o755))

	s := NewPRScanner()
	prs, err := s.Scan(worktree)
	require.NoError(t, err)
	assert.Empty(t, prs)

	path := filepath.Join(dir, "conv.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	_, _ = f.WriteString(`{"type":"user","message":"mentions \"pr-link\" in passing"}` + "\n" + prLink(1) + prLink(1))
	// A line still being written is left for the next scan.
	_, _ = f.WriteString(prLink(2)[:20])

	prs, err = s.Scan(worktree)
	require.NoError(t, err)
	assert.Equal(t, []PullRequest{{URL: "https://github.com/o/r/pull/1", Repo: "o/r", Number: 1}}, prs)

	_, _ = f.WriteString(prLink(2)[20:])
	prs, err = s.Scan(worktree)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, 2, prs[1].Number)
}
