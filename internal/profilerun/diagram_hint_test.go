package profilerun

import (
	"strings"
	"testing"
)

// Every Wingman answer prompt (page, research in the sandbox, workspace)
// tells the model it may answer with one Mermaid block, in the trusted part
// of the prompt: after any untrusted page block and before the question.
func TestPrompts_CarryDiagramHint(t *testing.T) {
	const want = "When a diagram helps, you may include one Mermaid block (```mermaid, flowchart, sequence, state, class, er, gantt, pie, xychart, mindmap); keep it under 40 nodes."
	if diagramHint != want+"\n\n" {
		t.Fatalf("diagramHint = %q, want %q", diagramHint, want+"\n\n")
	}
	const q = "how does it flow?"
	page := "Body text. " + want // a page quoting the hint must not count as the hint
	cases := map[string]string{
		"page with page":     Prompt(AskRequest{URL: "https://a.example/", Title: "T", Page: page, Question: q}),
		"page follow-up":     Prompt(AskRequest{Question: q}),
		"research first":     sandboxPrompt(&AskRequest{URL: "https://a.example/", Title: "T", Page: page}, q),
		"research follow-up": sandboxPrompt(nil, q),
		"workspace":          workPrompt(AskRequest{URL: "https://a.example/", Page: page, Question: q}),
		"workspace bare":     workPrompt(AskRequest{Question: q}),
	}
	for name, p := range cases {
		hint := strings.LastIndex(p, diagramHint+questionMarker)
		if hint < 0 {
			t.Errorf("%s: no diagram hint right before the question:\n%s", name, p)
			continue
		}
		if closeTag := strings.LastIndex(p, "</page-"); closeTag > hint {
			t.Errorf("%s: hint sits inside the untrusted page block", name)
		}
		if got := QuestionOf(p); got != q {
			t.Errorf("%s: QuestionOf = %q, want %q", name, got, q)
		}
	}
}
