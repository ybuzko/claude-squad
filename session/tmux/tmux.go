package tmux

import (
	"bytes"
	"claude-squad/cmd"
	"claude-squad/log"
	"claude-squad/termio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/term"
)

const ProgramClaude = "claude"

const ProgramAider = "aider"
const ProgramGemini = "gemini"

// TmuxSession represents a managed tmux session
type TmuxSession struct {
	// Initialized by NewTmuxSession
	//
	// The name of the tmux session and the sanitized name used for tmux commands.
	sanitizedName string
	program       string
	// env is exported into the tmux session (and so into program) on Start.
	env map[string]string
	// ptyFactory is used to create a PTY for the tmux session.
	ptyFactory PtyFactory
	// cmdExec is used to execute commands in the tmux session.
	cmdExec cmd.Executor

	// Initialized by Start or Restore
	//
	// ptmx is a PTY is running the tmux attach command. This can be resized to change the
	// stdout dimensions of the tmux pane. On detach, we close it and set a new one.
	// This should never be nil.
	ptmx *os.File
	// monitor monitors the tmux pane content and sends signals to the UI when it's status changes
	monitor *statusMonitor
	// detachedCols/detachedRows is the size last set with SetDetachedSize (0 = unknown);
	// the background client started on detach starts at it.
	detachedCols, detachedRows int

	// Initialized by Attach
	// Deinitilaized by Detach
	//
	// Channel to be closed at the very end of detaching. Used to signal callers.
	attachCh chan struct{}
	// undivert hands terminal input back to the TUI; set while attached.
	undivert func()
	// attachOut carries the attached client's output to the terminal until detach.
	attachOut *stoppableWriter
	// attached is true while the terminal shows this session. The TUI keeps running
	// meanwhile, so calls from it that would write to t.ptmx (now the attached client)
	// are refused or deferred; mu guards ptmx and attached against Detach, which runs
	// on the input goroutine.
	attached bool
	mu       sync.Mutex
	// While attached, we use some goroutines to manage the window size and stdin/stdout. This stuff
	// is used to terminate them on Detach. We don't want them to outlive the attached window.
	ctx    context.Context
	cancel func()
	wg     *sync.WaitGroup
}

const TmuxPrefix = "claudesquad_"

// ErrSessionNotFound is returned when the tmux session backing an instance is gone, which
// happens whenever the tmux server dies (reboot, crash, `tmux kill-server`).
var ErrSessionNotFound = errors.New("tmux session no longer exists")

var whiteSpaceRegex = regexp.MustCompile(`\s+`)

func toClaudeSquadTmuxName(str string) string {
	str = whiteSpaceRegex.ReplaceAllString(str, "")
	str = strings.ReplaceAll(str, ".", "_") // tmux replaces all . with _
	return fmt.Sprintf("%s%s", TmuxPrefix, str)
}

// NewTmuxSession creates a new TmuxSession with the given name and program.
func NewTmuxSession(name string, program string) *TmuxSession {
	return newTmuxSession(name, program, MakePtyFactory(), cmd.MakeExecutor())
}

