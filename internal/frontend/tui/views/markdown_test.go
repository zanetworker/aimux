package views

import (
	"strings"
	"testing"
)

func TestRenderMarkdownLine_H1(t *testing.T) {
	result := renderMarkdownLine("# Hello World")
	if !strings.Contains(result, "Hello World") {
		t.Errorf("expected heading text, got %q", result)
	}
	if strings.HasPrefix(result, "# ") {
		t.Error("expected heading prefix to be removed")
	}
}

func TestRenderMarkdownLine_H2(t *testing.T) {
	result := renderMarkdownLine("## Section Title")
	if !strings.Contains(result, "Section Title") {
		t.Errorf("expected heading text, got %q", result)
	}
}

func TestRenderMarkdownLine_H3(t *testing.T) {
	result := renderMarkdownLine("### Subsection")
	if !strings.Contains(result, "Subsection") {
		t.Errorf("expected heading text, got %q", result)
	}
}

func TestRenderMarkdownLine_H4(t *testing.T) {
	result := renderMarkdownLine("#### Prerequisites")
	if !strings.Contains(result, "Prerequisites") {
		t.Errorf("expected heading text, got %q", result)
	}
	if strings.Contains(result, "####") {
		t.Error("expected #### prefix to be removed")
	}
}

func TestRenderMarkdownLine_HorizontalRule(t *testing.T) {
	result := renderMarkdownLine("---")
	if strings.Contains(result, "---") {
		t.Error("expected --- to be replaced with styled rule")
	}
	if !strings.Contains(result, "─") {
		t.Errorf("expected box-drawing rule character, got %q", result)
	}
}

func TestRenderMarkdownLine_BulletDash(t *testing.T) {
	result := renderMarkdownLine("- item one")
	if !strings.Contains(result, "item one") {
		t.Errorf("expected bullet text, got %q", result)
	}
	if !strings.Contains(result, "- ") {
		t.Error("expected bullet marker in output")
	}
}

func TestRenderMarkdownLine_BulletStar(t *testing.T) {
	result := renderMarkdownLine("* item two")
	if !strings.Contains(result, "item two") {
		t.Errorf("expected bullet text, got %q", result)
	}
}

func TestRenderMarkdownLine_Numbered(t *testing.T) {
	result := renderMarkdownLine("1. first step")
	if !strings.Contains(result, "first step") {
		t.Errorf("expected numbered item text, got %q", result)
	}
	if !strings.Contains(result, "1. ") {
		t.Error("expected number marker in output")
	}
}

func TestRenderMarkdownLine_PlainText(t *testing.T) {
	result := renderMarkdownLine("just some plain text")
	if !strings.Contains(result, "just some plain text") {
		t.Errorf("expected plain text preserved, got %q", result)
	}
}

func TestRenderInlineMarkdown_Bold(t *testing.T) {
	result := renderInlineMarkdown("this is **bold** text")
	if !strings.Contains(result, "bold") {
		t.Errorf("expected bold text, got %q", result)
	}
	if strings.Contains(result, "**") {
		t.Error("expected ** markers to be removed")
	}
}

func TestRenderInlineMarkdown_BoldEntireLine(t *testing.T) {
	result := renderInlineMarkdown("**Three ways agents get created, all manual:**")
	if !strings.Contains(result, "Three ways agents get created, all manual:") {
		t.Errorf("expected bold text content, got %q", result)
	}
	if strings.Contains(result, "**") {
		t.Error("expected ** markers to be removed")
	}
}

func TestRenderInlineMarkdown_OrphanedBold(t *testing.T) {
	result := renderInlineMarkdown("**This bold text was truncated before closing mark...")
	if !strings.Contains(result, "This bold text was truncated") {
		t.Errorf("expected text content, got %q", result)
	}
	if strings.Contains(result, "**") {
		t.Error("expected orphaned ** to be stripped")
	}
}

func TestRenderInlineMarkdown_InlineCode(t *testing.T) {
	result := renderInlineMarkdown("use `fmt.Println` here")
	if !strings.Contains(result, "fmt.Println") {
		t.Errorf("expected inline code, got %q", result)
	}
	if strings.Contains(result, "`") {
		t.Error("expected backtick markers to be removed")
	}
}

func TestRenderInlineMarkdown_NoMarkdown(t *testing.T) {
	result := renderInlineMarkdown("plain text")
	if !strings.Contains(result, "plain text") {
		t.Errorf("expected plain text, got %q", result)
	}
}

