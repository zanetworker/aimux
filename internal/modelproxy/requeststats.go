package modelproxy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// OutcomeStats is how one kind of request went over a window.
type OutcomeStats struct {
	Outcome string
	Count   int
	// Failed counts the requests the client was told about, by status or by a recorded failure.
	Failed int
	Mean   time.Duration
	P95    time.Duration
	// MeanPrompt averages only the requests whose prompt size Codex reported; PromptSamples is how many that
	// was. A turn paused at a tool call reports none, and counting it as zero would halve the average.
	MeanPrompt    int
	PromptSamples int
	// Details counts what each fallback said about itself.
	Details map[string]int
}

// RequestStats summarises a window of the proxy's request history.
type RequestStats struct {
	Window   time.Duration
	Total    int
	Outcomes []OutcomeStats
	// Damaged counts lines that could not be read; Incomplete says the file could not be read to its end, so
	// the summary covers less than it claims.
	Damaged    int
	Incomplete bool
}

// ReadRequestStats summarises the request history at path, and its one rolled-over file, over the window ending
// at now. It reads files only, so it works while the proxy runs.
func ReadRequestStats(path string, window time.Duration, now time.Time) (RequestStats, error) {
	stats := RequestStats{Window: window}
	records, damaged, incomplete, err := readRequestRecords(path, now.Add(-window))
	if err != nil {
		return stats, err
	}
	stats.Damaged, stats.Incomplete = damaged, incomplete
	byOutcome := map[string]*OutcomeStats{}
	durations := map[string][]time.Duration{}
	prompts := map[string]int{}
	for _, record := range records {
		stats.Total++
		outcome := byOutcome[record.Outcome]
		if outcome == nil {
			outcome = &OutcomeStats{Outcome: record.Outcome, Details: map[string]int{}}
			byOutcome[record.Outcome] = outcome
		}
		outcome.Count++
		durations[record.Outcome] = append(durations[record.Outcome], time.Duration(record.DurationMs)*time.Millisecond)
		// An estimated prompt is the last known size copied forward, not a measurement of this request.
		if prompt := record.InputTokens + record.CachedInputTokens; prompt > 0 && !record.Estimated {
			outcome.PromptSamples++
			prompts[record.Outcome] += prompt
		}
		if record.Error != "" || record.Status >= 400 {
			outcome.Failed++
		}
		if record.Detail != "" {
			outcome.Details[record.Detail]++
		}
	}
	for name, outcome := range byOutcome {
		outcome.Mean = meanDuration(durations[name])
		outcome.P95 = percentileDuration(durations[name], 95)
		if outcome.PromptSamples > 0 {
			outcome.MeanPrompt = prompts[name] / outcome.PromptSamples
		}
		stats.Outcomes = append(stats.Outcomes, *outcome)
	}
	// Slowest first: the row worth reading is the one costing the most time.
	sort.Slice(stats.Outcomes, func(i, j int) bool {
		if stats.Outcomes[i].Mean != stats.Outcomes[j].Mean {
			return stats.Outcomes[i].Mean > stats.Outcomes[j].Mean
		}
		return stats.Outcomes[i].Outcome < stats.Outcomes[j].Outcome
	})
	return stats, nil
}

func readRequestRecords(path string, since time.Time) ([]line, int, bool, error) {
	// Read the archive first, then the live file. A rollover in between would hide a whole file, so the live
	// file's identity is checked afterwards and the pair read again when it changed.
	for attempt := 0; ; attempt++ {
		before, _ := os.Stat(path)
		records, damaged, incomplete, found, err := readRecordFiles(path, since)
		if err != nil {
			return nil, 0, false, err
		}
		if !found {
			return nil, 0, false, fmt.Errorf("no request history at %s yet: it is written by a proxy running this aimux", path)
		}
		after, _ := os.Stat(path)
		if sameFile(before, after) {
			return records, damaged, incomplete, nil
		}
		if attempt > 0 {
			// It rotated again during the retry: a file may have slipped past unread, so say the summary is
			// not the whole window rather than imply it is.
			return records, damaged, true, nil
		}
	}
}

func readRecordFiles(path string, since time.Time) (records []line, damaged int, incomplete, found bool, err error) {
	for _, name := range []string{path + ".1", path} {
		file, openErr := os.Open(name) // #nosec G304 -- the owner's configured history path
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				continue
			}
			return nil, 0, false, false, fmt.Errorf("read the model proxy request history: %w", openErr)
		}
		found = true
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				continue
			}
			var record line
			if json.Unmarshal([]byte(text), &record) != nil {
				damaged++
				continue
			}
			at, parseErr := time.Parse(time.RFC3339, record.At)
			if parseErr != nil || at.Before(since) {
				if parseErr != nil {
					damaged++
				}
				continue
			}
			records = append(records, record)
		}
		// A line longer than the buffer stops the scan, so the rest of that file is missing from the summary.
		if scanner.Err() != nil {
			incomplete = true
		}
		_ = file.Close()
	}
	return records, damaged, incomplete, found, nil
}

func sameFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && after.Size() >= before.Size()
}

func meanDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var total time.Duration
	for _, v := range values {
		total += v
	}
	return total / time.Duration(len(values))
}

// percentileDuration returns the value at the pth percentile, as a zero-based rank: with 20 values, p95 is the
// 19th, not the slowest.
func percentileDuration(values []time.Duration, p int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (len(sorted)*p + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
