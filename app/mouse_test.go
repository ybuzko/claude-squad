package app

import (
	"claude-squad/session"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestWheelScrollsThePaneUnderThePointer(t *testing.T) {
	h := newPollHome(t)
	for i := 0; i < 30; i++ {
		h.list.AddInstance(ticket(fmt.Sprintf("TSA-%d", 100+i), session.Paused, true))
	}
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 40})
	_ = h.View()
	wheel := func(x, y int) {
		h.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	}

	// Over the list: the window scrolls, the selection stays.
	wheel(5, 10)
	wheel(5, 10)
	assert.Equal(t, "TSA-100", h.list.GetSelectedInstance().Title)
	assert.Contains(t, ansi.Strip(h.list.String()), "↑ 2 more")

	// Over the preview or the menu rows: the list does not move.
	wheel(h.listWidth+5, 10)
	wheel(5, h.contentHeight+2)
	assert.Contains(t, ansi.Strip(h.list.String()), "↑ 2 more")
}
