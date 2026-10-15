package tmux

import (
	"bytes"
	"testing"
)

func TestAltScreenFilterDropsSwitchesSplitAcrossWrites(t *testing.T) {
	input := "a\x1b[?1049h\x1b[22;0;0tb\x1b[?25lc\x1b[?1049ld\x1b"
	for split := 0; split <= len(input); split++ {
		var out bytes.Buffer
		f := newAltScreenFilter(&out)
		for _, part := range []string{input[:split], input[split:]} {
			if n, err := f.Write([]byte(part)); err != nil || n != len(part) {
				t.Fatalf("split %d: Write(%q) = %d, %v", split, part, n, err)
			}
		}
		// The trailing lone ESC may be the start of a switch, so it waits for more output.
		if got, want := out.String(), "a\x1b[22;0;0tb\x1b[?25lcd"; got != want {
			t.Fatalf("split %d: got %q, want %q", split, got, want)
		}
	}
}
