// Package notify posts desktop notifications. Only macOS is supported; every
// other platform is a no-op so callers need no build tags.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// timeout bounds the osascript process so a stuck prompt cannot accumulate.
const timeout = 5 * time.Second

// Enabled reports whether desktop notifications are supported here.
func Enabled() bool { return runtime.GOOS == "darwin" }

// MacOS posts a Notification Center banner. It returns immediately; delivery
// happens in the background and failures are ignored.
func MacOS(title, body string) {
	if !Enabled() {
		return
	}
	script := fmt.Sprintf("display notification %s with title %s", quote(body), quote(title))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = exec.CommandContext(ctx, "osascript", "-e", script).Run()
	}()
}

// quote renders s as an AppleScript string literal. Backslashes and quotes are
// escaped and line breaks flattened, so a filter name cannot break the script.
func quote(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
	)
	return `"` + r.Replace(s) + `"`
}
