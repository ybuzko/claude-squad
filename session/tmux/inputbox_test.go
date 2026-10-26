package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInputBoxEmpty(t *testing.T) {
	const rule = "\x1b[38;5;244m────────────\n"
	const status = "\x1b[39m  \x1b[38;5;220m⏵⏵ auto mode on\x1b[39m\n"
	// A past prompt in the transcript also starts with ❯; only the last one counts.
	const transcript = "\x1b[38;5;239m\x1b[48;5;237m❯ redo the scan\x1b[39m\n\n● Done.\n\n"

	cases := map[string]struct {
		screen string
		want   bool
	}{
		"empty":                       {transcript + rule + "\x1b[39m❯ \n" + rule + status, true},
		"dimmed suggestion":           {transcript + rule + "\x1b[39m❯ \x1b[2mapply the status changes\x1b[0m\n" + rule + status, true},
		"typed text":                  {transcript + rule + "\x1b[39m❯ half a sentence\n" + rule + status, false},
		"typed after a truecolor run": {rule + "\x1b[38;2;2;2;2m❯ hi\n" + rule, false},
		"permission dialog":           {transcript + " Do you want to proceed?\n \x1b[38;5;153m❯ 1. Yes\x1b[39m\n   2. No\n", false},
		"no input line":               {"$ \n", false},
	}
	for name, c := range cases {
		assert.Equal(t, c.want, inputBoxEmpty(c.screen), name)
	}
}
