package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
)

// privateText appears in every fixture transcript: no log line, error or status
// may ever contain it.
const privateText = "zebra lantern marmalade"

var promptSession = regexp.MustCompile(`(?m)^Session (\d+) \(([a-z, ]+)\):`)

// fakeBackend answers each request through answer (default: a good title and
// description for every session asked) and keeps the requests.
type fakeBackend struct {
	mu     sync.Mutex
	reqs   []NamingRequest
	answer func(req NamingRequest) (NamingReply, error)
}

func (f *fakeBackend) Complete(_ context.Context, req NamingRequest) (NamingReply, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.answer != nil {
		return f.answer(req)
	}
	return goodReply(req), nil
}

func (f *fakeBackend) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeBackend) last() NamingRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func goodReply(req NamingRequest) NamingReply {
	var items []map[string]string
	for _, m := range promptSession.FindAllStringSubmatch(req.Prompt, -1) {
		items = append(items, map[string]string{
			"id": m[1], "title": "Fix the parser bug", "description": "Fixes a parser bug in session " + m[1] + ".",
		})
	}
	b, _ := json.Marshal(map[string]any{"sessions": items})
	return NamingReply{Text: string(b), Model: req.Model, InputTokens: 300, OutputTokens: 40}
}

type namerFixture struct {
	t        *testing.T
	dir      string
	now      time.Time
	sessions map[string]Session
	backend  *fakeBackend
	fallback *fakeBackend
	logs     *bytes.Buffer
	usage    []cost.Usage
	namer    *Namer
	cfg      NamerConfig
}

func newNamerFixture(t *testing.T) *namerFixture {
	t.Helper()
	f := &namerFixture{
		t: t, dir: t.TempDir(),
		now:      time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local),
		sessions: map[string]Session{},
		backend:  &fakeBackend{},
		fallback: &fakeBackend{},
		logs:     &bytes.Buffer{},
	}
	var logMu sync.Mutex
	f.cfg = NamerConfig{
		Model:      "haiku",
		DailyLimit: 20,
		Lookup: func(id string) (Session, bool) {
			s, ok := f.sessions[id]
			return s, ok
		},
		Primary:  f.backend,
		Fallback: f.fallback,
		RecordUsage: func(u cost.Usage) error {
			f.usage = append(f.usage, u)
			return nil
		},
		Now: func() time.Time { return f.now },
		Logf: func(format string, args ...any) {
			logMu.Lock()
			defer logMu.Unlock()
			fmt.Fprintf(f.logs, format+"\n", args...)
		},
		BatchWait: 20 * time.Millisecond,
	}
	f.namer = NewNamer(f.cfg)
	return f
}

