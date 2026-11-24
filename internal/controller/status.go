package controller

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"
)

type ServiceState string

const (
	ServiceOK           ServiceState = "ok"
	ServiceDegraded     ServiceState = "degraded"
	ServiceDown         ServiceState = "down"
	ServiceNotInstalled ServiceState = "not installed"
)

type Attention struct {
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
}

type ServiceStatus struct {
	Name         string       `json:"name"`
	State        ServiceState `json:"state"`
	Reason       string       `json:"reason,omitempty"`
	Build        string       `json:"build,omitempty"`
	BuildNote    string       `json:"build_note,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	Address      string       `json:"address,omitempty"`
	LastActivity *time.Time   `json:"last_activity,omitempty"`
	ActivityNote string       `json:"activity_note,omitempty"`
	RestartFix   string       `json:"-"`
	Attention    []Attention  `json:"attention,omitempty"`
}

type StatusProbe struct {
	Name string
	// Address and RestartFix fill the row when the probe gives no answer in time.
	Address    string
	RestartFix string
	Run        func(ctx context.Context) ServiceStatus
}

type StatusOverview struct {
	CLIBuild string          `json:"cli_build"`
	Services []ServiceStatus `json:"services"`
}

// ServicesOverview runs the probes concurrently and returns rows in probe order.
func ServicesOverview(ctx context.Context, cliBuild string, probes []StatusProbe, limit time.Duration) StatusOverview {
	overview := StatusOverview{CLIBuild: cliBuild, Services: make([]ServiceStatus, len(probes))}
	var probesDone sync.WaitGroup
	for i, probe := range probes {
		probesDone.Go(func() {
			overview.Services[i] = runStatusProbe(ctx, probe, limit)
		})
	}
	probesDone.Wait()

	for i := range overview.Services {
		service := &overview.Services[i]
		addReasonItem(service)
		if problem, older := olderBuild(service, cliBuild); older {
			service.Attention = append(slices.Clone(service.Attention), Attention{Problem: problem, Fix: service.RestartFix})
		}
	}
	return overview
}

// addReasonItem puts a down or degraded row's reason first in its attention list, so every front shows why the
// service is unhealthy next to its other problems. A row that already lists that problem keeps its own fix.
func addReasonItem(service *ServiceStatus) {
	if service.State != ServiceDown && service.State != ServiceDegraded {
		return
	}
	problem := service.Reason
	if problem == "" {
		if len(service.Attention) > 0 {
			return
		}
		// An unhealthy row is never silent, even when its probe gave no reason.
		problem = string(service.State)
	}
	if slices.ContainsFunc(service.Attention, func(item Attention) bool { return item.Problem == problem }) {
		return
	}
	service.Attention = append([]Attention{{Problem: problem, Fix: service.RestartFix}}, service.Attention...)
}

// olderBuild reports whether the service runs an older build than the CLI. Commit counts restart at each tag, so
// they are compared only when both builds count from the same tag.
func olderBuild(service *ServiceStatus, cliBuild string) (string, bool) {
	if service.BuildNote != "" {
		return "", false
	}
	cliTag, cliCount, ok := describeParts(cliBuild)
	if !ok {
		return "", false
	}
	tag, count, ok := describeParts(service.Build)
	if !ok || tag != cliTag || count >= cliCount {
		return "", false
	}
	return fmt.Sprintf("build %d is older than the CLI (%d)", count, cliCount), true
}

// describeParts splits a git describe version into its tag and commit count.
func describeParts(version string) (string, uint64, bool) {
	match := buildNumberSuffix.FindStringSubmatch(version)
	if match == nil {
		return "", 0, false
	}
	count, err := strconv.ParseUint(match[2], 10, 64)
	return match[1], count, err == nil
}

func runStatusProbe(ctx context.Context, probe StatusProbe, limit time.Duration) ServiceStatus {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	result := make(chan ServiceStatus, 1)
	go func() {
		row := probe.Run(ctx)
		// A probe unblocked by cancellation must not win the race with the timeout.
		if ctx.Err() == nil {
			result <- row
		}
	}()

	select {
	case row := <-result:
		return row
	case <-ctx.Done():
		// A timely result may already be buffered when the deadline becomes ready.
		select {
		case row := <-result:
			return row
		default:
			return ServiceStatus{
				Name: probe.Name, State: ServiceDown, Reason: fmt.Sprintf("no answer in %s", limit),
				Address: probe.Address, RestartFix: probe.RestartFix,
			}
		}
	}
}

// Attention lists every issue in service order, with its service name prefixed.
func (o StatusOverview) Attention() []Attention {
	var items []Attention
	for _, service := range o.Services {
		for _, item := range service.Attention {
			item.Problem = service.Name + ": " + item.Problem
			items = append(items, item)
		}
	}
	return items
}

var buildNumberSuffix = regexp.MustCompile(`^(.+)-([0-9]+)-g[0-9a-f]+$`)

// BuildNumber returns the commit count from a git describe version.
func BuildNumber(version string) string {
	if match := buildNumberSuffix.FindStringSubmatch(version); match != nil {
		return match[2]
	}
	return version
}
