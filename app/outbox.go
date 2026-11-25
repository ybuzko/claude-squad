package app

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The outbox lets something outside the TUI (`cs send`, a script, another Claude
// session) hand a prompt to a session: one file per session in
// ~/.claude-squad/outbox, named after the session title or ticket id. Each Linear poll
// delivers what it can the way ticket updates are delivered: paused sessions are
// resumed with the prompt, idle ones get it typed in, busy ones wait. Delivered files
// move to outbox/sent.

const (
	outboxDirName  = "outbox"
	outboxSentName = "sent"
	outboxFileExt  = ".md"
)

// OutboxDir is where queued messages wait.
func OutboxDir() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, outboxDirName), nil
}

// QueueMessage stores a prompt for the session with that title or ticket id; the
// running cs delivers it on a later poll. A message already waiting for the session is
// replaced. It returns the file written.
func QueueMessage(name, prompt string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("invalid session name %q", name)
	}
	if flattenPrompt(prompt) == "" {
		return "", fmt.Errorf("empty message for %s", name)
	}
	dir, err := OutboxDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+outboxFileExt)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// flattenPrompt makes a message one line: a newline typed into the input box would
// submit the prompt half way.
func flattenPrompt(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// outboxSentMsg reports a queued message typed into an idle session; see
// ticketUpdateSentMsg.
type outboxSentMsg struct {
	instance *session.Instance
	path     string
	skipped  bool
	err      error
}

// outboxTarget finds the active session a queued file is for, by title or ticket id.
func (m *home) outboxTarget(name string) *session.Instance {
	for _, inst := range m.list.GetInstances() {
		if strings.EqualFold(inst.Title, name) || (inst.IssueID != "" && strings.EqualFold(inst.IssueID, name)) {
			return inst
		}
	}
	return nil
}

// deliverOutbox hands queued messages to their sessions. A message puts its session to
// work, so no more are started than max_concurrent leaves room for; the rest wait for
// a later poll, as do messages for busy sessions and for sessions that are not in the
// active list (archived ones are not woken).
func (m *home) deliverOutbox() []tea.Cmd {
	dir, err := OutboxDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.SliceStable(entries, func(a, b int) bool {
		return strings.ToLower(entries[a].Name()) < strings.ToLower(entries[b].Name())
	})

	room := 1 << 30
	if m.dispatcher != nil && m.dispatcher.cfg.Linear.MaxConcurrent > 0 {
		room = m.dispatcher.cfg.Linear.MaxConcurrent - m.workingCount()
		// Deliveries still in flight are about to turn their sessions busy.
		for inst := range m.delivering {
			if !isBusy(inst) {
				room--
			}
		}
	}

	var cmds []tea.Cmd
	persist := false
	for _, e := range entries {
		if room <= 0 {
			break
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), outboxFileExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		inst := m.outboxTarget(strings.TrimSuffix(e.Name(), outboxFileExt))
		if inst == nil {
			if !m.outboxWarned[path] {
				if m.outboxWarned == nil {
					m.outboxWarned = make(map[string]bool)
				}
				m.outboxWarned[path] = true
				log.WarningLog.Printf("outbox: no active session for %s; the message waits", e.Name())
			}
			continue
		}
		if m.delivering[inst] || inst.Restoring || inst.Pausing || inst.Status == session.Loading || inst.Attached() {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			log.WarningLog.Printf("outbox: reading %s: %v", e.Name(), err)
			continue
		}
		prompt := flattenPrompt(string(raw))
		if prompt == "" {
			markOutboxSent(path)
			continue
		}
		if inst.Paused() {
			// Filed as sent before the resume starts, so a slow resume cannot deliver twice.
			markOutboxSent(path)
			inst.NewsAt = time.Now()
			persist = true
			cmds = append(cmds, m.resumeInBackground(inst, prompt, "queued message "+e.Name()))
			room--
			continue
		}
		if !acceptsPrompt(inst) {
			continue
		}
		m.delivering[inst] = true
		room--
		cmds = append(cmds, func() tea.Msg {
			empty, err := inst.InputBoxEmpty()
			if err != nil || !empty {
				return outboxSentMsg{instance: inst, path: path, skipped: true}
			}
			return outboxSentMsg{instance: inst, path: path, err: inst.SendPrompt(prompt)}
		})
	}
	if persist {
		if err := m.saveInstances(); err != nil {
			cmds = append(cmds, m.handleError(err))
		}
	}
	return cmds
}

func (m *home) handleOutboxSent(msg outboxSentMsg) tea.Cmd {
	inst := msg.instance
	delete(m.delivering, inst)
	if msg.skipped {
		return nil
	}
	if msg.err != nil {
		return m.handleError(fmt.Errorf("sending queued message to %s: %w", inst.Title, msg.err))
	}
	log.InfoLog.Printf("%s: sent queued message %s", inst.Title, filepath.Base(msg.path))
	markOutboxSent(msg.path)
	inst.NewsAt = time.Now()
	if err := m.saveInstances(); err != nil {
		return m.handleError(err)
	}
	return nil
}

// markOutboxSent moves a delivered message to outbox/sent, stamped with the time.
func markOutboxSent(path string) {
	sent := filepath.Join(filepath.Dir(path), outboxSentName)
	if err := os.MkdirAll(sent, 0o755); err != nil {
		log.WarningLog.Printf("outbox: %v", err)
	}
	name := strings.TrimSuffix(filepath.Base(path), outboxFileExt)
	dest := filepath.Join(sent, name+"-"+time.Now().Format("20060102-150405")+outboxFileExt)
	if err := os.Rename(path, dest); err != nil {
		log.WarningLog.Printf("outbox: filing %s as sent: %v", filepath.Base(path), err)
		// Never leave it to be delivered again.
		_ = os.Remove(path)
	}
}
