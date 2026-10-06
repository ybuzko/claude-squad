package session

import "time"

// Diff stats cost a `git add -N .` plus a `git diff` in the worktree, which in a large
// repo is ~0.1–0.15 s of CPU. Upstream ran both for every instance every 500 ms; these
// bounds keep idle instances nearly free while active ones stay current.
const (
	// DiffMinInterval is the shortest gap between two diff refreshes of one instance.
	DiffMinInterval = 5 * time.Second
	// DiffMaxAge refreshes an instance even with no sign of activity (e.g. files edited
	// by hand in its worktree).
	DiffMaxAge = 60 * time.Second
)

// diffSchedule tracks when an instance's diff stats were last computed and whether
// anything happened since. Only the metadata tick's goroutine for this instance
// touches it, and ticks never overlap.
type diffSchedule struct {
	last     time.Time
	lastFull bool
	activity bool
	hookTS   int64
}

// NoteDiffActivity records signs that the instance may have changed files since the
// last refresh: its pane output changed, or its Claude Code hook status was rewritten
// (hookTS is the status file's timestamp, 0 when there is none).
func (i *Instance) NoteDiffActivity(paneChanged bool, hookTS int64) {
	if paneChanged {
		i.diffSched.activity = true
	}
	if hookTS != 0 && hookTS != i.diffSched.hookTS {
		i.diffSched.hookTS = hookTS
		i.diffSched.activity = true
	}
}

// DiffDue reports whether the diff stats should be recomputed now. The selected
// instance refreshes every DiffMinInterval and right away when it becomes selected (the
// diff pane needs its full content); others only after activity, at most every
// DiffMinInterval, or once they are DiffMaxAge old.
func (i *Instance) DiffDue(now time.Time, selected bool) bool {
	s := &i.diffSched
	if s.last.IsZero() || (selected && !s.lastFull) {
		return true
	}
	elapsed := now.Sub(s.last)
	if elapsed >= DiffMaxAge {
		return true
	}
	if elapsed < DiffMinInterval {
		return false
	}
	return selected || s.activity
}

// MarkDiffComputed records a refresh; full is true for a diff with content (selected).
func (i *Instance) MarkDiffComputed(now time.Time, full bool) {
	i.diffSched = diffSchedule{last: now, lastFull: full, hookTS: i.diffSched.hookTS}
}
