package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/secretref"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// serversEnv is what the servers commands read; tests point Home at a temp
// directory.
type serversEnv struct {
	Home string
}

func newServersCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newServersCmdWith(serversEnv{Home: home})
}

func newServersCmdWith(env serversEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "The MCP servers aimux knows: find the secrets a server needs",
	}
	cmd.AddCommand(newServersDiscoverCmd(env), newServersMigrateCmd(env))
	return cmd
}

func newServersDiscoverCmd(_ serversEnv) *cobra.Command {
	var cwd string
	c := &cobra.Command{
		Use:   "discover <name> [-- command args...]",
		Short: "List the variables a server appears to need and suggest a secret reference for each; never reads a value",
		Long: "Reads ${NAME} references in the command, names defined in <cwd>/.env and <cwd>/../.env (names only,\n" +
			"never values), and environment reads in the server's source under <cwd>. Prints one line per finding and a\n" +
			"suggested reference for servers.yaml: a file reference when a .env defines the name, else a Keychain field\n" +
			"of the item aimux/<name>.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, command := args[0], args[1:]
			if cwd == "" && len(command) == 0 {
				return errors.New("give the server's directory with --cwd, or its command after --")
			}
			fs, err := secretref.Discover(secretref.ServerDef{Command: command, Cwd: cwd})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			sug := secretref.Suggest(name, fs)
			if jsonOutput {
				type finding struct {
					Name  string `json:"name,omitempty"`
					Kind  string `json:"kind"`
					Where string `json:"where"`
				}
				items := []finding{}
				for _, f := range fs {
					items = append(items, finding{Name: f.Name, Kind: f.Kind, Where: f.Where})
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Server      string            `json:"server"`
					Findings    []finding         `json:"findings"`
					Suggestions map[string]string `json:"suggestions"`
				}{name, items, sug})
			}
			if len(fs) == 0 {
				_, _ = fmt.Fprintf(out, "%s: no variables found\n", name)
				return nil
			}
			for _, f := range fs {
				if f.Kind == "note" {
					_, _ = fmt.Fprintf(out, "note: stopped scanning %s after many files; some reads may be missing\n", f.Where)
					continue
				}
				_, _ = fmt.Fprintf(out, "%-24s %-12s %s\n", f.Name, f.Kind, f.Where)
			}
			names := make([]string, 0, len(sug))
			for n := range sug {
				names = append(names, n)
			}
			sort.Strings(names)
			_, _ = fmt.Fprintln(out, "\nSuggested references (paste under the server's secrets.env in servers.yaml, one per name):")
			for _, n := range names {
				_, _ = fmt.Fprintf(out, "  %s: %s\n", strconv.Quote(n), sug[n])
			}
			return nil
		},
	}
	c.Flags().StringVar(&cwd, "cwd", "", "The server's working directory (its .env and source are read for names only)")
	return c
}

func newServersMigrateCmd(env serversEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Split tool-sources.yaml into servers.yaml (what exists) and tool-sets.yaml (what sandboxes may use)",
		Long: "Writes ~/.config/aimux/servers.yaml from tool-sources.yaml's sources and ~/.config/aimux/tool-sets.yaml from its\n" +
			"sets, keeping comments, and keeps the old file as tool-sources.yaml.migrated. Server names do not change, so\n" +
			"approved pins still apply. Refuses when either new file exists.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := filepath.Dir(toolreg.DefaultPath(env.Home))
			if err := toolreg.MigrateSplit(dir); err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
					"servers": toolreg.ServersPath(env.Home), "sets": toolreg.SetsPath(env.Home), "migrated": toolreg.DefaultPath(env.Home) + ".migrated"})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s and %s; tool-sources.yaml is kept as tool-sources.yaml.migrated.\n",
				toolreg.ServersPath(env.Home), toolreg.SetsPath(env.Home))
			return nil
		},
	}
}
