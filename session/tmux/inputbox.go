package tmux

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// InputBoxEmpty reports whether Claude Code's input box holds no typed text, so a
// prompt sent to the session will not be glued onto something a person is writing.
// The dimmed suggestion Claude shows in an empty box does not count as text.
func (t *TmuxSession) InputBoxEmpty() (bool, error) {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false, err
	}
	return inputBoxEmpty(content), nil
}

// inputBoxEmpty finds the input line, the last one starting with "❯", in a pane
// capture with escape sequences and checks that nothing after the marker is drawn
// at normal intensity. A pane without an input line (a dialog, a shell) is not empty.
func inputBoxEmpty(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		text := strings.TrimLeftFunc(undimmedText(lines[i]), isBlank)
		if rest, ok := strings.CutPrefix(text, "❯"); ok {
			return strings.TrimFunc(rest, isBlank) == ""
		}
	}
	return false
}

func isBlank(r rune) bool {
	return unicode.IsSpace(r) || r == ' '
}

// undimmedText returns the visible text of a line that is not drawn dimmed (SGR 2),
// dropping all escape sequences.
func undimmedText(line string) string {
	var b strings.Builder
	dim := false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			i++
			if i < len(line) && line[i] == '[' {
				start := i + 1
				for i++; i < len(line) && (line[i] < 0x40 || line[i] > 0x7e); i++ {
				}
				if i < len(line) && line[i] == 'm' {
					dim = applySGR(dim, line[start:i])
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if !dim {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// applySGR updates the dim flag for one SGR parameter list. Colour arguments
// (38;5;n, 38;2;r;g;b and the 48/58 forms) are skipped so a component of 2 is not
// read as "dim".
func applySGR(dim bool, params string) bool {
	if params == "" {
		return false
	}
	ps := strings.Split(params, ";")
	for k := 0; k < len(ps); k++ {
		switch ps[k] {
		case "0", "22":
			dim = false
		case "2":
			dim = true
		case "38", "48", "58":
			if k+1 < len(ps) && ps[k+1] == "5" {
				k += 2
			} else if k+1 < len(ps) && ps[k+1] == "2" {
				k += 4
			}
		}
	}
	return dim
}