// NewTmuxSessionWithDeps creates a new TmuxSession with provided dependencies for testing.
func NewTmuxSessionWithDeps(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *TmuxSession {
	return newTmuxSession(name, program, ptyFactory, cmdExec)
}

func newTmuxSession(name string, program string, ptyFactory PtyFactory, cmdExec cmd.Executor) *TmuxSession {
	return &TmuxSession{
		sanitizedName: toClaudeSquadTmuxName(name),
		program:       program,
		ptyFactory:    ptyFactory,
		cmdExec:       cmdExec,
	}
}

// SetEnv sets environment variables exported into the tmux session on Start.
func (t *TmuxSession) SetEnv(env map[string]string) {
	t.env = env
}

// SetProgram changes the command run by the next Start. It has no effect on a
// session that is already running.
func (t *TmuxSession) SetProgram(program string) {
	t.program = program
}

// BaseProgram returns the bare executable name of a program string such as
// "/usr/local/bin/claude \"/triage X\"" → "claude".
func BaseProgram(program string) string {
	fields := strings.Fields(program)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// Start creates and starts a new tmux session, then attaches to it. Program is the command to run in
// the session (ex. claude). workdir is the git worktree directory.
func (t *TmuxSession) Start(workDir string) error {
	// Check if the session already exists
	if t.DoesSessionExist() {
		return fmt.Errorf("tmux session already exists: %s", t.sanitizedName)
	}

	// Create a new detached tmux session and start claude in it
	args := []string{"new-session", "-d", "-s", t.sanitizedName, "-c", workDir}
	envKeys := make([]string, 0, len(t.env))
	for k := range t.env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, t.env[k]))
	}
	args = append(args, t.program)
	cmd := exec.Command("tmux", args...)

	ptmx, err := t.ptyFactory.Start(cmd)
	if err != nil {
		// Cleanup any partially created session if any exists.
		if t.DoesSessionExist() {
			cleanupCmd := exec.Command("tmux", "kill-session", "-t", t.sanitizedName)
			if cleanupErr := t.cmdExec.Run(cleanupCmd); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
		}
		return fmt.Errorf("error starting tmux session: %w", err)
	}

	// Poll for session existence with exponential backoff
	timeout := time.After(2 * time.Second)
	sleepDuration := 5 * time.Millisecond
	for !t.DoesSessionExist() {
		select {
		case <-timeout:
			if cleanupErr := t.Close(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
			return fmt.Errorf("timed out waiting for tmux session %s: %v", t.sanitizedName, err)
		default:
			time.Sleep(sleepDuration)
			// Exponential backoff up to 50ms max
			if sleepDuration < 50*time.Millisecond {
				sleepDuration *= 2
			}
		}
	}
	ptmx.Close()

	// Set history limit to enable scrollback (default is 2000, we'll use 10000 for more history)
	historyCmd := exec.Command("tmux", "set-option", "-t", t.sanitizedName, "history-limit", "10000")
	if err := t.cmdExec.Run(historyCmd); err != nil {
		log.InfoLog.Printf("Warning: failed to set history-limit for session %s: %v", t.sanitizedName, err)
	}

	// Enable mouse scrolling for the session
	mouseCmd := exec.Command("tmux", "set-option", "-t", t.sanitizedName, "mouse", "on")
	if err := t.cmdExec.Run(mouseCmd); err != nil {
		log.InfoLog.Printf("Warning: failed to enable mouse scrolling for session %s: %v", t.sanitizedName, err)
	}

	err = t.Restore()
	if err != nil {
		if cleanupErr := t.Close(); cleanupErr != nil {
			err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
		}
		return fmt.Errorf("error restoring tmux session: %w", err)
	}

	return nil
}

// CheckAndHandleTrustPrompt checks the pane content once for a trust prompt and dismisses it if found.
// Returns true if the prompt was found and handled.
func (t *TmuxSession) CheckAndHandleTrustPrompt() bool {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false
	}

	if BaseProgram(t.program) == ProgramClaude {
		return t.answerClaudeTrustPrompt(content)
	} else {
		if strings.Contains(content, "Open documentation url for more info") {
			if err := t.TapDAndEnter(); err != nil {
				log.ErrorLog.Printf("could not tap enter on trust screen: %v", err)
			}
			return true
		}
	}
	return false
}

// Claude Code's folder-trust dialog. Current versions ask "Is this a project you
// created or one you trust?" with "No, exit" highlighted by default, so a bare Enter
// would quit; older versions asked trustPromptLegacy with "Yes" as the default.
const (
	trustPromptAccept = "Yes, I trust this folder"
	trustPromptLegacy = "Do you trust the files in this folder?"
	mcpServerPrompt   = "new MCP server"
	selectCursor      = "❯"
)

type trustAction int

const (
	trustNone  trustAction = iota // no trust dialog on screen
	trustEnter                    // the accepting option is selected: confirm it
	trustDown                     // another option is selected: move down first
)

// trustPromptAction decides how to answer the trust dialog in a captured pane.
func trustPromptAction(pane string) trustAction {
	pane = ansi.Strip(pane)
	if strings.Contains(pane, trustPromptAccept) {
		for _, line := range strings.Split(pane, "\n") {
			if strings.Contains(line, selectCursor) {
				if strings.Contains(line, trustPromptAccept) {
					return trustEnter
				}
				return trustDown
			}
		}
		return trustDown
	}
	if strings.Contains(pane, trustPromptLegacy) || strings.Contains(pane, mcpServerPrompt) {
		return trustEnter
	}
	return trustNone
}

