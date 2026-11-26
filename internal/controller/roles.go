package controller

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zanetworker/aimux/internal/parts"
)

// RoleRow is one kind of work and what answers it.
type RoleRow struct {
	Role string `json:"role"`
	// Entry is the name under models the role uses, Model what that entry holds, Engine who answers it. All three
	// are empty for the main conversation, whose model is chosen in the session.
	Entry  string `json:"entry,omitempty"`
	Model  string `json:"model,omitempty"`
	Engine string `json:"engine,omitempty"`
	// Note says what is wrong, when the engine is not one the running proxy answers with.
	Note string `json:"note,omitempty"`
}

// RolesView is the roles as the settings state them, the main conversation first. running is the engines the
// proxy says it answers with; nil when it does not say.
func RolesView(p parts.PartSettings, running []string) []RoleRow {
	rows := []RoleRow{{Role: "main"}}
	roles := make([]string, 0, len(p.Roles))
	for role := range p.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		row := RoleRow{Role: role, Entry: p.Roles[role]}
		row.Model, _ = p.RoleModel(role)
		row.Engine = parts.ModelEngine(row.Model)
		if running != nil && !slices.Contains(running, row.Engine) {
			row.Note = "the running proxy does not answer with " + row.Engine + ", so these calls would be refused"
		}
		rows = append(rows, row)
	}
	return rows
}

// SubagentDir is a folder of subagent definitions, and what to call it in a table.
type SubagentDir struct {
	Path  string
	Label string
}

// SubagentRow is one subagent definition and what it would run on.
type SubagentRow struct {
	Name      string `json:"name"`
	DefinedIn string `json:"defined_in"`
	// Model is what its definition names; empty when it names none.
	Model string `json:"model,omitempty"`
	// RunsOn is the engine that would answer it; empty when it uses the session's own model, whichever that is.
	RunsOn string `json:"runs_on,omitempty"`
	// FromRole: it names no model, and the subagent role decides.
	FromRole bool `json:"from_role,omitempty"`
}

// SubagentModels reads the subagent definitions in dirs and says what each would run on, given the settings. A
// definition that names a model keeps it, since the harness lets that win; one that names none takes the
// subagent role's model when one is set, and the session's otherwise. It reads the front matter only and
// changes nothing. A folder that is not there, and a file without front matter, are passed over.
func SubagentModels(dirs []SubagentDir, p parts.PartSettings) []SubagentRow {
	roleModel, roleSet := p.RoleModel(parts.RoleSubagent)
	var rows []SubagentRow
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir.Path, "*.md"))
		sort.Strings(files)
		for _, file := range files {
			name, model, ok := subagentFrontMatter(file)
			if !ok {
				continue
			}
			row := SubagentRow{Name: name, DefinedIn: dir.Label, Model: model}
			switch {
			case model == "inherit":
				// The session's own model, on whichever engine that is.
			case model != "":
				row.RunsOn = parts.ModelEngine(model)
			case roleSet:
				row.RunsOn, row.FromRole = parts.ModelEngine(roleModel), true
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// subagentFrontMatter reads a definition's name and model from the YAML block between its first two --- lines.
// Only a regular file is read, and no more than the block.
func subagentFrontMatter(path string) (name, model string, ok bool) {
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", "", false
	}
	file, err := os.Open(path) // #nosec G304 -- a subagent definition in the owner's own folders
	if err != nil {
		return "", "", false
	}
	defer func() { _ = file.Close() }()
	lines := bufio.NewScanner(file)
	if !lines.Scan() || strings.TrimSpace(lines.Text()) != "---" {
		return "", "", false
	}
	var block strings.Builder
	for closed := false; !closed; {
		if !lines.Scan() || block.Len() > 64<<10 {
			return "", "", false
		}
		if closed = strings.TrimSpace(lines.Text()) == "---"; !closed {
			block.WriteString(lines.Text() + "\n")
		}
	}
	var front struct {
		Name  string `yaml:"name"`
		Model string `yaml:"model"`
	}
	if yaml.Unmarshal([]byte(block.String()), &front) != nil || front.Name == "" {
		return "", "", false
	}
	return front.Name, front.Model, true
}
