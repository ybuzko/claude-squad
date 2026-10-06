package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSetupCommandLogsOutputAndRunsInDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	logPath, err := RunSetupCommand("pwd && echo hello >&2 && touch marker", dir, "TSA-1")
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dir, "marker"))
	assert.NoError(t, statErr, "command should run inside the worktree dir")

	logData, err := os.ReadFile(logPath)
	require.NoError(t, err)
	log := string(logData)
	assert.Contains(t, log, "$ pwd && echo hello")
	assert.Contains(t, log, "hello")
	assert.Contains(t, log, filepath.Base(dir))
	assert.True(t, strings.HasSuffix(logPath, filepath.Join("setup-logs", "TSA-1.log")), logPath)
}

func TestRunSetupCommandReportsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	logPath, err := RunSetupCommand("echo boom; exit 3", t.TempDir(), "TSA 2/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 3")
	assert.True(t, strings.HasSuffix(logPath, "TSA_2_x.log"), logPath)
	logData, _ := os.ReadFile(logPath)
	assert.Contains(t, string(logData), "boom")
}

func TestRunSetupCommandNoopWhenEmpty(t *testing.T) {
	inst := &Instance{Title: "TSA-3"}
	assert.NoError(t, inst.runSetupCommand())
}