// answerClaudeTrustPrompt accepts Claude Code's trust dialog if one is showing,
// moving the selection onto the accepting option before confirming. It re-reads the
// pane after every move so it never confirms "No, exit". Returns true if a dialog
// was found.
func (t *TmuxSession) answerClaudeTrustPrompt(content string) bool {
	found := false
	for step := 0; step < 4; step++ {
		switch trustPromptAction(content) {
		case trustNone:
			return found
		case trustEnter:
			if err := t.sendKey("Enter"); err != nil {
				log.ErrorLog.Printf("could not confirm trust dialog for %s: %v", t.sanitizedName, err)
			}
			return true
		case trustDown:
			found = true
			if err := t.sendKey("Down"); err != nil {
				log.ErrorLog.Printf("could not move selection in trust dialog for %s: %v", t.sanitizedName, err)
				return true
			}
			time.Sleep(200 * time.Millisecond)
			var err error
			if content, err = t.CapturePaneContent(); err != nil {
				return true
			}
		}
	}
	log.WarningLog.Printf("trust dialog for %s: accepting option never got selected", t.sanitizedName)
	return true
}

// sendKey sends one named key (tmux send-keys syntax) to the session's pane.
func (t *TmuxSession) sendKey(key string) error {
	return t.cmdExec.Run(exec.Command("tmux", "send-keys", "-t", t.sanitizedName, key))
}

// ResetTerminalModes undoes what an attached tmux client may have changed in the
// terminal. Detaching closes the client's pty, so tmux never gets to restore them:
//   - ESC [r      full-screen scroll region (DECSTBM). A leftover region from attaching
//     at a smaller size squeezes every frame into those rows; ConPTY keeps it across
//     resizes.
//   - ESC [?6l    origin mode off; ESC [?7h auto-wrap on; ESC [4l insert mode off
//   - ESC (B      ASCII character set
//   - ESC [?1l    normal cursor keys (DECCKM). In application mode Windows Terminal
//     sends Home/End as ESC O H / ESC O F, which bubbletea does not decode.
//   - ESC >       numeric keypad (DECKPNM)
//
// Resetting the scroll region and origin mode moves the cursor home.
const ResetTerminalModes = "\x1b[r\x1b[?6l\x1b[?7h\x1b[4l\x1b(B\x1b[?1l\x1b>"

// wheelLinesPerNotch is how many wheel events one scroll step sends to the program;
// terminals usually scroll about three lines per notch.
const wheelLinesPerNotch = 3

// ForwardScroll scrolls the program inside the pane rather than tmux's scrollback.
// Full-screen programs such as Claude Code draw on the alternate screen, which has no
// tmux history, so they have to scroll their own view. When the program tracks the
// mouse in SGR mode it gets wheel events; on the alternate screen without mouse
// tracking it gets PageUp/PageDown. Returns false when neither applies, and the caller
// falls back to scrolling tmux's history.
func (t *TmuxSession) ForwardScroll(up bool) (bool, error) {
	out, err := t.cmdExec.Output(exec.Command("tmux", "display-message", "-p", "-t", t.sanitizedName,
		"#{alternate_on} #{mouse_sgr_flag} #{mouse_any_flag} #{mouse_button_flag} #{mouse_standard_flag} #{pane_width} #{pane_height}"))
	// Without the pane's modes, fall back to tmux history rather than failing the scroll.
	if err != nil {
		log.WarningLog.Printf("could not query pane modes for %s: %v", t.sanitizedName, err)
		return false, nil
	}
	f := strings.Fields(string(out))
	if len(f) != 7 {
		log.WarningLog.Printf("unexpected pane mode output for %s: %q", t.sanitizedName, out)
		return false, nil
	}
	alt, sgr := f[0] == "1", f[1] == "1"
	tracking := f[2] == "1" || f[3] == "1" || f[4] == "1"
	switch {
	case sgr && tracking:
		width, _ := strconv.Atoi(f[5])
		height, _ := strconv.Atoi(f[6])
		button := 65 // wheel down
		if up {
			button = 64
		}
		event := fmt.Sprintf("\x1b[<%d;%d;%dM", button, max(width/2, 1), max(height/2, 1))
		for i := 0; i < wheelLinesPerNotch; i++ {
			if err := t.sendHex(event); err != nil {
				return true, err
			}
		}
		return true, nil
	case alt:
		key := "PageDown"
		if up {
			key = "PageUp"
		}
		return true, t.sendKey(key)
	}
	return false, nil
}

// sendHex sends raw bytes to the pane as input (tmux send-keys -H).
func (t *TmuxSession) sendHex(raw string) error {
	args := []string{"send-keys", "-t", t.sanitizedName, "-H"}
	for _, b := range []byte(raw) {
		args = append(args, fmt.Sprintf("%02x", b))
	}
	return t.cmdExec.Run(exec.Command("tmux", args...))
}

