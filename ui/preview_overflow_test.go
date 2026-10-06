package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// After the terminal shrinks, the tmux pane still holds output laid out for the old,
// wider size until Claude redraws, and capture-pane -J joins wrapped lines. Such lines
// must not wrap inside the preview: every extra line pushes the frame past the
// terminal height and bubbletea drops lines from the top, so the whole UI jumps.
func TestPreviewNeverExceedsItsSizeWithWideLines(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(50, 20)
	wide := strings.Repeat("x", 130)
	lines := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		lines = append(lines, "\x1b[32m"+wide+"\x1b[0m")
	}
	p.previewState = previewState{text: strings.Join(lines, "\n")}

	out := strings.Split(p.String(), "\n")
	assert.LessOrEqual(t, len(out), 20, "height")
	for i, l := range out {
		assert.LessOrEqual(t, ansi.StringWidth(l), 50+previewPaneStyle.GetHorizontalFrameSize(), "line %d width", i)
	}
}
