package session

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"errors"
	"path/filepath"

	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

type Status int

const (
	// Running is the status when the instance is running and claude is working.
	Running Status = iota
	// Ready is if the claude instance is ready to be interacted with (waiting for user input).
	Ready
	// Loading is if the instance is loading (if we are starting it up or something).
	Loading
	// Paused is if the instance is paused (worktree removed but branch preserved).
	Paused
	// Blocked is if claude is stuck mid-task on a permission prompt or a question and
	// cannot proceed until a human acts. Only set from Claude Code hook status files.
	Blocked
	// Done is if the instance's Linear ticket reached a done state. The session is
	// kept until the user cleans it up.
	Done
)

// Instance is a running instance of claude code.
type Instance struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
	// Status is the status of the instance.
	Status Status
	// Program is the program to run in the instance.
	Program string
	// Height is the height of the instance.
	Height int
	// Width is the width of the instance.
	Width int
	// CreatedAt is the time the instance was created.
	CreatedAt time.Time
	// UpdatedAt is the time the instance was last updated.
	UpdatedAt time.Time
	// AutoYes is true if the instance should automatically press enter when prompted.
	AutoYes bool
	// Prompt is the initial prompt to pass to the instance on startup
	Prompt string

	// IssueID is the Linear identifier (e.g. "TSA-123") for ticket instances; empty otherwise.
	IssueID string
	// IssueUUID is the Linear issue UUID, used for state lookups.
	IssueUUID string
	IssueURL  string
	// IssueTitle and IssueDueDate (YYYY-MM-DD, empty if none) are shown in the list;
	// every poll refreshes them.
	IssueTitle   string
	IssueDueDate string
	// IssueState is the ticket's Linear workflow state name (e.g. "Blocked by Client").
	IssueState string
	// ClaudeSessionID is the Claude Code session id reported by the SessionStart hook,
	// used to resume the conversation after the tmux session dies.
	ClaudeSessionID string
	// HookReason is the human-readable reason from the last hook status (not persisted).
	HookReason string
	// SetupCommand runs in the worktree whenever it is (re)created, before the program.
	SetupCommand string

	// Archived ticket instances are paused and kept out of the active list so their
	// branch, worktree path and Claude session survive until the ticket comes back.
	Archived   bool
	ArchivedAt time.Time
	// InView records whether the ticket was in the Linear view at the last poll. The
	// dispatcher acts only on changes: leaving the view archives, returning restores.
	InView bool
	// Restoring is true while an archived instance is being brought back (worktree,
	// setup command, tmux). Not persisted.
	Restoring bool
	// diffSched throttles diff-stat refreshes; see diff_schedule.go.
	diffSched diffSchedule
	// StatusChangedAt is when Status last changed (for ticket sessions, the time of the
	// hook event that changed it). The list puts the most recently idle sessions first.
	// Not persisted: hook status files restore it for ticket sessions.
	StatusChangedAt time.Time

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// selectedBranch is the existing branch to start on (empty = new branch from HEAD)
	selectedBranch string
	// newBranchName, when set, names the new branch to create instead of deriving one
	// from the title; baseRef is the ref it is created from (empty = HEAD).
	newBranchName string
	baseRef       string

	// The below fields are initialized upon calling Start().

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.TmuxSession
	// gitWorktree is the git worktree for the instance.
	gitWorktree *git.GitWorktree
}

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		Title:     i.Title,
		Path:      i.Path,
		Branch:    i.Branch,
		Status:    i.Status,
		Height:    i.Height,
		Width:     i.Width,
		CreatedAt: i.CreatedAt,
		UpdatedAt: time.Now(),
		Program:   i.Program,
		AutoYes:   i.AutoYes,

		IssueID:         i.IssueID,
		IssueUUID:       i.IssueUUID,
		IssueURL:        i.IssueURL,
		ClaudeSessionID: i.ClaudeSessionID,
		SetupCommand:    i.SetupCommand,
		IssueTitle:      i.IssueTitle,
		IssueDueDate:    i.IssueDueDate,
		IssueState:      i.IssueState,
		Archived:        i.Archived,
		ArchivedAt:      i.ArchivedAt,
		InView:          i.InView,
	}

	// Only include worktree data if gitWorktree is initialized
	if i.gitWorktree != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:         i.gitWorktree.GetRepoPath(),
			WorktreePath:     i.gitWorktree.GetWorktreePath(),
			SessionName:      i.Title,
			BranchName:       i.gitWorktree.GetBranchName(),
			BaseCommitSHA:    i.gitWorktree.GetBaseCommitSHA(),
			IsExistingBranch: i.gitWorktree.IsExistingBranch(),
		}
	}

	// Only include diff stats if they exist
	if i.diffStats != nil {
		data.DiffStats = DiffStatsData{
			Added:   i.diffStats.Added,
			Removed: i.diffStats.Removed,
			Content: i.diffStats.Content,
		}
	}

	return data
}

