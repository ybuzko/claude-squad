package app

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/notify"
	"claude-squad/session"
	"claude-squad/status"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The notifier shows a desktop notification when a session needs you (a permission
// prompt or a question) and, with notify = "blocked+idle", when one finishes its turn.
// It goes by the Claude Code hooks, so sessions without a status file (a program other
// than Claude Code, or one started before cs exported CS_STATUS_KEY) never notify.
// Nothing is shown for the session you have open, and events that arrive close together
// go out as one notification.

// notifyDelay is how long the notifier gathers events before showing them. It also
// drops events that are already over: a finished session the update or PR watcher has
// handed more work, or a prompt answered from the Claude app.
const notifyDelay = 3 * time.Second

type notifier struct {
	idle bool // also notify when a session finishes its turn
	// last is the hook status each session had at the previous tick; a notification
	// goes out when it changes to a Notification or Stop event.
	last      map[*session.Instance]hookSig
	pending   []notifyEvent
	scheduled bool
	send      func(ctx context.Context, title, body string) error
}

type hookSig struct {
	ts     int64
	event  string
	reason string
}

type notifyEvent struct {
	inst    *session.Instance
	blocked bool
	reason  string
}

type notifyFlushMsg struct{}

type notifySentMsg struct{ err error }

func newNotifier(cfg *config.Config) *notifier {
	if cfg.Notify == config.NotifyOff {
		return nil
	}
	return &notifier{
		idle: cfg.Notify == config.NotifyBlockedIdle,
		last: make(map[*session.Instance]hookSig),
		send: notify.Send,
	}
}

// noteHookStatus is called on every metadata tick with the session's hook status (nil
// if it has none) and queues a notification when a new hook event calls for one.
func (m *home) noteHookStatus(inst *session.Instance, hs *status.Status) tea.Cmd {
	n := m.notifier
	if n == nil || hs == nil {
		return nil
	}
	sig := hookSig{ts: hs.TS, event: hs.Event, reason: hs.Reason}
	last, seen := n.last[inst]
	n.last[inst] = sig
	// The first look after cs starts only records where things are.
	if !seen || sig == last {
		return nil
	}
	ev := notifyEvent{inst: inst, reason: hs.Reason}
	switch {
	case hs.Event == "Notification" && hs.State == status.StateBlocked && hs.Notification != "idle_prompt":
		ev.blocked = true
	case hs.Event == "Stop" && hs.State == status.StateIdle && n.idle:
	default:
		return nil
	}
	if !notifyWanted(inst) {
		return nil
	}
	pending := n.pending[:0]
	for _, p := range n.pending {
		if p.inst != inst {
			pending = append(pending, p)
		}
	}
	n.pending = append(pending, ev)
	if n.scheduled {
		return nil
	}
	n.scheduled = true
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
			return nil
		case <-time.After(notifyDelay):
			return notifyFlushMsg{}
		}
	}
}

// notifyWanted reports whether a notification about inst would tell you anything: not
// while you have it open, or while cs is pausing or resuming it.
func notifyWanted(inst *session.Instance) bool {
	return !inst.Attached() && !inst.Restoring && !inst.Pausing && !inst.Archived
}

// handleNotifyFlush shows the events gathered since the first one, minus those that are
// over by now.
func (m *home) handleNotifyFlush() tea.Cmd {
	n := m.notifier
	if n == nil {
		return nil
	}
	n.scheduled = false
	var events []notifyEvent
	for _, ev := range n.pending {
		want := session.Ready
		if ev.blocked {
			want = session.Blocked
		}
		if ev.inst.Status == want && notifyWanted(ev.inst) && m.list.Contains(ev.inst) {
			events = append(events, ev)
		}
	}
	n.pending = nil
	for inst := range n.last {
		if !m.list.Contains(inst) && !m.archive.Contains(inst) {
			delete(n.last, inst)
		}
	}
	if len(events) == 0 {
		return nil
	}
	title, body := notification(events)
	log.InfoLog.Printf("notify: %s: %s", title, strings.ReplaceAll(body, "\n", " | "))
	send := n.send
	return func() tea.Msg {
		return notifySentMsg{err: send(m.ctx, title, body)}
	}
}

func (m *home) handleNotifySent(msg notifySentMsg) tea.Cmd {
	if msg.err == nil {
		return nil
	}
	if errors.Is(msg.err, notify.ErrUnsupported) {
		log.WarningLog.Printf("desktop notifications off: %v", msg.err)
		m.notifier = nil
		return nil
	}
	log.WarningLog.Printf("desktop notification: %v", msg.err)
	return nil
}

// notification words the events as one notification, sessions that need you first.
func notification(events []notifyEvent) (title, body string) {
	var blocked, idle []notifyEvent
	for _, ev := range events {
		if ev.blocked {
			blocked = append(blocked, ev)
		} else {
			idle = append(idle, ev)
		}
	}
	if len(events) == 1 {
		ev := events[0]
		title = sessionName(ev.inst) + " finished"
		if ev.blocked {
			title = sessionName(ev.inst) + " needs you"
		}
		lines := []string{ev.reason}
		if ev.inst.IsTicket() && ev.inst.IssueTitle != "" {
			lines = append(lines, ev.inst.IssueTitle)
		}
		return title, strings.TrimSpace(strings.Join(lines, "\n"))
	}
	var parts []string
	if len(blocked) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s you", len(blocked), plural(len(blocked), "needs", "need")))
	}
	if len(idle) > 0 {
		parts = append(parts, fmt.Sprintf("%d finished", len(idle)))
	}
	title = strings.Join(parts, ", ")
	var lines []string
	for _, ev := range append(blocked, idle...) {
		line := sessionName(ev.inst)
		if ev.reason != "" {
			line += ": " + ev.reason
		}
		lines = append(lines, truncate(line, 120))
	}
	return title, strings.Join(lines, "\n")
}

func sessionName(inst *session.Instance) string {
	if inst.IsTicket() {
		return inst.IssueID
	}
	return inst.Title
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
