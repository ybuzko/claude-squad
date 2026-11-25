package notify

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToastScriptEscapesText(t *testing.T) {
	script := toastScript("TSA-1 needs you", `Allow "rm" in <dir> & Bob's file`)
	assert.Contains(t, script, `<text>TSA-1 needs you</text>`)
	// XML-escaped, then ' doubled for the PowerShell literal.
	assert.Contains(t, script, `<text>Allow &#34;rm&#34; in &lt;dir&gt; &amp; Bob&#39;s file</text>`)
	assert.NotContains(t, script, "Bob's")
}

func TestPowerShellStringDoublesQuotes(t *testing.T) {
	assert.Equal(t, `'it''s'`, powerShellString("it's"))
	assert.Equal(t, "'a’’b'", powerShellString("a’b"))
}

func TestEncodePowerShellIsUTF16LEBase64(t *testing.T) {
	enc := encodePowerShell("é ok")
	raw, err := base64.StdEncoding.DecodeString(enc)
	require.NoError(t, err)
	units := make([]uint16, len(raw)/2)
	for k := range units {
		units[k] = uint16(raw[2*k]) | uint16(raw[2*k+1])<<8
	}
	assert.Equal(t, "é ok", string(utf16.Decode(units)))
}

func TestAppleScriptString(t *testing.T) {
	assert.Equal(t, `"say \"hi\" \\ bye"`, appleScriptString(`say "hi" \ bye`))
	assert.False(t, strings.Contains(appleScriptString("x"), "'"))
}
