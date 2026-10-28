package app

import (
	"claude-squad/linear"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/status"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The update watcher hands new Linear comments by other people to the ticket's
// session, so an answer to something the agent asked for is acted on before anyone
// opens the session. Comments by the API key's own user are skipped: they are either
// the user's or posted by an agent on the user's behalf, and the two look the same.

// updateLookback caps how far back one poll asks Linear for comments, should a
// session stay busy for that long.
const updateLookback = 14 * 24 * time.Hour

// ticketUpdateSentMsg reports an update typed into an idle session. skipped means the
// session turned out not to be ready (someone typing); the next poll tries again.
type ticketUpdateSentMsg struct {
	instance *session.Instance
	seen     time.Time
	skipped  bool
	err      error
}

// watchedTickets returns the Linear ids of the active ticket sessions and the time to
// fetch comments from. Sessions that have never been watched start at now, so turning
// the watcher on does not replay old comments; it returns true if any were.
func (m *home) watchedTickets(now time.Time) (ids []string, since time.Time, seeded bool) {
	if m.dispatcher == nil || !m.dispatcher.cfg.Linear.WatchUpdates {
		return nil, time.Time{}, false
	}
	since = now
	for _, inst := range m.list.GetInstances() {
		if inst.IssueUUID == "" {
			continue
		}
		if inst.LinearSeenAt.IsZero() {
			inst.LinearSeenAt = now
			seeded = true
		}
		ids = append(ids, inst.IssueUUID)
		if inst.LinearSeenAt.Before(since) {
			since = inst.LinearSeenAt
		}
	}
	if floor := now.Add(-updateLookback); since.Before(floor) {
		since = floor
	}
	return ids, since, seeded
}

// deliverTicketUpdates hands each session the comments by others newer than its
// LinearSeenAt: paused sessions are resumed with the update prompt, idle ones get it
// typed in, and busy ones wait for a later poll. comments is oldest first.
func (m *home) deliverTicketUpdates(comments []linear.Comment) []tea.Cmd {
	d := m.dispatcher
	if d.delivering == nil {
		d.delivering = make(map[*session.Instance]bool)
	}
	byIssue := make(map[string][]linear.Comment)
	for _, c := range comments {
		if !c.IsMe {
			byIssue[c.IssueID] = append(byIssue[c.IssueID], c)
		}
	}
	var cmds []tea.Cmd
	persist := false
	for _, inst := range m.list.GetInstances() {
		var news []linear.Comment
		for _, c := range byIssue[inst.IssueUUID] {
			if c.CreatedAt.After(inst.LinearSeenAt) {
				news = append(news, c)
			}
		}
		// A session open in the terminal waits: the user is in it.
		if len(news) == 0 || d.delivering[inst] || inst.Restoring || inst.Status == session.Loading ||
			inst.Attached() {
			continue
		}
		prompt := m.updatePrompt(inst.IssueID, news)
		seen := news[len(news)-1].CreatedAt
		if inst.Paused() {
			inst.LinearSeenAt = seen
			inst.LinearUpdateAt = time.Now()
			persist = true
			cmds = append(cmds, m.resumeInBackground(inst, prompt, fmt.Sprintf("%d new Linear comment(s)", len(news))))
			continue
		}
		if !acceptsPrompt(inst) {
			continue
		}
		d.delivering[inst] = true
		cmds = append(cmds, func() tea.Msg {
			empty, err := inst.InputBoxEmpty()
			if err != nil || !empty {
				return ticketUpdateSentMsg{instance: inst, skipped: true}
			}
			return ticketUpdateSentMsg{instance: inst, seen: seen, err: inst.SendPrompt(prompt)}
		})
	}
	if persist {
		if err := m.saveInstances(); err != nil {
			cmds = append(cmds, m.handleError(err))
		}
	}
	return cmds
}

func (m *home) handleTicketUpdateSent(msg ticketUpdateSentMsg) tea.Cmd {
	inst := msg.instance
	delete(m.dispatcher.delivering, inst)
	if msg.skipped {
		return nil
	}
	if msg.err != nil {
		return m.handleError(fmt.Errorf("sending Linear update to %s: %w", inst.Title, msg.err))
	}
	log.InfoLog.Printf("%s: sent new Linear comments to the session", inst.Title)
	inst.LinearSeenAt = msg.seen
	inst.LinearUpdateAt = time.Now()
	if err := m.saveInstances(); err != nil {
		return m.handleError(err)
	}
	return nil
}

// acceptsPrompt reports whether a running session is waiting at its input box. The
// hook status is authoritative for ticket sessions; without one, fall back to the
// pane heuristic.
func acceptsPrompt(inst *session.Instance) bool {
	hs, err := status.Read(inst.IssueID)
	if err != nil {
		log.WarningLog.Printf("could not read hook status for %s: %v", inst.IssueID, err)
		return false
	}
	if hs != nil {
		return hs.AcceptsPrompt()
	}
	return inst.Status == session.Ready
}

// updatePrompt fills in the configured update prompt for a ticket's new comments.
func (m *home) updatePrompt(issueID string, news []linear.Comment) string {
	parts := make([]string, 0, len(news))
	for _, c := range news {
		author := c.Author
		if author == "" {
			author = "someone"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", author, c.CreatedAt.Local().Format("Jan 2 15:04")))
	}
	r := strings.NewReplacer("{ISSUE_ID}", issueID, "{UPDATES}", strings.Join(parts, ", "))
	return r.Replace(m.dispatcher.cfg.Linear.UpdatePrompt)
}
