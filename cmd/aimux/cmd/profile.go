package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/profile"
	"gopkg.in/yaml.v3"
)

func newProfileCmd(store *profile.Store) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage profiles (list, show, check, migrate; save|get|delete act on the v1 store)",
		Long:  "Profiles are v2 manifests: builtins plus YAML files in $AIMUX_PROFILE_DIR or ~/.config/aimux/profiles.\nThe v1 save, get and delete commands still work on the old store; run aimux profile migrate to convert it.",
	}

	for _, v1 := range []*cobra.Command{newProfileSaveCmd(store), newProfileGetCmd(store), newProfileDeleteCmd(store)} {
		v1.PreRun = func(c *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(c.ErrOrStderr(), migrationNote) // once per invocation
		}
		cmd.AddCommand(v1)
	}
	cmd.AddCommand(newProfileListCmd(store))
	cmd.AddCommand(newProfileShowCmd())
	cmd.AddCommand(newProfileCheckCmd())
	cmd.AddCommand(newProfileMigrateCmd(store))
	return cmd
}

func newProfileSaveCmd(store *profile.Store) *cobra.Command {
	var provider, dir, model, mode string

	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := profile.Profile{
				Name:     args[0],
				Provider: provider,
				Dir:      dir,
				Model:    model,
				Mode:     mode,
			}
			if err := store.Save(p); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}
			if jsonOutput {
				b, _ := json.MarshalIndent(p, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Profile %q saved.\n", args[0])
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&provider, "provider", "", "Provider (claude, codex)")
	cmd.Flags().StringVar(&dir, "dir", "", "Working directory")
	cmd.Flags().StringVar(&model, "model", "", "Model override")
	cmd.Flags().StringVar(&mode, "mode", "", "Mode (plan, auto)")
	return cmd
}

// builtinProfileNames lists the builtin v2 profiles in display order.
var builtinProfileNames = []string{"page", "work-readonly", "research", "research-with-work", "coding", "base"}

const migrationNote = "note: v1 profiles are replaced by ~/.config/aimux/profiles; run aimux profile migrate"

// profileDir is $AIMUX_PROFILE_DIR or ~/.config/aimux/profiles.
func profileDir() string {
	if d := os.Getenv("AIMUX_PROFILE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "aimux", "profiles")
	}
	return filepath.Join(home, ".config", "aimux", "profiles")
}

// loadUserProfiles loads the profile dir and prints each per-file error to
// stderr, one per line. Good profiles are returned even when some files fail.
func loadUserProfiles(cmd *cobra.Command) (map[string]profile.Manifest, error) {
	user, err := profile.LoadDir(profileDir())
	if user == nil {
		return nil, err // dir unreadable: nothing usable
	}
	if err != nil {
		for _, line := range strings.Split(err.Error(), "\n") {
			if line != "" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), line)
			}
		}
	}
	return user, nil
}

func shortDigest(d string) string {
	if len(d) > len("sha256:")+12 {
		return d[:len("sha256:")+12]
	}
	return d
}

type profileRow struct {
	Name      string `json:"name"`
	Runs      string `json:"runs"`
	Available bool   `json:"available"`
	Digest    string `json:"digest"`
}

func newProfileListCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List builtin and user profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := loadUserProfiles(cmd)
			if err != nil {
				return err
			}
			names := append([]string{}, builtinProfileNames...)
			userNames := make([]string, 0, len(user))
			for n := range user {
				userNames = append(userNames, n)
			}
			sort.Strings(userNames)
			names = append(names, userNames...)

			rows := make([]profileRow, 0, len(names))
			for _, n := range names {
				ref := n
				if _, isUser := user[n]; !isUser {
					ref = "builtin:" + n
				}
				m, err := profile.Resolve(ref, user)
				if err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "profile %s: %v\n", n, err)
					rows = append(rows, profileRow{Name: n, Digest: "-"})
					continue
				}
				rows = append(rows, profileRow{Name: n, Runs: m.Runs, Available: m.Available, Digest: m.Digest()})
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"profiles": rows, "count": len(rows)}, "", "  ")
				_, _ = fmt.Fprintln(out, string(b))
				return nil
			}
			_, _ = fmt.Fprintf(out, "%-20s %-8s %-10s %s\n", "NAME", "RUNS", "AVAILABLE", "DIGEST")
			for _, r := range rows {
				_, _ = fmt.Fprintf(out, "%-20s %-8s %-10t %s\n", r.Name, r.Runs, r.Available, shortDigest(r.Digest))
			}
			return nil
		},
	}
}

func newProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a profile resolved through its extends chain, with digest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := loadUserProfiles(cmd)
			if err != nil {
				return err
			}
			m, err := profile.Resolve(args[0], user)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				b, _ := json.MarshalIndent(struct {
					profile.Manifest
					Digest string `json:"digest"`
				}{m, m.Digest()}, "", "  ")
				_, _ = fmt.Fprintln(out, string(b))
				return nil
			}
			b, err := yaml.Marshal(m)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprint(out, string(b))
			_, _ = fmt.Fprintf(out, "digest: %s\n", m.Digest())
			return nil
		},
	}
}

func newProfileCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <name>",
		Short: "Validate a profile; exits 1 with the error if it does not resolve",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			user, err := loadUserProfiles(cmd)
			if err != nil {
				return err
			}
			m, err := profile.Resolve(args[0], user)
			if err != nil {
				return fmt.Errorf("profile %s: %w", args[0], err)
			}
			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"name": m.Name, "ok": true, "digest": m.Digest()}, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: ok %s\n", m.Name, m.Digest())
			return nil
		},
	}
}

func newProfileMigrateCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Write each v1 profile as a v2 file in the profile dir; never overwrites",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := profileDir()
			old := store.List()
			manifests, renames := profile.MigrateV1(old)
			for _, p := range old { // v2 has no mode yet: say so rather than drop it silently
				if p.Mode != "" {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: v1 mode %q is not carried; profiles v2 have no mode field yet\n", p.Name, p.Mode)
				}
			}
			var lines []string
			var written, skipped []string
			if len(manifests) > 0 {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return fmt.Errorf("create profile dir: %w", err)
				}
			}
			for i, m := range manifests {
				if nn, ok := renames[old[i].Name]; ok {
					lines = append(lines, fmt.Sprintf("renamed %s to %s: %s is a builtin profile", old[i].Name, nn, old[i].Name))
				}
				data, err := yaml.Marshal(m)
				if err != nil {
					return fmt.Errorf("marshal %s: %w", m.Name, err)
				}
				path := filepath.Join(dir, m.Name+".yaml")
				// O_EXCL makes "never overwrite" atomic, not a check-then-write race.
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
				if errors.Is(err, os.ErrExist) {
					skipped = append(skipped, m.Name)
					lines = append(lines, fmt.Sprintf("skip %s: %s already exists", m.Name, path))
					continue
				}
				if err != nil {
					return fmt.Errorf("write %s: %w", path, err)
				}
				_, werr := f.Write(data)
				if cerr := f.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					return fmt.Errorf("write %s: %w", path, werr)
				}
				written = append(written, m.Name)
				lines = append(lines, fmt.Sprintf("wrote %s", path))
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				b, _ := json.MarshalIndent(map[string]any{"written": written, "skipped": skipped, "renamed": renames}, "", "  ")
				_, _ = fmt.Fprintln(out, string(b))
				return nil
			}
			if len(lines) == 0 {
				_, _ = fmt.Fprintln(out, "No v1 profiles to migrate.")
			}
			for _, l := range lines {
				_, _ = fmt.Fprintln(out, l)
			}
			return nil
		},
	}
}

func newProfileGetCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show a profile's details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, ok := store.Get(args[0])
			if !ok {
				return fmt.Errorf("profile %q not found (available: %v)", args[0], store.Names())
			}
			if jsonOutput {
				b, _ := json.MarshalIndent(p, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Name:     %s\n", p.Name)
				if p.Provider != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", p.Provider)
				}
				if p.Dir != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Dir:      %s\n", p.Dir)
				}
				if p.Model != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Model:    %s\n", p.Model)
				}
				if p.Mode != "" {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Mode:     %s\n", p.Mode)
				}
			}
			return nil
		},
	}
}

func newProfileDeleteCmd(store *profile.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := store.Delete(args[0]); err != nil {
				return err
			}
			if jsonOutput {
				result := map[string]any{"deleted": args[0]}
				b, _ := json.MarshalIndent(result, "", "  ")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(b))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Profile %q deleted.\n", args[0])
			}
			return nil
		},
	}
}
