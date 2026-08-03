package otel

import (
	"testing"
)

func TestParseSessionJSONL_UserAndAssistant(t *testing.T) {
	data := []byte(`{"type":"user","message":{"role":"user","content":"hello"},"promptId":"p1","sessionId":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]},"promptId":"p1","sessionId":"s1"}
{"type":"user","message":{"role":"user","content":"what is 2+2?"},"promptId":"p2","sessionId":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"2+2 is 4."}]},"promptId":"p2","sessionId":"s1"}
`)
	replies := ParseSessionReplies(data)
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2", len(replies))
	}
	if replies["p1"] != "Hi there!" {
		t.Errorf("p1 reply = %q, want %q", replies["p1"], "Hi there!")
	}
	if replies["p2"] != "2+2 is 4." {
		t.Errorf("p2 reply = %q, want %q", replies["p2"], "2+2 is 4.")
	}
}

func TestParseSessionJSONL_MultiBlock(t *testing.T) {
	data := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me think"},{"type":"text","text":"First part."},{"type":"text","text":"Second part."}]},"promptId":"p1"}
`)
	replies := ParseSessionReplies(data)
	if replies["p1"] != "First part.\nSecond part." {
		t.Errorf("multi-block reply = %q", replies["p1"])
	}
}

func TestParseSessionJSONL_NoAssistant(t *testing.T) {
	data := []byte(`{"type":"user","message":{"role":"user","content":"hello"},"promptId":"p1"}
`)
	replies := ParseSessionReplies(data)
	if len(replies) != 0 {
		t.Errorf("expected 0 replies, got %d", len(replies))
	}
}

func TestParseSessionJSONL_EmptyInput(t *testing.T) {
	replies := ParseSessionReplies(nil)
	if replies == nil {
		t.Error("expected non-nil empty map")
	}
	if len(replies) != 0 {
		t.Errorf("expected 0 replies, got %d", len(replies))
	}
}
