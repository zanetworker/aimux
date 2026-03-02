package terminal

import (
	"os/exec"
	"testing"
)

func TestStartAndClose(t *testing.T) {
	cmd := exec.Command("echo", "hello")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sess.Close()

	buf := make([]byte, 1024)
	n, _ := sess.Read(buf)
	if n == 0 {
		t.Error("expected output from echo")
	}
	got := string(buf[:n])
	if got == "" {
		t.Error("expected non-empty output")
	}
}

func TestResize(t *testing.T) {
	cmd := exec.Command("cat")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sess.Close()

	if err := sess.Resize(120, 40); err != nil {
		t.Errorf("Resize() error: %v", err)
	}
}

func TestWrite(t *testing.T) {
	cmd := exec.Command("cat")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer sess.Close()

	data := []byte("hello\n")
	n, err := sess.Write(data)
	if err != nil {
		t.Errorf("Write() error: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write() = %d, want %d", n, len(data))
	}
}

func TestDoubleClose(t *testing.T) {
	cmd := exec.Command("echo", "hi")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if err := sess.Close(); err != nil {
		t.Errorf("first Close() error: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("second Close() should not error: %v", err)
	}
}

func TestAlive(t *testing.T) {
	cmd := exec.Command("cat")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if !sess.Alive() {
		t.Error("Alive() should be true before close")
	}

	sess.Close()

	if sess.Alive() {
		t.Error("Alive() should be false after close")
	}
}

func TestReadAfterClose(t *testing.T) {
	cmd := exec.Command("echo", "hello")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	sess.Close()

	buf := make([]byte, 1024)
	_, err = sess.Read(buf)
	if err == nil {
		t.Error("Read() after Close() should return error")
	}
}

func TestWriteAfterClose(t *testing.T) {
	cmd := exec.Command("cat")
	sess, err := Start(cmd)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	sess.Close()

	_, err = sess.Write([]byte("hello\n"))
	if err == nil {
		t.Error("Write() after Close() should return error")
	}
}
