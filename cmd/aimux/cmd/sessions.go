package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/history"
)

type sessionsDiscoverFn func(opts history.DiscoverOpts, dir string) ([]history.Session, error)
type sessionsSearchFn func(query, dir string) ([]history.ContentMatch, error)
type sessionsPickerFn func(sessions []history.Session) (history.Session, error)
type sessionsResumeFn func(sessionID string, danger bool)

func newSessionsCmd(discover sessionsDiscoverFn, search sessionsSearchFn, picker sessionsPickerFn, resume sessionsResumeFn) *cobra.Command {
	var dir string
	var listMode, exportMode, danger bool
	var limit int
	var fields string

	cmd := &cobra.Command{
		Use:   "sessions [query]",
		Short: "Browse and search past sessions",
		Long:  "List, search, and resume past AI agent sessions. Without --list, launches interactive picker (TTY only).",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := history.DiscoverOpts{Dir: dir, Limit: 0}
			allSessions, err := discover(opts, "")
			if err != nil {
				return fmt.Errorf("session discovery failed: %w", err)
			}

			var filtered []history.Session
			for _, s := range allSessions {
				if s.TurnCount <= 5 && s.CostUSD == 0 {
					continue
				}
				if s.LastActive.IsZero() {
					continue
				}
				if s.IsSubagent {
					continue
				}
				filtered = append(filtered, s)
			}

			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query != "" {
				filtered = searchSessionsFiltered(filtered, query, search)
				if len(filtered) == 0 {
					return fmt.Errorf("no sessions matching %q", query)
				}
			}

			total := len(filtered)
			truncated := false
			if limit > 0 && len(filtered) > limit {
				filtered = filtered[:limit]
				truncated = true
			}

			if exportMode {
				for _, s := range filtered {
					data, _ := json.Marshal(s)
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				}
				return nil
			}

			if listMode || !IsInteractive() {
				if jsonOutput {
					result := map[string]any{
						"sessions": filtered,
						"count":    len(filtered),
					}
					if truncated {
						result["total"] = total
						result["truncated"] = true
						result["hint"] = "use --limit to control result count"
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					printSessionsTableCobra(cmd, filtered, parseFields(fields))
				}
				return nil
			}

			if picker == nil {
				return fmt.Errorf("interactive mode not available")
			}
			selected, err := picker(filtered)
			if err != nil {
				return err
			}
			if resume != nil {
				resume(selected.ID, danger)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", "", "Scope to a specific directory")
	cmd.Flags().BoolVarP(&listMode, "list", "l", false, "Table output (scriptable)")
	cmd.Flags().BoolVar(&exportMode, "export", false, "JSONL output for eval pipelines")
	cmd.Flags().BoolVarP(&danger, "danger", "d", false, "Resume with --dangerously-skip-permissions")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max sessions to show (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "Comma-separated fields: id,provider,project,age,turns,cost,annotation,prompt,tags")
	return cmd
}

func searchSessionsFiltered(allSessions []history.Session, query string, searchFn sessionsSearchFn) []history.Session {
	matched := history.FilterByPrompt(allSessions, query)
	if len(matched) >= 3 {
		return matched
	}
	if searchFn == nil {
		return matched
	}
	contentMatches, err := searchFn(query, "")
	if err != nil {
		return matched
	}
	seen := make(map[string]bool)
	for _, s := range matched {
		seen[s.ID] = true
	}
	sessionByID := make(map[string]history.Session)
	for _, s := range allSessions {
		sessionByID[s.ID] = s
	}
	for _, cm := range contentMatches {
		if seen[cm.SessionID] {
			continue
		}
		if s, ok := sessionByID[cm.SessionID]; ok {
			matched = append(matched, s)
			seen[cm.SessionID] = true
		}
	}
	return matched
}

func printSessionsTableCobra(cmd *cobra.Command, sessions []history.Session, fields []string) {
	if len(sessions) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
		return
	}
	if len(fields) == 0 {
		fields = []string{"id", "project", "age", "turns", "cost", "prompt"}
	}
	var headers []string
	for _, f := range fields {
		headers = append(headers, strings.ToUpper(f))
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(headers, "\t"))

	for _, s := range sessions {
		var vals []string
		for _, f := range fields {
			switch f {
			case "id":
				vals = append(vals, s.ID)
			case "provider":
				vals = append(vals, s.Provider)
			case "project":
				vals = append(vals, shortProject(s.Project))
			case "age":
				vals = append(vals, shortAgeFmt(s.LastActive))
			case "turns":
				vals = append(vals, fmt.Sprintf("%d", s.TurnCount))
			case "cost":
				vals = append(vals, fmt.Sprintf("$%.2f", s.CostUSD))
			case "annotation":
				a := s.Annotation
				if a == "" {
					a = "-"
				}
				vals = append(vals, a)
			case "prompt":
				p := s.Title
				if p == "" {
					p = s.FirstPrompt
				}
				if len(p) > 40 {
					p = p[:37] + "..."
				}
				if p == "" {
					p = "-"
				}
				vals = append(vals, p)
			case "tags":
				if len(s.Tags) > 0 {
					vals = append(vals, strings.Join(s.Tags, ","))
				} else {
					vals = append(vals, "-")
				}
			default:
				vals = append(vals, "")
			}
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(vals, "\t"))
	}
}

func shortProject(path string) string {
	if path == "" {
		return "(unknown)"
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) > 0 && parts[len(parts)-1] != "" {
		return parts[len(parts)-1]
	}
	return path
}

func shortAgeFmt(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
	}
}
