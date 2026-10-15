package tmux

import (
	"bytes"
	"io"
)

// altScreenSeqs are the alternate-screen switches a tmux client writes on attach (smcup)
// and on exit (rmcup).
var altScreenSeqs = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1049l")}

// altScreenFilter drops alternate-screen switches from an attached tmux client's output.
// cs is already on the alternate screen (Bubble Tea), so tmux's switch is redundant, and
// the Windows console (ConPTY) answers it by rebuilding the alternate screen at the main
// screen's size. The main screen is not resized while an app is on the alternate one, so
// after the window changes size (e.g. moving to another monitor) every attach brought back
// a screen of the old size: lines wrapped early and the picture was garbled until a resize.
type altScreenFilter struct {
	w io.Writer
	// pending holds a trailing partial match carried into the next Write.
	pending []byte
}

func newAltScreenFilter(w io.Writer) *altScreenFilter {
	return &altScreenFilter{w: w}
}

func (f *altScreenFilter) Write(p []byte) (int, error) {
	buf := append(f.pending, p...)
	f.pending = nil
	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); {
		if buf[i] == 0x1b {
			rest := buf[i:]
			matched, partial := 0, false
			for _, seq := range altScreenSeqs {
				if bytes.HasPrefix(rest, seq) {
					matched = len(seq)
					break
				}
				if len(rest) < len(seq) && bytes.HasPrefix(seq, rest) {
					partial = true
				}
			}
			if matched > 0 {
				i += matched
				continue
			}
			if partial {
				f.pending = append([]byte(nil), rest...)
				break
			}
		}
		out = append(out, buf[i])
		i++
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
