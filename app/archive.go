package app

import (
	"claude-squad/keys"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/status"
	"claude-squad/ui"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The archive keeps ticket sessions whose work is over for now: paused (worktree
// removed, branch kept), tmux and Claude stopped, out of the active list and out of
// instance_limit. Each keeps its worktree path and Claude session id, so restoring
// recreates the worktree where it was and `claude --resume` continues the same
// conversation. Archived instances are persisted with the rest in state.json.

// ticketArchivedMsg reports the background half of archiving (pause + tmux stop).
type ticketArchivedMsg struct {
	instance *session.Instance
	err      error
	// prevInView is restored if archiving fails, so an automatic archive is retried.
	prevInView bool
}

// ticketRestoredMsg reports the background half of restoring (worktree, setup, tmux).
type ticketRestoredMsg struct {
	instance *session.Instance
	prompt   string
	err      error
}

// resumePromptMsg re-checks whether a restored session is ready for its prompt.
type resumePromptMsg struct {
	instance *session.Instance
	prompt   string
	attempt  int
}

// archivedDeletedMsg reports the background half of a permanent delete.
type archivedDeletedMsg struct {
	title string
	err   error
}

// saveInstances persists the active list and the archive together.
func (m *home) saveInstances() error {
	all := append([]*session.Instance{}, m.list.GetInstances()...)
	all = append(all, m.archive.GetInstances()...)
	return m.storage.SaveInstances(all)
}

// activeList is the list on screen: the archive while it is shown, else the sessions.
func (m *home) activeList() *ui.List {
	if m.showArchive {
		return m.archive
	}
	return m.list
}

func (m *home) resumePrompt(issueID string) string {
	if m.dispatcher == nil {
		return ""
	}
	return strings.ReplaceAll(m.dispatcher.cfg.Spawn.ResumePrompt, "{ISSUE_ID}", issueID)
}

// archiveTicket moves a ticket session into the archive right away, then pauses it
// and stops its tmux session in the background. auto marks an archive triggered by
// the ticket leaving the view; a manual one keeps InView, so a ticket archived by hand
// while still in the view is not restored until it leaves and comes back.
func (m *home) archiveTicket(inst *session.Instance, auto bool) tea.Cmd {
	prevInView := inst.InView
	m.list.Remove(inst)
	inst.Archived = true
	inst.ArchivedAt = time.Now()
	if auto {
		inst.InView = false
	}
	m.archive.Prepend(inst)
	m.tabbedWindow.CleanupTerminalForInstance(inst.Title)

	var cmds []tea.Cmd
	if err := m.saveInstances(); err != nil {
		cmds = append(cmds, m.handleError(err))
	}
	cmds = append(cmds, func() tea.Msg {
		return ticketArchivedMsg{instance: inst, err: inst.StopForArchive(), prevInView: prevInView}
	})
	return tea.Batch(cmds...)
}

// restoreTicket moves an archived session back into the active list and resumes it in
// the background: worktree recreated at its old path, setup command, then
// `claude --resume`. A non-empty prompt is sent once Claude has started.
func (m *home) restoreTicket(inst *session.Instance, prompt string) tea.Cmd {
	m.archive.Remove(inst)
	inst.Archived = false
	inst.ArchivedAt = time.Time{}
	inst.Restoring = true
	m.list.AddInstance(inst)()

	// The old status file describes the previous run; its absence until SessionStart
	// fires is how resumePromptMsg knows the resumed Claude is up.
	if err := status.Remove(inst.IssueID); err != nil {
		log.WarningLog.Printf("could not remove stale status for %s: %v", inst.IssueID, err)
	}

	var cmds []tea.Cmd
	if err := m.saveInstances(); err != nil {
		cmds = append(cmds, m.handleError(err))
	}
	cmds = append(cmds, m.instanceChanged(), func() tea.Msg {
		return ticketRestoredMsg{instance: inst, prompt: prompt, err: inst.Resume()}
	})
	return tea.Batch(cmds...)
}

func (m *home) handleTicketArchived(msg ticketArchivedMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.err != nil {
		// Pause refuses to go on when it cannot commit the worktree, so the session may
		// still be running. Put it back where it is visible and can be retried.
		inst := msg.instance
		m.archive.Remove(inst)
		inst.Archived = false
		inst.ArchivedAt = time.Time{}
		inst.InView = msg.prevInView
		m.list.AddInstance(inst)()
		cmds = append(cmds, m.handleError(fmt.Errorf("archiving %s failed, kept it active: %w", inst.Title, msg.err)))
	}
	if err := m.saveInstances(); err != nil {
		cmds = append(cmds, m.handleError(err))
	}
	return tea.Batch(append(cmds, m.instanceChanged())...)
}

func (m *home) handleTicketRestored(msg ticketRestoredMsg) tea.Cmd {
	inst := msg.instance
	inst.Restoring = false
	var cmds []tea.Cmd
	if msg.err != nil {
		// The instance stays in the active list as paused; `r` retries.
		cmds = append(cmds, m.handleError(fmt.Errorf("restoring %s: %w", inst.Title, msg.err)))
	} else {
		cmds = append(cmds, tea.WindowSize(), m.trustPromptCheckCmd(inst, 0))
		if msg.prompt != "" {
			cmds = append(cmds, m.resumePromptCmd(inst, msg.prompt, 0))
		}
	}
	if err := m.saveInstances(); err != nil {
		cmds = append(cmds, m.handleError(err))
	}
	cmds = append(cmds, m.instanceChanged())
	cmds = append(cmds, m.drainPending()...)
	return tea.Batch(cmds...)
}

// resumePromptPoll is how often, and how many times, a restored session is checked
// for readiness before its prompt is sent anyway.
const (
	resumePromptPoll     = 1 * time.Second
	resumePromptAttempts = 40
)

func (m *home) resumePromptCmd(inst *session.Instance, prompt string, attempt int) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(resumePromptPoll):
			return resumePromptMsg{instance: inst, prompt: prompt, attempt: attempt}
		}
	}
}

