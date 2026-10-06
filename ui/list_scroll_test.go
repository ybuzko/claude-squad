package ui

import (
	"claude-squad/session"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newScrollList(n, width, height int) *List {
	sp := spinner.New()
	l := NewList(&sp, false)
	for i := 0; i < n; i++ {
		l.AddInstance(&session.Instance{Title: fmt.Sprintf("TSA-%d", 100+i), Status: session.Ready})
	}
	l.SetSize(width, height)
	return l
}

func renderedLines(l *List) []string {
	return strings.Split(ansi.Strip(l.String()), "\n")
}

func TestListFitsWithoutScrolling(t *testing.T) {
	l := newScrollList(3, 40, 20)
	out := ansi.Strip(l.String())
	assert.Len(t, strings.Split(out, "\n"), 20)
	assert.NotContains(t, out, "more")
	for i := 0; i < 3; i++ {
		assert.Contains(t, out, fmt.Sprintf("TSA-%d", 100+i))
	}
}

func TestListScrollsToKeepSelectionVisible(t *testing.T) {
	// 20 lines: 3 header + 2 hint lines leaves room for 7 two-line items.
	l := newScrollList(12, 40, 20)

	out := ansi.Strip(l.String())
	require.Len(t, strings.Split(out, "\n"), 20)
	assert.Contains(t, out, "TSA-100")
	assert.Contains(t, out, "TSA-106")
	assert.NotContains(t, out, "TSA-107")
	assert.NotContains(t, out, "↑")
	assert.Contains(t, out, "↓ 5 more")

	l.SetSelectedInstance(11)
	out = ansi.Strip(l.String())
	assert.Len(t, strings.Split(out, "\n"), 20)
	assert.Contains(t, out, "TSA-111")
	assert.NotContains(t, out, "TSA-104")
	assert.Contains(t, out, "↑ 5 more")
	assert.NotContains(t, out, "↓")

	// Moving back up past the window's top scrolls up by one.
	l.SetSelectedInstance(4)
	out = ansi.Strip(l.String())
	assert.Contains(t, out, "TSA-104")
	assert.Contains(t, out, "TSA-110")
	assert.Contains(t, out, "↑ 4 more")
	assert.Contains(t, out, "↓ 1 more")
}

func TestListNeverExceedsHeight(t *testing.T) {
	for _, h := range []int{1, 3, 4, 5, 6, 9} {
		l := newScrollList(10, 40, h)
		l.SetSelectedInstance(9)
		lines := renderedLines(l)
		assert.LessOrEqual(t, len(lines), h, "height %d", h)
	}
}

func TestListOffsetClampsAfterItemsRemoved(t *testing.T) {
	l := newScrollList(12, 40, 20)
	l.SetSelectedInstance(11)
	_ = l.String()
	l.items = l.items[:3]
	l.SetSelectedInstance(2)
	out := ansi.Strip(l.String())
	assert.Contains(t, out, "TSA-100")
	assert.NotContains(t, out, "more")
}
