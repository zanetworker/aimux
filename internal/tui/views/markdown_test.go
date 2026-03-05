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
	// Should NOT contain the raw "# " prefix (it's stripped and styled)
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
	// The ** markers should be stripped
	if strings.Contains(result, "**") {
		t.Error("expected ** markers to be removed")
	}
}

func TestRenderInlineMarkdown_InlineCode(t *testing.T) {
	result := renderInlineMarkdown("use `fmt.Println` here")
	if !strings.Contains(result, "fmt.Println") {
		t.Errorf("expected inline code, got %q", result)
	}
	// The backtick markers should be stripped
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

	if len(result) != len(lines) {
		t.Fatalf("expected %d lines, got %d", len(lines), len(result))
	}

	// Code fence lines should be styled
	if !strings.Contains(result[1], "```go") {
		t.Errorf("expected code fence, got %q", result[1])
	}

	// Code content should preserve indentation
	if !strings.Contains(result[3], "    fmt.Println") {
		t.Errorf("expected indented code line, got %q", result[3])
	}

	// Closing fence
	if !strings.Contains(result[5], "```") {
		t.Errorf("expected closing fence, got %q", result[5])
	}
}

func TestRenderMarkdownLines_Truncation(t *testing.T) {
	lines := []string{strings.Repeat("x", 200)}
	result := renderMarkdownLines(lines, 50)

	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	// The rendered line should be truncated (accounting for "    " prefix)
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

	// All lines should have the 4-space indent prefix
	for i, line := range result {
		if !strings.HasPrefix(line, "    ") {
			t.Errorf("line %d missing indent prefix: %q", i, line)
		}
	}
}
