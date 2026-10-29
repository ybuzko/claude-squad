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

// D archives every session, not just tickets, and the archive is there without Linear.
func TestKillKeyArchivesPlainSessions(t *testing.T) {
	h := newPollHome(t)
	h.dispatcher = nil
	plain := &session.Instance{Title: "scratch", Status: session.Ready}
	h.list.AddInstance(plain)

	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	assert.Equal(t, stateConfirm, h.state)
	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	assert.Nil(t, h.list.FindByTitle("scratch"))
	assert.Same(t, plain, h.archive.FindByTitle("scratch"))
	assert.True(t, plain.Archived)

	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	assert.True(t, h.showArchive)
}
