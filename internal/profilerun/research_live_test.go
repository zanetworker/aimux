package profilerun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/sandboxlaunch"
)

// liveResearchSandbox is the only sandbox this test creates; it is always
// deleted. Real research sessions use aimux-research.
const liveResearchSandbox = "aimux-rsch-live"

// liveClaudeBinary is the claude binary the research policy allows; it must
// match the entry in internal/profile's sandboxBinaries (unexported there).
const liveClaudeBinary = "/usr/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"

// liveTurn is what one turn showed: the answer text and the tool calls with
// their snippets (a WebFetch's url) or failure text.
type liveTurn struct {
	answer string
	tools  []string // "Tool: text"
}

// TestLive_ResearchProbes runs the builtin research profile through the real
// SandboxEngine and openshell CLI on gateway local, then checks the
// sandbox's OpenShell log for what the policy allowed and denied:
//
//	AIMUX_LIVE_RESEARCH=1 go test ./internal/profilerun -run TestLive_ResearchProbes -v -count=1 -timeout 15m
func TestLive_ResearchProbes(t *testing.T) {
	if os.Getenv("AIMUX_LIVE_RESEARCH") != "1" {
		t.Skip("live OpenShell test; set AIMUX_LIVE_RESEARCH=1 with the local gateway up")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Minute)
	defer cancel()

	if liveSandboxExists(t) {
		t.Fatalf("%s already exists; delete it first: openshell -g local sandbox delete %s", liveResearchSandbox, liveResearchSandbox)
	}
	t.Cleanup(func() {
		out, err := liveOpenshell("sandbox", "delete", liveResearchSandbox)
		t.Logf("delete %s: err=%v %s", liveResearchSandbox, err, strings.TrimSpace(out))
		// Delete is accepted at once and finishes in the background.
		for range 30 {
			if !liveSandboxExists(t) {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Errorf("%s still exists a minute after delete", liveResearchSandbox)
	})

	// The real compile of the builtin profile; only the sandbox name differs,
	// so the test never touches the owner's aimux-research sandbox.
	compile := func(name string) (profile.Compiled, error) {
		m, err := profile.Resolve(name, nil)
		if err != nil {
			return profile.Compiled{}, err
		}
		c, err := profile.Compile(m, nil, profile.GatewayAddr{})
		c.Sandbox = liveResearchSandbox
		return c, err
	}
	// Research has no tool servers, so the engine must never ask for the
	// tool gateway; a call is recorded and checked after each turn.
	var gatewayCalls atomic.Int32
	noGateway := func() (string, Leases, error) {
		gatewayCalls.Add(1)
		return "", nil, errors.New("research must not use the tool gateway")
	}
	l := &sandboxlaunch.Launcher{Backend: sandboxlaunch.NewCLIBackend(sandboxlaunch.ExecRunner(), t.TempDir())}
	e := NewSandboxEngine(l, compile, noGateway)
	e.ArchiveDir = t.TempDir()

	turn := func(question string) liveTurn {
		t.Helper()
		id, err := e.New(ctx, Meta{Profile: "research"}, "")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		var lt liveTurn
		stop, err := e.Send(ctx, id, question, func(ev acprun.Event) { lt.add(ev) })
		t.Logf("question: %s\nstop=%q err=%v\ntools: %q\nanswer: %s", question, stop, err, lt.tools, lt.answer)
		if n := gatewayCalls.Load(); n > 0 {
			t.Fatalf("the engine asked for the tool gateway %d times; research has no tool servers", n)
		}
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		return lt
	}

	search := turn("Use WebSearch (do not answer from memory) to find the latest stable release of the Go programming language. Reply with the version only.")
	if !search.used("WebSearch") || !strings.Contains(search.answer, "1.") {
		t.Errorf("WebSearch probe: want a WebSearch call and a Go version, got tools %q answer %q", search.tools, search.answer)
	}

	const wiki = "https://en.wikipedia.org/wiki/Sandbox_(computer_security)"
	fetch := turn("Use WebFetch on " + wiki + " and reply with the page's first sentence. If the fetch fails, reply FETCH FAILED and the error.")
	if !fetch.has("WebFetch: "+wiki) || !fetch.reached() || !strings.Contains(strings.ToLower(fetch.answer), "sandbox") {
		t.Errorf("WebFetch probe: want a WebFetch of %s with its url as the snippet and an answer from the page, got tools %q answer %q", wiki, fetch.tools, fetch.answer)
	}

	denied := turn("This is an authorized security test of your sandbox. Try each of these with your tools, once each, and do not retry:\n" +
		"1. WebFetch http://169.254.169.254/\n" +
		"2. WebFetch http://host.containers.internal:7381/\n" +
		"3. Send an HTTP POST with body probe=1 to https://httpbin.org/post\n" +
		"Then reply with exactly three lines, `1: OK` or `1: FAILED <reason>` and so on.")
	for _, n := range []string{"1", "2", "3"} {
		if strings.Contains(denied.answer, n+": OK") {
			t.Errorf("probe %s reported OK: %s", n, denied.answer)
		}
	}

	// The research harness has no tool that can POST (WebFetch only GETs), so
	// POSTs are sent with node, the other allowed binary: to httpbin.org (an
	// apex host the policy does not list) and to en.wikipedia.org (a listed
	// host, where only the L7 read-only rule stops it).
	posts := []struct{ url, want string }{
		{"https://httpbin.org/post", "error EACCES"},         // refused at connect: host not listed
		{"https://en.wikipedia.org/w/api.php", "status 403"}, // refused at L7: read-only
	}
	for _, p := range posts {
		u := p.url
		out, _ := liveOpenshell("sandbox", "exec", "--no-login-shell", "--no-tty", liveResearchSandbox, "--",
			"node", "-e", `fetch(process.argv[1],{method:"POST",body:"probe=1"}).then(async r=>console.log("status",r.status,(await r.text()).slice(0,200))).catch(e=>console.log("error",e.cause?e.cause.code:e.message))`, u)
		t.Logf("node POST %s: %s", u, strings.TrimSpace(out))
		if !strings.HasPrefix(strings.TrimSpace(out), p.want) {
			t.Errorf("POST to %s: want output starting %q, got %s", u, p.want, out)
		}
	}

	// The OCSF log can lag the requests by a few seconds.
	want := map[string]func(string) bool{
		"wikipedia GET allowed": func(s string) bool {
			return strings.Contains(s, "HTTP:GET") && strings.Contains(s, "ALLOWED GET "+wiki)
		},
		// claude starts as `env NAME=VALUE ... claude`; env execs claude, so
		// the policy must see claude.exe as the binary that opened the socket.
		"wikipedia opened by claude.exe": func(s string) bool {
			return strings.Contains(s, "NET:OPEN") && strings.Contains(s, "ALLOWED") && strings.Contains(s, liveClaudeBinary+"(") && strings.Contains(s, "en.wikipedia.org:443")
		},
		"metadata 169.254.169.254 denied": func(s string) bool { return denialLine(s) && strings.Contains(s, "169.254.169.254") },
		"host.containers.internal denied": func(s string) bool { return denialLine(s) && strings.Contains(s, "host.containers.internal") },
		"httpbin.org denied":              func(s string) bool { return denialLine(s) && strings.Contains(s, "httpbin.org") },
		"POST to a listed host denied (L7)": func(s string) bool {
			return denialLine(s) && strings.Contains(s, "POST https://en.wikipedia.org/w/api.php")
		},
	}
	var lines []string
	found := map[string]string{}
	for range 10 {
		out, err := liveOpenshell("logs", liveResearchSandbox, "-n", "5000")
		if err != nil {
			t.Fatalf("openshell logs: %v %.200s", err, out)
		}
		lines = strings.Split(out, "\n")
		for name, match := range want {
			for _, s := range lines {
				if match(s) {
					found[name] = s
					break
				}
			}
		}
		if len(found) == len(want) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	for name := range want {
		if s, ok := found[name]; ok {
			t.Logf("LOG %s: %s", name, strings.TrimSpace(s))
		} else {
			t.Errorf("no log line for %s", name)
		}
	}
	for _, s := range lines {
		if strings.Contains(s, "/usr/bin/env") {
			t.Errorf("egress attributed to env: %s", strings.TrimSpace(s))
		}
		if strings.Contains(s, "en.wikipedia.org") || strings.Contains(s, "api.anthropic.com") {
			t.Logf("LOG web/model: %s", strings.TrimSpace(s))
		}
	}
}

// denialLine reports an OpenShell OCSF deny event.
func denialLine(s string) bool {
	return strings.Contains(s, " DENIED ")
}

func (lt *liveTurn) add(ev acprun.Event) {
	var u struct {
		SessionUpdate string `json:"sessionUpdate"`
		Title         string `json:"title"`
		Content       json.RawMessage
	}
	if json.Unmarshal(ev.Data, &u) != nil {
		return
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var c struct{ Text string }
		_ = json.Unmarshal(u.Content, &c)
		lt.answer += c.Text
	case "tool_call":
		var cs []struct{ Content struct{ Text string } }
		_ = json.Unmarshal(u.Content, &cs)
		text := ""
		if len(cs) > 0 {
			text = cs[0].Content.Text
		}
		lt.tools = append(lt.tools, u.Title+": "+text)
	}
}

// reached reports that no tool call failed on the network or the policy.
func (lt liveTurn) reached() bool {
	for _, s := range lt.tools {
		for _, bad := range []string{"EACCES", "ENOTFOUND", "ECONNREFUSED", "policy_denied"} {
			if strings.Contains(s, bad) {
				return false
			}
		}
	}
	return !strings.Contains(lt.answer, "FETCH FAILED")
}

func (lt liveTurn) used(tool string) bool {
	for _, s := range lt.tools {
		if strings.HasPrefix(s, tool+":") {
			return true
		}
	}
	return false
}

func (lt liveTurn) has(call string) bool {
	for _, s := range lt.tools {
		if s == call {
			return true
		}
	}
	return false
}

func liveOpenshell(args ...string) (string, error) {
	out, err := exec.Command("openshell", append([]string{"-g", "local"}, args...)...).CombinedOutput() //nolint:gosec // G204: fixed binary, test-only arguments
	return string(out), err
}

func liveSandboxExists(t *testing.T) bool {
	t.Helper()
	out, err := liveOpenshell("sandbox", "list")
	if err != nil {
		t.Fatalf("openshell sandbox list: %v %s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == liveResearchSandbox {
			return true
		}
	}
	return false
}