// Restore attaches to an existing session and restores the window size
func (t *TmuxSession) Restore() error {
	// attach-session against a missing session still forks a process successfully, so the
	// PTY start below would report no error while leaving us attached to nothing. Check
	// first so callers can tell "session is gone" apart from "PTY failed".
	if !t.DoesSessionExist() {
		return ErrSessionNotFound
	}

	ptmx, err := t.startClient(t.detachedCols, t.detachedRows)
	if err != nil {
		return err
	}
	t.ptmx = ptmx
	t.monitor = newStatusMonitor()
	return nil
}

// startClient starts a tmux client attached to the session in a new PTY of cols x rows
// (the factory's default size when 0).
func (t *TmuxSession) startClient(cols, rows int) (*os.File, error) {
	attach := exec.Command("tmux", "attach-session", "-t", t.sanitizedName)
	var ptmx *os.File
	var err error
	if f, ok := t.ptyFactory.(sizedPtyFactory); ok && cols > 0 && rows > 0 {
		ptmx, err = f.StartWithSize(attach, cols, rows)
	} else {
		ptmx, err = t.ptyFactory.Start(attach)
	}
	if err != nil {
		return nil, fmt.Errorf("error opening PTY: %w", err)
	}
	return ptmx, nil
}

// freshClient replaces the background client with a new one of cols x rows. Nothing
// reads the background client's output while cs shows its overview, so tens of KB of
// frames drawn for the preview size pile up in its PTY; copying that backlog to the
// terminal on attach left a stale picture until a resize forced a full redraw. A new
// client draws its first frame for the real terminal, as a plain `tmux attach` does.
func (t *TmuxSession) freshClient(cols, rows int) error {
	if !t.DoesSessionExist() {
		return ErrSessionNotFound
	}
	ptmx, err := t.startClient(cols, rows)
	if err != nil {
		return err
	}
	if t.ptmx != nil {
		_ = t.ptmx.Close()
	}
	t.ptmx = ptmx
	return nil
}

type statusMonitor struct {
	// Store hashes to save memory.
	prevOutputHash []byte
}

func newStatusMonitor() *statusMonitor {
	return &statusMonitor{}
}

// hash hashes the string.
func (m *statusMonitor) hash(s string) []byte {
	h := sha256.New()
	// TODO: this allocation sucks since the string is probably large. Ideally, we hash the string directly.
	h.Write([]byte(s))
	return h.Sum(nil)
}

// ErrAttached is returned for input sent by cs to a session the user has open.
var ErrAttached = errors.New("session is open in the terminal")

// Attached reports whether the terminal currently shows this session.
func (t *TmuxSession) Attached() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attached
}

// writeInput types into the session through the background client.
func (t *TmuxSession) writeInput(b []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attached {
		return ErrAttached
	}
	if t.ptmx == nil {
		return ErrSessionNotFound
	}
	_, err := t.ptmx.Write(b)
	return err
}

