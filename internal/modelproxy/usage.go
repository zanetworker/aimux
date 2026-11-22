package modelproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// UsageWindow is one Codex rate-limit window, such as the weekly one (10080 minutes).
type UsageWindow struct {
	Minutes     int64     `json:"window_minutes"`
	UsedPercent int       `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitempty"`
}

// CodexUsage is the ChatGPT plan's Codex allowance as Codex reports it, in the stable shape the HUD reads from
// ~/.aimux/codex-usage.json.
type CodexUsage struct {
	Plan         string        `json:"plan"`
	UsageAllowed bool          `json:"usage_allowed"`
	Windows      []UsageWindow `json:"windows"`
	ResetCredits int           `json:"reset_credits_available"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// parseRateLimits reads Codex's account/rateLimits/read result. Identifiers in it (account, credit ids) are dropped.
func parseRateLimits(raw json.RawMessage) (CodexUsage, error) {
	type window struct {
		UsedPercent        int    `json:"usedPercent"`
		WindowDurationMins int64  `json:"windowDurationMins"`
		ResetsAt           *int64 `json:"resetsAt"`
	}
	var res struct {
		OrdinaryUsageAllowed *bool `json:"ordinaryUsageAllowed"`
		RateLimits           *struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
			PlanType  string  `json:"planType"`
		} `json:"rateLimits"`
		RateLimitResetCredits *struct {
			AvailableCount int `json:"availableCount"`
		} `json:"rateLimitResetCredits"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return CodexUsage{}, fmt.Errorf("decode Codex rate limits: %w", err)
	}
	if res.RateLimits == nil {
		return CodexUsage{}, errors.New("codex rate limits response has no rateLimits")
	}
	u := CodexUsage{Plan: res.RateLimits.PlanType, UsageAllowed: res.OrdinaryUsageAllowed != nil && *res.OrdinaryUsageAllowed}
	for _, w := range []*window{res.RateLimits.Primary, res.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		uw := UsageWindow{Minutes: w.WindowDurationMins, UsedPercent: w.UsedPercent}
		if w.ResetsAt != nil {
			uw.ResetsAt = time.Unix(*w.ResetsAt, 0).UTC()
		}
		u.Windows = append(u.Windows, uw)
	}
	if res.RateLimitResetCredits != nil {
		u.ResetCredits = res.RateLimitResetCredits.AvailableCount
	}
	return u, nil
}

// ReadUsage asks the proxy's Codex process for the plan's current Codex allowance. It costs no tokens, so its
// result is also the standing Codex check GET /health reports.
func (b *CodexBackend) ReadUsage(ctx context.Context) (CodexUsage, error) {
	u, err := b.readUsage(ctx)
	b.vitals.observe(ctx, &b.vitals.probe, err)
	return u, err
}

func (b *CodexBackend) readUsage(ctx context.Context) (CodexUsage, error) {
	p, err := b.process(ctx)
	if err != nil {
		return CodexUsage{}, err
	}
	var raw json.RawMessage
	if err := p.call(ctx, "account/rateLimits/read", nil, &raw); err != nil {
		return CodexUsage{}, err
	}
	u, err := parseRateLimits(raw)
	if err != nil {
		return CodexUsage{}, err
	}
	u.UpdatedAt = time.Now().UTC()
	return u, nil
}

// WriteUsageFile replaces path atomically with u as JSON (mode 0600), creating its folder when missing.
func WriteUsageFile(path string, u CodexUsage) error {
	data, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Codex usage: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".codex-usage-*.json")
	if err != nil {
		return fmt.Errorf("write Codex usage: %w", err)
	}
	_, werr := tmp.Write(append(data, '\n'))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write Codex usage: %w", errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// PollUsage reads the Codex allowance now and then every interval until ctx ends, writing each reading to path.
// A failed read is reported on stderr and leaves the last good file in place; its updated_at shows its age.
func PollUsage(ctx context.Context, read func(context.Context) (CodexUsage, error), path string, every time.Duration, stderr io.Writer) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		u, err := read(readCtx)
		cancel()
		if err == nil {
			err = WriteUsageFile(path, u)
		}
		if err != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintf(stderr, "model proxy: Codex usage not updated: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