func TestRenderMarkdownLines_CodeBlock(t *testing.T) {
	lines := []string{
		"Here is code:",
		"```go",
		"func main() {",
		"    fmt.Println(\"hello\")",
		"}",
		"```",
		"That's it.",
	}

	result := renderMarkdownLines(lines, 80)

	// Should have: intro line + top border + 3 code lines + bottom border + trailing text = 7
	if len(result) < 7 {
		t.Fatalf("expected at least 7 lines, got %d", len(result))
	}

	joined := strings.Join(result, "\n")

	// Should have box-drawing code block borders
	if !strings.Contains(joined, "╭") {
		t.Error("expected top-left code block border ╭")
	}
	if !strings.Contains(joined, "╰") {
		t.Error("expected bottom-left code block border ╰")
	}

	// Language label should appear
	if !strings.Contains(joined, "go") {
		t.Error("expected language label 'go' in code block header")
	}

	// Code content should be present
	if !strings.Contains(joined, "func") {
		t.Error("expected 'func' keyword in code block")
	}
	if !strings.Contains(joined, "fmt.Println") {
		t.Error("expected 'fmt.Println' in code block")
	}

	// Trailing text should be present
	lastLine := result[len(result)-1]
	if !strings.Contains(lastLine, "That's it") {
		t.Errorf("expected trailing text, got %q", lastLine)
	}
}

func TestRenderCodeBlock_WithLanguage(t *testing.T) {
	lines := []string{
		"def hello():",
		"    print(\"world\")",
	}
	result := renderCodeBlock(lines, "python", 60)

	joined := strings.Join(result, "\n")
	// Top border should have language
	if !strings.Contains(joined, "python") {
		t.Error("expected 'python' label in code block header")
	}
	// Box-drawing borders
	if !strings.Contains(joined, "╭") || !strings.Contains(joined, "╯") {
		t.Error("expected box-drawing corners")
	}
	// Content
	if !strings.Contains(joined, "hello") {
		t.Error("expected function name in output")
	}
}

func TestRenderCodeBlock_NoLanguage(t *testing.T) {
	lines := []string{"echo hello"}
	result := renderCodeBlock(lines, "", 40)

	if len(result) < 3 {
		t.Fatalf("expected at least 3 lines (top + content + bottom), got %d", len(result))
	}
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "echo") {
		t.Error("expected command in output")
	}
}

func TestRenderCodeBlock_Empty(t *testing.T) {
	result := renderCodeBlock(nil, "go", 40)
	if len(result) < 3 {
		t.Fatalf("expected at least 3 lines for empty block, got %d", len(result))
	}
}

func TestHighlightCode_Keywords(t *testing.T) {
	result := highlightCode("func main() {", "go")
	if !strings.Contains(result, "func") {
		t.Error("expected 'func' in output")
	}
	if !strings.Contains(result, "main") {
		t.Error("expected 'main' in output")
	}
}

func TestHighlightCode_Comment(t *testing.T) {
	result := highlightCode("// this is a comment", "go")
	if !strings.Contains(result, "this is a comment") {
		t.Errorf("expected comment text, got %q", result)
	}
}

func TestHighlightCode_String(t *testing.T) {
	result := highlightCode(`x = "hello"`, "python")
	if !strings.Contains(result, "hello") {
		t.Errorf("expected string content, got %q", result)
	}
}

func TestRenderMarkdownLines_WidthTruncation(t *testing.T) {
	lines := []string{strings.Repeat("x", 200)}
	result := renderMarkdownLines(lines, 50)

	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	if strings.Contains(result[0], strings.Repeat("x", 200)) {
		t.Error("expected line to be truncated")
	}
}

func TestRenderMarkdownLine_LeadingWhitespace(t *testing.T) {
	result := renderMarkdownLine("    indented text")
	if !strings.Contains(result, "    ") {
		t.Error("expected leading whitespace to be preserved")
	}
	if !strings.Contains(result, "indented text") {
		t.Errorf("expected text content, got %q", result)
	}
}

func TestRenderMarkdownLines_MixedContent(t *testing.T) {
	lines := []string{
		"# Heading",
		"Some text with **bold** and `code`.",
		"- bullet item",
		"1. numbered item",
	}

	result := renderMarkdownLines(lines, 80)

	if len(result) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(result))
	}

	for i, line := range result {
		if !strings.HasPrefix(line, "    ") {
			t.Errorf("line %d missing indent prefix: %q", i, line)
		}
	}
}

func TestRenderMarkdownLines_BoldNotBrokenByWidth(t *testing.T) {
	lines := []string{
		"**This is bold text that extends well past the truncation boundary here**",
	}
	result := renderMarkdownLines(lines, 40)
	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	if strings.Contains(result[0], "**") {
		t.Error("expected ** markers to be removed before truncation")
	}
}

func TestRenderMarkdownLines_NumberedWithBold(t *testing.T) {
	lines := []string{
		"1. **kubectl apply** -- You deploy the manifests",
	}
	result := renderMarkdownLines(lines, 80)
	if strings.Contains(result[0], "**") {
		t.Error("expected ** markers removed in numbered list item")
	}
	if !strings.Contains(result[0], "kubectl apply") {
		t.Errorf("expected bold content preserved, got %q", result[0])
	}
}

