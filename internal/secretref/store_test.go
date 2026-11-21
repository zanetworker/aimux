package secretref

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/keyring"
)

func fakeKeychain(t *testing.T) map[string]string {
	t.Helper()
	items := map[string]string{}
	oldR, oldW := readItem, writeItem
	readItem = func(_ context.Context, item string) (string, error) {
		v, ok := items[item]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	writeItem = func(item, value string) error { items[item] = value; return nil }
	oldL := lockDir
	lockDir = t.TempDir()
	t.Cleanup(func() { readItem, writeItem, lockDir = oldR, oldW, oldL })
	return items
}

func TestStore_MergesIntoServerItem(t *testing.T) {
	items := fakeKeychain(t)
	items["aimux/JirAIa"] = `{"headers":{"X":"h-value"},"env":{"OLD":"o-value"}}`
	if err := Store(context.Background(), "JirAIa", "JIRA_TOKEN", "newfakevalue"); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Headers map[string]any    `json:"headers"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(items["aimux/JirAIa"]), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Env["JIRA_TOKEN"] != "newfakevalue" || doc.Env["OLD"] != "o-value" || doc.Headers["X"] != "h-value" {
		t.Fatalf("merge lost something: %+v", doc)
	}
}

func TestStore_NewItemAndBadNames(t *testing.T) {
	items := fakeKeychain(t)
	if err := Store(context.Background(), "sauron", "CLIENT_SECRET", "s-value-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items["aimux/sauron"], `"CLIENT_SECRET":"s-value-1"`) {
		t.Fatalf("item: %s", items["aimux/sauron"])
	}
	for _, c := range [][2]string{{"bad server", "N"}, {"ok", "BAD NAME"}, {"ok", ""}} {
		if err := Store(context.Background(), c[0], c[1], "v"); err == nil {
			t.Errorf("%q/%q: want an error", c[0], c[1])
		}
	}
}

func TestStore_UnreadableItemWritesNothing(t *testing.T) {
	items := fakeKeychain(t)
	readItem = func(context.Context, string) (string, error) { return "", errors.New("locked") }
	if err := Store(context.Background(), "x", "N", "v"); err == nil {
		t.Fatal("want an error")
	}
	if len(items) != 0 {
		t.Fatal("nothing must be written when the item cannot be read")
	}
}

func TestStore_RejectsIncompatibleItem(t *testing.T) {
	for _, raw := range []string{"null", `{"env":"x"}`, `[1]`} {
		items := fakeKeychain(t)
		items["aimux/x"] = raw
		if err := Store(context.Background(), "x", "N", "v"); err == nil {
			t.Errorf("%s: want an error", raw)
		}
		if items["aimux/x"] != raw {
			t.Errorf("%s: item rewritten", raw)
		}
	}
}

func TestStore_SerializesPerItem(t *testing.T) {
	items := fakeKeychain(t)
	var mu sync.Mutex
	readItem = func(_ context.Context, item string) (string, error) {
		mu.Lock()
		v, ok := items[item]
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // widen the read-modify-write window
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	writeItem = func(item, value string) error { mu.Lock(); items[item] = value; mu.Unlock(); return nil }
	var wg sync.WaitGroup
	for _, n := range []string{"A", "B", "C"} {
		wg.Add(1)
		go func(n string) { defer wg.Done(); _ = Store(context.Background(), "x", n, "v"+n) }(n)
	}
	wg.Wait()
	for _, n := range []string{"A", "B", "C"} {
		if !strings.Contains(items["aimux/x"], `"`+n+`":"v`+n+`"`) {
			t.Fatalf("lost %s: %s", n, items["aimux/x"])
		}
	}
}
