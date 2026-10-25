// Package push tells a person's devices that an agent is waiting for them,
// so a permission request reaches the lock screen while no client is open.
package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message is one notification. It names what is asked, never the tool's
// input: a relay or the lock screen may show it to anyone.
type Message struct {
	Title      string
	Body       string
	Link       string // opens the session in a client; optional
	ApprovalID string
	SessionID  string
	Expires    time.Time // not worth delivering after this; zero: deliver now or never
}

// Notifier delivers a Message.
type Notifier interface {
	Notify(ctx context.Context, m Message) error
}

// ForApproval words the notification for a permission request in a
// session. Both titles are shortened to fit about two lock-screen lines.
func ForApproval(sessionTitle, toolTitle string) Message {
	tool := shorten(toolTitle, 120)
	if tool == "" {
		tool = "a tool call"
	}
	body := tool
	if s := shorten(sessionTitle, 60); s != "" {
		body = s + ": " + tool
	}
	return Message{Title: "Approval needed", Body: body}
}

func shorten(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Retracter is a Notifier that can take back a notification once its
// approval is resolved, so a phone that was offline does not show it later.
type Retracter interface {
	Retract(ctx context.Context, m Message) error
}

// Send delivers m through every notifier at once, so a slow one does not
// hold up the others, and returns their errors joined.
func Send(ctx context.Context, notifiers []Notifier, m Message) error {
	return each(notifiers, func(n Notifier) error { return n.Notify(ctx, m) })
}

// Retract takes m back through the notifiers that can.
func Retract(ctx context.Context, notifiers []Notifier, m Message) error {
	return each(notifiers, func(n Notifier) error {
		if r, ok := n.(Retracter); ok {
			return r.Retract(ctx, m)
		}
		return nil
	})
}

func each(notifiers []Notifier, do func(Notifier) error) error {
	errs := make([]error, len(notifiers))
	var wg sync.WaitGroup
	for i, n := range notifiers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = do(n)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// statusError describes a refused request with the server's own reason.
func statusError(service string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return fmt.Errorf("%s answered %s: %s", service, resp.Status, strings.TrimSpace(string(body)))
}