// TapEnter sends an enter keystroke to the tmux pane.
func (t *TmuxSession) TapEnter() error {
	if err := t.writeInput([]byte{0x0D}); err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

// TapDAndEnter sends 'D' followed by an enter keystroke to the tmux pane.
func (t *TmuxSession) TapDAndEnter() error {
	if err := t.writeInput([]byte{0x44, 0x0D}); err != nil {
		return fmt.Errorf("error sending enter keystroke to PTY: %w", err)
	}
	return nil
}

func (t *TmuxSession) SendKeys(keys string) error {
	return t.writeInput([]byte(keys))
}

// HasUpdated checks if the tmux pane content has changed since the last tick. It also returns true if
// the tmux pane has a prompt for aider or claude code.
func (t *TmuxSession) HasUpdated() (updated bool, hasPrompt bool) {
	content, err := t.CapturePaneContent()
	if err != nil {
		log.ErrorLog.Printf("error capturing pane content in status monitor: %v", err)
		return false, false
	}

	// Only set hasPrompt for claude and aider. Use these strings to check for a prompt.
	if t.program == ProgramClaude {
		hasPrompt = strings.Contains(content, "No, and tell Claude what to do differently")
	} else if strings.HasPrefix(t.program, ProgramAider) {
		hasPrompt = strings.Contains(content, "(Y)es/(N)o/(D)on't ask again")
	} else if strings.HasPrefix(t.program, ProgramGemini) {
		hasPrompt = strings.Contains(content, "Yes, allow once")
	}

	if !bytes.Equal(t.monitor.hash(content), t.monitor.prevOutputHash) {
		t.monitor.prevOutputHash = t.monitor.hash(content)
		return true, hasPrompt
	}
	return false, hasPrompt
}

func (t *TmuxSession) Attach() (chan struct{}, error) {
	// From here on the TUI must not type into or resize t.ptmx: it is the client the
	// terminal shows. Calls made meanwhile see attached and back off.
	t.mu.Lock()
	if cols, rows, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		if err := t.freshClient(cols, rows); err != nil {
			t.mu.Unlock()
			return nil, err
		}
	}
	t.attached = true
	ptmx := t.ptmx
	t.mu.Unlock()

	t.attachCh = make(chan struct{})
	t.wg = &sync.WaitGroup{}
	t.ctx, t.cancel = context.WithCancel(context.Background())

	// The terminal answers the queries a new tmux client sends (device attributes,
	// colours: "[?62c", "]10;rgb:f8f8f8", ...), and the answers arrive as input right
	// after attaching. Anything within the first 50ms is taken to be such an answer
	// and dropped rather than typed into the session.
	start := time.Now()
	undivert, err := termio.Divert(func(b []byte) {
		if time.Since(start) < 50*time.Millisecond {
			log.InfoLog.Printf("nuked first stdin: %s", b)
			return
		}
		// Ctrl+Q leaves the session.
		if len(b) == 1 && b[0] == 17 {
			t.Detach()
			return
		}
		_, _ = ptmx.Write(b)
	})
	if err != nil {
		t.cancel()
		t.cancel, t.ctx, t.wg = nil, nil, nil
		t.attachCh = nil
		t.mu.Lock()
		t.attached = false
		t.mu.Unlock()
		return nil, err
	}
	t.mu.Lock()
	t.undivert = undivert
	t.mu.Unlock()

	// The output copier ends some time after the ptmx is closed: a read blocked on a
	// closed PTY can take seconds to return. Detach does not wait for it; it stops the
	// copier's writer instead, so nothing the old client prints lands on the TUI.
	// Input reaches the session through the divert function above.
	ctx := t.ctx
	out := &stoppableWriter{w: newAltScreenFilter(os.Stdout)}
	t.attachOut = out
	go func() {
		_, _ = io.Copy(out, ptmx)
		// When io.Copy returns, it means the connection was closed
		// This could be due to normal detach or Ctrl-D
		// Check if the context is done to determine if it was a normal detach
		select {
		case <-ctx.Done():
			// Normal detach, do nothing
		default:
			// If context is not done, it was likely an abnormal termination (Ctrl-D)
			// Print warning message
			fmt.Fprintf(os.Stderr, "\n\033[31mError: Session terminated without detaching. Use Ctrl-Q to properly detach from tmux sessions.\033[0m\n")
		}
	}()

	t.monitorWindowSize()
	return t.attachCh, nil
}

// DetachSafely disconnects from the current tmux session without panicking
func (t *TmuxSession) DetachSafely() error {
	// Only detach if we're actually attached
	if t.attachCh == nil {
		return nil // Already detached
	}

	var errs []error

	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.attachOut != nil {
		t.attachOut.stop()
	}

	// Close the attached pty session.
	t.mu.Lock()
	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing attach pty session: %w", err))
		}
		t.ptmx = nil
	}
	t.mu.Unlock()

	if t.wg != nil {
		t.wg.Wait()
		t.wg = nil
	}

	t.ctx = nil
	t.endAttach()

	if len(errs) > 0 {
		return fmt.Errorf("errors during detach: %v", errs)
	}
	return nil
}

// Detach disconnects from the current tmux session and reconnects the background
// client. It runs on the terminal input goroutine (Ctrl+Q) while the TUI keeps going.
func (t *TmuxSession) Detach() {
	// Cancel first, so the output copier sees a normal detach when its PTY closes.
	t.cancel()

	t.attachOut.stop()
	t.mu.Lock()
	err := t.ptmx.Close()
	t.mu.Unlock()
	if err != nil {
		// This is a fatal error. We can't detach if we can't close the PTY. It's better to just panic and have the
		// user re-invoke the program than to ruin their terminal pane.
		msg := fmt.Sprintf("error closing attach pty session: %v", err)
		log.ErrorLog.Println(msg)
		panic(msg)
	}
	t.wg.Wait()
	// The attached tmux client changed terminal modes (scroll region, cursor keys, ...)
	// and closing it abruptly leaves them set. Restore them.
	_, _ = os.Stdout.WriteString(ResetTerminalModes)

	t.mu.Lock()
	if err := t.Restore(); err != nil {
		// The session ended while open (the program exited). There is no background
		// client to go back to; the instance notices the session is gone.
		log.ErrorLog.Printf("could not reconnect to %s after detaching: %v", t.sanitizedName, err)
		t.ptmx = nil
	}
	t.mu.Unlock()

	t.cancel = nil
	t.ctx = nil
	t.wg = nil
	t.endAttach()
}

