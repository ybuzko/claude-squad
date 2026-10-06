package ui

import (
	"claude-squad/session"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDueLabel(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, "", dueLabel("", now))
	assert.Equal(t, "due 10/5", dueLabel("2026-10-05", now))
	assert.Equal(t, "due 1/9/27", dueLabel("2027-01-09", now))
	assert.Equal(t, "due soon", dueLabel("soon", now), "unparseable dates are shown as-is")
}

func renderRow(i *session.Instance, width int) []string {
	sp := spinner.New()
	r := &InstanceRenderer{spinner: &sp}
	r.setWidth(width)
	return strings.Split(ansi.Strip(r.Render(i, 1, false, false)), "\n")
}

func TestTicketRowShowsDueDateAndTicketTitle(t *testing.T) {
	inst := &session.Instance{
		Title: "TSA-183", IssueID: "TSA-183", Status: session.Ready,
		Branch:       "agent/tsa-183-lactalis-cost-codes-upload-configure",
		IssueTitle:   "[Lactalis] Cost Codes - Upload & Configure",
		IssueDueDate: time.Now().Format("2006-01-02"),
	}
	lines := renderRow(inst, 80)
	assert.Contains(t, lines[0], "TSA-183 (due "+time.Now().Format("1/2")+")")
	assert.Contains(t, lines[1], "[Lactalis] Cost Codes - Upload & Configure")
	assert.NotContains(t, lines[1], "agent/")
	assert.NotContains(t, lines[1], branchIcon)

	// Narrow: the title is cut, never wrapped onto more lines.
	lines = renderRow(inst, 40)
	assert.Len(t, lines, 2)
	assert.Contains(t, lines[1], "[Lactalis]")
	assert.Contains(t, lines[1], "...")
}

func TestTicketRowWithoutDueDateOrTitleYet(t *testing.T) {
	inst := &session.Instance{Title: "TSA-1", IssueID: "TSA-1", Status: session.Ready, Branch: "agent/tsa-1-x"}
	lines := renderRow(inst, 80)
	assert.NotContains(t, lines[0], "due")
	assert.Contains(t, lines[1], branchIcon+"-agent/tsa-1-x", "falls back to the branch until a poll fills the title")
}

func TestPlainSessionRowKeepsBranch(t *testing.T) {
	inst := &session.Instance{Title: "scratch", Status: session.Ready, Branch: "yaroslav/scratch"}
	lines := renderRow(inst, 80)
	assert.Contains(t, lines[1], branchIcon+"-yaroslav/scratch")
}

func TestTicketRowShowsLinearState(t *testing.T) {
	inst := &session.Instance{
		Title: "TSA-168", IssueID: "TSA-168", Status: session.Ready,
		IssueTitle: "Thor Xpress Transport", IssueState: "Blocked by Client",
	}
	lines := renderRow(inst, 80)
	assert.Contains(t, lines[0], "TSA-168 · Blocked by Client")

	plain := &session.Instance{Title: "scratch", Status: session.Ready, IssueState: "ignored"}
	assert.NotContains(t, renderRow(plain, 80)[0], "ignored", "only ticket rows show a state")
}

func TestSelectedTicketRowKeepsHighlightAcrossStyledState(t *testing.T) {
	// Tests have no terminal, so lipgloss would emit no colors at all.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	sp := spinner.New()
	r := &InstanceRenderer{spinner: &sp}
	r.setWidth(80)
	inst := &session.Instance{Title: "TSA-168", IssueID: "TSA-168", Status: session.Ready, IssueState: "Blocked by Client"}
	row := strings.Split(r.Render(inst, 1, true, false), "\n")[0]
	require.Contains(t, row, "\x1b[", "colors are on")
	// After the styled state, the rest of the row must still be drawn on the selection
	// background, i.e. a background color is set again after every reset.
	parts := strings.Split(row, "\x1b[0m")
	for i, p := range parts[1:] {
		if ansi.Strip(p) == "" {
			continue // nothing drawn between two resets
		}
		assert.Contains(t, p, "48;", "segment %d after a reset is drawn without the selection background: %q", i+1, p)
	}
}
