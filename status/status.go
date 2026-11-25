// Package status is the contract between Claude Code hooks and the TUI. The
// `cs hook` subcommand writes one JSON file per session; the TUI reads it on its
// metadata tick and prefers it over the pane-diff heuristic.
package status

import (
	"claude-squad/config"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KeyEnv names the status file of the session; cs sets it on every tmux session it
// starts. Hooks are a no-op without it (or IssueIDEnv), so installing `cs hook`
// globally does not affect Claude Code sessions cs did not start.
const KeyEnv = "CS_STATUS_KEY"

// IssueIDEnv is the ticket id of a ticket session. Sessions started before KeyEnv
// existed only have this one, and their status file is named after it.
const IssueIDEnv = "CS_ISSUE_ID"

// KeyFromEnv returns the status file key for the hook's session, "" outside cs.
func KeyFromEnv(getenv func(string) string) string {
	if key := getenv(KeyEnv); key != "" {
		return key
	}
	return getenv(IssueIDEnv)
}

const (
	StateRunning = "running"
	// StateBlocked means Claude Code is stuck mid-task on a permission prompt or an
	// explicit question; nothing proceeds until a human acts.
	StateBlocked = "blocked"
	// StateIdle means Claude Code ended its turn at a natural checkpoint and is
	// waiting for the next prompt.
	StateIdle = "idle"
)

type Status struct {
	State     string `json:"state"`
	TS        int64  `json:"ts"`
	Reason    string `json:"reason,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Notification is Claude Code's notification_type for a blocked state, e.g.
	// "permission_prompt", or "idle_prompt" when it is only waiting for the next prompt.
	Notification string `json:"notification,omitempty"`
	// Event is the hook event that wrote the state, e.g. "Stop" for a finished turn as
	// opposed to "SessionStart" for a session that was just resumed.
	Event string `json:"event,omitempty"`
}

// AcceptsPrompt reports whether Claude Code is waiting at its input box, so text
// typed into the session lands as a new prompt rather than an answer to a dialog.
func (s *Status) AcceptsPrompt() bool {
	return s.State == StateIdle || s.State == StateBlocked && s.Notification == "idle_prompt"
}

func Dir() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "status"), nil
}

func pathFor(key string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, key)
	return filepath.Join(dir, safe+".json"), nil
}

// Read returns the status for a session key, or (nil, nil) when no hook has written one.
func Read(key string) (*Status, error) {
	if key == "" {
		return nil, nil
	}
	path, err := pathFor(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse status file %s: %w", path, err)
	}
	return &s, nil
}

// Remove drops a stale status file, e.g. before respawning a ticket.
func Remove(key string) error {
	if key == "" {
		return nil
	}
	path, err := pathFor(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Write atomically replaces the status file.
func Write(key string, s Status) error {
	path, err := pathFor(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// hookInput is the subset of the Claude Code hook stdin payload we read.
type hookInput struct {
	SessionID            string `json:"session_id"`
	HookEventName        string `json:"hook_event_name"`
	Source               string `json:"source"`
	NotificationType     string `json:"notification_type"`
	Message              string `json:"message"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// HandleHook implements `cs hook`: it reads a Claude Code hook payload and updates the
// status file named by key (see KeyFromEnv). It returns nil without doing anything
// when key is empty.
func HandleHook(stdin io.Reader, key string, now time.Time) error {
	if key == "" {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		return fmt.Errorf("read hook input: %w", err)
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parse hook input: %w", err)
	}

	prev, err := Read(key)
	if err != nil {
		return err
	}
	next := Status{TS: now.Unix(), SessionID: in.SessionID, Event: in.HookEventName}
	if next.SessionID == "" && prev != nil {
		next.SessionID = prev.SessionID
	}

	switch in.HookEventName {
	case "UserPromptSubmit", "PostToolUse":
		next.State = StateRunning
	case "Stop":
		next.State = StateIdle
		next.Reason = lastLine(in.LastAssistantMessage)
	case "Notification":
		next.State = StateBlocked
		next.Reason = in.Message
		next.Notification = in.NotificationType
	case "SessionStart":
		switch in.Source {
		case "startup":
			next.State = StateRunning
		case "resume", "clear":
			next.State = StateIdle
		default:
			// Compaction and forks keep whatever state the session was in.
			if prev == nil {
				return nil
			}
			next.State = prev.State
			next.Reason = prev.Reason
		}
	default:
		return nil
	}
	return Write(key, next)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 160 {
				return l[:160] + "…"
			}
			return l
		}
	}
	return ""
}
