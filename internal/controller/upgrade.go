package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// UpgradeOutcome is what an upgrade did, or in a dry run would do, to one service.
type UpgradeOutcome string

const (
	// UpgradeDone: the service runs its own copy of this build.
	UpgradeDone UpgradeOutcome = "upgraded"
	// UpgradeRestarted: its copy was already this build; only the process was started again.
	UpgradeRestarted UpgradeOutcome = "restarted"
	// UpgradeCurrent: it already runs this build from its own copy and was left alone.
	UpgradeCurrent UpgradeOutcome = "current"
	// UpgradeFailed: see Problem; Restored says whether the previous program runs again.
	UpgradeFailed UpgradeOutcome = "failed"
	// UpgradeNotInstalled: the service has no launchd job.
	UpgradeNotInstalled UpgradeOutcome = "not installed"
)

// ServiceProgram is the file name of a service's own copy of aimux inside its folder. The build it replaced is
// kept beside it as ServiceProgram + ".prev".
const ServiceProgram = "aimux"

// ServiceJob is the supervisor's entry for one service (a launchd job).
type ServiceJob interface {
	// Program is the file the job runs, or "" when the job is not installed.
	Program() (string, error)
	// Point makes the job run program and starts it from there.
	Point(program string) error
	// Restart starts the job's process again from the file it already runs.
	Restart() error
}

// UpgradeService is one background service that runs from its own copy of the program.
type UpgradeService struct {
	ID   string // the name typed on the command line, "model-proxy"
	Name string // the name shown, "model proxy"
	Dir  string // the service's folder, holding ServiceProgram and the previous build
	Job  ServiceJob
	// Probe reads the service's health: after a restart it must report this build and a start time after the
	// restart.
	Probe StatusProbe
	// Hold, when set, takes whatever other lock guards this service's program (the promote script's, for the model
	// proxy) for the length of the upgrade. An error says who holds it; the upgrade is then refused.
	Hold func() (release func(), err error)
}

type UpgradeOptions struct {
	Source string // the program to install, normally the running CLI
	Build  string // the version Source reports
	// Wait bounds how long a restarted service may take to answer on the new build; 20 seconds when zero. It is
	// counted from when the restart returns.
	Wait   time.Duration
	DryRun bool
}

type UpgradeResult struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Outcome UpgradeOutcome `json:"outcome"`
	From    string         `json:"from,omitempty"` // the build it ran before, when it answered
	To      string         `json:"to"`
	// MovedFrom is the file the job ran before, when the upgrade pointed it at the service's own copy.
	MovedFrom string `json:"moved_from,omitempty"`
	Problem   string `json:"problem,omitempty"`
	// Restored is true when a failed upgrade put the previous program back and it answers again.
	Restored bool `json:"restored,omitempty"`
}

// UpgradeServices brings each service to opts.Build, one at a time and in the order given: it copies opts.Source
// into the service's folder, restarts the service and waits for it to answer on the new build. A service that does
// not answer gets its previous program back, and the next service is still tried.
func UpgradeServices(ctx context.Context, services []UpgradeService, opts UpgradeOptions) []UpgradeResult {
	if opts.Wait <= 0 {
		opts.Wait = 20 * time.Second
	}
	results := make([]UpgradeResult, len(services))
	for i, service := range services {
		// Once interrupted, no further service is touched.
		if ctx.Err() != nil && !opts.DryRun {
			results[i] = UpgradeResult{ID: service.ID, Name: service.Name, To: opts.Build, Outcome: UpgradeFailed,
				Problem: "the upgrade was interrupted before this service; nothing was changed"}
			continue
		}
		results[i] = upgradeService(ctx, service, opts)
	}
	return results
}

