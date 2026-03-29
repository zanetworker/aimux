package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Send sends a macOS notification without sound. No-op on non-darwin.
func Send(title, body string) {
	send(title, body, false)
}

// SendWithSound sends a macOS notification with the default sound. No-op on non-darwin.
func SendWithSound(title, body string) {
	send(title, body, true)
}

func send(title, body string, sound bool) {
	if runtime.GOOS != "darwin" {
		return
	}
	script := buildAppleScript(title, body, sound)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "osascript", "-e", script).Run()
	}()
}

func buildAppleScript(title, body string, sound bool) string {
	title = strings.ReplaceAll(title, `"`, `\"`)
	body = strings.ReplaceAll(body, `"`, `\"`)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	if sound {
		script += ` sound name "default"`
	}
	return script
}