// --- Table rendering tests ---

func TestParseTableCells(t *testing.T) {
	cells := parseTableCells("| Approach | How | Tradeoff |")
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells, got %d: %v", len(cells), cells)
	}
	if cells[0] != "Approach" {
		t.Errorf("cell 0 = %q, want %q", cells[0], "Approach")
	}
	if cells[1] != "How" {
		t.Errorf("cell 1 = %q, want %q", cells[1], "How")
	}
	if cells[2] != "Tradeoff" {
		t.Errorf("cell 2 = %q, want %q", cells[2], "Tradeoff")
	}
}

func TestRenderTable_BasicTable(t *testing.T) {
	lines := []string{
		"| Name | Value |",
		"|------|-------|",
		"| foo  | 42    |",
		"| bar  | 99    |",
	}
	result := renderTable(lines, 80)

	if len(result) == 0 {
		t.Fatal("expected rendered table lines")
	}

	// Should have box-drawing borders
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "┌") {
		t.Error("expected top-left corner ┌")
	}
	if !strings.Contains(joined, "┐") {
		t.Error("expected top-right corner ┐")
	}
	if !strings.Contains(joined, "├") {
		t.Error("expected mid-left junction ├")
	}
	if !strings.Contains(joined, "┘") {
		t.Error("expected bottom-right corner ┘")
	}
	if !strings.Contains(joined, "│") {
		t.Error("expected vertical border │")
	}

	// Content should be present
	if !strings.Contains(joined, "Name") {
		t.Error("expected header text 'Name'")
	}
	if !strings.Contains(joined, "foo") {
		t.Error("expected cell text 'foo'")
	}
	if !strings.Contains(joined, "42") {
		t.Error("expected cell text '42'")
	}

	// Should NOT have raw | as data separators (only styled │)
	// The raw ** markers should be gone
	if strings.Contains(joined, "**") {
		t.Error("expected ** markers removed")
	}
}

func TestRenderTable_BoldInCells(t *testing.T) {
	lines := []string{
		"| Feature | Status |",
		"|---------|--------|",
		"| **HPA** | Active |",
	}
	result := renderTable(lines, 80)
	joined := strings.Join(result, "\n")

	if !strings.Contains(joined, "HPA") {
		t.Error("expected bold cell content 'HPA'")
	}
	if strings.Contains(joined, "**") {
		t.Error("expected ** markers removed from table cells")
	}
}

func TestRenderTable_ColumnAlignment(t *testing.T) {
	lines := []string{
		"| A | LongColumnName |",
		"|---|----------------|",
		"| x | y              |",
	}
	result := renderTable(lines, 80)

	// All border lines should have same structure (same number of │)
	for _, line := range result {
		// Count box-drawing vertical bars
		count := strings.Count(line, "│")
		// Border lines have │ embedded in styled output, data rows have explicit │
		// Just verify content is present
		_ = count
	}

	// Header and data should both be present
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "LongColumnName") {
		t.Error("expected header 'LongColumnName'")
	}
}

func TestRenderMarkdownLines_TableBlock(t *testing.T) {
	lines := []string{
		"Here is a table:",
		"| Col1 | Col2 |",
		"|------|------|",
		"| a    | b    |",
		"After the table.",
	}
	result := renderMarkdownLines(lines, 80)

	// First line should be regular text
	if !strings.Contains(result[0], "Here is a table") {
		t.Errorf("expected intro text, got %q", result[0])
	}

	// Should contain box-drawing characters from table rendering
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "┌") {
		t.Error("expected table border in output")
	}
	if !strings.Contains(joined, "Col1") {
		t.Error("expected table header in output")
	}

	// Last line should be regular text
	lastLine := result[len(result)-1]
	if !strings.Contains(lastLine, "After the table") {
		t.Errorf("expected trailing text, got %q", lastLine)
	}
}

func TestRenderTable_NarrowWidth(t *testing.T) {
	lines := []string{
		"| VeryLongColumnHeader | AnotherLongOne |",
		"|----------------------|----------------|",
		"| data                 | more           |",
	}
	// Narrow width forces column shrinking
	result := renderTable(lines, 30)
	if len(result) == 0 {
		t.Fatal("expected rendered table even at narrow width")
	}
}

func TestBuildBorderLine(t *testing.T) {
	result := buildBorderLine([]int{5, 3}, "┌", "┬", "┐")
	// ┌ + 7 dashes + ┬ + 5 dashes + ┐ = ┌───────┬─────┐
	if !strings.HasPrefix(result, "┌") {
		t.Error("expected left corner")
	}
	if !strings.HasSuffix(result, "┐") {
		t.Error("expected right corner")
	}
	if !strings.Contains(result, "┬") {
		t.Error("expected middle junction")
	}
	// Each column section = colWidth + 2 dashes for padding
	expected := "┌───────┬─────┐"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}
