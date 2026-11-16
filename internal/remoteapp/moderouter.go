package remoteapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/zanetworker/aimux/internal/autoroute"
	"github.com/zanetworker/aimux/internal/decide"
)

// buildModeRouter is the mode-router decider from home's
// ~/.config/aimux/deciders.yaml (the built-in one without a file), logging
// to ~/.aimux/logs/decisions.jsonl. A chat backend reaches its model with
// apiKey (sessions.api_key; decide.ChatClient). A config that cannot be used is
// reported on errOut and gives nil: Auto then always asks the person,
// and serve runs as before. Nothing here reads a key or calls a model.
func buildModeRouter(home, apiKey string, errOut io.Writer) *decide.Decider {
	path := filepath.Join(home, ".config", "aimux", "deciders.yaml")
	cfg, err := autoroute.LoadDecider(path)
	if err == nil {
		var d *decide.Decider
		d, err = decide.Build(autoroute.DeciderName, cfg, decide.Deps{LLM: decide.ChatClient(apiKey),
			Log: &decide.Log{Path: filepath.Join(home, ".aimux", "logs", "decisions.jsonl")}})
		if err == nil {
			return d
		}
	}
	where := path
	if strings.Contains(err.Error(), path) {
		where = "it" // the error names the file already
	}
	_, _ = fmt.Fprintf(errOut, "Auto mode is off: %v (fix %s, then restart remote serve)\n", err, where)
	return nil
}

// modeRouterWarmTimeout bounds the warm-up at serve start.
const modeRouterWarmTimeout = 15 * time.Second

// warmModeRouter readies d once, in the background at serve start, so the
// first Auto question pays neither the key read nor the TLS handshake
// (decide.Decider.Warm: no decision is made and nothing is spent). It
// logs how long that took on errOut; a failure shows on the first route.
func warmModeRouter(ctx context.Context, d *decide.Decider, errOut io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, modeRouterWarmTimeout)
	defer cancel()
	start := time.Now()
	d.Warm(ctx)
	if errors.Is(ctx.Err(), context.Canceled) {
		return // serve is stopping
	}
	_, _ = fmt.Fprintf(errOut, "Auto: mode router warm-up took %d ms\n", time.Since(start).Milliseconds())
}