// FromInstanceData creates a new Instance from serialized data
func FromInstanceData(data InstanceData) (*Instance, error) {
	instance := &Instance{
		Title:     data.Title,
		Path:      data.Path,
		Branch:    data.Branch,
		Status:    data.Status,
		Height:    data.Height,
		Width:     data.Width,
		CreatedAt: data.CreatedAt,
		UpdatedAt: data.UpdatedAt,
		Program:   data.Program,

		IssueID:         data.IssueID,
		IssueUUID:       data.IssueUUID,
		IssueURL:        data.IssueURL,
		ClaudeSessionID: data.ClaudeSessionID,
		SetupCommand:    data.SetupCommand,
		IssueTitle:      data.IssueTitle,
		IssueDueDate:    data.IssueDueDate,
		IssueState:      data.IssueState,
		Archived:        data.Archived,
		ArchivedAt:      data.ArchivedAt,
		InView:          data.InView,

		gitWorktree: git.NewGitWorktreeFromStorage(
			data.Worktree.RepoPath,
			data.Worktree.WorktreePath,
			data.Worktree.SessionName,
			data.Worktree.BranchName,
			data.Worktree.BaseCommitSHA,
			data.Worktree.IsExistingBranch,
		),
		diffStats: &git.DiffStats{
			Added:   data.DiffStats.Added,
			Removed: data.DiffStats.Removed,
			Content: data.DiffStats.Content,
		},
	}

	if instance.Paused() {
		instance.started = true
		instance.tmuxSession = instance.newTmuxSession()
	} else {
		if err := instance.Start(false); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

// Options for creating a new instance
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// If AutoYes is true, then
	AutoYes bool
	// Branch is an existing branch name to start the session on (empty = new branch from HEAD)
	Branch string
	// NewBranchName, when set, is the exact name of the new branch to create (instead of
	// deriving one from the title). Mutually exclusive with Branch.
	NewBranchName string
	// BaseRef is the ref a new branch is created from (e.g. "origin/main"; empty = HEAD).
	BaseRef string
	// Linear ticket metadata; empty for hand-created instances.
	IssueID      string
	IssueUUID    string
	IssueURL     string
	IssueTitle   string
	IssueDueDate string
	IssueState   string
	// SetupCommand runs in the worktree before the program starts (empty = none).
	SetupCommand string
}

func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// Convert path to absolute
	absPath, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return &Instance{
		Title:          opts.Title,
		Status:         Ready,
		Path:           absPath,
		Program:        opts.Program,
		Height:         0,
		Width:          0,
		CreatedAt:      t,
		UpdatedAt:      t,
		AutoYes:        false,
		IssueID:        opts.IssueID,
		IssueUUID:      opts.IssueUUID,
		IssueURL:       opts.IssueURL,
		IssueTitle:     opts.IssueTitle,
		IssueDueDate:   opts.IssueDueDate,
		IssueState:     opts.IssueState,
		SetupCommand:   opts.SetupCommand,
		selectedBranch: opts.Branch,
		newBranchName:  opts.NewBranchName,
		baseRef:        opts.BaseRef,
	}, nil
}

