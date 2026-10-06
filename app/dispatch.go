package app

import (
	"claude-squad/config"
	"claude-squad/linear"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/status"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// dispatcher owns the Linear side of the TUI: polling the view, queueing tickets
// that are over the concurrency cap, and spawning instances for them through the
// same creation path the `n` key uses.
type dispatcher struct {
	cfg    *config.Config
	client *linear.Client
	rule   linear.DoneRule
	// pending holds view issues with no instance yet, in view order. It is rebuilt on
	// every poll and drained whenever capacity frees up.
	pending []linear.Issue
	// polling is true while a poll is in flight, so a manual trigger cannot overlap it.
	polling bool
}

func newDispatcher(cfg *config.Config) (*dispatcher, error) {
	if !cfg.Linear.Enabled {
		return nil, nil
	}
	if cfg.LinearAPIKey() == "" {
		return nil, fmt.Errorf("linear is enabled but no API key is set (LINEAR_API_KEY or linear.api_key)")
	}
	if cfg.Linear.ViewID == "" {
		return nil, fmt.Errorf("linear is enabled but linear.view_id is not set")
	}
	if cfg.Spawn.RepoPath == "" {
		return nil, fmt.Errorf("linear is enabled but spawn.repo_path is not set")
	}
	return &dispatcher{
		cfg:    cfg,
		client: linear.NewClient(cfg.LinearAPIKey()),
		rule: linear.DoneRule{
			StateTypes: cfg.Linear.DoneStateTypes,
			StateIDs:   cfg.Linear.DoneStateIDs,
		},
	}, nil
}

func (d *dispatcher) pollInterval() time.Duration {
	return time.Duration(d.cfg.Linear.PollIntervalSec) * time.Second
}

// linearPollTickMsg asks the main loop to start a poll.
type linearPollTickMsg struct{}

// linearPollDoneMsg carries a finished poll: the view contents and the current
// state of every ticket that already has an instance.
type linearPollDoneMsg struct {
	view []linear.Issue
	live []linear.Issue
	err  error
}

// ticketLookupDoneMsg carries the result of a manual `t` lookup.
type ticketLookupDoneMsg struct {
	issue linear.Issue
	err   error
}

// trustPromptCheckMsg re-checks a freshly spawned instance for Claude Code's trust
// dialog so the triage prompt is not stuck behind it.
type trustPromptCheckMsg struct {
	instance *session.Instance
	attempt  int
}

// schedulePollCmd sleeps one poll interval, then asks for a poll.
func (m *home) schedulePollCmd() tea.Cmd {
	if m.dispatcher == nil {
		return nil
	}
	interval := m.dispatcher.pollInterval()
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(interval):
			return linearPollTickMsg{}
		}
	}
}

