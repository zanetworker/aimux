package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zanetworker/aimux/internal/parts"
)

// Making the background services match the parts section of the config file. The owner states what he wants;
// PlanParts says what would change; ApplyParts does exactly that plan. aimux changes only what it owns in a
// job file, the arguments that come from the settings, and a change that leaves the service not answering is
// undone: the job file it had is written back and the service started from it again.

// PartJob is a service's job as the operating system's supervisor holds it.
type PartJob interface {
	// Read returns the job file; an error that is os.ErrNotExist when there is none.
	Read() ([]byte, error)
	// Write replaces the job file.
	Write([]byte) error
	// Reload makes the supervisor run what the file says, started afresh.
	Reload() error
	// Loaded reports whether the supervisor has the job at all. One that is not loaded is not running and will
	// not be started again by itself.
	Loaded() (bool, error)
}

// PartService is one managed service and how to reach it.
type PartService struct {
	Service parts.Service
	Dir     string // its folder, where its program lives and what aimux knows about its job is kept
	Job     PartJob
	Probe   StatusProbe
	// ProbeFor, when set, gives the probe for the service as it runs with the given arguments (those after the
	// program). A service whose address is in its arguments is asked where it will be, not where it usually is.
	ProbeFor func(args []string) StatusProbe
	// CLIBuild is the build of the aimux making the change. A service that reports another build has its
	// arguments left alone until it is upgraded: it may not know the ones it would be given.
	CLIBuild string
	// Hold, when set, takes whatever other lock guards the service for the length of a change.
	Hold func() (release func(), err error)
}

// probeFor is the probe for the service as it runs with args.
func (svc PartService) probeFor(args []string) StatusProbe {
	if svc.ProbeFor != nil {
		return svc.ProbeFor(args)
	}
	return svc.Probe
}

// What a plan can say about a service.
const (
	ActionNone   = "none"             // it already runs what the settings state
	ActionAdopt  = "adopt"            // its job is taken under management as it is: recorded, not restarted
	ActionChange = "change arguments" // its job's arguments change, and it restarts
	ActionStart  = "start"            // its job is as the settings state, and is not loaded: it is started
	ActionSkip   = "skip"             // nothing is done, and Note says why
)

// JobChange is one line of a plan.
type JobChange struct {
	Part     string   `json:"part"`
	Service  string   `json:"service"`
	Action   string   `json:"action"`
	Was      []string `json:"was,omitempty"` // the arguments after the program, now
	Now      []string `json:"now,omitempty"` // and after the change
	Restarts bool     `json:"restarts"`
	Note     string   `json:"note,omitempty"`
	// sum is the job file as it was when this line was made, so that apply can tell it changed since.
	sum [sha256.Size]byte
}

// How applying one line went.
const (
	ApplyUnchanged  = "unchanged"
	ApplyAdopted    = "adopted"
	ApplyChanged    = "changed"
	ApplyStarted    = "started"
	ApplyRolledBack = "rolled back"
	ApplyFailed     = "failed"
	ApplySkipped    = "skipped"
)

