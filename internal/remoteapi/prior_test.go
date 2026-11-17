package remoteapi

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profilerun"
)

func TestNewSession_PriorValidated(t *testing.T) {
	r := newProfileRig(t)
	var mu sync.Mutex
	var got [][]string
	r.f.sendF = func(ctx context.Context, _, _ string, _ func(acprun.Event)) (string, error) {
		mu.Lock()
		got = append(got, profilerun.PriorFrom(ctx))
		mu.Unlock()
		return "end_turn", nil
	}
	r.serve()
	six := `["a","b","c","d","e","f"]`
	r.expectRefused(`{"profile":"page","url":"https://e.example/a","text":"q","prior":`+six+`}`, 400, "bad_request")
	long := strings.Repeat("x", 2001)
	r.expectRefused(`{"profile":"page","url":"https://e.example/a","text":"q","prior":["`+long+`"]}`, 400, "bad_request")

	// Five questions of exactly 2,000 characters are the limit and pass, in order.
	edge := strings.Repeat("y", 2000)
	body := `{"profile":"page","url":"https://e.example/a","text":"q","prior":["one","two","` + edge + `"]}`
	if resp, out := r.do("POST", "/v1/sessions", body); resp.StatusCode != 202 {
		t.Fatalf("valid prior: %d %v", resp.StatusCode, out)
	}
	r.idle("p-1")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !reflect.DeepEqual(got[0], []string{"one", "two", edge}) {
		t.Fatalf("engine saw prior %q", got)
	}
}

// image and prior exist only on a profile session: without a profile they
// are refused (400 bad_request), never silently dropped, and a follow-up
// message cannot carry them.
func TestNewSession_ImageOrPriorNeedProfile(t *testing.T) {
	r := newProfileRig(t).serve().as("device")
	img := `{"mimeType":"image/png","data":"` + pngB64(64) + `"}`
	for name, body := range map[string]string{
		"image": `{"cwd":"/w/app","text":"q","image":` + img + `}`,
		"prior": `{"cwd":"/w/app","text":"q","prior":["a"]}`,
		"empty": `{"cwd":"/w/app","text":"q","prior":[]}`,
	} {
		resp, out := r.do("POST", "/v1/sessions", body)
		if resp.StatusCode != 400 || out["code"] != "bad_request" || !strings.Contains(out["error"].(string), "need a profile session") {
			t.Errorf("%s: %d %v", name, resp.StatusCode, out)
		}
	}
	if resp, out := r.do("POST", "/v1/sessions", `{"cwd":"/w/app","text":"q"}`); resp.StatusCode != 202 {
		t.Fatalf("plain session: %d %v", resp.StatusCode, out)
	}
	for name, body := range map[string]string{
		"image": `{"text":"more","image":` + img + `}`,
		"prior": `{"text":"more","prior":["a"]}`,
	} {
		resp, out := r.do("POST", "/v1/sessions/s1/messages", body)
		if resp.StatusCode != 400 || out["code"] != "bad_request" || !strings.Contains(out["error"].(string), "need a profile session") {
			t.Errorf("message %s: %d %v", name, resp.StatusCode, out)
		}
	}
}

// A request refused with 429 client_limit never reads the page transcripts.
func TestProfileSession_LimitedRequestReadsNoTranscripts(t *testing.T) {
	pr := newPriorRig(t)
	pr.seed("old-page", "wingman", "page", "", priorURL, q(1, "Q1", "A1"))
	w := pr.api.Scopes.Clients["wingman"]
	w.TurnsPerMinute = 1
	pr.api.Scopes.Clients["wingman"] = w
	clock := &testClock{t: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	pr.api.Now = clock.now
	var reads atomic.Int32
	inner := pr.api.Session
	pr.api.Session = func(ctx context.Context, id string, n int) (SessionDetail, error) {
		reads.Add(1)
		return inner(ctx, id, n)
	}
	pr.serve()
	wantPrior(t, pr.ask(`{"profile":"research","url":"`+priorURL+`","text":"Q2"}`), []string{"Q1"})
	if reads.Load() == 0 {
		t.Fatal("the allowed request read no transcript")
	}
	reads.Store(0)
	resp, out := pr.do("POST", "/v1/sessions", `{"profile":"research","url":"`+priorURL+`","text":"Q3"}`)
	if resp.StatusCode != 429 || out["code"] != "client_limit" {
		t.Fatalf("second turn: %d %v", resp.StatusCode, out)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("a refused request read %d transcripts", n)
	}
}