// IsTicket reports whether the instance was spawned for a Linear ticket.
func (i *Instance) IsTicket() bool {
	return i.IssueID != ""
}

// newTmuxSession builds the tmux session for this instance. Ticket instances export
// CS_ISSUE_ID so Claude Code hooks can report status for them.
func (i *Instance) newTmuxSession() *tmux.TmuxSession {
	s := tmux.NewTmuxSession(i.Title, i.Program)
	if i.IsTicket() {
		s.SetEnv(map[string]string{"CS_ISSUE_ID": i.IssueID})
	}
	return s
}

// ResumeProgram is the command used to restart a ticket instance's tmux session so
// the Claude Code conversation continues instead of re-running the initial prompt.
// Hand-created instances always get their original program back.
func (i *Instance) ResumeProgram() string {
	if !i.IsTicket() {
		return i.Program
	}
	fields := strings.Fields(i.Program)
	if len(fields) == 0 {
		return i.Program
	}
	binary := fields[0]
	if i.ClaudeSessionID != "" {
		return fmt.Sprintf("%s --resume %s", binary, i.ClaudeSessionID)
	}
	return binary + " -c"
}

func (i *Instance) RepoName() (string, error) {
	if !i.started {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	return i.gitWorktree.GetRepoName(), nil
}

func (i *Instance) SetStatus(status Status) {
	if status != i.Status {
		i.StatusChangedAt = time.Now()
	}
	i.Status = status
}

// SetSelectedBranch sets the branch to use when starting the instance.
func (i *Instance) SetSelectedBranch(branch string) {
	i.selectedBranch = branch
}

// firstTimeSetup is true if this is a new instance. Otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	var tmuxSession *tmux.TmuxSession
	if i.tmuxSession != nil {
		// Use existing tmux session (useful for testing)
		tmuxSession = i.tmuxSession
	} else {
		// Create new tmux session
		tmuxSession = i.newTmuxSession()
	}
	i.tmuxSession = tmuxSession

	if firstTimeSetup {
		if i.selectedBranch != "" {
			gitWorktree, err := git.NewGitWorktreeFromBranch(i.Path, i.selectedBranch, i.Title)
			if err != nil {
				return fmt.Errorf("failed to create git worktree from branch: %w", err)
			}
			i.gitWorktree = gitWorktree
			i.Branch = i.selectedBranch
		} else if i.newBranchName != "" {
			gitWorktree, err := git.NewGitWorktreeForBranch(i.Path, i.Title, i.newBranchName, i.baseRef)
			if err != nil {
				return fmt.Errorf("failed to create git worktree for branch: %w", err)
			}
			i.gitWorktree = gitWorktree
			i.Branch = gitWorktree.GetBranchName()
		} else {
			gitWorktree, branchName, err := git.NewGitWorktree(i.Path, i.Title)
			if err != nil {
				return fmt.Errorf("failed to create git worktree: %w", err)
			}
			i.gitWorktree = gitWorktree
			i.Branch = branchName
		}
	}

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%v (cleanup error: %v)", setupErr, cleanupErr)
			}
		} else {
			i.started = true
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session. If the tmux server died since we last ran (reboot,
		// crash, `tmux kill-server`), the session is gone but the worktree and branch
		// are still on disk. Park the instance as Paused so Resume can rebuild it.
		// Reporting an error here would be worse than useless: LoadInstances aborts on
		// the first failure, so a single dead session would hide every other instance.
		if err := tmuxSession.Restore(); err != nil {
			if errors.Is(err, tmux.ErrSessionNotFound) {
				log.WarningLog.Printf(
					"tmux session for %q no longer exists; pausing instance so it can be resumed", i.Title)
				i.SetStatus(Paused)
				return nil
			}
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
	} else {
		// Setup git worktree first
		if err := i.gitWorktree.Setup(); err != nil {
			setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
			return setupErr
		}

		if err := i.runSetupCommand(); err != nil {
			setupErr = err
			return setupErr
		}

		// Create new session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
			setupErr = fmt.Errorf("failed to start new session: %w", err)
			return setupErr
		}
	}

	i.SetStatus(Running)

	return nil
}

