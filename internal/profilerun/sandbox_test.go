package profilerun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/harnessconfig"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
	"github.com/zanetworker/aimux/internal/toolgateway"
)

// sbBackend is a fake OpenShell backend. Exec answers with the stream
// script returns for the session it is asked to run.
type sbBackend struct {
	tokens *toolgateway.Tokens

	mu        sync.Mutex
	ensures   map[string]int // by sandbox
	inEnsure  atomic.Int32
	maxEnsure atomic.Int32
	execs     []sbExec
	downloads []string // remote paths
	script    func(session string) (out string, closeErr, execErr error)
	block     chan struct{}               // when set, the exec reader blocks until closed
	head      func(session string) string // read before the block (claude's first lines)
	closed    chan struct{}               // closed when an exec reader is closed
	// closes records, per exec reader Close, whether its token was still
	// valid then (it must be: the process goes before the token).
	closes []bool
}

type sbExec struct {
	sandbox string
	args    []string
	prompt  string
	token   string
	allow   map[string][]string // what the token allowed while the turn ran
}

func (b *sbBackend) Ensure(_ context.Context, c profile.Compiled) error {
	n := b.inEnsure.Add(1)
	defer b.inEnsure.Add(-1)
	for {
		m := b.maxEnsure.Load()
		if n <= m || b.maxEnsure.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond) // long enough for a racing Ensure to overlap
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ensures == nil {
		b.ensures = map[string]int{}
	}
	b.ensures[c.Sandbox]++
	return nil
}

func (b *sbBackend) Exec(ctx context.Context, c profile.Compiled, cfg, prompt string, args []string) (io.ReadCloser, error) {
	var parsed struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal([]byte(cfg), &parsed)
	tok := ""
	for _, s := range parsed.MCPServers {
		tok = strings.TrimPrefix(s.Headers["Authorization"], "Bearer ")
	}
	session, allow, _ := b.tokens.Lookup(tok)
	b.mu.Lock()
	b.execs = append(b.execs, sbExec{sandbox: c.Sandbox, args: args, prompt: prompt, token: tok, allow: allow})
	b.mu.Unlock()
	out, closeErr, execErr := b.script(session)
	if execErr != nil {
		return nil, execErr
	}
	onClose := func() {
		_, _, valid := b.tokens.Lookup(tok)
		b.mu.Lock()
		b.closes = append(b.closes, valid)
		b.mu.Unlock()
	}
	head := ""
	if b.head != nil {
		head = b.head(session)
	}
	return &sbReader{head: strings.NewReader(head), r: strings.NewReader(out), closeErr: closeErr, block: b.block, closed: b.closed, ctx: ctx, onClose: onClose}, nil
}

func (b *sbBackend) Download(_ context.Context, sandbox, remote, local string) error {
	b.mu.Lock()
	b.downloads = append(b.downloads, remote)
	b.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
		return err
	}
	return os.WriteFile(local, []byte(`{"type":"user"}`+"\n"), 0o600)
}

type sbReader struct {
	head     *strings.Reader
	r        io.Reader
	closeErr error
	block    chan struct{}
	closed   chan struct{}
	ctx      context.Context
	once     sync.Once
	onClose  func()
}

func (s *sbReader) Read(p []byte) (int, error) {
	if s.head != nil && s.head.Len() > 0 {
		return s.head.Read(p)
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-s.ctx.Done(): // the real runner kills the process group on cancel
			return 0, s.ctx.Err()
		}
	}
	return s.r.Read(p)
}

func (s *sbReader) Close() error {
	s.once.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
		if s.closed != nil {
			close(s.closed)
		}
	})
	return s.closeErr
}

