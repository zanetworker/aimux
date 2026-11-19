package profilerun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/errcode"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// stageScript is what a fake stage engine does on Send.
type stageScript struct {
	deltas  []string
	tools   []ToolResult // reported to the turn's tool-result collector
	calls   []string     // tool_call titles emitted
	err     error
	waitCtx bool // block until the turn is cancelled
}

type fakeStage struct {
	mu        sync.Mutex
	script    stageScript
	metas     []Meta
	pages     []string
	prompts   []string
	priors    [][]string
	cancelled []string
	started   chan struct{}
}

func newFakeStage(s stageScript) *fakeStage {
	return &fakeStage{script: s, started: make(chan struct{}, 4)}
}

func (f *fakeStage) New(_ context.Context, m Meta, page string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metas = append(f.metas, m)
	f.pages = append(f.pages, page)
	return m.Profile + "-child", nil
}

func (f *fakeStage) Send(ctx context.Context, _ string, text string, onEvent func(acprun.Event)) (string, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, text)
	f.priors = append(f.priors, PriorFrom(ctx))
	s := f.script
	f.mu.Unlock()
	f.started <- struct{}{}
	for _, title := range s.calls {
		onEvent(updateEvent(map[string]any{"sessionUpdate": "tool_call", "toolCallId": title, "title": title, "status": "pending"}))
	}
	if collect := toolResultsFrom(ctx); collect != nil {
		for _, r := range s.tools {
			collect(r)
		}
	}
	for _, d := range s.deltas {
		onEvent(updateEvent(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": d}}))
	}
	if s.waitCtx {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if s.err != nil {
		return "", s.err
	}
	return "end_turn", nil
}

func (f *fakeStage) Cancel(session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, session)
	return nil
}

func updateEvent(u map[string]any) acprun.Event {
	b, _ := json.Marshal(u)
	return acprun.Event{Kind: acprun.KindUpdate, Data: b}
}

// plannerReply is a planner's JSON answer.
func plannerReply(queries []string, notes string) string {
	b, _ := json.Marshal(map[string]any{"public_queries": queries, "private_notes": notes})
	return string(b)
}

// runCompile compiles the planner as having two servers: jira (internal)
// and gh (outside).
func runCompile(name string) (profile.Compiled, error) {
	reg := toolreg.Registry{Sources: map[string]toolreg.Source{
		"jira": {Type: toolreg.TypeMCPHTTP, URL: "http://jira.example/mcp", Authorship: toolreg.AuthorshipInternal},
		"gh":   {Type: toolreg.TypeCLI, Pack: "gh", Authorship: toolreg.AuthorshipOutside},
	}}
	return profile.Compiled{
		Profile:  name,
		Grant:    profile.NewToolGrant(map[string][]string{"jira": {"gh_search", "search"}, "gh": {"issue_view"}}),
		Snapshot: toolreg.NewSnapshot(reg, nil, nil),
	}, nil
}

type runRig struct {
	e                      *RunEngine
	planner, reader, write *fakeStage
	store                  *Store
	events                 []acprun.Event
	mu                     sync.Mutex
}

func newRunRig(t *testing.T, planner, reader, writer stageScript) *runRig {
	t.Helper()
	r := &runRig{planner: newFakeStage(planner), reader: newFakeStage(reader), write: newFakeStage(writer), store: newStore(t)}
	r.e = &RunEngine{Planner: r.planner, Reader: r.reader, Writer: r.write, Compile: runCompile,
		Resolve: func(name string) (profile.Manifest, error) { return profile.Resolve(name, nil) }, Store: r.store}
	return r
}

func (r *runRig) start(t *testing.T, m Meta, page string) string {
	t.Helper()
	m.Profile = "research-with-work"
	id, err := r.e.New(context.Background(), m, page)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.Put(id, m); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *runRig) send(ctx context.Context, id, q string) (string, error) {
	return r.e.Send(ctx, id, q, func(ev acprun.Event) {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	})
}

// texts are the streamed answer text and the aimux notices, in order.
func (r *runRig) texts() (answer string, notices []string, titles []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		var u struct {
			Kind    string `json:"sessionUpdate"`
			Title   string `json:"title"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(ev.Data, &u)
		switch u.Kind {
		case "agent_message_chunk":
			answer += u.Content.Text
		case "aimux_notice":
			notices = append(notices, u.Content.Text)
		case "tool_call":
			titles = append(titles, u.Title)
		}
	}
	return answer, notices, titles
}

func TestRun_StagesInOrderAndWriterGetsLabeledInputs(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"openshell egress release notes"}, "NOTES the egress plan")}, calls: []string{"mcp__jira__search"}},
		stageScript{deltas: []string{"READER found the 0.1.8 release"}, calls: []string{"WebFetch"},
			tools: []ToolResult{{Name: "WebFetch", Input: `{"url":"https://github.com/NVIDIA/OpenShell/releases"}`, Output: "release page"}}},
		stageScript{deltas: []string{"ANSWER text"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	stop, err := r.send(context.Background(), id, "how does our egress plan compare with the latest release")
	if err != nil || stop != "end_turn" {
		t.Fatalf("Send = %q %v", stop, err)
	}
	answer, notices, titles := r.texts()
	if answer != "ANSWER text" {
		t.Fatalf("streamed answer %q: only the writer's text reaches the client", answer)
	}
	if len(notices) != 3 || notices[0] != "Looking in your workspace" || notices[1] != "Searching the web: 1 query" || notices[2] != "Writing the answer" {
		t.Fatalf("notices %q", notices)
	}
	if !reflect.DeepEqual(titles, []string{"workspace: mcp__jira__search", "web: WebFetch"}) {
		t.Fatalf("tool titles %q, want each prefixed with its stage", titles)
	}
	if len(r.planner.metas) != 1 || r.planner.metas[0].Profile != "work-readonly" ||
		len(r.reader.metas) != 1 || r.reader.metas[0].Profile != "research" ||
		len(r.write.metas) != 1 || r.write.metas[0].Profile != "page" {
		t.Fatalf("children: planner %+v reader %+v writer %+v", r.planner.metas, r.reader.metas, r.write.metas)
	}
	w := r.write.prompts[0]
	for _, want := range []string{"how does our egress plan compare with the latest release", "NOTES the egress plan", "READER found the 0.1.8 release", "https://github.com/NVIDIA/OpenShell/releases"} {
		if !strings.Contains(w, want) {
			t.Errorf("writer prompt lacks %q", want)
		}
	}
	m, _ := r.store.Get(id)
	if m.Run == nil || m.Run.Stage != StageDone || !reflect.DeepEqual(m.Run.Links, []string{"https://github.com/NVIDIA/OpenShell/releases"}) ||
		m.Run.Children["planner"] != "work-readonly-child" || m.Run.Children["writer"] != "page-child" {
		t.Fatalf("run state %+v", m.Run)
	}
}

func TestRun_ReaderSeesOnlyAcceptedQueries(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"openshell egress release notes", "RHAISTRAT-2955 status"}, "SECRETNOTES about Acme")},
			tools: []ToolResult{{Name: "mcp__jira__search", Input: `{"jql":"key = RHAISTRAT-2955"}`, Output: "JIRAOUTPUT"}}},
		stageScript{deltas: []string{"found"}},
		stageScript{deltas: []string{"answer"}},
	)
	id := r.start(t, Meta{Client: "wingman", URL: "https://docs.example/plan", WithPage: true}, "PAGETEXT internal plan")
	ctx := WithPrior(context.Background(), []string{"EARLIER question"})
	if _, err := r.send(ctx, id, "QUESTIONWORDS compare the plan"); err != nil {
		t.Fatal(err)
	}
	if r.planner.pages[0] != "PAGETEXT internal plan" {
		t.Fatalf("planner page %q, want the page", r.planner.pages[0])
	}
	p := r.reader.prompts[0]
	if !strings.Contains(p, "openshell egress release notes") {
		t.Fatalf("reader prompt lacks the accepted query: %q", p)
	}
	for _, leak := range []string{"RHAISTRAT", "SECRETNOTES", "Acme", "PAGETEXT", "QUESTIONWORDS", "EARLIER", "JIRAOUTPUT", "docs.example"} {
		if strings.Contains(p, leak) {
			t.Errorf("reader prompt carries %q", leak)
		}
	}
	if r.reader.pages[0] != "" || r.reader.priors[0] != nil || r.reader.metas[0].URL != "" {
		t.Fatalf("reader got page %q prior %v url %q", r.reader.pages[0], r.reader.priors[0], r.reader.metas[0].URL)
	}
	_, notices, _ := r.texts()
	if !slicesContain(notices, "1 query was not sent: issue_key") {
		t.Fatalf("notices %q, want the rejection by reason", notices)
	}
	m, _ := r.store.Get(id)
	if len(m.Run.Rejected) != 1 || m.Run.Rejected[0].Reason != "issue_key" || !reflect.DeepEqual(m.Run.Accepted, []string{"openshell egress release notes"}) {
		t.Fatalf("run state %+v", m.Run)
	}
}

func TestRun_NoAcceptedQueriesSkipsReaderAndSaysSo(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"RHAISTRAT-2955"}, "notes")}},
		stageScript{deltas: []string{"never"}},
		stageScript{deltas: []string{"answer"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(context.Background(), id, "what is our plan"); err != nil {
		t.Fatal(err)
	}
	if len(r.reader.metas) != 0 {
		t.Fatal("the reader ran with no accepted query")
	}
	w := r.write.prompts[0]
	if !strings.Contains(w, "The web was not searched") || !strings.Contains(w, "issue_key") {
		t.Fatalf("writer prompt does not say the web was not used and why: %q", w)
	}
	if strings.Contains(w, "RHAISTRAT") {
		t.Fatal("the writer prompt repeats the rejected query")
	}
}

func TestRun_PlannerBadJSONFailsWithCode(t *testing.T) {
	r := newRunRig(t, stageScript{deltas: []string{"I could not find anything."}}, stageScript{}, stageScript{})
	id := r.start(t, Meta{Client: "wingman"}, "")
	_, err := r.send(context.Background(), id, "q")
	if e, ok := errcode.Of(err); !ok || e.Code != errcode.PlannerOutputInvalid {
		t.Fatalf("err = %v, want planner_output_invalid", err)
	}
	if len(r.reader.metas)+len(r.write.metas) != 0 {
		t.Fatal("a later stage ran after an invalid plan")
	}
	m, _ := r.store.Get(id)
	if m.Run == nil || m.Run.Stage != StageFailed {
		t.Fatalf("run state %+v, want failed", m.Run)
	}
}

func TestRun_PlannerFailureFailsWithCode(t *testing.T) {
	r := newRunRig(t, stageScript{err: errors.New("tool gateway: lease refused")}, stageScript{}, stageScript{})
	id := r.start(t, Meta{Client: "wingman"}, "")
	_, err := r.send(context.Background(), id, "q")
	if e, ok := errcode.Of(err); !ok || e.Code != errcode.RunPlannerFailed {
		t.Fatalf("err = %v, want run_planner_failed", err)
	}
}

func TestRun_CancelDuringReaderStopsWriter(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"openshell release notes"}, "notes")}},
		stageScript{waitCtx: true},
		stageScript{deltas: []string{"answer"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	done := make(chan error, 1)
	go func() { _, err := r.send(context.Background(), id, "q"); done <- err }()
	<-r.planner.started
	select {
	case <-r.reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never started")
	}
	if err := r.e.Cancel(id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled run succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after Cancel")
	}
	if len(r.write.metas) != 0 {
		t.Fatal("the writer started after a cancel")
	}
	if !reflect.DeepEqual(r.reader.cancelled, []string{"research-child"}) {
		t.Fatalf("reader cancelled %v, want its active child", r.reader.cancelled)
	}
}

// Outside reads come from the compiled grant and snapshot: a tool named
// like a GitHub tool on an internal server is internal; a call on the gh
// source is outside, attempted even when it failed.
func TestRun_OutsideFlagsFromSnapshotNotNames(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply(nil, "notes")}, tools: []ToolResult{
			{Name: "mcp__jira__gh_search", Output: "ok"},
			{Name: "mcp__gh__issue_view", Output: "boom", Failed: true},
		}},
		stageScript{}, stageScript{deltas: []string{"answer"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(context.Background(), id, "q"); err != nil {
		t.Fatal(err)
	}
	m, _ := r.store.Get(id)
	if !reflect.DeepEqual(m.Run.OutsideAttempted, []string{"gh"}) || len(m.Run.OutsideSucceeded) != 0 {
		t.Fatalf("outside attempted %v succeeded %v, want [gh] and none", m.Run.OutsideAttempted, m.Run.OutsideSucceeded)
	}
}

func TestRun_ImageRefused(t *testing.T) {
	r := newRunRig(t, stageScript{}, stageScript{}, stageScript{})
	id := r.start(t, Meta{Client: "wingman"}, "")
	ctx := WithImage(context.Background(), &acprun.Image{MimeType: "image/png", Data: "aGk="})
	_, err := r.send(ctx, id, "what is in this image")
	if e, ok := errcode.Of(err); !ok || e.Code != errcode.ImageInvalid {
		t.Fatalf("err = %v, want image_invalid", err)
	}
	if len(r.planner.metas) != 0 {
		t.Fatal("the planner ran for a question with an image")
	}
}

func slicesContain(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The links a run reports are the pages the reader fetched, never a URL the
// writer wrote on its own or one the reader failed to fetch.
func TestRun_LinksAreFetchedURLsOnly(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"openshell release notes"}, "notes")}},
		stageScript{deltas: []string{"found"}, tools: []ToolResult{
			{Name: "WebFetch", Input: `{"url":"https://github.com/NVIDIA/OpenShell/releases"}`, Output: "page"},
			{Name: "WebFetch", Input: `{"url":"https://broken.example/x"}`, Output: "404", Failed: true},
			{Name: "WebFetch", Input: `{"url":"https://github.com/NVIDIA/OpenShell/releases"}`, Output: "again"},
			{Name: "WebSearch", Input: `{"query":"openshell"}`, Output: "results"},
		}},
		stageScript{deltas: []string{"See https://invented.example/secret for details"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	var reported []string
	ctx := WithTurnLinks(context.Background(), func(l []string) { reported = l })
	if _, err := r.send(ctx, id, "q"); err != nil {
		t.Fatal(err)
	}
	want := []string{"https://github.com/NVIDIA/OpenShell/releases"}
	if !reflect.DeepEqual(reported, want) {
		t.Fatalf("reported links %v, want %v", reported, want)
	}
}
