package app

import (
	"claude-squad/session"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestResumeKeyRunsInTheBackground(t *testing.T) {
	h := newPollHome(t)
	paused := &session.Instance{Title: "s1", Status: session.Paused}
	h.list.AddInstance(paused)

	// The key returns at once; the resume itself is in a Cmd and the row shows it.
	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	assert.True(t, paused.Restoring)

	h.handleResumeDone(resumeDoneMsg{instance: paused, err: errors.New("worktree gone")})
	assert.False(t, paused.Restoring, "a failed resume leaves it paused for another try")
	assert.True(t, paused.Paused())
}

func TestResumeKeyIgnoresRunningSessions(t *testing.T) {
	h := newPollHome(t)
	running := &session.Instance{Title: "s1", Status: session.Running}
	h.list.AddInstance(running)
	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	assert.False(t, running.Restoring)
}

func TestCheckoutKeyIsGone(t *testing.T) {
	h := newPollHome(t)
	running := &session.Instance{Title: "s1", Status: session.Running}
	h.list.AddInstance(running)
	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	assert.Equal(t, session.Running, running.Status)
	assert.Equal(t, stateDefault, h.state)
}