func upgradeService(ctx context.Context, svc UpgradeService, opts UpgradeOptions) UpgradeResult {
	res := UpgradeResult{ID: svc.ID, Name: svc.Name, To: opts.Build}
	// fail is for a failure before the service or its files were touched.
	fail := func(format string, args ...any) UpgradeResult {
		res.Outcome, res.Problem = UpgradeFailed, fmt.Sprintf(format, args...)+"; nothing was changed"
		return res
	}
	was, err := svc.Job.Program()
	if err != nil {
		return fail("read its launchd job: %v", err)
	}
	if was == "" {
		res.Outcome = UpgradeNotInstalled
		return res
	}
	own := filepath.Join(svc.Dir, ServiceProgram)
	recovered := false
	if !opts.DryRun {
		if svc.Hold != nil {
			releaseHold, err := svc.Hold()
			if err != nil {
				return fail("%v", err)
			}
			defer releaseHold()
		}
		// One upgrade of a service at a time: two would each move the running build aside and lose it.
		release, err := lockService(svc.Dir)
		if errors.Is(err, errServiceLocked) {
			return fail("another upgrade of %s is running", svc.Name)
		}
		if err != nil {
			return fail("lock the service's folder: %v", err)
		}
		defer release()
		if was, err = svc.Job.Program(); err != nil || was == "" {
			return fail("read its launchd job again under the lock: %v", err)
		}
		// A file left aside means an upgrade was killed after it swapped the program and before the new build was
		// seen to answer. The file aside is the last build known to work, so it goes back first: otherwise the
		// unchecked build would become what a later failure returns to.
		if left := filepath.Join(svc.Dir, replacedName); fileExists(left) {
			if err := os.Rename(left, own); err != nil {
				return fail("put back the build an interrupted upgrade left aside: %v", err)
			}
			recovered = true // the process still running is the unchecked build
		}
		// A note left by a first move that was killed names the file the job ran before it. The job may already
		// point at the unchecked copy; a failure must still return to that file.
		// The note is only believed when it names an existing program by its full path. It lives in the service's
		// folder, which only the owner can write, so its content is the owner's as much as the job file is.
		if from, err := os.ReadFile(filepath.Join(svc.Dir, movedFromName)); err == nil { // #nosec G304 -- written by an earlier upgrade
			info, statErr := os.Stat(string(from)) //nolint:gosec // G703: a path an earlier upgrade wrote, in an owner-only folder
			if statErr == nil && filepath.IsAbs(string(from)) && info.Mode().IsRegular() {
				was = string(from)
				recovered = true
			}
		}
	}
	before := runStatusProbe(ctx, svc.Probe, statusProbeLimit)
	// A degraded service still serves, so it has a build to report and is not restarted for being degraded.
	answering := before.State == ServiceOK || before.State == ServiceDegraded
	if answering {
		res.From = before.Build
	}
	same, err := sameContent(opts.Source, own)
	if err != nil {
		return fail("compare %s with %s: %v", opts.Source, own, err)
	}
	needCopy, needPoint := !same, was != own
	switch {
	case !needCopy && !needPoint && !recovered && answering && before.Build == opts.Build:
		res.Outcome = UpgradeCurrent
		return res
	case !needCopy && !needPoint:
		res.Outcome = UpgradeRestarted
	default:
		res.Outcome = UpgradeDone
	}
	if needPoint {
		res.MovedFrom = was
	}
	if opts.DryRun {
		return res
	}
	// The first probe is cut short by an interrupt, which would make a healthy service look down.
	if ctx.Err() != nil {
		return fail("the upgrade was interrupted")
	}
	// On a first move the only earlier build is the file the job runs. If an install already replaced it with
	// this same build, a failure has nothing to return to.
	noEarlier := false
	if needPoint {
		noEarlier, _ = sameContent(opts.Source, was)
	}

	// The build being replaced waits aside until the new one answers: then it becomes the previous build, and
	// until then the previous build already kept is not touched.
	aside := ""
	if needCopy {
		if aside, err = installCopy(opts.Source, svc.Dir); err != nil {
			return fail("%v", err)
		}
	}
	moved := filepath.Join(svc.Dir, movedFromName)
	if needPoint {
		// Written before the job is switched, so a run after a kill knows where the job pointed.
		if err := os.WriteFile(moved, []byte(was), 0o600); err != nil { // #nosec G703 -- a fixed name in the service's own folder
			if aside != "" {
				_ = os.Rename(aside, own)
			}
			return fail("note the file the job ran before the move: %v", err)
		}
	}
	since := time.Now()
	if needPoint {
		err = svc.Job.Point(own)
	} else {
		err = svc.Job.Restart()
	}
	if err == nil {
		err = waitUpgraded(ctx, svc, since, opts.Build, opts.Wait)
	}
	if err == nil {
		_ = os.Remove(moved)
		switch {
		case needPoint && !noEarlier:
			// The file the job ran before the move is the build to go back to. It wins over anything aside: after
			// a first move that was killed, what is aside is the unchecked copy that move left.
			if err = keepCopy(was, svc.Dir, own+".prev"); err == nil && aside != "" {
				err = os.Remove(aside)
			}
		case aside != "":
			err = os.Rename(aside, own+".prev")
		}
		if err != nil {
			res.Problem = fmt.Sprintf("the build it replaced was not kept: %v", err)
			// Nothing may stay aside after a success: the next run would take it for an interrupted swap and put
			// the older build back over this one.
			if aside != "" {
				_ = os.Remove(aside)
			}
		}
		return res
	}

	res.Outcome, res.Problem = UpgradeFailed, err.Error()
	if !needCopy && !needPoint {
		return res // a plain restart has nothing to put back
	}
	// The restore runs to its end even when the upgrade was interrupted: an interrupt must not leave the job on
	// a build that was never seen to answer.
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opts.Wait+statusProbeLimit)
	defer cancel()
	if rerr := restoreService(restoreCtx, svc, own, aside, was, needCopy, needPoint, opts.Wait); rerr != nil {
		res.Problem += "; putting the previous program back failed: " + rerr.Error()
		if noEarlier {
			res.Problem += fmt.Sprintf("; the file the job ran before (%s) is this same build, so no earlier build was left to return to", was)
		}
		return res
	}
	_ = os.Remove(moved)
	res.Restored = true
	return res
}

