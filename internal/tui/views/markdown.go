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
	mdH4Style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF")).Bold(true)
	mdHRStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#374151"))
	mdCodeLangStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).Italic(true)
	mdCodeGutterStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#4B5563"))
	mdCodeLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#93C5FD")).
			Background(lipgloss.Color("#1A1A2E"))
	mdCodeKeywordStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#C678DD")).
				Background(lipgloss.Color("#1A1A2E")).Bold(true)
	mdCodeStringStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#98C379")).
				Background(lipgloss.Color("#1A1A2E"))
	mdCodeCommentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#5C6370")).Italic(true).
				Background(lipgloss.Color("#1A1A2E"))
	mdCodeBgStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1A1A2E"))
	mdBoldStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D1FAE5")).Bold(true)
	mdInlineCodeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F9A8D4"))
	mdBulletStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	mdNumberStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	mdTableBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#4B5563"))
	mdTableHeaderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB")).Bold(true)
)

var (
	boldRe       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	numberedRe   = regexp.MustCompile(`^(\s*)(\d+\.\s)(.*)$`)
	tableSepRe   = regexp.MustCompile(`^\|[\s:]*-+[\s:]*[-|\s:]*\|$`)
	tableRowRe   = regexp.MustCompile(`^\|.*\|$`)
)

// renderMarkdownLines renders a slice of output lines with markdown-aware
// styling. It tracks fenced code block state across lines.
//
// Key design: markdown rendering happens BEFORE width truncation so that
// markers like ** and ` are consumed by the regex before any truncation
// can split them. Width limiting uses lipgloss.MaxWidth which respects
// ANSI escape sequences.
//
// Tables are collected as consecutive runs of | rows and rendered as a
// block with aligned columns and box-drawing borders.
func renderMarkdownLines(lines []string, innerW int) []string {
	var result []string

	maxW := lipgloss.NewStyle().MaxWidth(innerW)

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Collect fenced code block as a unit
		if strings.HasPrefix(trimmed, "```") {
			lang := strings.TrimPrefix(trimmed, "```")
			lang = strings.TrimSpace(lang)
			i++ // skip opening fence
			var codeLines []string
			for i < len(lines) {
				if strings.TrimSpace(lines[i]) == "```" {
					i++ // skip closing fence
					break
				}
				codeLines = append(codeLines, lines[i])
				i++
			}
			rendered := renderCodeBlock(codeLines, lang, innerW)
			for _, cl := range rendered {
				result = append(result, "    "+cl)
			}
			continue
		}

		// Collect consecutive table rows into a block
		if tableRowRe.MatchString(trimmed) {
			var tableLines []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if tableRowRe.MatchString(t) || tableSepRe.MatchString(t) {
					tableLines = append(tableLines, t)
					i++
				} else {
					break
				}
			}
			rendered := renderTable(tableLines, innerW)
			for _, tl := range rendered {
				result = append(result, "    "+tl)
			}
			continue
		}

		rendered := renderMarkdownLine(line)
		result = append(result, "    "+maxW.Render(rendered))
		i++
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

	// Horizontal rule
	if trimmed == "---" || trimmed == "***" || trimmed == "___" {
		return leading + mdHRStyle.Render(strings.Repeat("─", 40))
	}

	// Headings (check longer prefixes first)
	if strings.HasPrefix(trimmed, "#### ") {
		return leading + mdH4Style.Render(trimmed[5:])
	}
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

