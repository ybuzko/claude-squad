package app

import (
	"testing"

	"claude-squad/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// press handles a key the way the program does: a mapped key first only highlights its
// menu entry and is re-sent, then handled.
func press(h *home, msg tea.KeyMsg) {
	h.handleKeyPress(msg)
	if h.keySent {
		h.handleKeyPress(msg)
	}
}

func typeRunes(h *home, s string) {
	for _, r := range s {
		press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// The list re-sorts and ticket sessions get prepended while a new session is being
// named; keys must keep going to the new session, not whatever row is last.
func TestNamingFollowsTheNewSessionWhenTheListReorders(t *testing.T) {
	h := newPollHome(t)
	h.appConfig.Spawn.RepoPath = t.TempDir()
	h.dispatcher.cfg = h.appConfig
	other := ticket("TSA-1", session.Ready, true)
	h.list.AddInstance(other)

	press(h, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	require.Equal(t, stateNew, h.state)
	typeRunes(h, "ab")

	// A ticket session lands below the new one, so it is no longer last.
	late := ticket("TSA-2", session.Ready, true)
	h.list.AddInstance(late)
	typeRunes(h, "c")

	require.NotNil(t, h.naming)
	assert.Equal(t, "abc", h.naming.Title)
	assert.Equal(t, "TSA-1", other.Title)
	assert.Equal(t, "TSA-2", late.Title)

	// Esc removes the new session only.
	press(h, tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.naming)
	assert.Equal(t, 2, h.list.NumInstances())
	assert.NotNil(t, h.list.FindByTitle("TSA-1"))
	assert.NotNil(t, h.list.FindByTitle("TSA-2"))
}
