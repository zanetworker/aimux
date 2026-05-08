package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type quickLaunchEntry struct {
	Path     string `json:"path"`
	Basename string `json:"basename"`
	Exists   bool   `json:"exists"`
}

func (s *Server) handleQuickLaunchDirs(w http.ResponseWriter, r *http.Request) {
	dirs := s.cfg.QuickLaunch.Directories
	if dirs == nil {
		dirs = []string{}
	}

	var entries []quickLaunchEntry
	for _, d := range dirs {
		expanded := expandHomePath(d)
		_, err := os.Stat(expanded)
		entries = append(entries, quickLaunchEntry{
			Path:     expanded,
			Basename: filepath.Base(expanded),
			Exists:   err == nil,
		})
	}

	if entries == nil {
		entries = []quickLaunchEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"directories": entries})
}

func expandHomePath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
