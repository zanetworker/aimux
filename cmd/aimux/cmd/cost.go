package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/zanetworker/aimux/internal/controller"
)

func newCostCmd() *cobra.Command {
	var since, by, session string
	cmd := &cobra.Command{
		Use:   "cost",
		Short: "Show what your sessions spent: model calls and tokens from the ledger",
		Long: "Read the ledger (~/.aimux/ledger) and sum its model calls per session, per model, per day or per\n" +
			"role (--by role: main for a session's main conversation, subagent, background).\n" +
			"It counts the calls that passed the model proxy. A session that talks to its provider directly is not\n" +
			"in it. The COST column reads unpriced: the counts are real, prices are not applied yet.\n" +
			"SERVED is the model that answered. ASKED AS lists the names the client used when they differ from it,\n" +
			"which is how a Claude model name answered by another engine shows.\n" +
			"What the ledger could not show (calls with no session, unreadable or missing lines) is listed under the table.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := costOptions(since, by, session, time.Now())
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find the home directory for the ledger: %w", err)
			}
			report, err := controller.Meter(filepath.Join(home, ".aimux", "ledger"), opts)
			if errors.Is(err, controller.ErrAmbiguousSession) {
				return err
			}
			if err != nil {
				return fmt.Errorf("read the ledger: %w", err)
			}
			return writeCost(cmd.OutOrStdout(), report, opts.By, since, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&since, "since", "24h", "How far back: a duration such as 90m, 24h or 7d, or all")
	cmd.Flags().StringVar(&by, "by", "session", "Group by session, model, day or role (main, subagent, background)")
	cmd.Flags().StringVar(&session, "session", "", "Only this session: its id, or the start of it")
	return cmd
}

// costOptions turns the flags into meter options. now is when "since" counts back from.
func costOptions(since, by, session string, now time.Time) (controller.MeterOptions, error) {
	opts, err := controller.ParseMeterOptions(since, by, session, now)
	switch {
	case errors.Is(err, controller.ErrMeterBy):
		return opts, fmt.Errorf("unknown --by %q: choose session, model, day or role", by)
	case errors.Is(err, controller.ErrMeterSince):
		return opts, fmt.Errorf("unknown --since %q: use a duration such as 90m, 24h or 7d, or all", since)
	}
	return opts, err
}

