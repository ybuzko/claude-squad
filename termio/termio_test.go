package termio

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer safe for the router goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRouterSendsInputToTheTUIUnlessDiverted(t *testing.T) {
	src, keys := io.Pipe()
	tui := &syncBuffer{}
	outFile, err := os.CreateTemp(t.TempDir(), "out")
	require.NoError(t, err)
	out := &Output{f: outFile}
	r := newRouter(src, tui, out)
	go r.run()
	defer keys.Close()

	send := func(s string) {
		_, err := keys.Write([]byte(s))
		require.NoError(t, err)
	}

	send("j")
	assert.Eventually(t, func() bool { return tui.String() == "j" }, time.Second, time.Millisecond)

	session := &syncBuffer{}
	r.divert(func(b []byte) { _, _ = session.Write(b) })
	send("c")
	assert.Eventually(t, func() bool { return session.String() == "c" }, time.Second, time.Millisecond)
	assert.Equal(t, "j", tui.String(), "input for the session never reaches the TUI")

	// The TUI's frames are dropped while the session is open.
	_, err = out.Write([]byte("frame"))
	require.NoError(t, err)

	r.undivert()
	send("k")
	assert.Eventually(t, func() bool { return tui.String() == "jk" }, time.Second, time.Millisecond)
	_, err = out.Write([]byte("next"))
	require.NoError(t, err)

	written, err := os.ReadFile(outFile.Name())
	require.NoError(t, err)
	assert.Equal(t, "next", string(written))
}

func TestDivertWithoutStart(t *testing.T) {
	_, err := Divert(func([]byte) {})
	assert.ErrorIs(t, err, ErrNotStarted)
}