// add writes a transcript for a session with the given shown first prompt
// and registers it; it is interactive and active an hour ago.
func (f *namerFixture) add(id, firstPrompt string) Session {
	f.t.Helper()
	p := filepath.Join(f.dir, id+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`, firstPrompt+" please, "+privateText+" "+strings.Repeat("first words ", 60)),
		fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, "Early reply"),
		`{"type":"user","message":{"role":"user","content":"and the latest ask"}}`,
		fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, "Latest reply begins here "+privateText+" "+strings.Repeat("reply words ", 100)),
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	s := Session{ID: id, FilePath: p, FirstPrompt: firstPrompt, LastActive: f.now.Add(-time.Hour), TurnCount: 2}
	f.sessions[id] = s
	return s
}

func (f *namerFixture) noLeaks(extra ...string) {
	f.t.Helper()
	all := append([]string{f.logs.String(), fmt.Sprintf("%+v", f.namer.Status())}, extra...)
	for _, s := range all {
		if strings.Contains(s, privateText) || strings.Contains(s, "first words") || strings.Contains(s, "reply words") {
			f.t.Errorf("session text leaked: %q", s)
		}
	}
}

func TestNamingInput_CapsAndContents(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("a1", "Fix the thing")
	in := namingInput(s.FilePath, autoInputLimit, false)
	if n := len([]rune(in)); n > 500 || n < 400 {
		t.Errorf("automatic input is %d characters, want at most 500 and near it", n)
	}
	if !strings.Contains(in, "Fix the thing please") || !strings.Contains(in, "Latest reply begins here") {
		t.Error("automatic input must hold the first message and the start of the latest reply")
	}
	if strings.Contains(in, "Early reply") || strings.Contains(in, "latest ask") {
		t.Error("automatic input holds only the first message and the latest reply")
	}
	up := namingInput(s.FilePath, updateInputLimit, true)
	if n := len([]rune(up)); n > 1200 || n < 1000 {
		t.Errorf("update input is %d characters, want at most 1200 and near it", n)
	}
	if !strings.Contains(up, "and the latest ask") {
		t.Error("update input must hold the latest message too")
	}
	if namingInput(filepath.Join(f.dir, "missing.jsonl"), 500, false) != "" {
		t.Error("a missing transcript gives no input")
	}
}

func TestNamer_BatchesUpToFive(t *testing.T) {
	f := newNamerFixture(t)
	for i := 0; i < 3; i++ {
		f.add(fmt.Sprintf("c513a7b%d", i), fmt.Sprintf("c513a7b%d", i))
	}
	f.namer.Want([]string{"c513a7b0", "c513a7b1", "c513a7b2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Fatalf("calls = %d, want one request for 3 sessions", f.backend.calls())
	}
	req := f.backend.last()
	if got := len(promptSession.FindAllString(req.Prompt, -1)); got != 3 {
		t.Errorf("sessions in request = %d, want 3", got)
	}
	if req.MaxTokens != 3*60 {
		t.Errorf("MaxTokens = %d, want 180", req.MaxTokens)
	}
	if req.Model != "claude-haiku-5-5" {
		t.Errorf("model = %q", req.Model)
	}

	g := newNamerFixture(t)
	var ids []string
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("d513a7b%d", i)
		g.add(id, "")
		ids = append(ids, id)
	}
	g.namer.Want(ids)
	g.namer.runBatch(context.Background())
	g.namer.runBatch(context.Background())
	if g.backend.calls() != 2 {
		t.Fatalf("calls = %d, want 2 for 7 sessions", g.backend.calls())
	}
	first := len(promptSession.FindAllString(g.backend.reqs[0].Prompt, -1))
	second := len(promptSession.FindAllString(g.backend.reqs[1].Prompt, -1))
	if first != 5 || second != 2 {
		t.Errorf("batch sizes = %d, %d; want 5, 2", first, second)
	}
	g.noLeaks()
}

func TestNamer_WritesTitleAndDescriptionOnceAndRecordsUsage(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("c513a7b6", "Session c513a7b6")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	m := LoadMeta(s.FilePath)
	if m.Title != "Fix the parser bug" || m.TitleBy != "model" || m.Description == "" || m.DescriptionBy != "model" {
		t.Fatalf("meta = %+v", m)
	}
	if m.DescribedAt == "" || m.DescribedTurns != 2 {
		t.Errorf("described at/turns = %q/%d", m.DescribedAt, m.DescribedTurns)
	}
	if len(f.usage) != 1 || f.usage[0].Label != "naming" || f.usage[0].InputTokens != 300 || f.usage[0].OutputTokens != 40 {
		t.Errorf("usage = %+v", f.usage)
	}
	// once per session: shown again, never sent again
	f.sessions[s.ID] = Session{ID: s.ID, FilePath: s.FilePath, Title: m.Title, Description: m.Description, LastActive: s.LastActive}
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want 1", f.backend.calls())
	}
	f.noLeaks()
}

func TestNamer_FineTitleAsksOnlyForDescription(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("e1", "Refactor the billing export")
	s.AITitle = "Billing export refactor"
	f.sessions[s.ID] = s
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	req := f.backend.last()
	if !strings.Contains(req.Prompt, "Session 1 (description):") {
		t.Errorf("prompt should ask only for a description")
	}
	m := LoadMeta(s.FilePath)
	if m.Title != "" || m.TitleBy != "" {
		t.Errorf("title written for a fine shown title: %+v", m)
	}
	if m.Description == "" {
		t.Error("description not written")
	}
}

func TestNamer_UselessModelTitleIsDiscardedWithoutAnotherCall(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		return NamingReply{Text: `{"sessions":[{"id":"1","title":"Session c513a7b6","description":"Works on the parser. ` + privateText + `"}]}`, Model: req.Model}, nil
	}
	s := f.add("c513a7b6", "c513a7b6")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	m := LoadMeta(s.FilePath)
	if m.Title != "" {
		t.Errorf("useless model title stored: %q", m.Title)
	}
	if m.Description != "Works on the parser. "+privateText || m.DescribedAt == "" {
		t.Errorf("meta = %+v", m)
	}
	f.sessions[s.ID] = Session{ID: s.ID, FilePath: s.FilePath, FirstPrompt: "c513a7b6", Description: m.Description, LastActive: s.LastActive}
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want 1: a useless model title never triggers another call", f.backend.calls())
	}
	f.noLeaks()
}

func TestNamer_NothingUsableStillMarksTheSessionDone(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		return NamingReply{Text: `{"sessions":[{"id":"1","title":"c513a7b6","description":""}]}`, Model: req.Model}, nil
	}
	s := f.add("c513a7b6", "")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want 1", f.backend.calls())
	}
	if m := LoadMeta(s.FilePath); m.DescribedAt == "" || m.Title != "" || m.Description != "" {
		t.Errorf("meta = %+v, want only the attempt recorded", m)
	}
}

func TestNamer_SkipsSessionsItMustNotName(t *testing.T) {
	f := newNamerFixture(t)
	auto := f.add("auto1", "")
	auto.Automated = true
	sub := f.add("sub1", "")
	sub.IsSubagent = true
	old := f.add("old1", "")
	old.LastActive = f.now.Add(-15 * 24 * time.Hour)
	away := f.add("away1", "Fix the deploy")
	away.AwaySummary = "Claude Code's recap" // a fine title and a recap: nothing to make
	owner := f.add("owner1", "")
	ownerT := f.add("ownerT1", "")
	ours := f.add("ours1", "")
	for _, s := range []Session{auto, sub, old, away} {
		f.sessions[s.ID] = s
	}
	if err := UpdateMeta(owner.FilePath, func(m *Meta) { m.Description, m.DescriptionBy = "Mine", "owner" }); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(ownerT.FilePath, func(m *Meta) { m.Title, m.TitleBy = "My title", "owner" }); err != nil {
		t.Fatal(err)
	}
	if err := UpdateMeta(ours.FilePath, func(m *Meta) { m.DescribedAt = "2026-10-29T10:00:00Z" }); err != nil {
		t.Fatal(err)
	}
	f.namer.Want([]string{"auto1", "sub1", "old1", "away1", "owner1", "ownerT1", "ours1", "unknown"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 0 {
		t.Errorf("calls = %d, want 0", f.backend.calls())
	}

	// 14 days is the edge: active 13 days ago is still named
	recent := f.add("recent1", "")
	recent.LastActive = f.now.Add(-13 * 24 * time.Hour)
	f.sessions[recent.ID] = recent
	f.namer.Want([]string{"recent1"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want 1 for a session active 13 days ago", f.backend.calls())
	}
}

func TestNamer_OwnerValueSetDuringCallIsKept(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("c513a7b6", "")
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		// the owner edits on the phone while the model is answering
		_ = UpdateMeta(s.FilePath, func(m *Meta) { m.Title, m.TitleBy = "Owner title", "owner" })
		return goodReply(req), nil
	}
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if m := LoadMeta(s.FilePath); m.Title != "Owner title" || m.TitleBy != "owner" {
		t.Errorf("meta = %+v, want the owner's title kept", m)
	}
}

func TestNamer_DailyBudget(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.DailyLimit = 2
	f.cfg.StatePath = filepath.Join(f.dir, "naming-state.json")
	f.namer = NewNamer(f.cfg)
	for i := 0; i < 4; i++ {
		f.add(fmt.Sprintf("b%d", i), "")
	}
	f.namer.Want([]string{"b0", "b1", "b2"})
	f.namer.runBatch(context.Background())
	f.namer.runBatch(context.Background())
	if got := len(promptSession.FindAllString(f.backend.last().Prompt, -1)); f.backend.calls() != 1 || got != 2 {
		t.Fatalf("calls = %d with %d sessions, want 1 with 2 (the limit)", f.backend.calls(), got)
	}
	st := f.namer.Status()
	if st.Today != 2 || st.Limit != 2 {
		t.Errorf("status = %+v", st)
	}
	f.namer.Want([]string{"b3"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d after the limit, want 1", f.backend.calls())
	}

	// a restart the same day keeps the count
	f.namer = NewNamer(f.cfg)
	f.namer.Want([]string{"b3"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d after a restart, want 1", f.backend.calls())
	}

	// on request is not limited, but counted
	if _, err := f.namer.UpdateSummary(context.Background(), f.sessions["b3"]); err != nil {
		t.Fatal(err)
	}
	if st := f.namer.Status(); st.OnRequest != 1 || st.Today != 2 {
		t.Errorf("status = %+v, want 1 on request, 2 automatic", st)
	}

	// the next local day resets
	f.now = f.now.Add(24 * time.Hour)
	f.sessions["b2"] = Session{ID: "b2", FilePath: f.sessions["b2"].FilePath, LastActive: f.now.Add(-time.Hour)}
	f.namer.Want([]string{"b2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 3 {
		t.Errorf("calls = %d, want 3 after the day changed", f.backend.calls())
	}
	if st := f.namer.Status(); st.Today != 1 || st.OnRequest != 0 {
		t.Errorf("status next day = %+v", st)
	}
}

func TestNamer_FailureBackoffAndFailingState(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{}, fmt.Errorf("anthropic API status 529: %w", ErrModelCall)
	}
	f.add("f1", "")
	f.add("f2", "")
	f.add("f3", "")
	f.namer.Want([]string{"f1"})
	f.namer.runBatch(context.Background())
	if st := f.namer.Status(); st.Failing {
		t.Errorf("failing after one failure: %+v", st)
	}
	// backoff: shown again within the hour, not sent
	f.now = f.now.Add(30 * time.Minute)
	f.namer.Want([]string{"f1"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want 1 during the backoff", f.backend.calls())
	}
	f.namer.Want([]string{"f2"})
	f.namer.runBatch(context.Background())
	f.namer.Want([]string{"f3"})
	f.namer.runBatch(context.Background())
	st := f.namer.Status()
	if !st.Failing || st.Reason != "model call failed" {
		t.Errorf("status after 3 failures = %+v", st)
	}
	// after the hour it is tried again, and a success clears failing
	f.backend.answer = nil
	f.now = f.now.Add(61 * time.Minute) // past the pause that began with the third failure
	f.namer.Want([]string{"f1"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 4 {
		t.Errorf("calls = %d, want 4", f.backend.calls())
	}
	if st := f.namer.Status(); st.Failing || st.Reason != "" {
		t.Errorf("status after success = %+v", st)
	}
	f.noLeaks()
}

func TestNamer_FixedReasons(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrModelNotFound, "model not found"},
		{fmt.Errorf("x: %w", ErrUnauthorized), "API key rejected"},
		{context.DeadlineExceeded, "timed out"},
		{fmt.Errorf("run: %w", os.ErrNotExist), "model call failed"},
		{ErrBadReply, "unreadable model reply"},
		{ErrNoBackend, "no API key and no claude CLI"},
		{errors.New("anything " + privateText), "model call failed"},
	} {
		if got := failureReason(tc.err); got != tc.want {
			t.Errorf("failureReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestNamer_ReplyValidation(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		return NamingReply{Text: "```json\n" + `{"sessions":[{"id":"9","title":"Stray title","description":"` + privateText + `"},{"id":"1","title":"Real title here","description":"Real one."}]}` + "\n```"}, nil
	}
	s := f.add("v1", "")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if m := LoadMeta(s.FilePath); m.Title != "Real title here" || m.Description != "Real one." {
		t.Errorf("meta = %+v", m)
	}
	if !strings.Contains(f.logs.String(), "unknown session") {
		t.Errorf("unknown id not logged: %q", f.logs.String())
	}

	g := newNamerFixture(t)
	g.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{Text: "Sure! Here are titles: " + privateText}, nil
	}
	s2 := g.add("v2", "")
	g.namer.Want([]string{s2.ID})
	g.namer.runBatch(context.Background())
	if m := LoadMeta(s2.FilePath); m.Title != "" || m.Description != "" {
		t.Errorf("unreadable reply wrote %+v", m)
	}
	if st := g.namer.Status(); st.Reason != "" {
		t.Errorf("one failure is not failing: %+v", st)
	}
	f.noLeaks()
	g.noLeaks()
}

func TestNamer_ModelNotFoundRetriesOnceThroughCLIHaiku(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{}, fmt.Errorf("anthropic API status 404: %w", ErrModelNotFound)
	}
	s := f.add("m1", "")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.fallback.calls() != 1 || f.fallback.last().Model != "haiku" {
		t.Fatalf("fallback calls = %d, want 1 with model haiku", f.fallback.calls())
	}
	if m := LoadMeta(s.FilePath); m.Description == "" {
		t.Error("fallback reply not written")
	}

	g := newNamerFixture(t)
	g.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, ErrModelCall }
	g.add("m2", "")
	g.namer.Want([]string{"m2"})
	g.namer.runBatch(context.Background())
	if g.fallback.calls() != 0 {
		t.Error("only model not found falls back")
	}
}

func TestNamer_UnknownModelFallsBackWithOneLogLine(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.Model = "gpt-9"
	f.namer = NewNamer(f.cfg)
	f.add("u1", "")
	f.namer.Want([]string{"u1"})
	f.namer.runBatch(context.Background())
	if f.backend.last().Model != DefaultModel {
		t.Errorf("model = %q", f.backend.last().Model)
	}
	if n := strings.Count(f.logs.String(), "unknown naming model"); n != 1 {
		t.Errorf("log lines about the model = %d, want 1", n)
	}
}

func TestNamer_UpdateSummary(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("up1", "Fix login")
	if err := UpdateMeta(s.FilePath, func(m *Meta) { m.Title, m.TitleBy = "Owner title", "owner" }); err != nil {
		t.Fatal(err)
	}
	res, err := f.namer.UpdateSummary(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Owner title" || res.Description == "" {
		t.Errorf("result = %+v", res)
	}
	if strings.Join(res.Changed, ",") != "description" || strings.Join(res.Kept, ",") != "title" {
		t.Errorf("changed %v kept %v", res.Changed, res.Kept)
	}
	req := f.backend.last()
	if !strings.Contains(req.Prompt, "Session 1 (description):") || req.MaxTokens != 60 {
		t.Errorf("request asks for %q with %d tokens", promptSession.FindString(req.Prompt), req.MaxTokens)
	}
	if n := len([]rune(req.Prompt)); n > 1200+200 {
		t.Errorf("prompt is %d characters", n)
	}
	// works on old and automated sessions too, and again
	s.LastActive = f.now.Add(-60 * 24 * time.Hour)
	if _, err := f.namer.UpdateSummary(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if f.backend.calls() != 2 {
		t.Errorf("calls = %d", f.backend.calls())
	}

	if err := UpdateMeta(s.FilePath, func(m *Meta) { m.Description, m.DescriptionBy = "Mine", "owner" }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.namer.UpdateSummary(context.Background(), s); !errors.Is(err, ErrOwnerSet) {
		t.Errorf("err = %v, want ErrOwnerSet", err)
	}
	if f.backend.calls() != 2 {
		t.Error("no call when both are the owner's")
	}

	f.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, ErrModelCall }
	t2 := f.add("up2", "")
	_, err = f.namer.UpdateSummary(context.Background(), t2)
	if err == nil || !strings.Contains(err.Error(), "model call failed") {
		t.Errorf("err = %v, want the fixed reason", err)
	}
	f.noLeaks(fmt.Sprint(err))
}

func TestNamer_UpdateSummaryOneInFlightPerSession(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("busy1", "")
	release := make(chan struct{})
	started := make(chan struct{})
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		close(started)
		<-release
		return goodReply(req), nil
	}
	done := make(chan error)
	go func() {
		_, err := f.namer.UpdateSummary(context.Background(), s)
		done <- err
	}()
	<-started
	if _, err := f.namer.UpdateSummary(context.Background(), s); !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNamer_RunFillsABatchFromWant(t *testing.T) {
	f := newNamerFixture(t)
	for i := 0; i < 3; i++ {
		f.add(fmt.Sprintf("r%d", i), "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { f.namer.Run(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }() // a batch in progress finishes first
	f.namer.Want([]string{"r0"})
	f.namer.Want([]string{"r1", "r2", "r0"}) // r0 already queued
	deadline := time.Now().Add(2 * time.Second)
	for f.backend.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.backend.calls() != 1 {
		t.Fatalf("calls = %d, want 1", f.backend.calls())
	}
	if got := len(promptSession.FindAllString(f.backend.last().Prompt, -1)); got != 3 {
		t.Errorf("sessions in the batch = %d, want 3", got)
	}
}

func TestNamer_UpdateSummaryKeepsOwnerEditMadeDuringCall(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("up3", "")
	f.backend.answer = func(req NamingRequest) (NamingReply, error) {
		_ = UpdateMeta(s.FilePath, func(m *Meta) { m.Title, m.TitleBy = "Owner title", "owner" })
		return goodReply(req), nil
	}
	res, err := f.namer.UpdateSummary(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if m := LoadMeta(s.FilePath); m.Title != "Owner title" || m.TitleBy != "owner" || m.DescriptionBy != "model" {
		t.Errorf("meta = %+v", m)
	}
	if res.Title != "Owner title" || strings.Join(res.Kept, ",") != "title" {
		t.Errorf("result = %+v", res)
	}
}

// addUnanswered writes a session whose only entry is the first message.
func (f *namerFixture) addUnanswered(id string) Session {
	f.t.Helper()
	p := filepath.Join(f.dir, id+".jsonl")
	line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`, "Fix the parser "+privateText)
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	s := Session{ID: id, FilePath: p, LastActive: f.now.Add(-time.Minute), TurnCount: 1}
	f.sessions[id] = s
	return s
}

