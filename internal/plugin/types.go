package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type PanelType string

const (
	PanelMetricRow PanelType = "metric-row"
	PanelTable     PanelType = "table"
	PanelBarChart  PanelType = "bar-chart"
	PanelList      PanelType = "list"
)

type Panel struct {
	ID          string    `json:"id" yaml:"id"`
	Type        PanelType `json:"type" yaml:"type"`
	Title       string    `json:"title" yaml:"title"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
	Sortable    bool      `json:"sortable,omitempty" yaml:"sortable,omitempty"`
	Expandable  bool      `json:"expandable,omitempty" yaml:"expandable,omitempty"`
	Width       string    `json:"width,omitempty" yaml:"width,omitempty"`
}

type Plugin struct {
	Name       string   `json:"name" yaml:"name"`
	Tab        string   `json:"tab" yaml:"tab"`
	Command    string   `json:"command" yaml:"command"`
	CacheSecs  int      `json:"cache_seconds" yaml:"cache_seconds"`
	Panels     []Panel  `json:"panels" yaml:"panels"`
	AutoDetect []string `json:"-" yaml:"-"`
	BuiltIn    bool     `json:"-" yaml:"-"`
}

type MetricItem struct {
	Label string      `json:"label"`
	Value interface{} `json:"value"`
	Color string      `json:"color"`
}

type TableRow struct {
	Cells []interface{} `json:"cells"`
	Color string        `json:"color,omitempty"`
}

type TableData struct {
	Columns []string   `json:"columns"`
	Rows    []TableRow `json:"rows"`
}

type BarChartItem struct {
	Label     string   `json:"label"`
	Value     float64  `json:"value"`
	Secondary float64  `json:"secondary,omitempty"`
	Legend    []string `json:"legend,omitempty"`
}

type ListItem struct {
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	Body     string   `json:"body,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

type PanelData struct {
	Items   json.RawMessage `json:"items,omitempty"`
	Columns []string        `json:"columns,omitempty"`
	Rows    json.RawMessage `json:"rows,omitempty"`
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
