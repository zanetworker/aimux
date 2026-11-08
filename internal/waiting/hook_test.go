package waiting

import (
	"strings"
	"testing"
)

func TestHandleHookPermissionNotificationWritesMarker(t *testing.T) {
	dir := t.TempDir()
	in := `{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","title":"Permission needed","notification_type":"permission_prompt"}`
	if err := HandleHook(dir, strings.NewReader(in), t0); err != nil {
		t.Fatal(err)
	}
	w, ok := Read(dir, "s1")
	if !ok || w.Message != "Claude needs your permission to use Bash" || !w.Since.Equal(t0) {
		t.Fatalf("marker = %+v, %v", w, ok)
	}
}

func TestHandleHookOtherNotificationsIgnored(t *testing.T) {
	dir := t.TempDir()
	for _, typ := range []string{"idle_prompt", "auth_success", "elicitation_dialog"} {
		in := `{"session_id":"s1","hook_event_name":"Notification","message":"Claude is waiting for your input","notification_type":"` + typ + `"}`
		if err := HandleHook(dir, strings.NewReader(in), t0); err != nil {
			t.Fatal(err)
		}
	}
	if len(List(dir)) != 0 {
		t.Fatal("a non-permission notification wrote a marker")
	}
}

func TestHandleHookOlderClaudeWithoutTypeFallsBackToMessage(t *testing.T) {
	dir := t.TempDir()
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"Notification","message":"Claude needs your permission to use Edit"}`), t0)
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s2","hook_event_name":"Notification","message":"Claude is waiting for your input"}`), t0)
	got := List(dir)
	if _, ok := got["s1"]; !ok || len(got) != 1 {
		t.Fatalf("List = %+v, want s1 only", got)
	}
}

func TestHandleHookClearingEventsRemoveMarker(t *testing.T) {
	for _, ev := range ClearEvents {
		dir := t.TempDir()
		_ = Write(dir, "s1", "m", t0)
		if err := HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"`+ev+`"}`), t0); err != nil {
			t.Fatal(ev, err)
		}
		if _, ok := Read(dir, "s1"); ok {
			t.Errorf("%s left the marker", ev)
		}
	}
}

func TestHandleHookLeavesOtherSessionsAndUnknownEvents(t *testing.T) {
	dir := t.TempDir()
	_ = Write(dir, "s1", "m", t0)
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s2","hook_event_name":"Stop"}`), t0)
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"PreToolUse"}`), t0)
	if _, ok := Read(dir, "s1"); !ok {
		t.Fatal("marker of s1 removed by another session or an unknown event")
	}
}

func TestHandleHookBadInput(t *testing.T) {
	dir := t.TempDir()
	for _, in := range []string{"", "{not json", `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, `{"session_id":"../x","hook_event_name":"Notification","notification_type":"permission_prompt"}`} {
		if err := HandleHook(dir, strings.NewReader(in), t0); err == nil {
			t.Errorf("HandleHook(%q) = nil, want an error", in)
		}
	}
	if len(List(dir)) != 0 {
		t.Fatal("bad input wrote a marker")
	}
}

func TestHandleHookKeepsTranscriptAndAgent(t *testing.T) {
	dir := t.TempDir()
	in := `{"session_id":"s1","transcript_path":"/x/s1.jsonl","agent_id":"ag1","hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`
	if err := HandleHook(dir, strings.NewReader(in), t0); err != nil {
		t.Fatal(err)
	}
	m, ok := readMarker(dir, "s1")
	if !ok || m.TranscriptPath != "/x/s1.jsonl" || m.AgentID != "ag1" {
		t.Fatalf("marker = %+v, %v", m, ok)
	}
}

func TestHandleHookSubagentToolDoesNotClearMainThreadMarker(t *testing.T) {
	for _, ev := range []string{"PostToolUse", "PostToolUseFailure"} {
		dir := t.TempDir()
		_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"Notification","message":"m","notification_type":"permission_prompt"}`), t0)
		_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","agent_id":"sub1","hook_event_name":"`+ev+`"}`), t0)
		if _, ok := Read(dir, "s1"); !ok {
			t.Fatalf("%s from a subagent cleared the main thread's marker", ev)
		}
		_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"`+ev+`"}`), t0)
		if _, ok := Read(dir, "s1"); ok {
			t.Fatalf("%s from the main thread left the marker", ev)
		}
	}
	// A subagent's Stop-like events still clear (the turn is over for everyone).
	dir := t.TempDir()
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","hook_event_name":"Notification","message":"m","notification_type":"permission_prompt"}`), t0)
	_ = HandleHook(dir, strings.NewReader(`{"session_id":"s1","agent_id":"sub1","hook_event_name":"UserPromptSubmit"}`), t0)
	if _, ok := Read(dir, "s1"); ok {
		t.Fatal("UserPromptSubmit should clear whatever its agent")
	}
}
