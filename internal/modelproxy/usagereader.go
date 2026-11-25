package modelproxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime"
	"strings"
	"sync"
)

const (
	// maxUsageLine bounds one kept line of a stream. The events that carry usage are a few hundred bytes.
	maxUsageLine = 64 << 10
	// maxUsageBody bounds a kept non-streamed body, which has to be whole before its usage can be read.
	maxUsageBody = 8 << 20
	// ssePrefix is how much of a line is needed to tell an event name or a data line from anything else.
	ssePrefix = len("event:")
)

// usageReader watches a response body pass and keeps only its usage figures and model name. It reads an Anthropic
// Messages response, streamed or not, plain or gzip-compressed; anything else passes unread. It never reports an
// error, so it cannot slow or break the copy it is attached to.
type usageReader struct {
	kind usageKind
	sse  sseUsage
	body []byte // a non-streamed body, dropped once read
	full bool   // body passed maxUsageBody: its usage stays unknown

	gz     *io.PipeWriter // set when the body is gzip: Write feeds it, a goroutine parses what comes out
	gzDone chan struct{}
	broken bool // the gzip stream was not whole and valid to its end
	finish sync.Once

	model string
	usage Usage
	known bool
}

type usageKind int

const (
	usageUnreadable usageKind = iota
	usageStream
	usageJSON
)

// sseUsage follows a server-sent event stream line by line. Only event names and the data lines of the events
// that carry usage are collected; every other line is skipped unread, so no response text is held.
type sseUsage struct {
	line  []byte
	mode  sseMode
	event string
	some  bool // the line so far holds something other than a carriage return, kept or not
	stop  bool // a whole message_stop event was seen, to the blank line that ends it
}

type sseMode int

const (
	sseDeciding sseMode = iota
	sseKeeping
	sseSkipping
)

func newUsageReader(contentType, contentEncoding string) *usageReader {
	u := &usageReader{}
	switch mediaType, _, _ := mime.ParseMediaType(contentType); mediaType {
	case "text/event-stream":
		u.kind = usageStream
	case "application/json":
		u.kind = usageJSON
	default:
		return u
	}
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
	case "gzip":
		reader, writer := io.Pipe()
		u.gz, u.gzDone = writer, make(chan struct{})
		go u.readGzip(reader)
	default:
		u.kind = usageUnreadable
	}
	return u
}

// Write always reports the whole of p as written.
func (u *usageReader) Write(p []byte) (int, error) {
	switch {
	case u.kind == usageUnreadable:
	case u.gz != nil:
		// readGzip reads until the pipe closes, whatever it finds, so this cannot block for good or fail.
		_, _ = u.gz.Write(p)
	default:
		u.take(p)
	}
	return len(p), nil
}

func (u *usageReader) readGzip(compressed *io.PipeReader) {
	defer close(u.gzDone)
	// Whatever goes wrong, keep reading so Write never blocks. A stream cut short still gives what came before.
	defer func() { _, _ = io.Copy(io.Discard, compressed) }()
	plain, err := gzip.NewReader(compressed)
	if err != nil {
		u.broken = true
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := plain.Read(buf)
		u.take(buf[:n])
		if err != nil {
			// A missing or wrong checksum arrives here, after everything before it was read.
			u.broken = err != io.EOF
			return
		}
	}
}

func (u *usageReader) take(p []byte) {
	if u.kind == usageStream {
		u.takeStream(p)
		return
	}
	if u.full {
		return
	}
	if len(u.body)+len(p) > maxUsageBody {
		u.full, u.body = true, nil
		return
	}
	u.body = append(u.body, p...)
}

