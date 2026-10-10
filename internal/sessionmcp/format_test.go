package sessionmcp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/search"
)

func transcript(id, cwd string, n int, prose func(i int) string) search.Transcript {
	t := search.Transcript{Detail: search.Detail{SessionID: id, CWD: cwd, Title: "t", ModTime: time.Unix(1_700_000_000, 0), Exchanges: n}}
	for i := 0; i < n; i++ {
		t.Exchanges = append(t.Exchanges, search.Exchange{Seq: i, Prompt: fmt.Sprintf("prompt-%d", i), Prose: prose(i)})
	}
	return t
}

func TestFormatContinue_ResumeLine(t *testing.T) {
	out := formatContinue(transcript("bbbbbbbb-0000-0000-0000-000000000002", "/Users/me/research", 1, func(int) string { return "hi" }))
	if !strings.Contains(out, "cd /Users/me/research && claude --resume bbbbbbbb-0000-0000-0000-000000000002") {
		t.Errorf("missing resume line:\n%s", out)
	}
}

func TestFormatContinue_CapKeepsNewest(t *testing.T) {
	tr := transcript("s", "/x", 900, func(i int) string {
		if i == 450 {
			return strings.Repeat("g", 60_000)
		}
		return fmt.Sprintf("exchange-%d ", i) + strings.Repeat("x", 2_000)
	})
	out := formatContinue(tr)
	if len(out) > maxOutputChars+2000 {
		t.Errorf("len = %d, over cap", len(out))
	}
	if !strings.Contains(out, "exchange-899") {
		t.Error("newest exchange missing")
	}
	if strings.Contains(out, "exchange-0 ") {
		t.Error("oldest exchange should be dropped")
	}
	if !strings.Contains(out, "last") || !strings.Contains(out, "of 900 exchanges") {
		t.Error("cut header missing")
	}
}

func TestFormatContinue_GiantExchangeTruncated(t *testing.T) {
	out := formatContinue(transcript("s", "/x", 1, func(int) string { return strings.Repeat("g", 60_000) }))
	if !strings.Contains(out, "…[truncated]") {
		t.Error("missing truncation marker")
	}
	if len(out) >= maxExchangeChars+2000 {
		t.Errorf("len = %d, want < %d", len(out), maxExchangeChars+2000)
	}
}

func TestFormatSession_LastN(t *testing.T) {
	out := formatSession(transcript("s", "/x", 5, func(i int) string { return fmt.Sprintf("prose-%d", i) }), 2)
	for _, want := range []string{"prose-3", "prose-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"prose-0", "prose-1", "prose-2"} {
		if strings.Contains(out, not) {
			t.Errorf("should not contain %s", not)
		}
	}
	if all := formatSession(transcript("s", "/x", 5, func(i int) string { return fmt.Sprintf("prose-%d", i) }), 0); !strings.Contains(all, "prose-0") {
		t.Error("lastN=0 should show every exchange")
	}
}

func TestFormatSession_AutomatedFlag(t *testing.T) {
	tr := transcript("s", "/x", 1, func(int) string { return "p" })
	if strings.Contains(formatSession(tr, 0), "automated") {
		t.Error("human session flagged automated")
	}
	tr.Detail.Automated = true
	if !strings.Contains(formatSession(tr, 0), "automated") {
		t.Error("automated flag missing")
	}
}

func TestFormatVirtual_FullVsTail(t *testing.T) {
	short := transcript("aaaaaaaa-short", "/a", 3, func(i int) string { return fmt.Sprintf("short-%d.", i) })
	long := transcript("bbbbbbbb-long", "/b", 30, func(i int) string { return fmt.Sprintf("long-%d.", i) })
	long.Detail.ModTime = short.Detail.ModTime.Add(-time.Hour) // long is older
	out := formatVirtual([]search.Transcript{short, long}, 10)
	for i := 0; i < 3; i++ {
		if !strings.Contains(out, fmt.Sprintf("short-%d.", i)) {
			t.Errorf("short session missing exchange %d", i)
		}
	}
	if strings.Contains(out, "long-19.") || !strings.Contains(out, "long-20.") || !strings.Contains(out, "long-29.") {
		t.Error("long session should show only its last 10 exchanges")
	}
	if strings.Index(out, "bbbbbbbb") > strings.Index(out, "aaaaaaaa") {
		t.Error("older session should come first")
	}
	if !strings.Contains(out, "## Session 1 of 2: bbbbbbbb (/b)") {
		t.Errorf("section heading wrong:\n%s", out[:200])
	}
}

func TestFormatResults_Empty(t *testing.T) {
	if got := formatResults(nil, "hybrid", false); got != "No sessions found." {
		t.Errorf("got %q", got)
	}
}

func TestFormatResults_RankingHeader(t *testing.T) {
	rs := []search.Result{{SessionID: "aaaaaaaa-1", Title: "deck", CWD: "/x", ModTime: time.Now(), Snippet: "the Ying deck"}}
	if out := formatResults(rs, "hybrid", false); !strings.Contains(out, "keyword") || !strings.Contains(out, "semantic unavailable") {
		t.Errorf("degraded header missing:\n%s", out)
	}
	if out := formatResults(rs, "keyword", false); strings.Contains(out, "unavailable") {
		t.Errorf("keyword mode should not warn:\n%s", out)
	}
	if out := formatResults(rs, "hybrid", true); !strings.Contains(out, "hybrid") || !strings.Contains(out, "aaaaaaaa-1") || !strings.Contains(out, "the Ying deck") {
		t.Errorf("hybrid result wrong:\n%s", out)
	}
}

func TestFormatList(t *testing.T) {
	ts := time.Date(2026, 10, 10, 9, 5, 0, 0, time.Local)
	out := formatList([]search.Result{{SessionID: "aaaaaaaa-1", Title: "deck", CWD: "/x", ModTime: ts}})
	if !strings.Contains(out, "aaaaaaaa-1") || !strings.Contains(out, "2026-10-10 09:05") || !strings.Contains(out, "/x") {
		t.Errorf("got %q", out)
	}
	if formatList(nil) != "No sessions found." {
		t.Error("empty list")
	}
}

func TestFormatSession_CapKeepsNewest(t *testing.T) {
	out := formatSession(transcript("s", "/x", 100, func(i int) string { return fmt.Sprintf("ex-%d ", i) + strings.Repeat("x", 5_000) }), 0)
	if len(out) > maxOutputChars {
		t.Errorf("len = %d, over cap", len(out))
	}
	if !strings.Contains(out, "ex-99 ") || strings.Contains(out, "ex-0 ") || !strings.Contains(out, "of 100 exchanges") {
		t.Error("cap should keep the newest exchanges and say how many")
	}
}

func TestFormatVirtual_CapShrinksLongestFirst(t *testing.T) {
	big := transcript("aaaaaaaa-big", "/a", 20, func(i int) string { return fmt.Sprintf("big-%d.", i) + strings.Repeat("x", 15_000) })
	small := transcript("bbbbbbbb-small", "/b", 3, func(i int) string { return fmt.Sprintf("small-%d.", i) })
	out := formatVirtual([]search.Transcript{big, small}, 10)
	if len(out) > maxOutputChars {
		t.Errorf("len = %d, over cap", len(out))
	}
	if !strings.Contains(out, "small-0.") || !strings.Contains(out, "big-19.") || strings.Contains(out, "big-0.") {
		t.Error("the long section should shrink from its oldest end; the short one stays whole")
	}
}
