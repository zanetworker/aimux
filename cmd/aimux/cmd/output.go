package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ExitSuccess  = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitNotFound = 3
	ExitConfig   = 4
)

type OutputWriter struct {
	JSON   bool
	Stdout io.Writer
	Stderr io.Writer
}

func NewOutputWriter(jsonMode bool) *OutputWriter {
	return &OutputWriter{
		JSON:   jsonMode,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func (w *OutputWriter) WriteResult(data any) int {
	if w.JSON {
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return w.WriteError(fmt.Sprintf("failed to marshal result: %v", err), ExitError, nil)
		}
		_, _ = fmt.Fprintln(w.Stdout, string(b))
	} else {
		_, _ = fmt.Fprintln(w.Stdout, data)
	}
	return ExitSuccess
}

func (w *OutputWriter) WriteError(msg string, code int, extra map[string]any) int {
	if w.JSON {
		errObj := map[string]any{"error": msg, "code": code}
		for k, v := range extra {
			errObj[k] = v
		}
		b, _ := json.Marshal(errObj)
		_, _ = fmt.Fprintln(w.Stderr, string(b))
	} else {
		_, _ = fmt.Fprintf(w.Stderr, "Error: %s", msg)
		if vals, ok := extra["valid_values"]; ok {
			if sv, ok := vals.([]string); ok {
				_, _ = fmt.Fprintf(w.Stderr, " (must be one of: %s)", strings.Join(sv, ", "))
			}
		}
		_, _ = fmt.Fprintln(w.Stderr)
	}
	return code
}

func IsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) // #nosec G115
}