func writeCost(out io.Writer, report controller.MeterReport, by controller.MeterBy, since string, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if report.Rows == nil {
			report.Rows = []controller.MeterRow{}
		}
		return enc.Encode(report)
	}
	window := "in the last " + since
	if since == "all" {
		window = "all recorded"
	}
	var text strings.Builder
	if len(report.Rows) == 0 {
		_, _ = fmt.Fprintf(&text, "No model calls recorded %s.\n", window)
		// Only when there is no ledger at all: on a quiet day the proxy is doing its job.
		if report.Files == 0 {
			text.WriteString("The model proxy writes the ledger once it runs a build that has it: aimux upgrade model-proxy\n")
		}
	} else {
		if since == "all" {
			text.WriteString("Model calls, all recorded\n\n")
		} else {
			_, _ = fmt.Fprintf(&text, "Model calls %s\n\n", window)
		}
		header := map[controller.MeterBy]string{controller.MeterBySession: "SESSION", controller.MeterByModel: "MODEL", controller.MeterByDay: "DAY", controller.MeterByRole: "ROLE"}[by]
		served := by != controller.MeterByModel
		table := tabwriter.NewWriter(&text, 0, 0, 2, ' ', 0)
		row := func(key, asked, models string, r controller.MeterRow) {
			cells := []string{key, asked}
			if served {
				cells = append(cells, models)
			} else {
				cells = append(cells, costNames(r.Engines))
			}
			cells = append(cells, costNumbers(r)...)
			_, _ = fmt.Fprintln(table, strings.Join(cells, "\t"))
		}
		// By model the row is the served model, so the column beside it names the engine that served it.
		columns := []string{header, "ASKED AS"}
		if served {
			columns = append(columns, "SERVED")
		} else {
			columns = append(columns, "ENGINE")
		}
		_, _ = fmt.Fprintln(table, strings.Join(append(columns, "CALLS", "FAILED", "IN", "CACHED", "OUT", "COST"), "\t"))
		for _, r := range report.Rows {
			key := r.Key
			// The first eight characters of a session id, which --session accepts back.
			if by == controller.MeterBySession && len(key) > 8 {
				key = key[:8]
			}
			models := r.Models
			if !served {
				models = []string{r.Key}
			}
			row(printable(key), costAskedAs(r.Asked, models), costNames(r.Models), r)
		}
		_, _ = fmt.Fprintln(table, strings.Join(totalCells(report.Total), "\t"))
		_ = table.Flush()
	}
	var notes []string
	if report.Total.Estimated {
		notes = append(notes, "~ usage was estimated for some of these calls")
	}
	if report.NoUsage > 0 {
		notes = append(notes, plural(report.NoUsage, "call", "calls")+" carried no token figures (counted as requests; their tokens are in no sum)")
	}
	if report.Unmatched > 0 {
		notes = append(notes, plural(report.Unmatched, "call", "calls")+" with no session (counted in no row and not in the total)")
	}
	if report.Skipped > 0 {
		notes = append(notes, plural(report.Skipped, "line", "lines")+" skipped: written by another version of aimux")
	}
	if report.Torn > 0 {
		notes = append(notes, plural(report.Torn, "line", "lines")+" not whole")
	}
	writers := make([]string, 0, len(report.Gaps))
	for writer := range report.Gaps {
		writers = append(writers, writer)
	}
	slices.Sort(writers)
	for _, writer := range writers {
		missing, ranges := uint64(0), []string{}
		for _, gap := range report.Gaps[writer] {
			missing += gap.To - gap.From + 1
			if gap.From == gap.To {
				ranges = append(ranges, fmt.Sprintf("line %d", gap.From))
			} else {
				ranges = append(ranges, fmt.Sprintf("line %d to %d", gap.From, gap.To))
			}
		}
		notes = append(notes, fmt.Sprintf("%s missing from %s (%s)", plural(int(missing), "line", "lines"), printable(writer), // #nosec G115 -- a line count
			strings.Join(ranges, ", ")))
	}
	if report.Incomplete {
		notes = append(notes, "totals are incomplete")
	}
	if len(notes) > 0 {
		text.WriteString("\n" + strings.Join(notes, "\n") + "\n")
	}
	_, err := io.WriteString(out, text.String())
	return err
}

// totalCells is the last row: the sums, with nothing under the name columns.
func totalCells(r controller.MeterRow) []string {
	// Under the key, ASKED AS, and SERVED or ENGINE.
	return append([]string{"total", "", ""}, costNumbers(r)...)
}

// costNumbers is a row's counts. Every token count of a row with estimated usage is marked, the cached one too.
func costNumbers(r controller.MeterRow) []string {
	mark := func(n int64) string {
		if r.Estimated && n > 0 {
			return "~" + costCount(n)
		}
		return costCount(n)
	}
	return []string{strconv.Itoa(r.Requests), strconv.Itoa(r.Failed), mark(r.InputTokens), mark(r.CachedTokens), mark(r.OutputTokens), "unpriced"}
}

// costAskedAs is the names the client asked for that are not the model that answered, or "-" when it asked for
// what it got.
func costAskedAs(asked, served []string) string {
	var other []string
	for _, name := range asked {
		if !slices.Contains(served, name) {
			other = append(other, name)
		}
	}
	if len(other) == 0 {
		return "-"
	}
	return costNames(other)
}

// costNames joins model names for a cell, cut so one long list cannot widen the table past a terminal.
func costNames(names []string) string {
	clean := make([]string, len(names))
	for i, name := range names {
		clean[i] = printable(name)
	}
	return cut(strings.Join(clean, ", "), 40)
}

// costCount is a token count as a table shows it: whole below a thousand, then with one decimal.
func costCount(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999950:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}
