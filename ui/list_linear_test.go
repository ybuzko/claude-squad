package ui

import (
	"claude-squad/session"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newListWith(statuses ...session.Status) (*List, []*session.Instance) {
	sp := spinner.New()
	l := NewList(&sp, false)
	l.SetLinearMode(true)
	var items []*session.Instance
	for i, s := range statuses {
		inst := &session.Instance{Title: string(rune('A' + i)), Status: s}
		items = append(items, inst)
		l.AddInstance(inst)
	}
	return l, items
}

func TestSortByUrgencyOrdersBlockedIdleRunningPausedDone(t *testing.T) {
	l, items := newListWith(session.Done, session.Running, session.Ready, session.Paused, session.Blocked, session.Ready)
	l.SetSelectedInstance(1) // the Running one

	changed := l.SortByUrgency()
	require.True(t, changed)

	got := l.GetInstances()
	assert.Equal(t, []*session.Instance{items[4], items[2], items[5], items[1], items[3], items[0]}, got)
	assert.Same(t, items[1], l.GetSelectedInstance(), "selection follows the instance, not the index")
}

func TestSortByUrgencyIsStableAndReportsNoChange(t *testing.T) {
	l, items := newListWith(session.Blocked, session.Ready, session.Ready, session.Running)
	assert.False(t, l.SortByUrgency())
	assert.Equal(t, items, l.GetInstances())
}

func TestCountByStatus(t *testing.T) {
	l, _ := newListWith(session.Blocked, session.Ready, session.Ready, session.Running, session.Done)
	blocked, idle := l.CountByStatus()
	assert.Equal(t, 1, blocked)
	assert.Equal(t, 2, idle)
}

func TestFindByTitle(t *testing.T) {
	l, items := newListWith(session.Ready, session.Ready)
	assert.Same(t, items[1], l.FindByTitle("B"))
	assert.Nil(t, l.FindByTitle("Z"))
}
