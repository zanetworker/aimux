package views

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// Markdown-aware styles for trace output rendering.
var (
	mdH1Style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F9FAFB")).Bold(true)
	mdH2Style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB")).Bold(true)
	mdH3Style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D1D5DB")).Bold(true).Italic(true)
	mdCodeFenceStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))
	mdCodeLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#93C5FD"))
	mdBoldStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D1FAE5")).Bold(true)
	mdInlineCodeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F9A8D4"))
	mdBulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	mdNumberStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4")).Bold(true)
)

var (
	boldRe       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	numberedRe   = regexp.MustCompile(`^(\s*)(\d+\.\s)(.*)$`)
)

// truncateRunes truncates a string to maxRunes visible runes, appending "..."
// if truncation occurred. Safe for multi-byte UTF-8.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 3 {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-3]) + "..."
}

// renderMarkdownLines renders a slice of output lines with markdown-aware
// styling. It tracks fenced code block state across lines.
func renderMarkdownLines(lines []string, innerW int) []string {
	inCodeBlock := false
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Fenced code block toggle
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			line = truncateRunes(line, innerW)
			result = append(result, "    "+mdCodeFenceStyle.Render(line))
			continue
		}

		if inCodeBlock {
			line = truncateRunes(line, innerW)
			result = append(result, "    "+mdCodeLineStyle.Render(line))
			continue
		}

		line = truncateRunes(line, innerW)
		rendered := renderMarkdownLine(line)
		result = append(result, "    "+rendered)
	}

	return result
}

// renderMarkdownLine applies markdown styling to a single line.
func renderMarkdownLine(line string) string {
	trimmed := strings.TrimSpace(line)
	leading := ""
	if len(line) > len(trimmed) {
		leading = line[:len(line)-len(trimmed)]
	}

	// Headings
	if strings.HasPrefix(trimmed, "### ") {
		return leading + mdH3Style.Render(trimmed[4:])
	}
	if strings.HasPrefix(trimmed, "## ") {
		return leading + mdH2Style.Render(trimmed[3:])
	}
	if strings.HasPrefix(trimmed, "# ") {
		return leading + mdH1Style.Render(trimmed[2:])
	}

	// Bullet lists
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		bullet := trimmed[:2]
		rest := trimmed[2:]
		return leading + mdBulletStyle.Render(bullet) + renderInlineMarkdown(rest)
	}

	// Numbered lists
	if m := numberedRe.FindStringSubmatch(line); m != nil {
		return m[1] + mdNumberStyle.Render(m[2]) + renderInlineMarkdown(m[3])
	}

	// Plain text with inline formatting
	return leading + renderInlineMarkdown(trimmed)
}

// renderInlineMarkdown handles bold (**text**) and inline code (`code`)
// within a line of text. Unstyled segments get the default output color.
func renderInlineMarkdown(text string) string {
	// Use a sentinel to split around styled replacements
	const sentinel = "\x00"

	// Replace bold with styled text + sentinels
	text = boldRe.ReplaceAllStringFunc(text, func(match string) string {
		inner := match[2 : len(match)-2]
		return sentinel + mdBoldStyle.Render(inner) + sentinel
	})

	// Replace inline code with styled text + sentinels
	text = inlineCodeRe.ReplaceAllStringFunc(text, func(match string) string {
		inner := match[1 : len(match)-1]
		return sentinel + mdInlineCodeStyle.Render(inner) + sentinel
	})

	// If no replacements happened, style the whole line
	if !strings.Contains(text, sentinel) {
		return outputTextStyle.Render(text)
	}

	// Style unstyled segments with outputTextStyle
	parts := strings.Split(text, sentinel)
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		// Parts containing ANSI escapes are already styled
		if strings.Contains(part, "\x1b[") {
			b.WriteString(part)
		} else {
			b.WriteString(outputTextStyle.Render(part))
		}
	}
	return b.String()
}