// Kill terminates the instance and cleans up all resources
func (i *Instance) Kill() error {
	if !i.started {
		// If instance was never started, just return success
		return nil
	}

	var errs []error

	// Always try to cleanup both resources, even if one fails
	// Clean up tmux session first since it's using the git worktree
	if i.tmuxSession != nil {
		if err := i.tmuxSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close tmux session: %w", err))
		}
	}

	// Then clean up git worktree
	if i.gitWorktree != nil {
		if err := i.gitWorktree.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup git worktree: %w", err))
		}
	}

	return i.combineErrors(errs)
}

// combineErrors combines multiple errors into a single error
func (i *Instance) combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple cleanup errors occurred:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return fmt.Errorf("%s", errMsg)
}

func (i *Instance) Preview() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContent()
}

func (i *Instance) HasUpdated() (updated bool, hasPrompt bool) {
	if !i.started {
		return false, false
	}
	return i.tmuxSession.HasUpdated()
}

// CheckAndHandleTrustPrompt checks for and dismisses the trust prompt for supported programs.
func (i *Instance) CheckAndHandleTrustPrompt() bool {
	if !i.started || i.tmuxSession == nil {
		return false
	}
	program := tmux.BaseProgram(i.Program)
	if program != tmux.ProgramClaude &&
		program != tmux.ProgramAider &&
		program != tmux.ProgramGemini {
		return false
	}
	return i.tmuxSession.CheckAndHandleTrustPrompt()
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
func (i *Instance) TapEnter() {
	if !i.started || !i.AutoYes {
		return
	}
	if err := i.tmuxSession.TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

func (i *Instance) Attach() (chan struct{}, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	return i.tmuxSession.Attach()
}

func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmuxSession.SetDetachedSize(width, height)
}

// GetGitWorktree returns the git worktree for the instance
func (i *Instance) GetGitWorktree() (*git.GitWorktree, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	return i.gitWorktree, nil
}

// GetWorktreePath returns the worktree path for the instance, or empty string if unavailable
func (i *Instance) GetWorktreePath() string {
	if i.gitWorktree == nil {
		return ""
	}
	return i.gitWorktree.GetWorktreePath()
}

func (i *Instance) Started() bool {
	return i.started
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.started {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.Title = title
	return nil
}

func (i *Instance) Paused() bool {
	return i.Status == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	return i.tmuxSession.DoesSessionExist()
}

// Pause stops the tmux session and removes the worktree, preserving the branch
func (i *Instance) Pause() error {
	if !i.started {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("instance is already paused")
	}

	var errs []error

	// If the worktree is orphaned (path or .git missing), git cannot operate
	// on it. Skip dirty check and Remove, prune any lingering metadata, then
	// transition to Paused so the user can recover via Resume.
	if valid, err := i.gitWorktree.IsValidWorktree(); err != nil {
		errs = append(errs, fmt.Errorf("failed to validate worktree: %w", err))
		log.ErrorLog.Print(err)
	} else if !valid {
		log.WarningLog.Printf("worktree at %s is orphaned; skipping dirty check and remove",
			i.gitWorktree.GetWorktreePath())
		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}
		// Drop any leftover directory so a future Resume's `git worktree add` won't conflict.
		if err := os.RemoveAll(i.gitWorktree.GetWorktreePath()); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove orphaned worktree directory: %w", err))
			log.ErrorLog.Print(err)
		}
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
		}
		i.SetStatus(Paused)
		_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
		return i.combineErrors(errs)
	}

	// Check if there are any changes to commit
	if dirty, err := i.gitWorktree.IsDirty(); err != nil {
		errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
		log.ErrorLog.Print(err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub)
		commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s (paused)", i.Title, time.Now().Format(time.RFC822))
		if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
			errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
			log.ErrorLog.Print(err)
			// Return early if we can't commit changes to avoid corrupted state
			return i.combineErrors(errs)
		}
	}

	// Detach from tmux session instead of closing to preserve session output
	if err := i.tmuxSession.DetachSafely(); err != nil {
		errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
		log.ErrorLog.Print(err)
		// Continue with pause process even if detach fails
	}

	// Check if worktree exists before trying to remove it
	if _, err := os.Stat(i.gitWorktree.GetWorktreePath()); err == nil {
		// Remove worktree but keep branch
		if err := i.gitWorktree.Remove(); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove git worktree: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}

		// Only prune if remove was successful
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}
	}

	i.SetStatus(Paused)
	_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())

	if err := i.combineErrors(errs); err != nil {
		log.ErrorLog.Print(err)
		return err
	}
	return nil
}

