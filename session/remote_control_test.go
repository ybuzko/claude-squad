package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithRemoteControlNamesClaudeSessions(t *testing.T) {
	ticket := &Instance{Title: "TSA-196", IssueID: "TSA-196", IssueTitle: "Fix DSV's \"forwarder\" org"}
	assert.Equal(t,
		`claude --remote-control='TSA-196 Fix DSV'\''s "forwarder" org' "/triage-linear-ticket TSA-196"`,
		ticket.withRemoteControl(`claude "/triage-linear-ticket TSA-196"`))
	assert.Equal(t,
		`claude --remote-control='TSA-196 Fix DSV'\''s "forwarder" org' --resume abc`,
		ticket.withRemoteControl("claude --resume abc"))

	manual := &Instance{Title: "spike"}
	assert.Equal(t, `/home/u/.local/bin/claude --remote-control='spike'`,
		manual.withRemoteControl("/home/u/.local/bin/claude"))

	assert.Equal(t, "aider --model x", manual.withRemoteControl("aider --model x"))
	assert.Equal(t, "claude --rc x", manual.withRemoteControl("claude --rc x"))
	assert.Equal(t, "claude --remote-control=x", manual.withRemoteControl("claude --remote-control=x"))

	untitled := &Instance{Title: "TSA-7", IssueID: "TSA-7"}
	assert.Equal(t, `claude --remote-control='TSA-7' -c`, untitled.withRemoteControl("claude -c"))
}