// movedFromName holds, during a first move, the file the job ran before it.
const movedFromName = ".aimux-moved-from"

// replacedName is where the build being replaced waits, inside the service's folder, while the new one is checked.
const replacedName = ".aimux-replaced"

var errServiceLocked = errors.New("the service is locked by another upgrade")

// lockService takes the lock for one service's folder, a file beside it, without waiting. The lock goes when
// release is called or the process ends, so a killed upgrade leaves nothing to clear.
func lockService(dir string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- under ~/.aimux/services
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errServiceLocked
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// keepCopy copies the program at from to dest, staged in dir and moved in by rename.
func keepCopy(from, dir, dest string) error {
	staged, err := stageCopy(from, dir)
	if err != nil {
		return err
	}
	if err := os.Rename(staged, dest); err != nil { // #nosec G703 -- both are in the service's own folder
		_ = os.Remove(staged) // #nosec G703 -- a temp file this function made
		return err
	}
	return nil
}

// statusProbeLimit is how long one health read may take while an upgrade waits on a service.
const statusProbeLimit = 2 * time.Second

// installCopy puts a copy of source at dir's ServiceProgram and returns where the file it replaced is kept ("" when
// there was none). The new build is staged in dir and moved in by one rename, and the replaced build is first
// kept as a copy, so the program's path never stops existing, even if the upgrade is killed halfway.
func installCopy(source, dir string) (aside string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("make the service's folder: %w", err)
	}
	staged, err := stageCopy(source, dir)
	if err != nil {
		return "", fmt.Errorf("copy %s into %s: %w", source, dir, err)
	}
	own := filepath.Join(dir, ServiceProgram)
	if fileExists(own) {
		aside = filepath.Join(dir, replacedName)
		if err := keepCopy(own, dir, aside); err != nil {
			_ = os.Remove(staged)
			return "", fmt.Errorf("keep the running build aside: %w", err)
		}
	}
	if err := os.Rename(staged, own); err != nil {
		_ = os.Remove(staged)
		if aside != "" {
			_ = os.Remove(aside) // own was never replaced
		}
		return "", fmt.Errorf("install the new build: %w", err)
	}
	return aside, nil
}

func stageCopy(source, dir string) (string, error) {
	in, err := os.Open(source) // #nosec G304 G703 -- the program the owner is running, or the one its job ran
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	out, err := os.CreateTemp(dir, ".aimux-new-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chmod(0o755) // #nosec G302 -- a program must be executable
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// restoreService undoes a failed upgrade: the replaced file goes back (or the new one is removed on a first run),
// the job runs the program it ran before, and that program must answer again.
func restoreService(ctx context.Context, svc UpgradeService, own, aside, was string, copied, pointed bool, wait time.Duration) error {
	// The replaced build goes back by one rename, so the program's path is never empty.
	if copied && aside != "" {
		if err := os.Rename(aside, own); err != nil {
			return err
		}
	}
	since := time.Now()
	var err error
	if pointed {
		err = svc.Job.Point(was)
	} else {
		err = svc.Job.Restart()
	}
	if err != nil {
		return err
	}
	// A first move had nothing to put back: its copy goes only once the job no longer points at it.
	if copied && aside == "" {
		if err := os.Remove(own); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return waitUpgraded(ctx, svc, since, "", wait)
}

// waitUpgraded waits until the service answers as healthy from a process started after since, on build when one is
// given. A service still answering from before the restart, or on another build, has not been upgraded.
func waitUpgraded(ctx context.Context, svc UpgradeService, since time.Time, build string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	var last string
	for {
		row := runStatusProbe(ctx, svc.Probe, statusProbeLimit)
		switch {
		case row.State != ServiceOK:
			last = string(row.State)
			if row.Reason != "" {
				last = row.Reason
			}
		case row.StartedAt == nil || row.StartedAt.Before(since):
			last = "it still answers from before the restart"
		case build != "" && row.Build != build:
			last = fmt.Sprintf("it answers on build %s", row.Build)
		default:
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			on := ""
			if build != "" {
				on = " on build " + build
			}
			return fmt.Errorf("%s did not come up%s within %s (%s)", svc.Name, on, wait, last)
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// sameContent reports whether two files hold the same bytes. A missing b is simply different.
func sameContent(a, b string) (bool, error) {
	hb, err := fileHash(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ha, err := fileHash(a)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ha, hb), nil
}

func fileHash(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 G703 -- the CLI's own program, a service's copy, or the one its job ran
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
