package remoteapi

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

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
