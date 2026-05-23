package badge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rule defines how to extract a badge value from a project file.
type Rule struct {
	Path     string `yaml:"path"`      // file path relative to working dir
	JSONPath string `yaml:"json_path"` // dot-separated path into JSON (optional)
	Label    string `yaml:"label"`     // display label
	Color    string `yaml:"color"`     // hex color (optional)
}

// Badge is an evaluated badge ready for display.
type Badge struct {
	Label string
	Value string
	Color string
}

// Evaluate runs all badge rules against a working directory.
// Rules that fail (missing file, bad JSON path) are silently skipped.
func Evaluate(workDir string, rules []Rule) []Badge {
	var badges []Badge
	for _, r := range rules {
		b, ok := evaluate(workDir, r)
		if ok {
			badges = append(badges, b)
		}
	}
	return badges
}

func evaluate(workDir string, r Rule) (Badge, bool) {
	path := filepath.Join(workDir, r.Path)
	data, err := os.ReadFile(path) // #nosec G304 -- path from user config
	if err != nil {
		return Badge{}, false
	}

	value := strings.TrimSpace(string(data))

	if r.JSONPath != "" {
		v, ok := extractJSONPath(data, r.JSONPath)
		if !ok {
			return Badge{}, false
		}
		value = v
	} else {
		// For plain files, take first line only
		if idx := strings.IndexByte(value, '\n'); idx != -1 {
			value = value[:idx]
		}
	}

	return Badge{
		Label: r.Label,
		Value: value,
		Color: r.Color,
	}, true
}

func extractJSONPath(data []byte, path string) (string, bool) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", false
	}

	parts := strings.Split(path, ".")
	current := obj
	for _, key := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return "", false
		}
		current, ok = m[key]
		if !ok {
			return "", false
		}
	}
	return fmt.Sprintf("%v", current), true
}
