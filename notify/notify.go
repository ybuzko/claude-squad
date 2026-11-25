// Package notify shows desktop notifications: a Windows toast through PowerShell when
// running under WSL, Notification Center on macOS, notify-send on other Linux desktops.
package notify

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// ErrUnsupported means there is no way to show a notification here.
var ErrUnsupported = errors.New("no desktop notification method found (powershell.exe, osascript or notify-send)")

// windowsPowerShell is where powershell.exe lives when WSL's interop does not put the
// Windows directories on PATH.
const windowsPowerShell = "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"

// powerShellAppID is Windows PowerShell's AppUserModelID. A toast must name an app
// registered with Windows; this one is on every Windows install, so the toast shows up
// as coming from Windows PowerShell.
const powerShellAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// Send shows a notification with a title and body. It blocks until the notifier has
// handed it over, at most a few seconds.
func Send(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd, err := command(ctx, title, body)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", cmd.Path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func command(ctx context.Context, title, body string) (*exec.Cmd, error) {
	switch {
	case runtime.GOOS == "darwin":
		script := fmt.Sprintf("display notification %s with title %s", appleScriptString(body), appleScriptString(title))
		return exec.CommandContext(ctx, "osascript", "-e", script), nil
	case isWSL():
		ps, err := exec.LookPath("powershell.exe")
		if err != nil {
			if _, statErr := os.Stat(windowsPowerShell); statErr != nil {
				return nil, ErrUnsupported
			}
			ps = windowsPowerShell
		}
		cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(toastScript(title, body)))
		// Started from a Linux directory, Windows programs run in a UNC path and some
		// warn about it; a Windows drive avoids that.
		if _, err := os.Stat("/mnt/c"); err == nil {
			cmd.Dir = "/mnt/c"
		}
		return cmd, nil
	default:
		ns, err := exec.LookPath("notify-send")
		if err != nil {
			return nil, ErrUnsupported
		}
		return exec.CommandContext(ctx, ns, "--app-name=cs", title, body), nil
	}
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}

// toastScript is the PowerShell that shows a toast through the Windows Runtime API.
func toastScript(title, body string) string {
	var x strings.Builder
	x.WriteString(`<toast><visual><binding template="ToastGeneric">`)
	for _, text := range []string{title, body} {
		if text == "" {
			continue
		}
		x.WriteString("<text>")
		xml.EscapeText(&x, []byte(text))
		x.WriteString("</text>")
	}
	x.WriteString(`</binding></visual></toast>`)
	return strings.Join([]string{
		`$ErrorActionPreference = 'Stop'`,
		`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null`,
		`[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null`,
		`$xml = New-Object Windows.Data.Xml.Dom.XmlDocument`,
		`$xml.LoadXml(` + powerShellString(x.String()) + `)`,
		`$toast = New-Object Windows.UI.Notifications.ToastNotification $xml`,
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(` + powerShellString(powerShellAppID) + `).Show($toast)`,
	}, "\n")
}

// powerShellString quotes s as a PowerShell literal string.
func powerShellString(s string) string {
	// Besides ', PowerShell treats the typographic single quotes as quote characters.
	r := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
	return "'" + r.Replace(s) + "'"
}

// encodePowerShell encodes a script for -EncodedCommand (base64 of UTF-16LE), which
// sidesteps quoting it for the Windows command line.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for k, u := range units {
		b[2*k] = byte(u)
		b[2*k+1] = byte(u >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