// handleResumePrompt sends the prompt once the resumed Claude Code has reported in
// through its SessionStart hook (a status file reappears), or after a timeout.
func (m *home) handleResumePrompt(msg resumePromptMsg) tea.Cmd {
	inst := msg.instance
	if inst.Archived || inst.Paused() || !inst.Started() {
		return nil
	}
	hs, err := status.Read(inst.IssueID)
	if err != nil {
		log.WarningLog.Printf("could not read hook status for %s: %v", inst.IssueID, err)
	}
	if hs == nil && msg.attempt < resumePromptAttempts {
		return m.resumePromptCmd(inst, msg.prompt, msg.attempt+1)
	}
	if hs == nil {
		log.WarningLog.Printf("%s: no SessionStart report after resume; sending the prompt anyway", inst.Title)
	}
	return func() tea.Msg {
		// SessionStart fires as the session loads; give the input box a moment.
		time.Sleep(time.Second)
		if err := inst.SendPrompt(msg.prompt); err != nil {
			return fmt.Errorf("sending resume prompt to %s: %w", inst.Title, err)
		}
		return nil
	}
}

// deleteArchived permanently removes an archived session: record, branch and status
// file. The Claude transcript under ~/.claude/projects is left alone.
func (m *home) deleteArchived(inst *session.Instance) tea.Cmd {
	m.archive.Remove(inst)
	if inst.IsTicket() {
		if err := status.Remove(inst.IssueID); err != nil {
			log.WarningLog.Printf("could not remove status for %s: %v", inst.IssueID, err)
		}
	}
	var cmds []tea.Cmd
	if err := m.saveInstances(); err != nil {
		cmds = append(cmds, m.handleError(err))
	}
	cmds = append(cmds, m.instanceChanged(), func() tea.Msg {
		return archivedDeletedMsg{title: inst.Title, err: inst.Kill()}
	})
	return tea.Batch(cmds...)
}

// confirmThen shows a confirmation dialog; on confirm, fn runs on the UI goroutine and
// the Cmd it returns is executed (unlike confirmAction, whose result is discarded).
func (m *home) confirmThen(message string, fn func() tea.Cmd) tea.Cmd {
	m.confirmAction(message, nil)
	m.confirmationOverlay.OnConfirm = func() {
		m.state = stateDefault
		m.confirmedCmd = fn()
	}
	return nil
}

// toggleArchive switches the left pane between the sessions and the archive.
func (m *home) toggleArchive() tea.Cmd {
	m.showArchive = !m.showArchive
	m.menu.SetArchiveMode(m.showArchive)
	return tea.Batch(tea.WindowSize(), m.instanceChanged())
}

// handleArchiveKey handles keys while the archive is shown: navigate, restore, delete
// for good, or go back. Everything else is ignored so nothing acts on a stopped session.
func (m *home) handleArchiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc {
		return m, m.toggleArchive()
	}
	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}
	selected := m.archive.GetSelectedInstance()
	switch name {
	case keys.KeyArchiveView:
		return m, m.toggleArchive()
	case keys.KeyUp:
		m.archive.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.archive.Down()
		return m, m.instanceChanged()
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeyQuit:
		return m.handleQuit()
	case keys.KeyResume:
		if selected == nil {
			return m, nil
		}
		restore := m.restoreTicket(selected, "")
		// Show the restored session where it now lives.
		m.list.SelectInstance(selected)
		return m, tea.Batch(restore, m.toggleArchive())
	case keys.KeyKill:
		if selected == nil {
			return m, nil
		}
		message := fmt.Sprintf("[!] Permanently delete '%s'? Its branch and record go; the Claude transcript stays on disk.", selected.Title)
		return m, m.confirmThen(message, func() tea.Cmd { return m.deleteArchived(selected) })
	}
	return m, nil
}