// runPollCmd performs the Linear round trip in the background.
func (m *home) runPollCmd() tea.Cmd {
	d := m.dispatcher
	if d == nil || d.polling {
		return nil
	}
	d.polling = true

	var liveIDs []string
	for _, inst := range m.list.GetInstances() {
		if inst.IssueUUID != "" {
			liveIDs = append(liveIDs, inst.IssueUUID)
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()

		view, err := d.client.ViewIssues(ctx, d.cfg.Linear.ViewID)
		if err != nil {
			return linearPollDoneMsg{err: fmt.Errorf("linear poll: %w", err)}
		}
		live, err := d.client.IssuesByID(ctx, liveIDs)
		if err != nil {
			return linearPollDoneMsg{err: fmt.Errorf("linear state lookup: %w", err)}
		}
		return linearPollDoneMsg{view: view, live: live}
	}
}

// handlePollDone applies done-state transitions, rebuilds the pending queue, and
// spawns whatever the concurrency cap allows. It always reschedules the next poll.
func (m *home) handlePollDone(msg linearPollDoneMsg) tea.Cmd {
	d := m.dispatcher
	d.polling = false
	if msg.err != nil {
		return tea.Batch(m.handleError(msg.err), m.schedulePollCmd())
	}

	var cmds []tea.Cmd
	if m.applyTicketStates(msg.live) {
		if err := m.storage.SaveInstances(m.list.GetInstances()); err != nil {
			cmds = append(cmds, m.handleError(err))
		}
	}

	// Views often keep finished tickets visible; those need no session.
	d.pending = d.pending[:0]
	for _, issue := range linear.NewIssues(msg.view, func(id string) bool {
		return m.list.FindByTitle(id) != nil
	}) {
		if !d.rule.IsDone(issue) {
			d.pending = append(d.pending, issue)
		}
	}
	cmds = append(cmds, m.drainPending()...)
	cmds = append(cmds, m.schedulePollCmd())
	return tea.Batch(cmds...)
}

// applyTicketStates marks instances Done (or un-Done, if a ticket was reopened)
// from fresh Linear state. Returns true if anything changed.
func (m *home) applyTicketStates(live []linear.Issue) bool {
	byUUID := make(map[string]linear.Issue, len(live))
	for _, is := range live {
		byUUID[is.ID] = is
	}
	changed := false
	for _, inst := range m.list.GetInstances() {
		is, ok := byUUID[inst.IssueUUID]
		if !ok {
			continue
		}
		done := m.dispatcher.rule.IsDone(is)
		switch {
		case done && inst.Status != session.Done && inst.Status != session.Paused:
			inst.SetStatus(session.Done)
			changed = true
		case !done && inst.Status == session.Done:
			inst.SetStatus(session.Ready)
			changed = true
		}
	}
	return changed
}

// workingCount is the number of instances that count against max_concurrent:
// those actually doing work. Blocked, idle, paused and done instances are free.
func (m *home) workingCount() int {
	n := 0
	for _, inst := range m.list.GetInstances() {
		if inst.Status == session.Running || inst.Status == session.Loading {
			n++
		}
	}
	return n
}

// drainPending spawns queued tickets while capacity allows.
func (m *home) drainPending() []tea.Cmd {
	d := m.dispatcher
	if d == nil {
		return nil
	}
	var cmds []tea.Cmd
	for len(d.pending) > 0 &&
		m.workingCount() < d.cfg.Linear.MaxConcurrent &&
		m.list.NumInstances() < m.instanceLimit() {
		issue := d.pending[0]
		d.pending = d.pending[1:]
		if m.list.FindByTitle(issue.Identifier) != nil {
			continue
		}
		cmd, err := m.spawnTicket(issue)
		if err != nil {
			cmds = append(cmds, m.handleError(err))
			continue
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// ticketBranchName follows the backend repo's "<owner>/<ticket>-<slug>" convention.
func ticketBranchName(prefix string, issue linear.Issue) string {
	slug := nonSlugChars.ReplaceAllString(strings.ToLower(issue.Title), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 50 {
		slug = strings.TrimRight(slug[:50], "-")
	}
	name := prefix + strings.ToLower(issue.Identifier)
	if slug != "" {
		name += "-" + slug
	}
	return name
}

// spawnTicket creates and starts an instance for a Linear issue through the regular
// instance creation path. The returned Cmd runs Start in the background and reports
// via instanceStartedMsg with autoSpawned set.
func (m *home) spawnTicket(issue linear.Issue) (tea.Cmd, error) {
	d := m.dispatcher
	if m.list.FindByTitle(issue.Identifier) != nil {
		return nil, fmt.Errorf("an instance for %s already exists", issue.Identifier)
	}
	if m.list.NumInstances() >= m.instanceLimit() {
		return nil, fmt.Errorf("you can't create more than %d instances", m.instanceLimit())
	}

	// A status file left behind by an earlier session for this ticket would be
	// mistaken for the new session's state until its first hook fires.
	if err := status.Remove(issue.Identifier); err != nil {
		log.WarningLog.Printf("could not remove stale status for %s: %v", issue.Identifier, err)
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:         issue.Identifier,
		Path:          d.cfg.Spawn.RepoPath,
		Program:       strings.ReplaceAll(d.cfg.Spawn.Program, "{ISSUE_ID}", issue.Identifier),
		NewBranchName: ticketBranchName(d.cfg.Spawn.BranchPrefix, issue),
		BaseRef:       "origin/main",
		IssueID:       issue.Identifier,
		IssueUUID:     issue.ID,
		IssueURL:      issue.URL,
	})
	if err != nil {
		return nil, err
	}

	finalize := m.list.AddInstance(instance)
	instance.SetStatus(session.Loading)
	finalize()

	return func() tea.Msg {
		err := instance.Start(true)
		return instanceStartedMsg{instance: instance, err: err, autoSpawned: true}
	}, nil
}

// trustPromptDelays spaces out the checks for Claude Code's trust dialog after spawn.
var trustPromptDelays = []time.Duration{
	1 * time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second, 8 * time.Second,
}

func (m *home) trustPromptCheckCmd(instance *session.Instance, attempt int) tea.Cmd {
	if attempt >= len(trustPromptDelays) {
		return nil
	}
	delay := trustPromptDelays[attempt]
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(delay):
			return trustPromptCheckMsg{instance: instance, attempt: attempt}
		}
	}
}

func (m *home) handleTrustPromptCheck(msg trustPromptCheckMsg) tea.Cmd {
	inst := msg.instance
	if !inst.Started() || inst.Paused() {
		return nil
	}
	if inst.CheckAndHandleTrustPrompt() {
		log.InfoLog.Printf("dismissed trust prompt for %s", inst.Title)
	}
	return m.trustPromptCheckCmd(inst, msg.attempt+1)
}

// applyHookStatus maps a hook status file onto the instance. Returns true if the
// Claude session id changed and the instance should be persisted.
func applyHookStatus(inst *session.Instance, hs *status.Status) (persist bool) {
	switch hs.State {
	case status.StateBlocked:
		inst.SetStatus(session.Blocked)
	case status.StateIdle:
		inst.SetStatus(session.Ready)
	case status.StateRunning:
		inst.SetStatus(session.Running)
	}
	inst.HookReason = hs.Reason
	if hs.SessionID != "" && hs.SessionID != inst.ClaudeSessionID {
		inst.ClaudeSessionID = hs.SessionID
		return true
	}
	return false
}

// lookupTicketCmd resolves a hand-entered issue id in the background.
func (m *home) lookupTicketCmd(identifier string) tea.Cmd {
	d := m.dispatcher
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		issue, err := d.client.IssueByIdentifier(ctx, identifier)
		return ticketLookupDoneMsg{issue: issue, err: err}
	}
}

func (m *home) instanceLimit() int {
	return m.appConfig.InstanceLimit
}

// workPath is the repo new instances are created from: spawn.repo_path in Linear
// mode, otherwise the current directory as upstream does.
func (m *home) workPath() string {
	if m.dispatcher != nil {
		return m.dispatcher.cfg.Spawn.RepoPath
	}
	return "."
}
