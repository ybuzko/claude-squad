package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLaunchCommandNamesClaudeSessions(t *testing.T) {
	ticket := &Instance{Title: "TSA-196", IssueID: "TSA-196", IssueTitle: "Fix DSV's \"forwarder\" org"}
	assert.Equal(t,
		`claude --remote-control='TSA-196 Fix DSV'\''s "forwarder" org' "/triage-linear-ticket TSA-196"`,
		ticket.launchCommand(`claude "/triage-linear-ticket TSA-196"`))
	assert.Equal(t,
		`claude --remote-control='TSA-196 Fix DSV'\''s "forwarder" org' --resume abc`,
		ticket.launchCommand("claude --resume abc"))

	manual := &Instance{Title: "spike"}
	assert.Equal(t, `/home/u/.local/bin/claude --remote-control='spike'`,
		manual.launchCommand("/home/u/.local/bin/claude"))

	assert.Equal(t, "aider --model x", manual.launchCommand("aider --model x"))
	assert.Equal(t, "claude --rc x", manual.launchCommand("claude --rc x"))
	assert.Equal(t, "claude --remote-control=x", manual.launchCommand("claude --remote-control=x"))

	untitled := &Instance{Title: "TSA-7", IssueID: "TSA-7"}
	assert.Equal(t, `claude --remote-control='TSA-7' -c`, untitled.launchCommand("claude -c"))
}

func TestLaunchCommandAppendsTheSystemPrompt(t *testing.T) {
	SetAppendedSystemPrompt(`End with "TL;DR:" — it's for skimming.`)
	t.Cleanup(func() { SetAppendedSystemPrompt("") })

	inst := &Instance{Title: "TSA-7", IssueID: "TSA-7"}
	assert.Equal(t,
		`claude --remote-control='TSA-7' --append-system-prompt 'End with "TL;DR:" — it'\''s for skimming.' -c`,
		inst.launchCommand("claude -c"))
	assert.Equal(t, `claude --rc --append-system-prompt x`,
		inst.launchCommand("claude --rc --append-system-prompt x"), "flags already set are kept")
}
