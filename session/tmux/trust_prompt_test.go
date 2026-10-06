package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Captured from Claude Code on 2026-10-05; "No, exit" is the default selection.
const trustDialogNoSelected = `
 Accessing workspace:
 /home/yaroslav/.claude-squad/worktrees/agent/tsa-161-crsa-global-logistics-inc_18dbd31f96536d74
 Quick safety check: Is this a project you created or one you trust? (Like your own code, a
 well-known open source project, or work from your team). If not, take a moment to review what's in
 this folder first.
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ` + "\x1b[36m❯ No, exit\x1b[0m" + `
   Yes, I trust this folder
 Enter to confirm · Esc to cancel
`

const trustDialogYesSelected = `
 Quick safety check: Is this a project you created or one you trust?
   No, exit
 ` + "\x1b[36m❯ \x1b[1mYes, I trust this folder\x1b[0m" + `
 Enter to confirm · Esc to cancel
`

func TestTrustPromptAction(t *testing.T) {
	assert.Equal(t, trustDown, trustPromptAction(trustDialogNoSelected))
	assert.Equal(t, trustEnter, trustPromptAction(trustDialogYesSelected))
	assert.Equal(t, trustEnter, trustPromptAction("Do you trust the files in this folder?\n❯ 1. Yes, proceed"))
	assert.Equal(t, trustEnter, trustPromptAction("Found 1 new MCP server in .mcp.json"))
	assert.Equal(t, trustNone, trustPromptAction("● Working on TSA-168\n❯ \n"))
	assert.Equal(t, trustNone, trustPromptAction(""))
}
