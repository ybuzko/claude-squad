// Package status is the contract between Claude Code hooks and the TUI. The
// `cs hook` subcommand writes one JSON file per ticket; the TUI reads it on its
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

// IssueIDEnv is set by the spawner on the tmux session. Hooks are a no-op without it,
// so installing `cs hook` globally does not affect ordinary Claude Code sessions.
const IssueIDEnv = "CS_ISSUE_ID"

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
}

func Dir() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "status"), nil
}

func pathFor(issueID string) (string, error) {
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
	}, issueID)
	return filepath.Join(dir, safe+".json"), nil
}

// Read returns the status for an issue, or (nil, nil) when no hook has written one.
func Read(issueID string) (*Status, error) {
	path, err := pathFor(issueID)
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
func Remove(issueID string) error {
	path, err := pathFor(issueID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Write atomically replaces the status file.
func Write(issueID string, s Status) error {
	path, err := pathFor(issueID)
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
// status file for the ticket named by the CS_ISSUE_ID environment variable. It
// returns nil without doing anything when the variable is unset.
func HandleHook(stdin io.Reader, issueID string, now time.Time) error {
	if issueID == "" {
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

	prev, err := Read(issueID)
	if err != nil {
		return err
	}
	next := Status{TS: now.Unix(), SessionID: in.SessionID}
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
	return Write(issueID, next)
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
