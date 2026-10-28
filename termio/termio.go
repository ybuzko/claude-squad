// Package termio owns the terminal's input and the TUI's output, so a tmux session can
// be opened while the TUI keeps running behind it.
//
// Bubble Tea normally reads stdin itself. Opening a session used to block its event
// loop until the session was left, which froze polling and spawning, and the reader
// still took one chunk of whatever was typed: it was lost to the session and replayed
// to the list as a keypress after leaving. Here one goroutine reads the terminal and
// hands each chunk either to the TUI (through a pipe given to tea.WithInput) or, while
// a session is open, to that session. While it is open the TUI's output is dropped,
// so the event loop can keep running without drawing over the session.
package termio

import (
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

// ErrNotStarted is returned by Divert when Start has not been called.
var ErrNotStarted = errors.New("terminal input router not started")

var (
	activeMu sync.Mutex
	active   *router
)

// Start puts the terminal in raw mode and starts routing its input. input and output
// are for tea.WithInput and tea.WithOutput; stop restores the terminal.
func Start() (input io.Reader, output *Output, stop func(), err error) {
	src := os.Stdin
	if !term.IsTerminal(int(src.Fd())) {
		if src, err = os.Open("/dev/tty"); err != nil {
			return nil, nil, nil, err
		}
	}
	prev, err := term.MakeRaw(int(src.Fd()))
	if err != nil {
		return nil, nil, nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = term.Restore(int(src.Fd()), prev)
		return nil, nil, nil, err
	}
	out := &Output{f: os.Stdout}
	r := newRouter(src, pw, out)
	go r.run()

	activeMu.Lock()
	active = r
	activeMu.Unlock()
	return pr, out, func() {
		activeMu.Lock()
		active = nil
		activeMu.Unlock()
		_ = term.Restore(int(src.Fd()), prev)
	}, nil
}

// Divert sends all terminal input to fn and drops the TUI's output until restore is
// called. fn runs on the reading goroutine and must not keep the slice.
func Divert(fn func([]byte)) (restore func(), err error) {
	activeMu.Lock()
	r := active
	activeMu.Unlock()
	if r == nil {
		return nil, ErrNotStarted
	}
	r.divert(fn)
	return r.undivert, nil
}

type router struct {
	src io.Reader
	tui io.Writer
	out *Output

	mu sync.Mutex
	to func([]byte)
}

func newRouter(src io.Reader, tui io.Writer, out *Output) *router {
	return &router{src: src, tui: tui, out: out}
}

func (r *router) run() {
	buf := make([]byte, 1024)
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			r.mu.Lock()
			to := r.to
			r.mu.Unlock()
			if to != nil {
				to(buf[:n])
			} else if _, werr := r.tui.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *router) divert(fn func([]byte)) {
	r.mu.Lock()
	r.to = fn
	r.mu.Unlock()
	r.out.setDropping(true)
}

func (r *router) undivert() {
	r.mu.Lock()
	r.to = nil
	r.mu.Unlock()
	r.out.setDropping(false)
}

// Output is the TUI's stdout. It drops writes while a session is open. It keeps the
// file's descriptor, so Bubble Tea still sees a terminal for sizing and resize signals.
type Output struct {
	f *os.File

	mu       sync.Mutex
	dropping bool
}

func (o *Output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.dropping {
		return len(p), nil
	}
	return o.f.Write(p)
}

// setDropping waits for a write in progress, so a frame is never cut in half.
func (o *Output) setDropping(d bool) {
	o.mu.Lock()
	o.dropping = d
	o.mu.Unlock()
}

func (o *Output) Read(p []byte) (int, error) { return o.f.Read(p) }
func (o *Output) Close() error               { return nil }
func (o *Output) Fd() uintptr                { return o.f.Fd() }
