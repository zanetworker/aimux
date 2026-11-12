package remoteapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/zanetworker/aimux/internal/clientscope"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/profilerun"
)

// profileFixture is the shared part of api/testdata/clients/profiles/*.json:
// the profiles the server has and the scopes that differ from the defaults.
type profileFixture struct {
	Profiles []struct {
		Name      string `json:"name"`
		Runs      string `json:"runs"`
		Available bool   `json:"available"`
	} `json:"profiles"`
	Scopes map[string][]string `json:"scopes"`
}

func readProfileFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "testdata", "clients", "profiles", name)) //nolint:gosec // G304: fixed testdata directory
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// contractProfileAPI is an API serving fx, with the client set per call.
func contractProfileAPI(t *testing.T, fx profileFixture) (*API, func(client string)) {
	t.Helper()
	manifests := map[string]profile.Manifest{}
	for _, p := range fx.Profiles {
		manifests[p.Name] = profile.Manifest{Name: p.Name, Harness: "claude-code", Runs: p.Runs, Available: p.Available,
			Network: profile.NetworkSpec{Default: "deny"}} // resolved manifests always have a default
	}
	scopes := clientscope.Defaults()
	for client, profiles := range fx.Scopes {
		c := scopes.Clients[client]
		c.Profiles = profiles
		scopes.Clients[client] = c
	}
	var mu sync.Mutex
	client := clientDevice
	var b *Broker
	f := &fakeBackend{}
	b = NewBroker(f.deps(&b), BrokerOptions{})
	n := 0
	api := &API{Version: "test", Broker: b, Scopes: scopes,
		Sessions:        func(context.Context, int, bool) ([]Session, error) { return nil, nil },
		Projects:        func(context.Context) ([]Project, error) { return []Project{{Path: "/w/app", Name: "app"}}, nil },
		Create:          func(context.Context, string, string) (string, error) { return "new-1", nil },
		Profiles:        func() (map[string]profile.Manifest, error) { return manifests, nil },
		ProfileSessions: &profilerun.Store{Path: filepath.Join(t.TempDir(), "profiles.json")},
		NewProfileSession: func(context.Context, profilerun.Meta, string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			n++
			return fmt.Sprintf("p-%d", n), nil
		},
	}
	setClientOf(api, func(*http.Request) string { mu.Lock(); defer mu.Unlock(); return client })
	return api, func(c string) { mu.Lock(); client = c; mu.Unlock() }
}

// TestContract_Profiles drives the profile routes and fields through the
// contract with the clients' fixtures, and checks every /v1/profiles
// operation was exercised (TestContract leaves them to this test).
func TestContract_Profiles(t *testing.T) {
	var list struct {
		profileFixture
		Client string         `json:"client"`
		Want   map[string]any `json:"want"`
	}
	readProfileFixture(t, "list.json", &list)
	api, as := contractProfileAPI(t, list.profileFixture)
	c := newContract(t, api.Handler([]string{testLogin}, []string{"127.0.0.1"}))

	as(list.Client)
	st, body := c.call("GET", "/v1/profiles", "", testLogin)
	if st != 200 {
		t.Fatalf("list: %d %v", st, body)
	}
	for _, p := range body["profiles"].([]any) {
		e := p.(map[string]any)
		if d, _ := e["digest"].(string); !strings.HasPrefix(d, "sha256:") {
			t.Fatalf("%v: digest", e)
		}
		delete(e, "digest")
	}
	if !reflect.DeepEqual(body, list.Want) {
		t.Fatalf("list.json: got %v, want %v", body, list.Want)
	}
	st, body = c.call("GET", "/v1/profiles/page", "", testLogin)
	if st != 200 || body["name"] != "page" || body["allowed"] != true {
		t.Fatalf("detail: %d %v", st, body)
	}
	st, body = c.call("GET", "/v1/profiles/nope", "", testLogin)
	if st != 404 || body["code"] != "unknown_profile" {
		t.Fatalf("unknown detail: %d %v", st, body)
	}

	var scope struct {
		Cases []struct {
			Description   string         `json:"description"`
			Client        string         `json:"client"`
			Request       map[string]any `json:"request"`
			PageTextBytes int            `json:"pageTextBytes"`
			Status        int            `json:"status"`
			Code          string         `json:"code"`
			MaxAge        int            `json:"maxAge"`
		} `json:"cases"`
	}
	readProfileFixture(t, "new-session-scope.json", &scope)
	var started string
	for _, tc := range scope.Cases {
		if tc.PageTextBytes > 0 {
			tc.Request["pageText"] = strings.Repeat("a", tc.PageTextBytes)
		}
		raw, _ := json.Marshal(tc.Request)
		as(tc.Client)
		st, body := c.call("POST", "/v1/sessions", string(raw), testLogin)
		if st != tc.Status || (tc.Code != "" && body["code"] != tc.Code) || (tc.MaxAge != 0 && body["maxAge"] != float64(tc.MaxAge)) {
			t.Fatalf("new-session-scope.json %q: %d %v, want %d %s", tc.Description, st, body, tc.Status, tc.Code)
		}
		if st == 202 {
			started, _ = body["id"].(string)
		}
	}
	if started == "" {
		t.Fatal("no fixture case started a session")
	}
	waitFor(t, api.Broker, started, KindTurnDone)

	// The session list with its filters, and Wingman's history delete.
	as("wingman")
	st, body = c.call("GET", "/v1/sessions?client=wingman&url="+"https://example.com/a", "", testLogin)
	if st != 200 || len(body["sessions"].([]any)) != 1 {
		t.Fatalf("filtered list: %d %v", st, body)
	}
	st, body = c.call("PATCH", "/v1/sessions/"+started+"/meta", `{"hidden":true}`, testLogin)
	if st != 200 || body["hidden"] != true {
		t.Fatalf("hide: %d %v", st, body)
	}
	st, body = c.call("PATCH", "/v1/sessions/nope/meta", `{"hidden":true}`, testLogin)
	if st != 404 || body["code"] != "not_found" {
		t.Fatalf("hide another's: %d %v", st, body)
	}
	st, body = c.call("GET", "/v1/search?q=x", "", testLogin)
	if st != 403 || body["code"] != "forbidden_for_client" {
		t.Fatalf("search as wingman: %d %v", st, body)
	}
	// Limits: a 429 with Retry-After.
	api.Scopes.Clients["wingman"] = clientscope.Client{Profiles: []string{"page"}, MaxSessions: 2, TurnsPerMinute: 1}
	api.limits = limiter{}
	c.call("POST", "/v1/sessions", `{"profile":"page","text":"one","url":"https://example.com/b"}`, testLogin)
	st, body = c.call("POST", "/v1/sessions", `{"profile":"page","text":"two","url":"https://example.com/c"}`, testLogin)
	if st != 429 || body["code"] != "client_limit" {
		t.Fatalf("limit: %d %v", st, body)
	}
	as(clientDevice)
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if !strings.Contains(fmt.Sprint(body["capabilities"]), CapProfiles) {
		t.Fatalf("capabilities %v", body["capabilities"])
	}
	api.ProfileHealth = func() error { return fmt.Errorf("profile sessions file cannot be read") }
	_, body = c.call("GET", "/v1/health", "", testLogin)
	if body["profileStore"] == nil {
		t.Fatalf("health %v", body)
	}

	for path, item := range c.doc.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/profiles") {
			continue
		}
		for method, op := range item.Operations() {
			if !c.ops[op.OperationID] {
				t.Errorf("never exercised: %s %s", method, path)
			}
		}
	}
}