// parseTableCells splits a |-delimited row into cell strings, trimming whitespace.
func parseTableCells(line string) []string {
	// Remove leading/trailing |
	inner := strings.TrimPrefix(line, "|")
	inner = strings.TrimSuffix(inner, "|")
	parts := strings.Split(inner, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

// renderTable renders a block of consecutive markdown table lines with
// aligned columns and box-drawing borders.
//
// Layout:
//
//	┌──────────┬──────┬──────────┐
//	│ Approach │ How  │ Tradeoff │  ← header (bold)
//	├──────────┼──────┼──────────┤
//	│ HPA      │ Auto │ Complex  │  ← data rows
//	└──────────┴──────┴──────────┘
func renderTable(lines []string, maxWidth int) []string {
	if len(lines) == 0 {
		return nil
	}

	// Parse all rows, identify separator
	type row struct {
		cells []string
		isSep bool
	}
	var rows []row
	sepIdx := -1
	for i, line := range lines {
		if tableSepRe.MatchString(line) {
			rows = append(rows, row{isSep: true})
			if sepIdx < 0 {
				sepIdx = i
			}
		} else {
			rows = append(rows, row{cells: parseTableCells(line)})
		}
	}

	// Determine number of columns and max width per column
	numCols := 0
	for _, r := range rows {
		if !r.isSep && len(r.cells) > numCols {
			numCols = len(r.cells)
		}
	}
	if numCols == 0 {
		return nil
	}

	colWidths := make([]int, numCols)
	for _, r := range rows {
		if r.isSep {
			continue
		}
		for j := 0; j < numCols && j < len(r.cells); j++ {
			w := utf8.RuneCountInString(r.cells[j])
			if w > colWidths[j] {
				colWidths[j] = w
			}
		}
	}

	// Cap column widths so the table fits within maxWidth
	// Account for borders: │ col │ col │ = 1 + (colW+2)*numCols + 1 padding
	totalBorderChars := 1 + numCols // left │ + one │ per column
	totalPadding := numCols * 2     // 1 space each side per column
	availContent := maxWidth - totalBorderChars - totalPadding
	if availContent < numCols {
		availContent = numCols
	}

	// Proportionally shrink columns if they exceed available space
	totalContent := 0
	for _, w := range colWidths {
		totalContent += w
	}
	if totalContent > availContent {
		for j := range colWidths {
			colWidths[j] = colWidths[j] * availContent / totalContent
			if colWidths[j] < 1 {
				colWidths[j] = 1
			}
		}
	}

	bs := mdTableBorderStyle

	// Build border lines
	topBorder := buildBorderLine(colWidths, "┌", "┬", "┐")
	midBorder := buildBorderLine(colWidths, "├", "┼", "┤")
	botBorder := buildBorderLine(colWidths, "└", "┴", "┘")

	var result []string
	result = append(result, bs.Render(topBorder))

	for i, r := range rows {
		if r.isSep {
			result = append(result, bs.Render(midBorder))
			continue
		}

		// Determine if this is a header row (before the separator)
		isHeader := sepIdx > 0 && i < sepIdx

		var line strings.Builder
		line.WriteString(bs.Render("│"))
		for j := 0; j < numCols; j++ {
			cell := ""
			if j < len(r.cells) {
				cell = r.cells[j]
			}
			// Truncate cell if needed
			cellRunes := []rune(cell)
			if len(cellRunes) > colWidths[j] {
				if colWidths[j] > 3 {
					cell = string(cellRunes[:colWidths[j]-3]) + "..."
				} else {
					cell = string(cellRunes[:colWidths[j]])
				}
			}
			// Pad to column width
			pad := colWidths[j] - utf8.RuneCountInString(cell)
			padded := cell + strings.Repeat(" ", pad)

			line.WriteString(" ")
			if isHeader {
				line.WriteString(mdTableHeaderStyle.Render(padded))
			} else {
				line.WriteString(renderInlineMarkdown(padded))
			}
			line.WriteString(" ")
			line.WriteString(bs.Render("│"))
		}
		result = append(result, line.String())
	}

	result = append(result, bs.Render(botBorder))
	return result
}

// buildBorderLine creates a table border line like ┌──────┬──────┐
func buildBorderLine(colWidths []int, left, mid, right string) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range colWidths {
		b.WriteString(strings.Repeat("─", w+2)) // +2 for padding spaces
		if i < len(colWidths)-1 {
			b.WriteString(mid)
		}
	}
	b.WriteString(right)
	return b.String()
}

// renderCodeBlock renders a fenced code block with a visual container:
// a language label, left gutter, dark background, and basic syntax highlighting.
//
// Layout:
//
//	╭─ python ──────────────╮
//	│ def hello():          │
//	│     print("world")    │
//	╰───────────────────────╯
func renderCodeBlock(lines []string, lang string, maxWidth int) []string {
	if maxWidth < 10 {
		maxWidth = 10
	}

	// Content width = maxWidth minus gutter (│ ) and right padding
	contentW := maxWidth - 4 // "│ " prefix + " │" suffix
	if contentW < 4 {
		contentW = 4
	}

	bs := mdCodeGutterStyle
	bg := mdCodeBgStyle

	var result []string

	// Top border with language label
	if lang != "" {
		labelPart := "─ " + lang + " "
		ruleLen := maxWidth - utf8.RuneCountInString(labelPart) - 2 // ╭ and ╮
		if ruleLen < 1 {
			ruleLen = 1
		}
		topLine := bs.Render("╭"+labelPart) + bs.Render(strings.Repeat("─", ruleLen)+"╮")
		result = append(result, topLine)
	} else {
		topLine := bs.Render("╭" + strings.Repeat("─", maxWidth-2) + "╮")
		result = append(result, topLine)
	}

	// Code lines with gutter and background
	for _, line := range lines {
		// Truncate if needed
		visLine := line
		if utf8.RuneCountInString(visLine) > contentW {
			runes := []rune(visLine)
			if contentW > 3 {
				visLine = string(runes[:contentW-3]) + "..."
			} else {
				visLine = string(runes[:contentW])
			}
		}

		// Pad to fill background
		pad := contentW - utf8.RuneCountInString(visLine)
		if pad < 0 {
			pad = 0
		}

		styled := highlightCode(visLine, lang) + bg.Render(strings.Repeat(" ", pad))
		codeLine := bs.Render("│") + " " + styled + " " + bs.Render("│")
		result = append(result, codeLine)
	}

	// Handle empty code blocks
	if len(lines) == 0 {
		emptyLine := bs.Render("│") + " " + bg.Render(strings.Repeat(" ", contentW)) + " " + bs.Render("│")
		result = append(result, emptyLine)
	}

	// Bottom border
	botLine := bs.Render("╰" + strings.Repeat("─", maxWidth-2) + "╯")
	result = append(result, botLine)

	return result
}

// Common keywords for basic syntax highlighting across languages.
var codeKeywords = map[string]bool{
	// Go
	"func": true, "return": true, "if": true, "else": true,
	"for": true, "range": true, "var": true, "const": true,
	"type": true, "struct": true, "interface": true, "package": true,
	"import": true, "defer": true, "go": true, "chan": true,
	"select": true, "case": true, "switch": true, "default": true,
	"break": true, "continue": true, "map": true, "make": true,
	"nil": true, "true": true, "false": true,
	// Python
	"def": true, "class": true, "self": true, "None": true,
	"True": true, "False": true, "from": true, "as": true,
	"with": true, "try": true, "except": true, "finally": true,
	"raise": true, "yield": true, "lambda": true, "pass": true,
	"assert": true, "del": true, "global": true, "nonlocal": true,
	"async": true, "await": true, "in": true, "not": true,
	"and": true, "or": true, "is": true, "elif": true,
	// JS/TS
	"function": true, "let": true, "new": true, "this": true,
	"throw": true, "catch": true, "typeof": true, "instanceof": true,
	"export": true, "extends": true, "implements": true,
	"null": true, "undefined": true, "console": true,
	// Rust
	"fn": true, "mut": true, "pub": true, "mod": true,
	"use": true, "impl": true, "trait": true, "enum": true,
	"match": true, "loop": true, "while": true, "move": true,
	"Some": true, "Ok": true, "Err": true,
	// Shared
	"print": true, "println": true, "printf": true, "fmt": true,
}

// highlightCode applies basic syntax coloring to a code line.
// Not a full parser — just enough to make code blocks visually distinct.
func highlightCode(line, lang string) string {
	bg := lipgloss.Color("#1A1A2E")

	// Comment detection
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		leading := line[:len(line)-len(trimmed)]
		return mdCodeLineStyle.Render(leading) + mdCodeCommentStyle.Render(trimmed)
	}

	// Token-level highlighting
	const sentinel = "\x00"
	result := line

	// Highlight strings (double-quoted and single-quoted)
	stringRe := regexp.MustCompile(`"[^"]*"|'[^']*'`)
	result = stringRe.ReplaceAllStringFunc(result, func(m string) string {
		return sentinel + mdCodeStringStyle.Render(m) + sentinel
	})

	// Highlight keywords (only whole words not inside strings)
	parts := strings.Split(result, sentinel)
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.Contains(part, "\x1b[") {
			// Already styled (string literal)
			b.WriteString(part)
			continue
		}
		// Split into words and style keywords
		b.WriteString(highlightKeywords(part, bg))
	}

	return b.String()
}

