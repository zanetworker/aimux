package modelproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type prefixHash [sha256.Size]byte

type sessionKey struct {
	id     string
	config prefixHash
}

// SessionTag is the short, one-way name a conversation goes by in the request history: enough to group its
// requests and to look up a session you already know by hashing it, without the history naming any conversation.
func SessionTag(id string) string {
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("aimux-model-proxy-session\x00" + id))
	return hex.EncodeToString(sum[:4])
}

type sessionInput struct {
	key sessionKey
	// tag names this conversation in the request history.
	tag      string
	prefixes []prefixHash
	offsets  []int
	// messages runs parallel to prefixes[1:], so the thread's held count indexes the first new message.
	messages []inputMessage
	outcome  string
	// detail says what differed when outcome is a fallback, for the request log. It names shapes and counts,
	// never content.
	detail string
}

type inputMessage struct {
	role   string
	blocks []requestBlock
}

func newSessionInput(id string, req TurnRequest, count int) *sessionInput {
	tools := append([]dynamicTool(nil), req.Tools...)
	for i := range tools {
		tools[i].Name = req.ToolNames[tools[i].Name]
		tools[i].InputSchema = canonicalJSON(tools[i].InputSchema)
	}
	// These fields were decoded from a valid Messages request.
	config, _ := json.Marshal(struct {
		System string
		Tools  []dynamicTool
	}{req.System, tools})
	return &sessionInput{
		key: sessionKey{id, sha256.Sum256(config)}, tag: SessionTag(id), prefixes: make([]prefixHash, 1, count+1),
		offsets: make([]int, 0, count),
	}
}

func (s *sessionInput) record(msg conversationMessage, blocks []requestBlock, offset int) {
	s.prefixes = append(s.prefixes, hashMessage(s.prefixes[len(s.prefixes)-1], msg))
	s.offsets = append(s.offsets, offset)
	s.messages = append(s.messages, inputMessage{msg.Role, blocks})
}

// Hash every message field except transport-only cache markers, independent of JSON formatting.
func hashMessage(previous prefixHash, msg conversationMessage) prefixHash {
	var blocks []map[string]any
	raw := msg.Content
	switch {
	case len(raw) != 0 && raw[0] == '"':
		var text string
		_ = json.Unmarshal(raw, &text) // Already validated by decodeContent.
		blocks = []map[string]any{{"type": "text", "text": text}}
	case len(raw) != 0:
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		_ = decoder.Decode(&blocks) // Already validated by decodeContent.
		for _, block := range blocks {
			delete(block, "cache_control")
		}
	}
	message, _ := json.Marshal(struct {
		Role   string
		Blocks []map[string]any
	}{msg.Role, blocks})
	return sha256.Sum256(append(previous[:], message...))
}

func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	// Decoded JSON consists only of types Marshal supports.
	data, _ := json.Marshal(value)
	return data
}

type sessions struct {
	mu                        sync.Mutex
	entries                   map[sessionKey]*sessionEntry
	pauseTimeout, idleTimeout time.Duration
	closed                    bool
}

// An entry's busy channel grants one request or expiry callback exclusive access to the thread.
type sessionEntry struct {
	busy     chan struct{}
	thread   *sessionThread
	timer    *time.Timer
	deadline time.Time
	expired  string
}

type sessionThread struct {
	server   *appServer
	events   <-chan rpcMessage
	id       string
	model    string
	turnID   string
	prefixes []prefixHash
	pending  []pendingTool
	deferred []rpcMessage

	lastUncachedInputTokens, lastCachedInputTokens int
}

func (s *sessions) acquire(ctx context.Context, key sessionKey) (*sessionEntry, error) {
	for {
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		if s.closed {
			s.mu.Unlock()
			return nil, errors.New("codex backend is closed")
		}
		if s.entries == nil {
			s.entries = make(map[sessionKey]*sessionEntry)
		}
		entry := s.entries[key]
		if entry == nil {
			entry = &sessionEntry{}
			s.entries[key] = entry
		}
		busy := entry.busy
		if busy == nil {
			entry.busy = make(chan struct{})
			if entry.timer != nil {
				entry.timer.Stop()
				entry.timer = nil
				if entry.thread != nil && !time.Now().Before(entry.deadline) {
					entry.expired = entry.thread.expiryReason()
				}
			}
			s.mu.Unlock()
			return entry, nil
		}
		s.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *sessions) release(key sessionKey, entry *sessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(entry.busy)
	entry.busy = nil
	if s.closed {
		return
	}
	if entry.thread == nil && entry.expired == "" {
		delete(s.entries, key)
		return
	}

	delay := s.idleTimeout
	if entry.thread != nil && entry.thread.turnID != "" {
		delay = s.pauseTimeout
	}
	entry.deadline = time.Now().Add(delay)
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if entry.timer != timer || s.entries[key] != entry {
			s.mu.Unlock()
			return
		}
		entry.timer = nil
		thread := entry.thread
		if thread == nil {
			delete(s.entries, key)
			s.mu.Unlock()
			return
		}
		entry.busy = make(chan struct{})
		entry.thread = nil
		if thread.turnID != "" {
			// Keep the reason for the next request without keeping the thread subscribed.
			entry.expired = thread.expiryReason()
		}
		s.mu.Unlock()
		thread.close()
		s.release(key, entry)
	})
	entry.timer = timer
}