func (u *usageReader) takeStream(p []byte) {
	s := &u.sse
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		piece := p
		if end >= 0 {
			piece = p[:end]
		}
		s.some = s.some || len(bytes.Trim(piece, "\r")) > 0
		switch s.mode {
		case sseSkipping:
		case sseKeeping:
			if len(s.line)+len(piece) > maxUsageLine {
				s.mode, s.line = sseSkipping, s.line[:0]
				break
			}
			s.line = append(s.line, piece...)
		case sseDeciding:
			need := min(ssePrefix-len(s.line), len(piece))
			s.line = append(s.line, piece[:need]...)
			if len(s.line) < ssePrefix && end < 0 {
				break // the line's start has not all arrived
			}
			if s.wanted() {
				s.mode = sseKeeping
				s.line = append(s.line, piece[need:]...)
			} else {
				s.mode, s.line = sseSkipping, s.line[:0]
			}
		}
		if end < 0 {
			return
		}
		u.endLine()
		p = p[end+1:]
	}
}

// wanted reports whether the line now starting is an event name, or the data of an event that carries usage.
func (s *sseUsage) wanted() bool {
	if bytes.HasPrefix(s.line, []byte("event:")) {
		return true
	}
	return bytes.HasPrefix(s.line, []byte("data:")) && (s.event == "message_start" || s.event == "message_delta")
}

func (u *usageReader) endLine() {
	s := &u.sse
	line := bytes.TrimRight(s.line, "\r")
	kept, blank := s.mode == sseKeeping, !s.some
	s.mode, s.line, s.some = sseDeciding, s.line[:0], false
	if name, ok := bytes.CutPrefix(line, []byte("event:")); ok && kept {
		s.event = string(bytes.TrimSpace(name))
		return
	}
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok && kept {
		var event struct {
			Message messageUsage   `json:"message"`
			Usage   *reportedUsage `json:"usage"`
		}
		if json.Unmarshal(data, &event) == nil {
			u.record(event.Message)
			u.record(messageUsage{Usage: event.Usage})
		}
		return
	}
	if blank { // a blank line ends the event; a line that was skipped unread is not one
		s.stop = s.stop || s.event == "message_stop"
		s.event = ""
	}
}

type messageUsage struct {
	Model string         `json:"model"`
	Usage *reportedUsage `json:"usage"`
}

// reportedUsage tells a figure that was absent from one that was zero: a later event restates only some figures.
type reportedUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

func (u *usageReader) record(m messageUsage) {
	if m.Model != "" {
		u.model = m.Model
	}
	if m.Usage == nil {
		return
	}
	for _, figure := range []struct {
		from *int
		to   *int
	}{
		{m.Usage.InputTokens, &u.usage.InputTokens},
		{m.Usage.OutputTokens, &u.usage.OutputTokens},
		{m.Usage.CacheReadInputTokens, &u.usage.CacheReadInputTokens},
		{m.Usage.CacheCreationInputTokens, &u.usage.CacheCreationInputTokens},
	} {
		if figure.from != nil {
			*figure.to = *figure.from
			u.known = true // a count was read, even a zero; a usage object with none leaves it unknown
		}
	}
}

// result ends the reading and returns what was seen. known is false when the response carried no token count or
// could not be read; the usage is then empty. The model is returned whenever the response named one.
func (u *usageReader) result() (model string, usage Usage, known bool) {
	u.finish.Do(func() {
		if u.gz != nil {
			_ = u.gz.Close()
			<-u.gzDone
		}
		if u.kind == usageJSON {
			var message messageUsage
			if json.Unmarshal(u.body, &message) == nil {
				u.record(message)
			}
			u.body = nil
		}
		u.sse.line = nil
	})
	if !u.known {
		return u.model, Usage{}, false
	}
	return u.model, u.usage, true
}

// complete reports whether the answer was whole as far as the reader can tell: a gzip body valid to its end, and
// a stream it could read ending with a message_stop event. Where it cannot tell, it says yes. Call it after result.
func (u *usageReader) complete() bool {
	return !u.broken && (u.kind != usageStream || u.sse.stop)
}

// judges reports whether complete can find an answer unfinished: an event stream it follows, or a gzip body.
func (u *usageReader) judges() bool { return u.kind == usageStream || u.gz != nil }
