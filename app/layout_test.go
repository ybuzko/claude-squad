package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// The frame must fit the terminal exactly: bubbletea drops lines from the top of a
// frame that is too tall, which hides the header and makes the UI jump on every redraw.
func TestViewFitsTerminalAtAnySize(t *testing.T) {
	h := newPollHome(t)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {163, 41}, {200, 60}, {100, 30}, {60, 20}} {
		h.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		// render() is the unclipped frame: the layout itself must fit, not just the clip.
		lines := strings.Split(h.render(), "\n")
		assert.LessOrEqual(t, len(lines), size[1], "%dx%d: height", size[0], size[1])
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide", size[0], size[1], i, w)
				break
			}
		}
	}
}

func TestClipFrame(t *testing.T) {
	frame := "top\n" + strings.Repeat("y", 30) + "\nmid\nbottom"
	assert.Equal(t, "top\n"+strings.Repeat("y", 10)+"\nmid", clipFrame(frame, 10, 3))
	assert.Equal(t, frame, clipFrame(frame, 0, 0), "no size yet: untouched")
}

func TestResizeBurstResizesSessionsOnce(t *testing.T) {
	h := newPollHome(t)
	var cmds []tea.Cmd
	for _, w := range []int{100, 110, 120, 130} {
		_, cmd := h.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		cmds = append(cmds, cmd)
	}
	// Only the last event's timer is still current.
	stale := resizeSessionsMsg{seq: 1}
	assert.NotEqual(t, h.resizeSeq, stale.seq)
	assert.Equal(t, 4, h.resizeSeq)
	assert.Len(t, cmds, 4)
}

func TestFrameLeavesOneSpareColumn(t *testing.T) {
	h := newPollHome(t)
	h.Update(tea.WindowSizeMsg{Width: 156, Height: 47})
	for i, l := range strings.Split(h.render(), "\n") {
		if w := ansi.StringWidth(l); w > 155 {
			t.Fatalf("line %d is %d wide; a terminal-wide line wraps on any wide glyph", i, w)
		}
	}
}
