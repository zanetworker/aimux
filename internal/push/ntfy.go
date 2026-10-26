package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Ntfy publishes to an ntfy topic (ntfy.sh or a self-hosted server), which
// the ntfy phone app shows on the lock screen. Anyone who knows a topic on
// a public server can read it: use a hard-to-guess name or a token.
type Ntfy struct {
	Server string // e.g. https://ntfy.sh
	Topic  string
	Token  string // access token for a protected topic; optional
	Client *http.Client
}

const ntfyPriorityHigh = 4

// Notify publishes m as JSON to the server root, so titles and bodies in
// any language survive (ntfy's header form is ASCII-only).
func (n *Ntfy) Notify(ctx context.Context, m Message) error {
	body, err := json.Marshal(struct {
		Topic    string   `json:"topic"`
		Title    string   `json:"title"`
		Message  string   `json:"message"`
		Priority int      `json:"priority"`
		Tags     []string `json:"tags"`
		Click    string   `json:"click,omitempty"`
		// SequenceID lets Retract delete it again (ntfy 2.16 and later;
		// older servers ignore it).
		SequenceID string `json:"sequence_id,omitempty"`
	}{n.Topic, m.Title, m.Body, ntfyPriorityHigh, []string{"lock"}, m.Link, m.ApprovalID})
	if err != nil {
		return err
	}
	return n.do(ctx, http.MethodPost, strings.TrimRight(n.Server, "/")+"/", body)
}

// Retract deletes the approval's notification from the topic, so a phone
// that was offline does not show it after the approval is resolved. The
// ntfy app removes it from the notification drawer too, where it can.
func (n *Ntfy) Retract(ctx context.Context, m Message) error {
	if m.ApprovalID == "" {
		return nil
	}
	return n.do(ctx, http.MethodDelete, strings.TrimRight(n.Server, "/")+"/"+url.PathEscape(n.Topic)+"/"+url.PathEscape(m.ApprovalID), nil)
}

func (n *Ntfy) do(ctx context.Context, method, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	resp, err := client(n.Client).Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %s %s: %w", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError("ntfy "+method+" "+target, resp)
	}
	return nil
}