// runSetupCommand runs the configured setup command inside the worktree. Output goes
// to a per-instance log under the config dir so a failure can be diagnosed.
func (i *Instance) runSetupCommand() error {
	if i.SetupCommand == "" {
		return nil
	}
	logPath, err := RunSetupCommand(i.SetupCommand, i.gitWorktree.GetWorktreePath(), i.Title)
	if err != nil {
		return fmt.Errorf("setup command failed (log: %s): %w", logPath, err)
	}
	return nil
}

// RunSetupCommand runs command with the user's shell in dir, streaming combined
// output to a log file named after the instance. Returns the log path.
func RunSetupCommand(command, dir, title string) (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(configDir, "setup-logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", err
	}
	safeTitle := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, title)
	logPath := filepath.Join(logDir, safeTitle+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return "", err
	}
	defer logFile.Close()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	fmt.Fprintf(logFile, "$ %s\n(in %s)\n\n", command, dir)
	cmd := exec.Command(shell, "-lc", command)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Run(); err != nil {
		return logPath, err
	}
	return logPath, nil
}

// StopForArchive pauses a live instance (committing any uncommitted work to its branch
// and removing the worktree) and ends its tmux session, so nothing keeps running while
// it is archived. The branch, worktree path and Claude session id are kept, which is
// what Resume needs to continue the same conversation later.
func (i *Instance) StopForArchive() error {
	if i.Status != Paused {
		if err := i.Pause(); err != nil {
			return err
		}
	} else if err := i.removeLeftoverWorktree(); err != nil {
		return err
	}
	return i.CloseTmux()
}

// removeLeftoverWorktree handles an instance that was paused because its tmux session
// died: Pause left the worktree on disk so no work was lost. Commit anything
// uncommitted to the branch (locally, nothing is pushed) and remove the worktree, as
// Pause does for a live instance.
func (i *Instance) removeLeftoverWorktree() error {
	if i.gitWorktree == nil {
		return nil
	}
	valid, err := i.gitWorktree.IsValidWorktree()
	if err != nil {
		return fmt.Errorf("failed to validate worktree: %w", err)
	}
	if !valid {
		return nil
	}
	dirty, err := i.gitWorktree.IsDirty()
	if err != nil {
		return fmt.Errorf("failed to check if worktree is dirty: %w", err)
	}
	if dirty {
		commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s (archived)", i.Title, time.Now().Format(time.RFC822))
		if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
			return fmt.Errorf("failed to commit changes: %w", err)
		}
	}
	if err := i.gitWorktree.Remove(); err != nil {
		return fmt.Errorf("failed to remove git worktree: %w", err)
	}
	if err := i.gitWorktree.Prune(); err != nil {
		return fmt.Errorf("failed to prune git worktrees: %w", err)
	}
	return nil
}

