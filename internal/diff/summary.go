package diff

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// FileStat represents one file's change summary.
type FileStat struct {
	Path       string
	Insertions int
	Deletions  int
}

// TotalStat is the aggregate across all files.
type TotalStat struct {
	FileCount  int
	Insertions int
	Deletions  int
}

// GetDiffStat runs `git diff --stat --no-color` in dir and returns trimmed output.
// Returns empty string if no changes.
func GetDiffStat(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "--stat", "--no-color")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff --stat failed: %w\nOutput: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// GetFullDiff runs `git diff --no-color` in dir.
func GetFullDiff(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-color")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w\nOutput: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// GetFileDiff runs `git diff --no-color -- <file>` in dir for a single file.
func GetFileDiff(dir, file string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-color", "--", file)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff -- %s failed: %w\nOutput: %s", file, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// ListChangedFiles returns the list of file paths with uncommitted changes.
func ListChangedFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--no-color")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only failed: %w", err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// ParseDiffStat parses git diff --stat output.
// Each file line looks like ` path/to/file | 42 +++---`.
// The summary line looks like `3 files changed, 72 insertions(+), 11 deletions(-)`.
func ParseDiffStat(output string) ([]FileStat, TotalStat) {
	if output == "" {
		return nil, TotalStat{}
	}

	var files []FileStat
	var total TotalStat

	// Regex for file lines: ` path/to/file | 42 +++---`
	fileLineRe := regexp.MustCompile(`^\s*(.+?)\s*\|\s*(\d+)`)

	// Regex for total line: `3 files changed, 72 insertions(+), 11 deletions(-)`
	totalLineRe := regexp.MustCompile(`(\d+)\s+files?\s+changed(?:,\s*(\d+)\s+insertions?\([+]\))?(?:,\s*(\d+)\s+deletions?\([-]\))?`)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		// Try to match file line
		if matches := fileLineRe.FindStringSubmatch(line); matches != nil {
			files = append(files, FileStat{
				Path: strings.TrimSpace(matches[1]),
				// We don't have insertions/deletions per file from --stat alone
				// The number after | is total changes, but we can't split it
			})
			continue
		}

		// Try to match total line
		if matches := totalLineRe.FindStringSubmatch(line); matches != nil {
			fileCount, _ := strconv.Atoi(matches[1])
			insertions := 0
			deletions := 0

			if matches[2] != "" {
				insertions, _ = strconv.Atoi(matches[2])
			}
			if matches[3] != "" {
				deletions, _ = strconv.Atoi(matches[3])
			}

			total = TotalStat{
				FileCount:  fileCount,
				Insertions: insertions,
				Deletions:  deletions,
			}
		}
	}

	return files, total
}

// FormatCompact returns a compact multi-line summary:
// `Files changed: N (+X / -Y)\n  M path1\n  M path2\n`.
// Returns empty string if no changes.
func FormatCompact(stat string) string {
	if stat == "" {
		return ""
	}

	files, total := ParseDiffStat(stat)

	if total.FileCount == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Uncommitted: %d files (+%d / -%d)\n",
		total.FileCount, total.Insertions, total.Deletions))

	for _, f := range files {
		sb.WriteString(fmt.Sprintf("  M %s\n", f.Path))
	}

	return sb.String()
}
