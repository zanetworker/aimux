package cmd

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/zanetworker/aimux/internal/modelproxy"
)

// modelProxyStats prints the summary modelproxy builds from the proxy's request history. Reading and counting
// live in the package that writes the history; this is the rendering.
func modelProxyStats(path string, window time.Duration, out io.Writer, now func() time.Time) error {
	stats, err := modelproxy.ReadRequestStats(path, window, now())
	if err != nil {
		return err
	}
	if stats.Total == 0 {
		_, _ = fmt.Fprintf(out, "No requests in the last %s (%s).\n", window, path)
		writeHistoryWarnings(out, stats, window)
		return nil
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(table, "%d requests in the last %s\n\n", stats.Total, window)
	_, _ = fmt.Fprintln(table, "OUTCOME\tN\tMEAN\tP95\tPROMPT\tFAILED")
	for _, outcome := range stats.Outcomes {
		_, _ = fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%d\n", outcome.Outcome, outcome.Count,
			shortDuration(outcome.Mean), shortDuration(outcome.P95), tokens(outcome), outcome.Failed)
	}
	_ = table.Flush()
	writeFallbackDetails(out, stats.Outcomes)
	writeHistoryWarnings(out, stats, window)
	return nil
}

// writeHistoryWarnings says what the summary could not read, including when that left it with nothing to show.
func writeHistoryWarnings(out io.Writer, stats modelproxy.RequestStats, window time.Duration) {
	if stats.Damaged > 0 {
		_, _ = fmt.Fprintf(out, "\n%d damaged line(s) skipped.\n", stats.Damaged)
	}
	if stats.Incomplete {
		_, _ = fmt.Fprintf(out, "\nThe history could not be read to its end, so this covers less than %s.\n", window)
	}
}

// writeFallbackDetails prints what the fallbacks said about themselves, which is where the next fix usually hides.
func writeFallbackDetails(out io.Writer, outcomes []modelproxy.OutcomeStats) {
	type detail struct {
		outcome, text string
		count         int
	}
	var details []detail
	for _, outcome := range outcomes {
		for text, count := range outcome.Details {
			details = append(details, detail{outcome.Outcome, text, count})
		}
	}
	if len(details) == 0 {
		return
	}
	sort.Slice(details, func(i, j int) bool {
		if details[i].count != details[j].count {
			return details[i].count > details[j].count
		}
		return details[i].text < details[j].text
	})
	_, _ = fmt.Fprintln(out, "\nWHY THE FALLBACKS HAPPENED")
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, d := range details {
		if i == 5 {
			_, _ = fmt.Fprintf(table, "\t%d more shapes\n", len(details)-i)
			break
		}
		_, _ = fmt.Fprintf(table, "%d\t%s: %s\n", d.count, d.outcome, d.text)
	}
	_ = table.Flush()
}

func shortDuration(value time.Duration) string {
	return value.Round(100 * time.Millisecond).String()
}

// tokens shows a dash when no request of this kind reported a prompt size, which is not the same as an empty one.
func tokens(outcome modelproxy.OutcomeStats) string {
	if outcome.PromptSamples == 0 {
		return "-"
	}
	if outcome.MeanPrompt >= 1000 {
		return fmt.Sprintf("%.1fk", float64(outcome.MeanPrompt)/1000)
	}
	return fmt.Sprintf("%d", outcome.MeanPrompt)
}
