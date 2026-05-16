package deliver

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ValidSchemes lists the accepted --deliver target formats.
var ValidSchemes = []string{"stdout", "file:<path>", "webhook:<url>"}

// Deliver routes data to the specified target sink.
// Supported targets: "" or "stdout" (write to os.Stdout),
// "file:<path>" (write to file, creating dirs as needed),
// "webhook:<url>" (HTTP POST with application/json).
func Deliver(data []byte, target string) error {
	if target == "" || target == "stdout" {
		_, err := os.Stdout.Write(data)
		return err
	}

	if strings.HasPrefix(target, "file:") {
		path := strings.TrimPrefix(target, "file:")
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create deliver dir: %w", err)
		}
		return os.WriteFile(path, data, 0o600) // #nosec G304
	}

	if strings.HasPrefix(target, "webhook:") {
		url := strings.TrimPrefix(target, "webhook:")
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(data)) // #nosec G107
		if err != nil {
			return fmt.Errorf("webhook POST failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}
		return nil
	}

	return fmt.Errorf("--deliver scheme must be one of: %s (got: %q)", strings.Join(ValidSchemes, ", "), target)
}
