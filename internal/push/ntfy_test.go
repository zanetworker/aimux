package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNtfy_PublishesJSONToTheServerRoot(t *testing.T) {
	var got map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
	}))
	defer srv.Close()

	n := &Ntfy{Server: srv.URL + "/", Topic: "aimux-test", Token: "tk_secret"}
	err := n.Notify(context.Background(), Message{
		Title: "Approval needed", Body: "Fix the build: Bash go test ./…", Link: "https://mac.ts.net:8443/app/#/s/abc",
		ApprovalID: "a1", SessionID: "abc",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if path != "/" {
		t.Errorf("posted to %q, want the server root", path)
	}
	if auth != "Bearer tk_secret" {
		t.Errorf("Authorization %q", auth)
	}
	if got["topic"] != "aimux-test" || got["title"] != "Approval needed" || got["message"] != "Fix the build: Bash go test ./…" ||
		got["click"] != "https://mac.ts.net:8443/app/#/s/abc" || got["priority"] != float64(4) || got["sequence_id"] != "a1" {
		t.Errorf("published %v", got)
	}
}

func TestNtfy_NoTokenSendsNoAuthorization(t *testing.T) {
	var auth string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	if err := (&Ntfy{Server: srv.URL, Topic: "t"}).Notify(context.Background(), Message{Title: "x", Body: "y"}); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("Authorization %q, want none", auth)
	}
	if _, ok := got["click"]; ok {
		t.Errorf("click sent without a link: %v", got)
	}
}

func TestNtfy_ServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":40301,"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	err := (&Ntfy{Server: srv.URL, Topic: "t"}).Notify(context.Background(), Message{Title: "x", Body: "y"})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("got %v, want the status and the server's reason", err)
	}
}

func TestNtfy_UnreachableServerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if err := (&Ntfy{Server: url, Topic: "t"}).Notify(context.Background(), Message{Title: "x", Body: "y"}); err == nil {
		t.Fatal("no error from a closed server")
	}
}

func TestNtfy_RetractDeletesTheApprovalsNotification(t *testing.T) {
	var method, path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
	}))
	defer srv.Close()
	n := &Ntfy{Server: srv.URL + "/", Topic: "aimux-test", Token: "tk"}
	if err := n.Retract(context.Background(), Message{ApprovalID: "ap1"}); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || path != "/aimux-test/ap1" || auth != "Bearer tk" {
		t.Fatalf("%s %s auth %q", method, path, auth)
	}
}

func TestNtfy_RetractWithoutAnApprovalIsANoOp(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	if err := (&Ntfy{Server: srv.URL, Topic: "t"}).Retract(context.Background(), Message{}); err != nil || called {
		t.Fatalf("%v, called %v", err, called)
	}
}

func TestNtfy_RetractRefusedByAnOldServerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":40401,"error":"page not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	if err := (&Ntfy{Server: srv.URL, Topic: "t"}).Retract(context.Background(), Message{ApprovalID: "ap1"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("got %v", err)
	}
}
