package modelproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/ledger"
)

func requestOf(t *testing.T, body string) messageRequest {
	t.Helper()
	var r messageRequest
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConversationTokens(t *testing.T) {
	long := strings.Repeat("word ", 80000) // 400,000 characters
	text := requestOf(t, fmt.Sprintf(`{"system":"be brief","messages":[{"role":"user","content":%q}]}`, long))
	if got := conversationTokens(text); got < 95000 || got > 110000 {
		t.Errorf("400,000 characters of text = %d tokens, want about 100,000", got)
	}
	// Blocks: text, a tool's call and its result all count; an image counts as an image, not as its bytes.
	image := strings.Repeat("A", 2_000_000)
	blocks := requestOf(t, fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"text","text":%q},`+
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]},`+
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"path":%q}}]},`+
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":%q},{"type":"image","source":{"type":"base64","data":%q}}]}]}]}`,
		strings.Repeat("a", 4000), image, strings.Repeat("p", 4000), strings.Repeat("r", 8000), image))
	if got := conversationTokens(blocks); got < 4000+2*imageTokens || got > 5200+2*imageTokens {
		t.Errorf("blocks = %d tokens, want about %d: 16,000 characters and two images", got, 4000+2*imageTokens)
	}
	// The tools a request offers and its system prompt are part of what is sent.
	tools := requestOf(t, fmt.Sprintf(`{"system":[{"type":"text","text":%q}],"tools":[{"name":"Read","description":%q,"input_schema":{"type":"object"}}],"messages":[]}`,
		strings.Repeat("s", 4000), strings.Repeat("d", 4000)))
	if got := conversationTokens(tools); got < 1900 || got > 2300 {
		t.Errorf("system and tools = %d tokens, want about 2,000", got)
	}
	if got := conversationTokens(messageRequest{}); got != 0 {
		t.Errorf("nothing = %d", got)
	}
}

func TestShownUsage(t *testing.T) {
	// Codex's figure stands when it is the larger: it counts what it was sent, its own instructions included.
	codex := Usage{InputTokens: 5000, CacheReadInputTokens: 150000, OutputTokens: 40}
	if got := shownUsage(codex, 120000, 0); got != codex {
		t.Errorf("Codex's larger figure changed: %+v", got)
	}
	// A rough estimate a little over Codex's own count does not replace it.
	if got := shownUsage(codex, 170000, 0); got != codex {
		t.Errorf("a slightly larger estimate replaced Codex's count: %+v", got)
	}
	if got := shownUsage(Usage{InputTokens: 17}, 9000, 0); got != (Usage{InputTokens: 17}) {
		t.Errorf("a small conversation: %+v", got)
	}
	// Near the model's window the estimate is taken as soon as it is the larger: a warm conversation Codex reports
	// at 400,000 that would be 490,000 to send again is told as 490,000 against a window of 475,000.
	warm := Usage{InputTokens: 10000, CacheReadInputTokens: 390000}
	if got := shownUsage(warm, 490000, 475000); got.InputTokens+got.CacheReadInputTokens != 490000 || !got.estimated {
		t.Errorf("near the window: %+v", got)
	}
	// Far from it the same small difference changes nothing, and neither does it when the window is not known.
	if got := shownUsage(Usage{InputTokens: 100000}, 110000, 475000); got != (Usage{InputTokens: 100000}) {
		t.Errorf("far from the window: %+v", got)
	}
	if got := shownUsage(warm, 490000, 0); got != warm {
		t.Errorf("window not known: %+v", got)
	}
	// The conversation is larger than the copy Codex holds: the session is told the conversation's size, and
	// that the figure is not Codex's.
	got := shownUsage(codex, 900000, 0)
	if got.InputTokens+got.CacheReadInputTokens != 900000 || got.CacheReadInputTokens != 150000 || got.OutputTokens != 40 || !got.estimated {
		t.Errorf("a conversation larger than Codex's copy: %+v", got)
	}
}

