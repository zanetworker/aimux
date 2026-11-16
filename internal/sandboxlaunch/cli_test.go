package sandboxlaunch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunner_StreamsStdoutAndReportsExit(t *testing.T) {
	run := ExecRunner()
	rc, err := run(context.Background(), strings.NewReader("in\n"), "sh", "-c", `cat; echo out`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if err := rc.Close(); err != nil || string(b) != "in\nout\n" {
		t.Fatalf("out=%q close=%v", b, err)
	}

	rc, err = run(context.Background(), nil, "sh", "-c", `echo partial; echo boom >&2; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(rc)
	err = rc.Close()
	if string(b) != "partial\n" || err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("out=%q close=%v, want exit status 3 with stderr", b, err)
	}
}

func TestExecRunner_CloseEarlyStopsProcess(t *testing.T) {
	rc, err := ExecRunner()(context.Background(), nil, "sh", "-c", `echo first; exec sleep 30`)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- rc.Close() }()
	select {
	case <-done: //nolint:errcheck // the Close error is not under test; only that it returns
	case <-time.After(closeGrace + 5*time.Second):
		t.Fatal("Close did not stop the process")
	}
}

// A caller that stops reading after the result line must not kill a command
// that is about to exit on its own: the in-sandbox wrapper removes the token
// file only on a clean exit.
func TestExecRunner_CloseLetsAFinishingCommandExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "done")
	rc, err := ExecRunner()(context.Background(), nil, "sh", "-c", `echo result; sleep 0.3; echo tail; touch "$1"`, "sh", marker)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Close killed a command that was finishing")
	}
}

func TestExecRunner_CloseEarlyStillReportsExit(t *testing.T) {
	rc, err := ExecRunner()(context.Background(), nil, "sh", "-c", `echo result; echo more; exit 4`)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("Close = %v, want exit status 4", err)
	}
}

// A grandchild that holds stdout must not keep Close from returning: the
// whole process group is killed and the pipe closed within a hard bound.
func TestExecRunner_CloseBoundedWithGrandchild(t *testing.T) {
	rc, err := ExecRunner()(context.Background(), nil, "sh", "-c", `echo first; sleep 30 & echo $!; wait $!; echo after`)
	if err != nil {
		t.Fatal(err)
	}
	var first, pidLine string
	if _, err := fmt.Fscanln(rc, &first); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fscanln(rc, &pidLine); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(pidLine)
	if err != nil {
		t.Fatalf("pid %q: %v", pidLine, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	done := make(chan error, 1)
	go func() { done <- rc.Close() }()
	select {
	case <-done: //nolint:errcheck // the Close error is not under test; only that it returns
	case <-time.After(closeGrace + 3*time.Second):
		t.Fatal("Close did not return within the bound")
	}
	for i := 0; ; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if i == 40 {
			t.Fatalf("grandchild sleep %d still running", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecRunner_CtxCancelBoundedWithGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := ExecRunner()(ctx, nil, "sh", "-c", `sleep 30 & echo $!; wait $!; echo after`)
	if err != nil {
		t.Fatal(err)
	}
	var pidLine string
	if _, err := fmt.Fscanln(rc, &pidLine); err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(pidLine)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	done := make(chan struct{})
	go func() { _, _ = io.ReadAll(rc); _ = rc.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2*closeDrain + 3*time.Second):
		t.Fatal("read and Close did not return after ctx cancel")
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("grandchild sleep %d still running after cancel", pid)
	}
}
