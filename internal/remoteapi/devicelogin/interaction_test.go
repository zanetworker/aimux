package devicelogin

import (
	"testing"
	"time"
)

// The client actions the spec counts as use (opening a chat or a tab, pull
// to refresh, Retry, a search, any POST), each as the request the web client
// and the iPhone app send for it, and the background requests that must not
// count. Each row is checked end to end: the request, then whether the
// session is still live an hour after the one before it.
func TestInteraction_ClientActionsContract(t *testing.T) {
	const mark = "Aimux-Interaction: 1"
	rows := []struct {
		action, method, path string
		marked, counted      bool
	}{
		{"web: open a chat (first detail read)", "GET", "/v1/sessions/s1?turns=30", true, true},
		{"iPhone: open a chat (snapshot read)", "GET", "/v1/sessions/s1/events?wait=0", true, true},
		{"open a tab, pull to refresh, Retry", "GET", "/v1/sessions", true, true},
		{"search", "GET", "/v1/search?q=x&limit=30", true, true},
		{"new session: list projects", "GET", "/v1/projects", true, true},
		{"send a message", "POST", "/v1/sessions/s1/messages", false, true},
		{"edit or move a queued message", "PATCH", "/v1/sessions/s1/queue/i1", false, true},
		{"delete a queued message", "DELETE", "/v1/sessions/s1/queue/i1", false, true},
		{"background poll", "GET", "/v1/sessions", false, false},
		{"background snapshot", "GET", "/v1/sessions/s1/events?wait=0", false, false},
		{"event stream, even marked", "GET", "/v1/sessions/s1/events?after=3&log=L", true, false},
		{"event stream without a query, even marked", "GET", "/v1/sessions/s1/events", true, false},
		{"event stream with wait=1, even marked", "GET", "/v1/sessions/s1/events?wait=1", true, false},
	}
	for _, row := range rows {
		t.Run(row.action, func(t *testing.T) {
			r := newRig(t)
			auth := "Bearer " + r.pairEnclave("iphone").token
			r.clock.add(50 * time.Minute)
			var headers []string
			if row.marked {
				headers = append(headers, mark)
			}
			var body any
			if row.method == "POST" || row.method == "PATCH" {
				body = "{}"
			}
			if got := r.do(row.method, row.path, body, auth, headers...); got.code == 401 {
				t.Fatalf("refused within the hour: %v", got.body)
			}
			r.clock.add(50 * time.Minute) // 100 min since login, 50 since the action
			live := r.do("GET", "/v1/sessions", nil, auth).code == 200
			if live != row.counted {
				t.Fatalf("%s %s (marked %v): counted %v, want %v", row.method, row.path, row.marked, live, row.counted)
			}
		})
	}
}