// highlightKeywords colors keyword tokens within a plain text segment.
func highlightKeywords(text string, bg lipgloss.Color) string {
	kwStyle := mdCodeKeywordStyle
	plainStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#93C5FD")).Background(bg)

	// Split on word boundaries by iterating through runes
	var b strings.Builder
	word := ""
	for _, r := range text {
		if isWordChar(r) {
			word += string(r)
		} else {
			if word != "" {
				if codeKeywords[word] {
					b.WriteString(kwStyle.Render(word))
				} else {
					b.WriteString(plainStyle.Render(word))
				}
				word = ""
			}
			b.WriteString(plainStyle.Render(string(r)))
		}
	}
	if word != "" {
		if codeKeywords[word] {
			b.WriteString(kwStyle.Render(word))
		} else {
			b.WriteString(plainStyle.Render(word))
		}
	}
	return b.String()
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// renderInlineMarkdown handles bold (**text**) and inline code (`code`)
// within a line of text. Unstyled segments get the default output color.
// Also strips orphaned ** markers left by truncation or multi-line bold.
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

	// Strip orphaned markers that couldn't be matched (e.g., unclosed **)
	text = strings.ReplaceAll(text, "**", "")
	// Strip orphaned backticks only if they appear alone (not in ANSI escapes)
	if !strings.Contains(text, "\x1b[") {
		text = strings.ReplaceAll(text, "`", "")
	}

	// If no styled replacements happened, style the whole line
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