// okStream is a successful claude run for session.
func okStream(session string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"t1","name":"mcp__jira__search"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"no access"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
{"type":"result","subtype":"success","session_id":%q}
`, session, session)
}

var workServers2 = map[string][]string{"jira": {"search"}, "wiki": {"read_page"}}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func compiledFor(name string) profile.Compiled {
	return profile.Compiled{Profile: name, Digest: "dg-" + name, PolicyDigest: "pdg-" + name, Sandbox: "aimux-" + name, Harness: "claude-code",
		Servers: workServers2, Intent: harnessconfig.Intent{Mode: "print", Tools: workServers2}}
}

func newSandbox(t *testing.T) (*SandboxEngine, *sbBackend) {
	t.Helper()
	tokens := &toolgateway.Tokens{}
	b := &sbBackend{tokens: tokens, script: func(s string) (string, error, error) { return okStream(s), nil, nil }}
	e := NewSandboxEngine(&sandboxlaunch.Launcher{Backend: b},
		func(p string) (profile.Compiled, error) {
			if p == "broken" {
				return profile.Compiled{}, errors.New("no such profile")
			}
			return compiledFor(p), nil
		},
		func() (string, *toolgateway.Tokens, error) {
			return "http://host.containers.internal:7390", tokens, nil
		})
	e.ArchiveDir = filepath.Join(t.TempDir(), "archive")
	return e, b
}

func sendCollect(t *testing.T, e Engine, session, text string) (string, []map[string]any, error) {
	t.Helper()
	var evs []map[string]any
	stop, err := e.Send(context.Background(), session, text, func(ev acprun.Event) {
		if ev.Kind != acprun.KindUpdate {
			t.Errorf("event kind %q", ev.Kind)
		}
		var m map[string]any
		_ = json.Unmarshal(ev.Data, &m)
		evs = append(evs, m)
	})
	return stop, evs, err
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// The session id is fixed at New and is the claude session id: the first
// turn creates it with --session-id, later turns resume it. The page goes
// in the first prompt only, framed as untrusted data.
func TestSandboxEngine_SessionIDAndResume(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly", URL: "https://a", Title: "T"}, "PAGE BODY")
	if err != nil || !uuidRE.MatchString(sid) {
		t.Fatalf("New = %q %v, want a UUID", sid, err)
	}
	stop, evs, err := sendCollect(t, e, sid, "first")
	if err != nil || stop != "end_turn" {
		t.Fatalf("Send = %q %v", stop, err)
	}
	var kinds []string
	for _, ev := range evs {
		k, _ := ev["sessionUpdate"].(string)
		switch k {
		case "agent_message_chunk":
			kinds = append(kinds, "text:"+ev["content"].(map[string]any)["text"].(string))
		case "tool_call":
			kinds = append(kinds, "tool:"+ev["title"].(string)+":"+ev["status"].(string))
		default:
			kinds = append(kinds, k)
		}
	}
	want := []string{"text:Looking.", "tool:mcp__jira__search:pending", "tool:mcp__jira__search:failed", "text:\n\nDone."}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("events %q, want %q", kinds, want)
	}
	if _, _, err := sendCollect(t, e, sid, "second"); err != nil {
		t.Fatal(err)
	}
	if len(b.execs) != 2 {
		t.Fatalf("%d execs", len(b.execs))
	}
	first, second := b.execs[0].args, b.execs[1].args
	if argAfter(first, "--session-id") != sid || argAfter(first, "--resume") != "" {
		t.Fatalf("first turn args %q", first)
	}
	if argAfter(second, "--resume") != sid || argAfter(second, "--session-id") != "" {
		t.Fatalf("second turn args %q", second)
	}
	p1, p2 := b.execs[0].prompt, b.execs[1].prompt
	if !strings.Contains(p1, "PAGE BODY") || !strings.Contains(p1, "URL: https://a") || !strings.HasSuffix(p1, "Question: first") {
		t.Fatalf("first prompt %q", p1)
	}
	if strings.Contains(p2, "PAGE BODY") || !strings.HasSuffix(p2, "Question: second") {
		t.Fatalf("second prompt %q", p2)
	}
	for i, x := range b.execs {
		for _, a := range x.args {
			if strings.Contains(a, "Question:") || strings.Contains(a, "PAGE BODY") || strings.Contains(a, "first") {
				t.Fatalf("exec %d: prompt or page text on the command line: %q", i, x.args)
			}
		}
	}
	if b.execs[0].sandbox != "aimux-work-readonly" {
		t.Fatalf("sandbox %q", b.execs[0].sandbox)
	}
}

func TestSandboxEngine_ArchivesAfterTurn(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
			t.Fatal(err)
		}
	}
	want := "/sandbox/.claude/projects/-sandbox/" + sid + ".jsonl"
	if len(b.downloads) != 2 || b.downloads[0] != want || b.downloads[1] != want {
		t.Fatalf("downloads %q, want two of %s", b.downloads, want)
	}
	local := filepath.Join(e.ArchiveDir, "openshell-aimux-work-readonly", sid+".jsonl")
	if fi, err := os.Stat(local); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive %s: %v %v", local, fi, err)
	}
	// A turn that ends without a result has no transcript worth copying.
	b.script = func(string) (string, error, error) { return "", errors.New("exit 1"), nil }
	if _, _, err := sendCollect(t, e, sid, "q"); err == nil {
		t.Fatal("turn without a result succeeded")
	}
	if len(b.downloads) != 2 {
		t.Fatalf("archived after a failed start: %q", b.downloads)
	}
}

// Each session gets its own token, valid only while its claude process runs
// and allowing exactly the compiled servers; it is revoked when the turn
// ends, on success, on failure and on cancel.
func TestSandboxEngine_TokenPerSessionRevokedAtEnd(t *testing.T) {
	e, b := newSandbox(t)
	s1, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	s2, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if _, _, err := sendCollect(t, e, s1, "q"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, s2, "q"); err != nil {
		t.Fatal(err)
	}
	b.script = func(string) (string, error, error) {
		return `{"type":"result","is_error":true,"result":"boom","session_id":"x"}`, nil, nil
	}
	if _, _, err := sendCollect(t, e, s1, "q"); err == nil {
		t.Fatal("failed turn succeeded")
	}
	if len(b.execs) != 3 {
		t.Fatalf("%d execs", len(b.execs))
	}
	seen := map[string]bool{}
	for i, x := range b.execs {
		if x.token == "" || seen[x.token] {
			t.Fatalf("exec %d: token missing or reused", i)
		}
		seen[x.token] = true
		if !reflect.DeepEqual(x.allow, workServers2) {
			t.Fatalf("exec %d: token allowed %v while running, want %v", i, x.allow, workServers2)
		}
		if _, _, ok := b.tokens.Lookup(x.token); ok {
			t.Fatalf("exec %d: token still valid after the turn", i)
		}
		for _, a := range x.args {
			if strings.Contains(a, x.token) {
				t.Fatalf("exec %d: token on the command line", i)
			}
		}
	}
}

func TestSandboxEngine_CancelKillsTurnAndRevokes(t *testing.T) {
	e, b := newSandbox(t)
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err := e.Cancel(sid); !errors.Is(err, acprun.ErrNotRunning) {
		t.Fatalf("Cancel with no turn = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.Send(context.Background(), sid, "q", func(acprun.Event) {})
		done <- err
	}()
	waitFor(t, "the turn to start", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.execs) == 1 })
	if _, err := e.Send(context.Background(), sid, "again", func(acprun.Event) {}); !errors.Is(err, acprun.ErrBusy) {
		t.Fatalf("second Send during a turn = %v, want ErrBusy", err)
	}
	if err := e.Cancel(sid); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after Cancel")
	}
	select {
	case <-b.closed:
	default:
		t.Fatal("exec reader not closed")
	}
	if _, _, ok := b.tokens.Lookup(b.execs[0].token); ok {
		t.Fatal("token valid after cancel")
	}
}

// Errors from the sandbox carry up to 4 KiB of remote stderr, which can echo
// the MCP config; the token never leaves the engine in an error.
func TestSandboxEngine_RedactsTokenInErrors(t *testing.T) {
	e, b := newSandbox(t)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	tokenOf := func() string { b.mu.Lock(); defer b.mu.Unlock(); return b.execs[len(b.execs)-1].token }
	cases := map[string]func(tok string) (string, error, error){
		"exec":  func(tok string) (string, error, error) { return "", nil, errors.New("stderr: Bearer " + tok) },
		"close": func(tok string) (string, error, error) { return "", errors.New("exit 1: header " + tok), nil },
		"result": func(tok string) (string, error, error) {
			return `{"type":"result","is_error":true,"result":"bad ` + tok + `","session_id":"x"}`, nil, nil
		},
	}
	for name, mk := range cases {
		var tok string
		b.script = func(s string) (string, error, error) {
			b.mu.Lock()
			tok = b.execs[len(b.execs)-1].token
			b.mu.Unlock()
			return mk(tok)
		}
		_, _, err := sendCollect(t, e, sid, "q")
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if tok == "" || tok != tokenOf() || strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("%s: error %q (token %q)", name, err, tok)
		}
	}
}

// Two first sessions of one profile must not both create its sandbox; a
// changed digest ensures again.
func TestSandboxEngine_EnsureSerialisedPerSandbox(t *testing.T) {
	e, b := newSandbox(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b.ensures["aimux-work-readonly"] != 1 || b.maxEnsure.Load() != 1 {
		t.Fatalf("ensures %v, at most %d at once", b.ensures, b.maxEnsure.Load())
	}
	digest := "dg-2"
	compile := e.Compile
	e.Compile = func(p string) (profile.Compiled, error) {
		c, err := compile(p)
		c.PolicyDigest = digest
		return c, err
	}
	if _, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, ""); err != nil {
		t.Fatal(err)
	}
	if b.ensures["aimux-work-readonly"] != 2 {
		t.Fatalf("changed digest not ensured: %v", b.ensures)
	}
	if _, err := e.New(context.Background(), Meta{Profile: "broken"}, ""); err == nil {
		t.Fatal("a profile that does not compile started a session")
	}
}

// The stream must be the session asked for; anything else is an error.
func TestSandboxEngine_SessionMismatchIsError(t *testing.T) {
	e, b := newSandbox(t)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	b.script = func(string) (string, error, error) { return okStream("4bcd525c-3970-43f1-8420-b5e0d81d30b6"), nil, nil }
	if _, _, err := sendCollect(t, e, sid, "q"); err == nil || !strings.Contains(err.Error(), "not") {
		t.Fatalf("Send = %v", err)
	}
	if _, err := e.Send(context.Background(), "unknown", "q", func(acprun.Event) {}); !errors.Is(err, acprun.ErrNotRunning) {
		t.Fatalf("unknown session = %v", err)
	}
}

// A big page reaches claude whole: the prompt goes over stdin, which has no
// argument-size limit.
func TestSandboxEngine_LargePageNotTruncated(t *testing.T) {
	e, b := newSandbox(t)
	page := strings.Repeat("page text ", 30<<10) // 300 KiB
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, page)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.execs[0].prompt, page) {
		t.Fatalf("prompt is %d bytes and does not hold the %d byte page", len(b.execs[0].prompt), len(page))
	}
	for _, a := range b.execs[0].args {
		if strings.Contains(a, "page text") {
			t.Fatal("page text on the command line")
		}
	}
}

// A first turn cancelled after claude started has created the session:
// the retry resumes it (claude refuses a second --session-id), and the page,
// not yet answered, rides along once more, once.
func TestSandboxEngine_CancelledFirstTurnThenResume(t *testing.T) {
	e, b := newSandbox(t)
	b.block, b.closed = make(chan struct{}), make(chan struct{})
	b.head = func(s string) string {
		return fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", s)
	}
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "PAGE BODY")
	done := make(chan error, 1)
	go func() {
		_, err := e.Send(context.Background(), sid, "q1", func(acprun.Event) {})
		done <- err
	}()
	waitFor(t, "the first turn to start", func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.execs) == 1 })
	if err := e.Cancel(sid); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first turn = %v", err)
	}
	b.block, b.closed, b.head = nil, nil, nil
	if _, _, err := sendCollect(t, e, sid, "q2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, sid, "q3"); err != nil {
		t.Fatal(err)
	}
	second, third := b.execs[1], b.execs[2]
	if argAfter(second.args, "--resume") != sid || argAfter(second.args, "--session-id") != "" {
		t.Fatalf("retry args %q, want --resume %s", second.args, sid)
	}
	if n := strings.Count(second.prompt, "PAGE BODY"); n != 1 {
		t.Fatalf("retry prompt holds the page %d times", n)
	}
	if strings.Contains(third.prompt, "PAGE BODY") || argAfter(third.args, "--resume") != sid {
		t.Fatalf("third turn %q / %q", third.args, third.prompt)
	}
}

var errRemote = errors.New("remote exit 1")

// Remote stderr is cut to its last 4 KiB, so a token can arrive cut in two:
// a prefix ending the text, or a suffix where the cut fell. No fragment of
// 8 or more characters survives, and the error chain is kept.
func TestSandboxEngine_RedactsTruncatedToken(t *testing.T) {
	e, b := newSandbox(t)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	for name, cut := range map[string]func(tok string) string{
		"prefix ends the text": func(tok string) string { return "header: Bearer " + tok[:20] },
		"suffix after the cut": func(tok string) string { return tok[30:] + " more stderr" },
	} {
		var tok string
		b.script = func(string) (string, error, error) {
			b.mu.Lock()
			tok = b.execs[len(b.execs)-1].token
			b.mu.Unlock()
			return "", fmt.Errorf("%w: %s", errRemote, cut(tok)), nil
		}
		_, _, err := sendCollect(t, e, sid, "q")
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		for i := 0; i+8 <= len(tok); i++ {
			if strings.Contains(err.Error(), tok[i:i+8]) {
				t.Fatalf("%s: token fragment %q in %q", name, tok[i:i+8], err)
			}
		}
		if !strings.Contains(err.Error(), "[redacted]") {
			t.Fatalf("%s: %q not marked redacted", name, err)
		}
		if !errors.Is(err, errRemote) {
			t.Fatalf("%s: error chain lost: %v", name, err)
		}
	}
}

// A panicking event callback must still close the exec (killing claude)
// before the token is revoked, and both must happen.
func TestSandboxEngine_PanicClosesExecBeforeRevoke(t *testing.T) {
	e, b := newSandbox(t)
	sid, _ := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the callback's panic did not propagate")
			}
		}()
		_, _ = e.Send(context.Background(), sid, "q", func(acprun.Event) { panic("client went away") })
	}()
	b.mu.Lock()
	closes, tok := append([]bool(nil), b.closes...), b.execs[0].token
	b.mu.Unlock()
	if len(closes) != 1 || !closes[0] {
		t.Fatalf("exec closes %v, want one, while the token was still valid", closes)
	}
	if _, _, ok := b.tokens.Lookup(tok); ok {
		t.Fatal("token valid after the panicking turn")
	}
	// The session is not left marked busy.
	if _, _, err := sendCollect(t, e, sid, "q"); err != nil {
		t.Fatalf("next turn = %v", err)
	}
}

// A turn's model choice reaches claude as --model; none keeps the profile's.
func TestSandboxEngine_TurnModelOnArgv(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Send(WithModel(context.Background(), "opus"), sid, "q", func(acprun.Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sendCollect(t, e, sid, "again"); err != nil {
		t.Fatal(err)
	}
	if got := argAfter(b.execs[0].args, "--model"); got != "opus" {
		t.Fatalf("--model = %q in %q", got, b.execs[0].args)
	}
	if got := argAfter(b.execs[1].args, "--model"); got != "" {
		t.Fatalf("no choice: --model = %q", got)
	}
}

// Defence in depth: a model other than sonnet or opus is refused by the
// engine itself and nothing is launched.
func TestSandboxEngine_RefusesOtherModel(t *testing.T) {
	e, b := newSandbox(t)
	sid, err := e.New(context.Background(), Meta{Profile: "work-readonly"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"gpt", "opus --dangerously-skip-permissions", "Opus"} {
		if _, err := e.Send(WithModel(context.Background(), m), sid, "q", func(acprun.Event) {}); err == nil || !strings.Contains(err.Error(), "model") {
			t.Fatalf("model %q: err = %v", m, err)
		}
	}
	if len(b.execs) != 0 {
		t.Fatalf("%d execs launched", len(b.execs))
	}
}
