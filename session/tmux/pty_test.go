package tmux

import (
	"os/exec"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tmux asks the terminal for its size (and believes the answer) whenever the PTY has no
// pixel size, so every PTY cs creates or resizes must carry one.
func TestPtySizesIncludePixels(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	ptmx, err := Pty{}.Start(cmd)
	require.NoError(t, err)
	defer func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
	}()

	ws, err := pty.GetsizeFull(ptmx)
	require.NoError(t, err)
	assert.NotZero(t, ws.X)
	assert.NotZero(t, ws.Y)

	ts := &TmuxSession{ptmx: ptmx}
	require.NoError(t, ts.updateWindowSize(211, 38))
	ws, err = pty.GetsizeFull(ptmx)
	require.NoError(t, err)
	assert.Equal(t, pty.Winsize{Cols: 211, Rows: 38, X: 211 * cellWidthPx, Y: 38 * cellHeightPx}, *ws)
}