func (s *sessions) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, entry := range s.entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}
	clear(s.entries)
}

// A different configuration can belong to a subagent sharing the same session ID.
// Retire the old thread only when the request also carries that conversation.
func (s *sessions) replaceConfiguration(input *sessionInput) *sessionThread {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if key == input.key || key.id != input.key.id || entry.busy != nil || entry.thread == nil {
			continue
		}
		held := entry.thread.prefixes
		last := len(held) - 1
		incoming := len(input.prefixes) - 1
		if incoming >= last && input.prefixes[last] == held[last] ||
			incoming == last-1 && input.prefixes[incoming] == held[incoming] {
			entry.timer.Stop()
			delete(s.entries, key)
			return entry.thread
		}
	}
	return nil
}

// describeTail names the message and block shapes that did not answer the pending calls, so a fallback in the
// request log can be diagnosed without the conversation. It carries no text, only kinds and counts.
func describeTail(messages []inputMessage, pending int) string {
	shapes := make([]string, 0, len(messages))
	for i, msg := range messages {
		if i == maxTailDetail {
			shapes = append(shapes, fmt.Sprintf("+%d more", len(messages)-i))
			break
		}
		kinds := make([]string, 0, len(msg.blocks))
		for _, block := range msg.blocks {
			kinds = append(kinds, block.Type)
		}
		shapes = append(shapes, msg.role+":"+strings.Join(kinds, ","))
	}
	return fmt.Sprintf("pending=%d tail=[%s]", pending, strings.Join(shapes, " "))
}

const maxTailDetail = 6

// answerBlocks joins the blocks of the messages added since the handoff. Claude Code sends tool results over as
// many user messages as it likes and follows them with in-conversation system messages (reminders from its
// hooks), which steer the turn like any other text. An assistant message among them is history the proxy did not
// produce, so there is nothing to continue and it returns nil.
func answerBlocks(messages []inputMessage) []requestBlock {
	blocks := make([]requestBlock, 0, len(messages))
	for _, msg := range messages {
		if msg.role != "user" && msg.role != "system" {
			return nil
		}
		blocks = append(blocks, msg.blocks...)
	}
	return blocks
}

func (t *sessionThread) expiryReason() string {
	if t.turnID != "" {
		return "pause_timeout"
	}
	return "idle_timeout"
}

func (t *sessionThread) close() {
	if t.turnID != "" {
		// Cleanup is best effort if the process already exited.
		_ = t.server.interrupt(t.id, t.turnID)
	}
	t.server.unsubscribe(t.id)
}

func (t *sessionThread) match(p *appServer, req TurnRequest) (string, *toolContinuation) {
	input := req.session
	held, incoming := len(t.prefixes)-1, len(input.prefixes)-1
	switch {
	case t.server != p:
		return "app_server_restart", nil
	case t.model != req.Model:
		return "model_changed", nil
	case incoming <= held:
		if input.prefixes[incoming] == t.prefixes[incoming] {
			return "rewind", nil
		}
		return "compaction", nil
	case input.prefixes[held] != t.prefixes[held]:
		return "prefix_changed", nil
	case len(t.pending) == 0:
		// mapRequest has already validated the final user message and ordered any trailing reminders.
		return "hit_idle", nil
	default:
		// Claude Code answers parallel calls over as many user messages as it likes, and its hooks add more
		// after them, so every new message counts, not only the last one.
		if continuation := matchToolResults(t.pending, answerBlocks(input.messages[held:])); continuation != nil {
			return "hit_continuation", continuation
		}
	}
	if len(t.pending) != 0 {
		input.detail = describeTail(input.messages[held:], len(t.pending))
		return "unmatched_tool_result", nil
	}
	return "prefix_changed", nil
}

func (b *CodexBackend) runSession(ctx context.Context, req TurnRequest, emit func(Event) error) (Usage, error) {
	input := req.session
	input.outcome = "new_session"
	entry, err := b.sessions.acquire(ctx, input.key)
	if err != nil {
		return Usage{}, err
	}
	defer b.sessions.release(input.key, entry)
	if entry.expired != "" {
		input.outcome, entry.expired = entry.expired, ""
		if entry.thread != nil {
			entry.thread.close()
			entry.thread = nil
		}
	} else if entry.thread == nil {
		if replaced := b.sessions.replaceConfiguration(input); replaced != nil {
			input.outcome = "configuration_changed"
			replaced.close()
		}
	}
	emitted := false
	tracked := func(event Event) error {
		emitted = true
		return emit(event)
	}
	usage, reusable, err := b.runSessionTurn(ctx, entry, req, tracked)
	if err == nil || emitted || ctx.Err() != nil {
		return usage, err
	}
	if errors.Is(err, errAppServerExited) {
		input.outcome = "app_server_restart"
	} else if reusable {
		input.outcome = "thread_unavailable"
	} else {
		return usage, err
	}
	retried, _, err := b.runSessionTurn(ctx, entry, req, tracked)
	retried.InputTokens += usage.InputTokens
	retried.OutputTokens += usage.OutputTokens
	retried.CacheReadInputTokens += usage.CacheReadInputTokens
	return retried, err
}