// JobResult is how applying one line of a plan went.
type JobResult struct {
	JobChange
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// ApplyOptions bounds an apply.
type ApplyOptions struct {
	// Wait is how long a restarted service may take to answer; 20 seconds when zero.
	Wait time.Duration
}

// ErrStalePlan: the settings or a job file changed after the plan was made. Nothing was done; plan again.
var ErrStalePlan = errors.New("the settings or a job file changed since this plan was made")

// What aimux keeps in a service's folder about its job.
const (
	// JobRecordName marks a job as one aimux manages.
	JobRecordName = "job.json"
	// JobBeforeName is the job file as it was before aimux first managed it. It is written once and never again.
	JobBeforeName = "job.before-aimux.plist"
)

type jobRecord struct {
	Schema    int       `json:"schema"`
	Label     string    `json:"label"`
	ManagedAt time.Time `json:"managed_at"`
}

// PlanParts says what would change to make the services match the settings. A part the settings do not name is
// not in the plan: aimux leaves it as it finds it.
func PlanParts(services []PartService, s parts.Settings) []JobChange {
	var plan []JobChange
	for _, part := range parts.Parts() {
		stated, listed := s.Parts[part.ID]
		if !listed {
			continue
		}
		for _, id := range part.Services {
			at := slices.IndexFunc(services, func(svc PartService) bool { return svc.Service.ID == id })
			if at < 0 {
				continue // a service aimux does not manage, the tool gateway
			}
			plan = append(plan, planService(part.ID, services[at], stated))
		}
	}
	return plan
}

func planService(part string, svc PartService, stated parts.PartSettings) JobChange {
	change := JobChange{Part: part, Service: svc.Service.ID, Action: ActionSkip}
	if stated.Enabled == nil || !*stated.Enabled {
		change.Note = "turning a part off is not built yet; the service is left running"
		return change
	}
	file, err := svc.Job.Read()
	if errors.Is(err, os.ErrNotExist) {
		change.Note = "there is no job for this service to manage"
		return change
	}
	if err != nil {
		change.Note = "its job file could not be read: " + err.Error()
		return change
	}
	current, err := parts.JobArgs(file)
	if err != nil {
		change.Note = err.Error()
		return change
	}
	wanted, err := parts.WantedArgs(svc.Service, current, stated)
	if err != nil {
		change.Note = err.Error()
		return change
	}
	change.Was, change.Now, change.sum = current[1:], wanted[1:], sha256.Sum256(file)
	if !parts.SameArgs(svc.Service, current, wanted) {
		if svc.CLIBuild != "" {
			// The rule status uses for "older than the CLI": the same release, fewer commits.
			row := runStatusProbe(context.Background(), svc.probeFor(current[1:]), statusProbeLimit)
			if problem, older := olderBuild(&row, svc.CLIBuild); row.State != ServiceDown && older {
				change.Note = fmt.Sprintf("its %s, so it may not know the arguments it would be given; bring it up to date first: %s", problem, svc.Service.Update)
				return change
			}
		}
		change.Action, change.Restarts = ActionChange, true
		return change
	}
	// The file is right. "On" means running, and a job the supervisor holds is not always a running one: the
	// service is asked, where its arguments put it.
	loaded, err := svc.Job.Loaded()
	answers := err == nil && loaded && runStatusProbe(context.Background(), svc.probeFor(current[1:]), statusProbeLimit).State != ServiceDown
	switch {
	case err != nil:
		change.Action, change.Note = ActionSkip, "whether its job is loaded could not be told: "+err.Error()
	case !answers:
		change.Action, change.Restarts = ActionStart, true
		if loaded {
			change.Note = "its job is loaded and the service does not answer"
		}
	case !fileExists(filepath.Join(svc.Dir, JobRecordName)):
		change.Action = ActionAdopt
	default:
		change.Action = ActionNone
	}
	return change
}

// ApplyParts carries out plan, which PlanParts made from the same services and settings. Each service is
// planned again under its lock first: when that differs from the line given, nothing is done for any service and
// the error is ErrStalePlan. An error after that names the first service that could not be changed; the results
// say how each one went.
func ApplyParts(ctx context.Context, services []PartService, s parts.Settings, plan []JobChange, opts ApplyOptions) ([]JobResult, error) {
	wait := opts.Wait
	if wait <= 0 {
		wait = 20 * time.Second
	}
	results := make([]JobResult, len(plan))
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	// Every lock first, and every line checked against a fresh plan, before anything is changed.
	held := make([]*PartService, len(plan))
	for i, line := range plan {
		results[i] = JobResult{JobChange: line, Outcome: ApplySkipped, Detail: line.Note}
		at := slices.IndexFunc(services, func(svc PartService) bool { return svc.Service.ID == line.Service })
		if at < 0 || line.Action == ActionSkip {
			continue
		}
		svc := services[at]
		release, err := lockService(svc.Dir)
		if err != nil {
			return results, fmt.Errorf("%s: %w", svc.Service.Name, err)
		}
		releases = append(releases, release)
		if svc.Hold != nil {
			release, err := svc.Hold()
			if err != nil {
				return results, fmt.Errorf("%s: %w", svc.Service.Name, err)
			}
			releases = append(releases, release)
		}
		fresh := planService(line.Part, svc, s.Parts[line.Part])
		// The whole job file, not only its arguments: a program or an environment that changed since the plan
		// was shown is a job the owner did not see.
		if fresh.Action != line.Action || !slices.Equal(fresh.Was, line.Was) || !slices.Equal(fresh.Now, line.Now) || fresh.sum != line.sum {
			return results, fmt.Errorf("%s: %w", svc.Service.Name, ErrStalePlan)
		}
		held[i] = &svc
	}
	var first error
	for i, line := range plan {
		svc := held[i]
		if svc == nil {
			continue
		}
		if ctx.Err() != nil {
			// Interrupted: what was under way was finished or put back; nothing further is begun.
			results[i].Outcome, results[i].Detail = ApplySkipped, "not begun: interrupted"
			continue
		}
		outcome, detail, err := applyService(ctx, *svc, line, wait)
		results[i].Outcome, results[i].Detail = outcome, detail
		if err != nil && first == nil {
			first = fmt.Errorf("%s: %w", svc.Service.Name, err)
		}
	}
	return results, first
}

func applyService(ctx context.Context, svc PartService, line JobChange, wait time.Duration) (outcome, detail string, err error) {
	if line.Action == ActionNone {
		return ApplyUnchanged, "", nil
	}
	old, err := svc.Job.Read()
	if err != nil {
		return ApplyFailed, "its job file could not be read", err
	}
	if sha256.Sum256(old) != line.sum {
		// Another service's restart took a while, and this job file changed meanwhile.
		return ApplySkipped, "its job file changed while another service was being changed; nothing was done", ErrStalePlan
	}
	if err := keepBefore(svc, old); err != nil {
		return ApplyFailed, "the job file as it is could not be kept, so nothing was changed", err
	}
	if line.Action == ActionAdopt {
		if err := recordJob(svc); err != nil {
			return ApplyFailed, "", err
		}
		return ApplyAdopted, "", nil
	}
	probe := func(args []string) UpgradeService {
		return UpgradeService{Name: svc.Service.Name, Probe: svc.probeFor(args)}
	}
	// No slack before "since": the answer has to come from a process started after this reload, so another
	// listener that happened to start a moment ago on the same port is not taken for this job.
	if line.Action == ActionStart {
		since := time.Now()
		if err := svc.Job.Reload(); err != nil {
			return ApplyFailed, "its job could not be started", err
		}
		if err := waitUpgraded(ctx, probe(line.Now), since, "", wait); err != nil {
			return ApplyFailed, "its job was started, and the service does not answer", err
		}
		if err := recordJob(svc); err != nil {
			return ApplyStarted, "it runs; recording that failed", err
		}
		return ApplyStarted, "", nil
	}
	current, err := parts.JobArgs(old)
	if err != nil {
		return ApplyFailed, "", err
	}
	changed, err := parts.SetJobArgs(old, append([]string{current[0]}, line.Now...))
	if err != nil {
		return ApplyFailed, "", err
	}
	if err := svc.Job.Write(changed); err != nil {
		return ApplyFailed, "the job file could not be written; nothing was changed", err
	}
	since := time.Now()
	cause := svc.Job.Reload()
	if cause == nil {
		cause = waitUpgraded(ctx, probe(line.Now), since, "", wait)
	}
	if cause == nil {
		if err := recordJob(svc); err != nil {
			return ApplyChanged, "it runs with the new arguments; recording that failed", err
		}
		return ApplyChanged, "", nil
	}
	// The service does not answer with the new arguments: the job file it had goes back, and it starts from that.
	// An interrupt ends the wait above; putting things back is then finished all the same, in its own time.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait+statusProbeLimit)
	defer cancel()
	if err := svc.Job.Write(old); err != nil {
		return ApplyFailed, "the service did not come up, and its old job file could not be written back", fmt.Errorf("%w; writing the old job file back also failed: %v", cause, err)
	}
	since = time.Now()
	if err := svc.Job.Reload(); err != nil {
		return ApplyFailed, "the old job file is back, but starting the service from it failed", fmt.Errorf("%w; the old job file is back, but starting from it failed: %v", cause, err)
	}
	if err := waitUpgraded(ctx, probe(line.Was), since, "", wait); err != nil {
		return ApplyFailed, "the old job file is back, but the service does not answer from it either", fmt.Errorf("%w; the old job file is back, but %v", cause, err)
	}
	return ApplyRolledBack, "the service did not answer with the new arguments; its old job file is back and it runs from it", cause
}

// keepBefore keeps the job file as it is now, once: the file as the owner had it before aimux managed it.
func keepBefore(svc PartService, file []byte) error {
	path := filepath.Join(svc.Dir, JobBeforeName)
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(svc.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, file, 0o600)
}

func recordJob(svc PartService) error {
	path := filepath.Join(svc.Dir, JobRecordName)
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(svc.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobRecord{Schema: 1, Label: svc.Service.Label, ManagedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
