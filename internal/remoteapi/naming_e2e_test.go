package remoteapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/cost"
	"github.com/zanetworker/aimux/internal/history"
)

var promptSessionHeader = regexp.MustCompile(`(?m)^Session (\d+) \(`)

// countingBackend counts how often each topic word was sent to the model.
type countingBackend struct {
	mu     sync.Mutex
	calls  int
	topics map[string]int
}

func (b *countingBackend) Complete(_ context.Context, req history.NamingRequest) (history.NamingReply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	for _, topic := range []string{"alpha", "bravo"} {
		b.topics[topic] += strings.Count(req.Prompt, "topic-"+topic)
	}
	var out []map[string]string
	for _, m := range promptSessionHeader.FindAllStringSubmatch(req.Prompt, -1) {
		out = append(out, map[string]string{"id": m[1], "title": "Named task number " + m[1], "description": "A described session."})
	}
	text, _ := json.Marshal(map[string]any{"sessions": out})
	return history.NamingReply{Text: string(text)}, nil
}

func (b *countingBackend) snapshot() (int, map[string]int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, map[string]int{"alpha": b.topics["alpha"], "bravo": b.topics["bravo"]}
}

// End to end, with the real history scan and namer: a client polling the
// list again and again gets each shown session named exactly once.
func TestNaming_RepeatedPollsNameEachSessionOnce(t *testing.T) {
	root := t.TempDir()
	projects, registry := filepath.Join(root, "projects"), filepath.Join(root, "sessions")
	dir := filepath.Join(projects, "-w-app")
	for _, d := range []string{dir, registry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ts := time.Now().Add(-time.Minute).UTC()
	for _, id := range []string{"alpha", "bravo"} {
		lines := []string{
			fmt.Sprintf(`{"type":"user","sessionId":%q,"cwd":"/w/app","timestamp":%q,"message":{"role":"user","content":"c513a7b6"}}`, id, ts.Format(time.RFC3339)),
			fmt.Sprintf(`{"type":"assistant","sessionId":%q,"cwd":"/w/app","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"Working on topic-%s."}]}}`, id, ts.Add(time.Second).Format(time.RFC3339), id),
		}
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := &LocalIndex{ProjectsDir: projects, RegistryDir: registry, Broker: NewBroker(BrokerDeps{}, BrokerOptions{}), Discover: history.Discover}
	backend := &countingBackend{topics: map[string]int{}}
	namer := history.NewNamer(history.NamerConfig{Lookup: index.Lookup, Primary: backend, BatchWait: 20 * time.Millisecond,
		RecordUsage: func(cost.Usage) error { return nil }, Logf: func(string, ...any) {}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); namer.Run(ctx) }()
	defer func() { cancel(); <-done }()
	api := &API{Broker: index.Broker, Sessions: index.Sessions, Session: index.Session, Record: index.Record, Naming: namer, AutoName: true}
	srv := httptest.NewServer(api.Handler([]string{testLogin}, []string{"127.0.0.1"}))
	defer srv.Close()

	var last map[string]any
	for range 10 {
		resp, body := do(t, srv, "GET", "/v1/sessions", "")
		if resp.StatusCode != 200 {
			t.Fatalf("list: %d %v", resp.StatusCode, body)
		}
		last = body
		time.Sleep(30 * time.Millisecond)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, topics := backend.snapshot(); topics["alpha"] == 1 && topics["bravo"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 5 { // more polls after naming: still nothing sent
		do(t, srv, "GET", "/v1/sessions", "")
	}
	time.Sleep(100 * time.Millisecond)
	if _, topics := backend.snapshot(); topics["alpha"] != 1 || topics["bravo"] != 1 {
		t.Fatalf("sessions sent %v, want each exactly once (last list %v)", topics, last)
	}
	_, body := do(t, srv, "GET", "/v1/sessions", "")
	for _, s := range body["sessions"].([]any) {
		m := s.(map[string]any)
		if !strings.HasPrefix(m["title"].(string), "Named task") || m["description"] != "A described session." {
			t.Fatalf("session %v, want the model's title and description shown", m)
		}
	}
}
