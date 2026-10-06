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

// handlePollDone applies done-state labels, archives sessions whose ticket left the
// view, rebuilds the queue of tickets that need a session (new or returning from the
// archive), and starts whatever the concurrency cap allows. It always reschedules.
func (m *home) handlePollDone(msg linearPollDoneMsg) tea.Cmd {
	d := m.dispatcher
	d.polling = false
	if msg.err != nil {
		return tea.Batch(m.handleError(msg.err), m.schedulePollCmd())
	}

	var cmds []tea.Cmd
	persist := m.applyTicketStates(msg.live)
	if refreshTicketDetails(append(append([]linear.Issue{}, msg.view...), msg.live...),
		m.list.GetInstances(), m.archive.GetInstances()) {
		persist = true
	}

	plan := planView(msg.view, m.list.GetInstances(), m.archive.GetInstances())
	persist = persist || plan.changed
	for _, inst := range plan.archive {
		log.InfoLog.Printf("%s left the view; archiving its session", inst.IssueID)
		cmds = append(cmds, m.archiveTicket(inst, true))
	}
	if len(plan.archive) > 0 {
		cmds = append(cmds, m.instanceChanged())
	}
	if persist {
		if err := m.saveInstances(); err != nil {
			cmds = append(cmds, m.handleError(err))
		}
	}

	d.pending = plan.queue
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
		if isBusy(inst) {
			n++
		}
	}
	return n
}

// drainPending starts queued tickets while capacity allows: archived ones are
// restored with the resume prompt, the rest get a fresh session.
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
		if archived := m.archive.FindByTitle(issue.Identifier); archived != nil {
			archived.InView = true
			cmds = append(cmds, m.restoreTicket(archived, m.resumePrompt(issue.Identifier)))
			continue
		}
		cmd, err := m.spawnTicket(issue, true)
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
// inView marks a ticket that came from the view, so the poller archives the session
// once the ticket leaves it; `t` spawns pass false and the next poll settles it.
func (m *home) spawnTicket(issue linear.Issue, inView bool) (tea.Cmd, error) {
	d := m.dispatcher
	if m.list.FindByTitle(issue.Identifier) != nil || m.archive.FindByTitle(issue.Identifier) != nil {
		return nil, fmt.Errorf("an instance for %s already exists", issue.Identifier)
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
		IssueTitle:    issue.Title,
		IssueDueDate:  issue.DueDate,
		SetupCommand:  m.setupCommand(),
	})
	if err != nil {
		return nil, err
	}
	instance.InView = inView

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
	13 * time.Second, 21 * time.Second,
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

// instanceLimit caps the active instances the dispatcher fills up to on its own
// (spawns and restores). Manual n, N, t and archive restores may go past it.
func (m *home) instanceLimit() int {
	return m.appConfig.InstanceLimit
}

// setupCommand is the per-worktree setup step for new instances in Linear mode;
// empty otherwise, which keeps upstream behaviour for plain sessions.
func (m *home) setupCommand() string {
	if m.dispatcher != nil {
		return m.dispatcher.cfg.Spawn.SetupCommand
	}
	return ""
}

// workPath is the repo new instances are created from: spawn.repo_path in Linear
// mode, otherwise the current directory as upstream does.
func (m *home) workPath() string {
	if m.dispatcher != nil {
		return m.dispatcher.cfg.Spawn.RepoPath
	}
	return "."
}

// viewPlan is what one poll of the view asks the dispatcher to do.
type viewPlan struct {
	// archive holds live ticket instances whose ticket left the view and that are not
	// mid-turn. Busy ones are left for a later poll (their InView stays true).
	archive []*session.Instance
	// queue holds view issues, in view order, that need a session: archived tickets
	// that came back into the view, and tickets with no instance at all.
	queue []linear.Issue
	// changed is true when some instance's InView flag was updated and should be saved.
	changed bool
}

// isBusy reports whether an instance is in the middle of work that archiving would cut off.
func isBusy(inst *session.Instance) bool {
	return inst.Status == session.Running || inst.Status == session.Loading || inst.Restoring
}

// planView compares the view with what each ticket instance last saw and acts only on
// changes: a live session whose ticket left the view is archived, an archived one whose
// ticket came back is restored. A session archived by hand while its ticket was still
// in the view stays archived until the ticket leaves and returns. Tickets started with
// `t` from outside the view are never archived by the poller.
func planView(view []linear.Issue, active, archived []*session.Instance) viewPlan {
	var plan viewPlan
	inView := make(map[string]bool, len(view))
	for _, is := range view {
		inView[is.Identifier] = true
	}

	byID := make(map[string]*session.Instance)
	for _, inst := range active {
		if !inst.IsTicket() {
			continue
		}
		byID[inst.IssueID] = inst
		present := inView[inst.IssueID]
		switch {
		case present && !inst.InView:
			inst.InView = true
			plan.changed = true
		case !present && inst.InView:
			if !isBusy(inst) {
				plan.archive = append(plan.archive, inst)
			}
		}
	}

	restore := make(map[string]bool)
	for _, inst := range archived {
		if !inst.IsTicket() {
			continue
		}
		if _, live := byID[inst.IssueID]; live {
			continue
		}
		byID[inst.IssueID] = inst
		present := inView[inst.IssueID]
		switch {
		case present && !inst.InView:
			restore[inst.IssueID] = true
		case !present && inst.InView:
			inst.InView = false
			plan.changed = true
		}
	}

	for _, is := range view {
		if _, known := byID[is.Identifier]; !known || restore[is.Identifier] {
			plan.queue = append(plan.queue, is)
		}
	}
	return plan
}

// refreshTicketDetails copies each ticket's current title and due date from Linear
// onto its instances, active or archived. Returns true if anything changed.
func refreshTicketDetails(issues []linear.Issue, lists ...[]*session.Instance) bool {
	byID := make(map[string]linear.Issue, len(issues))
	for _, is := range issues {
		byID[is.Identifier] = is
	}
	changed := false
	for _, list := range lists {
		for _, inst := range list {
			is, ok := byID[inst.IssueID]
			if !inst.IsTicket() || !ok {
				continue
			}
			if inst.IssueTitle != is.Title || inst.IssueDueDate != is.DueDate {
				inst.IssueTitle, inst.IssueDueDate = is.Title, is.DueDate
				changed = true
			}
		}
	}
	return changed
}
