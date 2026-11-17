package acprun

import (
	"context"
	"errors"
	"testing"
	"time"
)

// An agent that does not advertise promptCapabilities.image is never sent
// an image: the prompt fails before anything goes out.
func TestRun_PromptImageNeedsAgentSupport(t *testing.T) {
	r := start(t, fakeSpec(t), "sess-1", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := r.PromptImage(ctx, "hello", &Image{MimeType: "image/png", Data: "iVBORw0KGgo="}, func(Event) {})
	if !errors.Is(err, ErrNoImages) {
		t.Fatalf("err = %v, want ErrNoImages", err)
	}
	// The run still works for text.
	if _, stop := prompt(t, r, "hello"); stop != "end_turn" {
		t.Fatalf("stop = %q", stop)
	}
}

// A runner without image support refuses an image turn and is not prompted;
// a nil image is a plain text turn.
func TestManager_SendImageNeedsImageRunner(t *testing.T) {
	prompted := 0
	fs := &fakeRunStarter{promptF: func(context.Context, string) (string, error) { prompted++; return "end_turn", nil }}
	m := NewManager(ManagerOptions{Start: fs.start})
	defer m.Close()
	_, err := m.SendImage(context.Background(), "a", "/w", "", "hi", &Image{MimeType: "image/png", Data: "x"}, func(Event) {})
	if !errors.Is(err, ErrNoImages) || prompted != 0 {
		t.Fatalf("err = %v, prompted %d", err, prompted)
	}
	if stop, err := m.SendImage(context.Background(), "a", "/w", "", "hi", nil, func(Event) {}); err != nil || stop != "end_turn" || prompted != 1 {
		t.Fatalf("text turn: %q %v, prompted %d", stop, err, prompted)
	}
}
