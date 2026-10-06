package tmux

import (
	cmd2 "claude-squad/cmd/cmd_test"
	"claude-squad/log"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize(false)
	code := m.Run()
	log.Close()
	os.Exit(code)
}

func scrollSession(modes string) (*TmuxSession, *[]string) {
	var sent []string
	exec := cmd2.MockCmdExec{
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte(modes + "\n"), nil },
		RunFunc: func(c *exec.Cmd) error {
			sent = append(sent, strings.Join(c.Args[1:], " "))
			return nil
		},
	}
	return newTmuxSession("TSA-1", "claude", nil, exec), &sent
}

func TestForwardScrollSendsWheelEventsToMouseTrackingPrograms(t *testing.T) {
	// alternate_on mouse_sgr mouse_any mouse_button mouse_standard width height
	s, sent := scrollSession("1 1 1 0 0 100 30")
	handled, err := s.ForwardScroll(true)
	require.NoError(t, err)
	assert.True(t, handled)
	require.Len(t, *sent, wheelLinesPerNotch)
	// ESC [ < 6 4 ; 5 0 ; 1 5 M  (wheel up at the pane's centre)
	assert.Equal(t, "send-keys -t claudesquad_TSA-1 -H 1b 5b 3c 36 34 3b 35 30 3b 31 35 4d", (*sent)[0])

	s, sent = scrollSession("1 1 1 0 0 100 30")
	_, _ = s.ForwardScroll(false)
	assert.Contains(t, (*sent)[0], "-H 1b 5b 3c 36 35 3b", "wheel down is button 65")
}

func TestForwardScrollUsesPageKeysOnAlternateScreenWithoutMouse(t *testing.T) {
	s, sent := scrollSession("1 0 0 0 0 100 30")
	handled, err := s.ForwardScroll(true)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, []string{"send-keys -t claudesquad_TSA-1 PageUp"}, *sent)
}

func TestForwardScrollLeavesOrdinaryPanesToTmuxHistory(t *testing.T) {
	s, sent := scrollSession("0 0 0 0 0 100 30")
	handled, err := s.ForwardScroll(true)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.Empty(t, *sent)
}

func TestForwardScrollFallsBackWhenModesAreUnknown(t *testing.T) {
	s, sent := scrollSession("")
	handled, err := s.ForwardScroll(true)
	require.NoError(t, err)
	assert.False(t, handled)
	assert.Empty(t, *sent)
}