// The size a session is told is the conversation it holds; the ledger keeps what Codex used.
func TestHandler_ACodexAnswerCarriesTheConversationsSize(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		backend, _ := newFakeBackend(t)
		dir := filepath.Join(t.TempDir(), "ledger")
		writer, err := ledger.OpenWriter(dir, LedgerName(StablePort))
		if err != nil {
			t.Fatal(err)
		}
		p := ledgerProxy{NewHandler(backend, testKey, WithLedger(writer)), dir, writer}
		// An earlier exchange of 800,000 characters, then a prompt the fake Codex answers reporting 17 tokens.
		body := fmt.Sprintf(`{"model":"codex:gpt-6.2-sol","max_tokens":64,"stream":%v,"messages":[{"role":"user","content":%q},{"role":"assistant","content":"ok"},{"role":"user","content":"hello"}]}`,
			streaming, strings.Repeat("word ", 160000))
		res := request(p.handler, http.MethodPost, "/v1/messages", body)
		_ = writer.Close()
		if res.Code != http.StatusOK {
			t.Fatalf("streaming %v: HTTP %d: %.300s", streaming, res.Code, res.Body)
		}
		total := 0
		if streaming {
			// The size is in the usage of the closing event.
			at := strings.LastIndex(res.Body.String(), `"usage":`)
			var usage Usage
			if at < 0 || json.NewDecoder(strings.NewReader(res.Body.String()[at+len(`"usage":`):])).Decode(&usage) != nil {
				t.Fatalf("no usage in the stream: %.300s", res.Body)
			}
			total = usage.InputTokens + usage.CacheReadInputTokens
		} else {
			var message struct{ Usage Usage }
			if err := json.Unmarshal(res.Body.Bytes(), &message); err != nil {
				t.Fatal(err)
			}
			total = message.Usage.InputTokens + message.Usage.CacheReadInputTokens
		}
		if total < 190000 || total > 220000 {
			t.Errorf("streaming %v: the session was told %d tokens, want about 200,000", streaming, total)
		}
		lines := p.lines(t)
		if len(lines) != 1 || lines[0].InputTokens != 17 || lines[0].Estimated {
			t.Errorf("streaming %v: the ledger holds %+v, want Codex's own 17 tokens", streaming, lines)
		}
	}
}

// The window Codex gives a model is in the file Codex keeps of its models. Claude Code assumes a million tokens
// for a name it does not know, so it has to be told.
func TestCodexContextWindow(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "models_cache.json")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(cache, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"models":[{"slug":"gpt-6.2-sol","context_window":500000,"effective_context_window_percent":95},{"slug":"gpt-6.2-astra","context_window":272000}]}`)
	if got, ok := CodexContextWindow(cache, "gpt-6.2-sol"); !ok || got != 475000 {
		t.Errorf("sol = %d, %v; want 95%% of 500,000", got, ok)
	}
	// No percentage given: a tenth is kept back, as room for the answer.
	if got, ok := CodexContextWindow(cache, "gpt-6.2-astra"); !ok || got != 244800 {
		t.Errorf("astra = %d, %v", got, ok)
	}
	for _, missing := range []string{"gpt-9", ""} {
		if got, ok := CodexContextWindow(cache, missing); ok || got != 0 {
			t.Errorf("%q = %d, %v", missing, got, ok)
		}
	}
	write(`not json`)
	if _, ok := CodexContextWindow(cache, "gpt-6.2-sol"); ok {
		t.Error("a file that is not JSON gave a window")
	}
	write(`{"models":[{"slug":"gpt-6.2-sol","context_window":-5}]}`)
	if _, ok := CodexContextWindow(cache, "gpt-6.2-sol"); ok {
		t.Error("a window that is not a size was used")
	}
	if _, ok := CodexContextWindow(filepath.Join(t.TempDir(), "none.json"), "gpt-6.2-sol"); ok {
		t.Error("no file gave a window")
	}
}

func TestSessionEnvWindow(t *testing.T) {
	lines := SessionEnvWindow(475000)
	want := []string{"export CLAUDE_CODE_MAX_CONTEXT_TOKENS='475000'", "export AIMUX_MODEL_PROXY_WINDOW='475000'"}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("lines = %q", lines)
	}
	if lines := SessionEnvWindow(0); lines != nil {
		t.Errorf("no window: %q", lines)
	}
}

func TestCodexSmallestWindow(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "models_cache.json")
	if err := os.WriteFile(cache, []byte(`{"models":[{"slug":"gpt-6.2-sol","context_window":500000,"effective_context_window_percent":95},{"slug":"gpt-6.2-astra","context_window":272000,"effective_context_window_percent":95},{"slug":"odd","context_window":0}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := CodexSmallestWindow(cache); !ok || got != 258400 {
		t.Errorf("smallest = %d, %v", got, ok)
	}
	if _, ok := CodexSmallestWindow(filepath.Join(t.TempDir(), "none.json")); ok {
		t.Error("no file gave a window")
	}
	if got := CodexModelsCache("/home/x", func(k string) string { return map[string]string{"CODEX_HOME": "/elsewhere"}[k] }); got != "/elsewhere/models_cache.json" {
		t.Errorf("with CODEX_HOME: %s", got)
	}
	if got := CodexModelsCache("/home/x", func(string) string { return "" }); got != "/home/x/.codex/models_cache.json" {
		t.Errorf("default: %s", got)
	}
}

// Content nested deeper than a request has reason to be is counted by its length, not walked.
func TestConversationTokens_DeepNesting(t *testing.T) {
	deep := strings.Repeat(`[`, 5000) + `"x"` + strings.Repeat(`]`, 5000)
	r := requestOf(t, `{"messages":[{"role":"user","content":`+deep+`}]}`)
	if got := conversationTokens(r); got < 2000 || got > 3000 {
		t.Errorf("deeply nested content = %d tokens, want its length over four", got)
	}
}
