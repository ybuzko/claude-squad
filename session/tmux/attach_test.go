package tmux

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// While a session is open in the terminal its ptmx is the client the user sees; the
// TUI, still running, must not type into it or resize it.
func TestOpenSessionRefusesInputAndDefersResize(t *testing.T) {
	s := newTmuxSession("open", "bash", nil, nil)
	s.attached = true

	assert.ErrorIs(t, s.SendKeys("hello"), ErrAttached)
	assert.Error(t, s.TapEnter())

	require.NoError(t, s.SetDetachedSize(80, 24))
	assert.Equal(t, 80, s.detachedCols, "the size is kept for the client started on detach")
	assert.Equal(t, 24, s.detachedRows)
}

func TestStoppableWriterDropsWritesAfterStop(t *testing.T) {
	var out bytes.Buffer
	w := &stoppableWriter{w: &out}
	_, _ = w.Write([]byte("frame"))
	w.stop()
	n, err := w.Write([]byte("late"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "frame", out.String())
}