// stoppableWriter passes writes through until stop; after that it drops them.
type stoppableWriter struct {
	mu      sync.Mutex
	w       io.Writer
	stopped bool
}

func (s *stoppableWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return len(p), nil
	}
	return s.w.Write(p)
}

// stop waits for a write in progress, then drops the rest.
func (s *stoppableWriter) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

// endAttach hands the terminal back to the TUI and signals the caller of Attach.
func (t *TmuxSession) endAttach() {
	t.attachOut = nil
	t.mu.Lock()
	t.attached = false
	undivert := t.undivert
	t.undivert = nil
	t.mu.Unlock()
	if undivert != nil {
		undivert()
	}
	if t.attachCh != nil {
		close(t.attachCh)
		t.attachCh = nil
	}
}

// Close terminates the tmux session and cleans up resources
func (t *TmuxSession) Close() error {
	var errs []error

	if t.ptmx != nil {
		if err := t.ptmx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("error closing PTY: %w", err))
		}
		t.ptmx = nil
	}

	// The session may already be gone (closed on pause, or the server died); there
	// is nothing to kill then and reporting a failure would mask the real cleanup.
	if t.DoesSessionExist() {
		cmd := exec.Command("tmux", "kill-session", "-t", t.sanitizedName)
		if err := t.cmdExec.Run(cmd); err != nil {
			errs = append(errs, fmt.Errorf("error killing tmux session: %w", err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple errors occurred during cleanup:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return errors.New(errMsg)
}

// SetDetachedSize set the width and height of the session while detached. This makes the
// tmux output conform to the specified shape. While the session is open in the
// terminal the size is only recorded; the background client starts at it on detach.
func (t *TmuxSession) SetDetachedSize(width, height int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.detachedCols, t.detachedRows = width, height
	if t.attached {
		return nil
	}
	return t.setPtySize(width, height)
}

// updateWindowSize updates the window size of the PTY.
func (t *TmuxSession) updateWindowSize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.setPtySize(cols, rows)
}

func (t *TmuxSession) setPtySize(cols, rows int) error {
	if t.ptmx == nil {
		return ErrSessionNotFound
	}
	return pty.Setsize(t.ptmx, winsize(cols, rows))
}

func (t *TmuxSession) DoesSessionExist() bool {
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := exec.Command("tmux", "has-session", fmt.Sprintf("-t=%s", t.sanitizedName))
	return t.cmdExec.Run(existsCmd) == nil
}

// CapturePaneContent captures the content of the tmux pane
func (t *TmuxSession) CapturePaneContent() (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-t", t.sanitizedName)
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("error capturing pane content: %v", err)
	}
	return string(output), nil
}

// CapturePaneContentWithOptions captures the pane content with additional options
// start and end specify the starting and ending line numbers (use "-" for the start/end of history)
func (t *TmuxSession) CapturePaneContentWithOptions(start, end string) (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-S", start, "-E", end, "-t", t.sanitizedName)
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane content with options: %v", err)
	}
	return string(output), nil
}

// CleanupSessions kills all tmux sessions that start with "session-"
func CleanupSessions(cmdExec cmd.Executor) error {
	// First try to list sessions
	cmd := exec.Command("tmux", "ls")
	output, err := cmdExec.Output(cmd)

	// If there's an error and it's because no server is running, that's fine
	// Exit code 1 typically means no sessions exist
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil // No sessions to clean up
		}
		return fmt.Errorf("failed to list tmux sessions: %v", err)
	}

	re := regexp.MustCompile(fmt.Sprintf(`%s.*:`, TmuxPrefix))
	matches := re.FindAllString(string(output), -1)
	for i, match := range matches {
		matches[i] = match[:strings.Index(match, ":")]
	}

	for _, match := range matches {
		log.InfoLog.Printf("cleaning up session: %s", match)
		if err := cmdExec.Run(exec.Command("tmux", "kill-session", "-t", match)); err != nil {
			return fmt.Errorf("failed to kill tmux session %s: %v", match, err)
		}
	}
	return nil
}
