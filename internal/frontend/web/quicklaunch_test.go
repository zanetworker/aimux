package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zanetworker/aimux/internal/config"
)

func TestHandleQuickLaunchDirs(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(0)
	s.SetConfig(config.Config{
		QuickLaunch: config.QuickLaunchConfig{
			Directories: []string{dir, "/nonexistent/path"},
		},
	})

	req := httptest.NewRequest("GET", "/api/quick-launch", nil)
	w := httptest.NewRecorder()
	s.handleQuickLaunchDirs(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Directories []quickLaunchEntry `json:"directories"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Directories) != 2 {
		t.Fatalf("got %d dirs, want 2", len(resp.Directories))
	}
	if !resp.Directories[0].Exists {
		t.Error("first dir should exist")
	}
	if resp.Directories[0].Basename != filepath.Base(dir) {
		t.Errorf("basename = %q, want %q", resp.Directories[0].Basename, filepath.Base(dir))
	}
	if resp.Directories[1].Exists {
		t.Error("second dir should not exist")
	}
}

func TestHandleQuickLaunchDirsEmpty(t *testing.T) {
	s := NewServer(0)
	s.SetConfig(config.Config{})

	req := httptest.NewRequest("GET", "/api/quick-launch", nil)
	w := httptest.NewRecorder()
	s.handleQuickLaunchDirs(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		Directories []quickLaunchEntry `json:"directories"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Directories) != 0 {
		t.Errorf("got %d dirs, want 0", len(resp.Directories))
	}
}

func TestExpandHomePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	result := expandHomePath("~/projects")
	want := filepath.Join(home, "projects")
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}

	abs := "/absolute/path"
	if expandHomePath(abs) != abs {
		t.Error("absolute path should not change")
	}
}