func TestNamingInput_AutomaticNeedsAReply(t *testing.T) {
	f := newNamerFixture(t)
	s := f.addUnanswered("w1")
	if in := namingInput(s.FilePath, autoInputLimit, false); in != "" {
		t.Errorf("automatic input without a reply = %d characters, want none", len(in))
	}
	if in := namingInput(s.FilePath, updateInputLimit, true); !strings.Contains(in, "Fix the parser") {
		t.Error("Update summary works without a reply")
	}
}

func TestNamer_WaitsForAReply(t *testing.T) {
	f := newNamerFixture(t)
	s := f.addUnanswered("w2")
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	f.namer.Want([]string{s.ID}) // shown again, nothing changed: not even read again
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 0 {
		t.Fatalf("calls = %d before any reply, want 0", f.backend.calls())
	}
	reply := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}` + "\n"
	fh, err := os.OpenFile(s.FilePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.WriteString(reply)
	_ = fh.Close()
	s.LastActive = f.now
	f.sessions[s.ID] = s
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d after the reply, want 1", f.backend.calls())
	}
}

func failThree(t *testing.T, f *namerFixture) {
	t.Helper()
	f.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, ErrModelCall }
	for _, id := range []string{"p1", "p2", "p3"} {
		f.add(id, "")
		f.namer.Want([]string{id})
		f.namer.runBatch(context.Background())
	}
	if !f.namer.Status().Failing {
		t.Fatal("not failing after 3 failures")
	}
	f.backend.answer = nil
}

func TestNamer_FailingPausesAllAutomaticBatches(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.StatePath = filepath.Join(f.dir, "naming-state.json")
	f.namer = NewNamer(f.cfg)
	failThree(t, f)
	f.add("fresh", "")
	f.now = f.now.Add(30 * time.Minute)
	f.namer.Want([]string{"fresh"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 3 {
		t.Fatalf("calls = %d, want no call while paused", f.backend.calls())
	}
	// a restart keeps the pause and the failing state
	f.namer = NewNamer(f.cfg)
	if st := f.namer.Status(); !st.Failing || st.Reason != "model call failed" || !st.Paused {
		t.Errorf("status after restart = %+v, want failing and paused", st)
	}
	f.add("fresh2", "")
	f.namer.Want([]string{"fresh2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 3 {
		t.Fatalf("calls = %d, want the pause kept across a restart", f.backend.calls())
	}
	// after the pause, batches run again
	f.now = f.now.Add(31 * time.Minute)
	if st := f.namer.Status(); st.Paused {
		t.Errorf("status after the pause = %+v, want not paused", st)
	}
	f.namer.Want([]string{"fresh2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 4 {
		t.Errorf("calls = %d, want a batch after the pause", f.backend.calls())
	}
}

func TestNamer_OnRequestRunsWhilePausedAndItsSuccessResumes(t *testing.T) {
	f := newNamerFixture(t)
	failThree(t, f)
	f.add("fresh", "")
	if _, err := f.namer.UpdateSummary(context.Background(), f.sessions["fresh"]); err != nil {
		t.Fatal(err)
	}
	if f.backend.calls() != 4 {
		t.Fatalf("calls = %d, want the on-request call while paused", f.backend.calls())
	}
	if st := f.namer.Status(); st.Failing {
		t.Errorf("a success must clear failing: %+v", st)
	}
	f.add("fresh2", "")
	f.namer.Want([]string{"fresh2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 5 {
		t.Errorf("calls = %d, want automatic batches resumed after a success", f.backend.calls())
	}
}

func TestNamer_BackoffSurvivesARestart(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.StatePath = filepath.Join(f.dir, "naming-state.json")
	f.namer = NewNamer(f.cfg)
	f.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, ErrModelCall }
	f.add("k1", "")
	f.namer.Want([]string{"k1"})
	f.namer.runBatch(context.Background())
	f.backend.answer = nil
	f.namer = NewNamer(f.cfg)
	f.now = f.now.Add(30 * time.Minute)
	f.namer.Want([]string{"k1"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d, want the backoff kept across a restart", f.backend.calls())
	}
	f.add("k2", "")
	f.namer.Want([]string{"k2"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 2 {
		t.Errorf("calls = %d, one failure must not pause other sessions", f.backend.calls())
	}
}

func TestNamer_UpdateSummaryErrorsWrapSentinels(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{fmt.Errorf("claude -p: %w", context.DeadlineExceeded), ErrTimeout},
		{fmt.Errorf("status 401: %w", ErrUnauthorized), ErrUnauthorized},
		{fmt.Errorf("status 404: %w", ErrModelNotFound), ErrModelNotFound},
		{fmt.Errorf("reply: %w", ErrBadReply), ErrBadReply},
		{fmt.Errorf("x %s", privateText), ErrModelCall},
	} {
		f := newNamerFixture(t)
		f.cfg.Fallback = nil
		f.namer = NewNamer(f.cfg)
		f.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, tc.err }
		s := f.add("e1", "")
		_, err := f.namer.UpdateSummary(context.Background(), s)
		if !errors.Is(err, tc.want) {
			t.Errorf("%v: err = %v, want %v", tc.err, err, tc.want)
		}
		f.noLeaks(fmt.Sprint(err))
	}
}

func TestNamer_RecordsResolvedModelId(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(NamingRequest) (NamingReply, error) { return NamingReply{}, ErrModelNotFound }
	f.add("r1", "")
	f.namer.Want([]string{"r1"})
	f.namer.runBatch(context.Background())
	if len(f.usage) != 1 || f.usage[0].Model != "claude-haiku-5-5" {
		t.Errorf("usage = %+v, want the alias resolved to claude-haiku-5-5", f.usage)
	}
}

func TestNamer_StateWriteIsAtomicAndPrivate(t *testing.T) {
	f := newNamerFixture(t)
	stateDir := filepath.Join(f.dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.cfg.StatePath = filepath.Join(stateDir, "naming-state.json")
	// an older file with a looser mode
	if err := os.WriteFile(f.cfg.StatePath, []byte(`{"day":"2000-01-01"}`), 0o644); err != nil { // #nosec G306 -- the looser mode under test
		t.Fatal(err)
	}
	before, _ := os.Stat(f.cfg.StatePath)
	f.namer = NewNamer(f.cfg)
	f.add("s1", "")
	f.namer.Want([]string{"s1"})
	f.namer.runBatch(context.Background())
	after, err := os.Stat(f.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", after.Mode().Perm())
	}
	if os.SameFile(before, after) {
		t.Error("the state was rewritten in place; it must be replaced by rename")
	}
	saved, _ := os.ReadFile(f.cfg.StatePath) // #nosec G304 -- a test temp file

	// a write that cannot finish leaves the old state whole
	if err := os.Chmod(stateDir, 0o500); err != nil { // #nosec G302 -- a read-only folder under test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) }) // #nosec G302 -- restores the test folder
	f.add("s2", "")
	f.namer.Want([]string{"s2"})
	f.namer.runBatch(context.Background())
	got, _ := os.ReadFile(f.cfg.StatePath) // #nosec G304 -- a test temp file
	if string(got) != string(saved) {
		t.Errorf("state changed by a write that could not complete:\n%s\nwant\n%s", got, saved)
	}
	if !strings.Contains(f.logs.String(), "could not save its state") {
		t.Error("the failed save is not logged")
	}
	if left, _ := filepath.Glob(filepath.Join(stateDir, ".*tmp*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestSession_AwaySummaryAtOmittedWhenZero(t *testing.T) {
	b, _ := json.Marshal(Session{})
	if strings.Contains(string(b), "away_summary_at") {
		t.Errorf("zero AwaySummaryAt serialized: %s", b)
	}
	b, _ = json.Marshal(Session{AwaySummaryAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if !strings.Contains(string(b), `"away_summary_at":"2026-01-01T00:00:00Z"`) {
		t.Errorf("AwaySummaryAt missing: %s", b)
	}
}

// A call cut short by its context (a phone that left, a server shutting
// down) is not a model failure: no failing state, no pause, no backoff.
func TestNamer_CancelledCallsAreNotFailures(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.StatePath = filepath.Join(f.dir, "naming-state.json")
	f.namer = NewNamer(f.cfg)
	f.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{}, fmt.Errorf("naming request: %w", context.Canceled)
	}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		f.add(id, "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f.namer.Want([]string{id})
		f.namer.runBatch(ctx)
		if _, err := f.namer.UpdateSummary(ctx, f.sessions[id]); err == nil {
			t.Fatal("a cancelled Update summary succeeded")
		}
	}
	if st := f.namer.Status(); st.Failing || st.Paused || st.Reason != "" {
		t.Fatalf("status after cancelled calls = %+v, want neither failing nor paused", st)
	}
	data, err := os.ReadFile(f.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "backoff") || strings.Contains(string(data), "failures") {
		t.Fatalf("state %s, want no backoff or failures saved", data)
	}
	// the sessions are named on the next batch
	f.backend.answer = nil
	before := f.backend.calls()
	f.namer.Want([]string{"c1"})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != before+1 {
		t.Fatalf("calls = %d, want c1 sent again (no backoff)", f.backend.calls()-before)
	}
}

func TestNamer_FailedMetaWriteBacksOffTheSession(t *testing.T) {
	f := newNamerFixture(t)
	f.cfg.StatePath = filepath.Join(f.dir, "naming-state.json")
	f.namer = NewNamer(f.cfg)
	s := f.add("c513a7b7", "")
	if err := os.WriteFile(MetaPath(s.FilePath), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.namer.Want([]string{s.ID})
		f.namer.runBatch(context.Background())
	}
	if f.backend.calls() != 1 {
		t.Fatalf("calls = %d, want 1: a session whose metadata cannot be saved waits out the backoff", f.backend.calls())
	}
	// the backoff survives a restart
	g := NewNamer(f.cfg)
	g.Want([]string{s.ID})
	g.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Fatalf("calls after restart = %d, want 1", f.backend.calls())
	}
	// and ends after namingBackoff
	f.now = f.now.Add(namingBackoff + time.Minute)
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 2 {
		t.Fatalf("calls after the backoff = %d, want 2", f.backend.calls())
	}
	f.noLeaks()
}

func TestNamer_TruncatedReplyKeepsCompleteSessions(t *testing.T) {
	f := newNamerFixture(t)
	// cut by max_tokens in the middle of the third session
	f.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{Text: "```json\n" + `{"sessions":[{"id":"1","title":"Fix the login flow","description":"Fixes the login flow."},{"id":"2","title":"Tidy the docs site","description":"Tidies the docs."},{"id":"3","title":"Rewrite the par`}, nil
	}
	a, b, c := f.add("t1", ""), f.add("t2", ""), f.add("t3", "")
	f.namer.Want([]string{a.ID, b.ID, c.ID})
	f.namer.runBatch(context.Background())
	if m := LoadMeta(a.FilePath); m.Title != "Fix the login flow" || m.Description != "Fixes the login flow." {
		t.Errorf("t1 meta = %+v", m)
	}
	if m := LoadMeta(b.FilePath); m.Title != "Tidy the docs site" {
		t.Errorf("t2 meta = %+v", m)
	}
	if m := LoadMeta(c.FilePath); m.Title != "" || m.DescribedAt != "" {
		t.Errorf("t3 was cut off and must stay unnamed: %+v", m)
	}
	if st := f.namer.Status(); st.Failing {
		t.Errorf("a cut reply with usable sessions is not a failure: %+v", st)
	}
	if !strings.Contains(f.logs.String(), "cut short") {
		t.Errorf("truncation not logged: %q", f.logs.String())
	}
	// nothing complete at all is still unreadable
	g := newNamerFixture(t)
	g.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{Text: `{"sessions":[{"id":"1","title":"Rewrite`}, nil
	}
	d := g.add("t4", "")
	g.namer.Want([]string{d.ID})
	g.namer.runBatch(context.Background())
	if m := LoadMeta(d.FilePath); m.DescribedAt != "" {
		t.Errorf("nothing usable was written: %+v", m)
	}
	if !strings.Contains(g.logs.String(), "unreadable reply") {
		t.Errorf("unreadable reply not logged: %q", g.logs.String())
	}
	f.noLeaks()
}

func TestBuildNamingRequest_AsksForShortDescriptions(t *testing.T) {
	req := buildNamingRequest("m", []namingItem{{input: "x", wantDesc: true}})
	if !strings.Contains(req.System, "one sentence of at most 20 words") {
		t.Errorf("system prompt %q", req.System)
	}
}

func TestNamer_UpdateSummaryCutReplyIsUnreadable(t *testing.T) {
	f := newNamerFixture(t)
	f.backend.answer = func(NamingRequest) (NamingReply, error) {
		return NamingReply{Text: `{"sessions":[{"id":"9","title":"Stray"},{"id":"1","title":"Cut her`}, nil
	}
	s := f.add("u1", "")
	if _, err := f.namer.UpdateSummary(context.Background(), s); !errors.Is(err, ErrBadReply) {
		t.Fatalf("err = %v, want ErrBadReply", err)
	}
	if m := LoadMeta(s.FilePath); m.DescribedAt != "" {
		t.Errorf("a cut reply was written: %+v", m)
	}
}

// Claude Code's recap is the description, but it does not name the session:
// a useless title (here Claude Code's own "Session <id>") still gets one.
func TestNamer_AwaySummaryWithAUselessTitleAsksOnlyForTheTitle(t *testing.T) {
	f := newNamerFixture(t)
	s := f.add("5e55a0b1-1111-4c4f-8a6b-123456789abc", "continue this session 0ddba11e")
	s.AITitle = "Session 5e55a0b1-1111-4c4f-8a6b-123456789abc"
	s.AwaySummary = "Claude Code's recap"
	f.sessions[s.ID] = s
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Fatalf("calls = %d, want 1", f.backend.calls())
	}
	if req := f.backend.last(); !strings.Contains(req.Prompt, "Session 1 (title):") {
		t.Errorf("prompt should ask only for a title")
	}
	m := LoadMeta(s.FilePath)
	if m.Title == "" || m.TitleBy != "model" || m.Description != "" {
		t.Errorf("meta = %+v, want a model title and no description of ours", m)
	}
	// Once: the next Want sends nothing.
	f.namer.Want([]string{s.ID})
	f.namer.runBatch(context.Background())
	if f.backend.calls() != 1 {
		t.Errorf("calls = %d after the session was named", f.backend.calls())
	}
}
