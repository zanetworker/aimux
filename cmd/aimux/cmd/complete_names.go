package cmd

import (
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/zanetworker/aimux/internal/parts"
)

// What Tab offers after a command that takes a name. The names an edit of the config file would take come from
// parts.EditCandidates, which applies the edits' own rules, so nothing is offered that the command would refuse.
// A completion never fails: it offers nothing instead.

// completer is cobra's ValidArgsFunction.
type completer = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// completeWords offers, for the word at each position, what at(position, words so far) lists. No file names.
func completeWords(at func(position int, before []string) []string) completer {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return at(len(args), args), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeEach offers every one of all() that is not on the command line yet.
func completeEach(all func() []string) completer {
	return completeWords(func(_ int, before []string) []string {
		return slices.DeleteFunc(all(), func(name string) bool { return slices.Contains(before, name) })
	})
}

// completeEdit offers, for each word of an edit of the config file under home, the list pick names: pick is
// given what the file would take now and the word's position. A model is never offered: it is typed, since a
// name with a colon in it does not survive every shell's completion whole.
func completeEdit(home string, pick func(c parts.Candidates, position int) []string) completer {
	return completeWords(func(position int, _ []string) []string {
		if home == "" {
			return nil
		}
		return pick(parts.EditCandidates(filepath.Join(home, ".aimux", "config.yaml")), position)
	})
}

// first gives list for the first word and nothing after it.
func first(list func(parts.Candidates) []string) func(parts.Candidates, int) []string {
	return func(c parts.Candidates, position int) []string {
		if position > 0 {
			return nil
		}
		return list(c)
	}
}
