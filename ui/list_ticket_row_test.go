package ui

import (
	"claude-squad/session"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
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
