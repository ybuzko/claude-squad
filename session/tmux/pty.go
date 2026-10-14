package tmux

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type PtyFactory interface {
	Start(cmd *exec.Cmd) (*os.File, error)
	Close()
}

// Pty starts a "real" pseudo-terminal (PTY) using the creack/pty package.
type Pty struct{}

func (pt Pty) Start(cmd *exec.Cmd) (*os.File, error) {
	return pty.StartWithSize(cmd, winsize(80, 24))
}

func (pt Pty) StartWithSize(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	return pty.StartWithSize(cmd, winsize(cols, rows))
}

// sizedPtyFactory is a PtyFactory that can start the PTY at a given size, so the
// program's first frame already has it.
type sizedPtyFactory interface {
	StartWithSize(cmd *exec.Cmd, cols, rows int) (*os.File, error)
}

// Nominal cell size in pixels reported with every PTY size cs sets. tmux (3.6+) sends
// the terminal CSI 18t / 14t size queries whenever the pixel size is zero, and adopts
// the reply over the PTY size. Windows Terminal answers with a stale size (its initial
// one), which then sticks to the attached session. A nonzero pixel size means tmux never
// asks; nothing here draws images, so the exact values do not matter.
const (
	cellWidthPx  = 10
	cellHeightPx = 20
)

// winsize is a PTY size of cols x rows with a nonzero pixel size (see cellWidthPx).
func winsize(cols, rows int) *pty.Winsize {
	return &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
		X:    uint16(cols * cellWidthPx),
		Y:    uint16(rows * cellHeightPx),
	}
}

func (pt Pty) Close() {}

func MakePtyFactory() PtyFactory {
	return Pty{}
}
