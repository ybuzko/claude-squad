package app

import (
	"claude-squad/config"
	"claude-squad/linear"
	"claude-squad/session"
	"claude-squad/ui"
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPollHome builds a home with a dispatcher but no Linear client: tests hand
// handlePollDone the view directly. State is written under a temp HOME.
func newPollHome(t *testing.T) *home {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Linear.Enabled = true
	cfg.Linear.PollIntervalSec = 3600

	sp := spinner.New()
	storage, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h := &home{
		ctx:          context.Background(),
		appConfig:    cfg,
		spinner:      sp,
		storage:      storage,
		menu:         ui.NewMenu(),
		errBox:       ui.NewErrBox(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		dispatcher:   &dispatcher{cfg: cfg},
	}
	h.list = ui.NewList(&h.spinner, false)
	h.archive = ui.NewList(&h.spinner, false)
	return h
}

func TestPollArchivesTicketThatLeftTheViewAndRestoresItWhenItReturns(t *testing.T) {
	h := newPollHome(t)
	inst := ticket("TSA-1", session.Ready, true)
	inst.Program = "claude"
	h.list.AddInstance(inst)

	// The ticket is gone from the view: archived on this poll.
	h.handlePollDone(linearPollDoneMsg{view: issues()})
	assert.Nil(t, h.list.FindByTitle("TSA-1"))
	require.Same(t, inst, h.archive.FindByTitle("TSA-1"))
	assert.True(t, inst.Archived)
	assert.False(t, inst.InView)
	assert.False(t, inst.ArchivedAt.IsZero())

	// Still gone: nothing changes.
	h.handlePollDone(linearPollDoneMsg{view: issues()})
	assert.Same(t, inst, h.archive.FindByTitle("TSA-1"))

	// Back in the view: restored into the active list (resume runs in the returned Cmd).
	h.handlePollDone(linearPollDoneMsg{view: issues("TSA-1")})
	assert.Nil(t, h.archive.FindByTitle("TSA-1"))
	require.Same(t, inst, h.list.FindByTitle("TSA-1"))
	assert.False(t, inst.Archived)
	assert.True(t, inst.Restoring)
	assert.True(t, inst.InView)
}

func TestPollRestoresOnlyUpToMaxConcurrent(t *testing.T) {
	h := newPollHome(t)
	h.appConfig.Linear.MaxConcurrent = 1
	a := ticket("TSA-1", session.Paused, false)
	b := ticket("TSA-2", session.Paused, false)
	a.Archived, b.Archived = true, true
	h.archive.AddInstance(a)
	h.archive.AddInstance(b)

	h.handlePollDone(linearPollDoneMsg{view: issues("TSA-1", "TSA-2")})
	assert.True(t, a.Restoring, "first in view order is restored")
	assert.NotNil(t, h.archive.FindByTitle("TSA-2"), "second waits: restoring counts as working")
	require.Len(t, h.dispatcher.pending, 1)
	assert.Equal(t, "TSA-2", h.dispatcher.pending[0].Identifier)
}

func TestPollLeavesBusyTicketRunningUntilIdle(t *testing.T) {
	h := newPollHome(t)
	inst := ticket("TSA-1", session.Running, true)
	h.list.AddInstance(inst)

	h.handlePollDone(linearPollDoneMsg{view: issues()})
	assert.Same(t, inst, h.list.FindByTitle("TSA-1"), "mid-turn: not archived yet")

	inst.Status = session.Ready
	h.handlePollDone(linearPollDoneMsg{view: issues()})
	assert.Same(t, inst, h.archive.FindByTitle("TSA-1"))
}

func TestFailedArchiveGoesBackToActiveList(t *testing.T) {
	h := newPollHome(t)
	inst := ticket("TSA-1", session.Ready, true)
	h.list.AddInstance(inst)

	h.archiveTicket(inst, true)
	require.NotNil(t, h.archive.FindByTitle("TSA-1"))

	h.handleTicketArchived(ticketArchivedMsg{instance: inst, err: assert.AnError, prevInView: true})
	assert.Nil(t, h.archive.FindByTitle("TSA-1"))
	assert.Same(t, inst, h.list.FindByTitle("TSA-1"))
	assert.False(t, inst.Archived)
	assert.True(t, inst.InView, "restored so the next poll retries the archive")
}

func TestInstanceLimitGatesDispatcherButNotManualSpawns(t *testing.T) {
	h := newPollHome(t)
	h.appConfig.InstanceLimit = 1
	h.dispatcher.cfg.Spawn.RepoPath = t.TempDir()
	h.list.AddInstance(ticket("TSA-1", session.Ready, true))

	// The poller waits: the active count is already at the limit.
	h.handlePollDone(linearPollDoneMsg{view: issues("TSA-1", "TSA-2")})
	assert.Nil(t, h.list.FindByTitle("TSA-2"))
	require.Len(t, h.dispatcher.pending, 1)

	// `t` (and n/N) go past it.
	_, err := h.spawnTicket(linear.Issue{Identifier: "TSA-3", Title: "Manual"}, false)
	require.NoError(t, err)
	assert.NotNil(t, h.list.FindByTitle("TSA-3"))
	assert.Equal(t, 2, h.list.NumInstances())
}