// CloseTmux kills the tmux session (and the program in it) while leaving the
// worktree and branch alone. A later Resume starts a fresh session with
// ResumeProgram, so for ticket instances the Claude conversation carries over.
func (i *Instance) CloseTmux() error {
	if !i.started || i.tmuxSession == nil {
		return nil
	}
	if !i.tmuxSession.DoesSessionExist() {
		return nil
	}
	return i.tmuxSession.Close()
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.started {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if i.Status != Paused {
		return fmt.Errorf("can only resume paused instances")
	}

	// Check if branch is checked out
	if checked, err := i.gitWorktree.IsBranchCheckedOut(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to check if branch is checked out: %w", err)
	} else if checked {
		return fmt.Errorf("cannot resume: branch is checked out, please switch to a different branch")
	}

	// Setup git worktree. Setup removes and re-adds the worktree from the branch, which
	// throws away anything uncommitted in it. After a normal Pause the directory is gone
	// and that is exactly what we want; but an instance paused because its tmux session
	// died still has its worktree — and the work in it — sitting on disk, so leave it be.
	if valid, err := i.gitWorktree.IsValidWorktree(); err != nil || !valid {
		if err != nil {
			log.WarningLog.Printf("could not validate worktree at %s, recreating it: %v",
				i.gitWorktree.GetWorktreePath(), err)
		}
		if err := i.gitWorktree.Setup(); err != nil {
			log.ErrorLog.Print(err)
			return fmt.Errorf("failed to setup git worktree: %w", err)
		}
		if err := i.runSetupCommand(); err != nil {
			log.ErrorLog.Print(err)
			return err
		}
	}

	// A fresh tmux session for a ticket should continue the Claude Code conversation
	// rather than re-run the initial triage prompt.
	i.tmuxSession.SetProgram(i.ResumeProgram())

	// Check if tmux session still exists from pause, otherwise create new one
	if i.tmuxSession.DoesSessionExist() {
		// Session exists, just restore PTY connection to it
		if err := i.tmuxSession.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// If restore fails, fall back to creating new session
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				log.ErrorLog.Print(err)
				// Cleanup git worktree if tmux session creation fails
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
					log.ErrorLog.Print(err)
				}
				return fmt.Errorf("failed to start new session: %w", err)
			}
		}
	} else {
		// Create new tmux session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			log.ErrorLog.Print(err)
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
			return fmt.Errorf("failed to start new session: %w", err)
		}
	}

	i.SetStatus(Running)
	return nil
}

// UpdateDiffStats updates the git diff statistics for this instance
func (i *Instance) UpdateDiffStats() error {
	if !i.started {
		i.diffStats = nil
		return nil
	}

	if i.Status == Paused {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	stats := i.gitWorktree.Diff()
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			return nil
		}
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	return nil
}

// ComputeDiff runs the expensive git diff I/O and returns the result without
// mutating instance state. Safe to call from a background goroutine.
func (i *Instance) ComputeDiff() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.Diff()
}

// ComputeDiffNumstat runs a lightweight git diff --numstat and returns only the
// added/removed line counts (Content is left empty). Safe to call from a
// background goroutine. Use this for instances whose full diff content is not
// currently needed so we avoid keeping large diffs in memory.
func (i *Instance) ComputeDiffNumstat() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.DiffNumstat()
}

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// SendPrompt sends a prompt to the tmux session
func (i *Instance) SendPrompt(prompt string) error {
	if !i.started {
		return fmt.Errorf("instance not started")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if err := i.tmuxSession.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}

	// Brief pause to prevent carriage return from being interpreted as newline
	time.Sleep(100 * time.Millisecond)
	if err := i.tmuxSession.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}

	return nil
}

// ForwardScroll scrolls the program running in the session (see
// TmuxSession.ForwardScroll). Returns false if the caller should scroll tmux's
// history instead.
func (i *Instance) ForwardScroll(up bool) (bool, error) {
	if !i.started || i.Status == Paused || i.tmuxSession == nil {
		return false, nil
	}
	return i.tmuxSession.ForwardScroll(up)
}

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContentWithOptions("-", "-")
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.TmuxSession) {
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmuxSession.SendKeys(keys)
}
