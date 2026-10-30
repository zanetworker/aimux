package remoteapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/remoteapi/chatqueue"
	"github.com/zanetworker/aimux/internal/terminput"
	"github.com/zanetworker/aimux/internal/trace"
)

func TestBroker_TurnEndedAndOnTurnEnd(t *testing.T) {
	release := make(chan error)
	ended := make(chan string, 4)
	f := &fakeBackend{sendF: func(ctx context.Context, s, text string, on func(acprun.Event)) (string, error) {
		if err := <-release; err != nil {
			return "", err
		}
		return "end_turn", nil
	}}
	var b *Broker
	b = NewBroker(f.deps(&b), BrokerOptions{OnTurnEnd: func(s string) { ended <- s }})
	q := QueueACP{Broker: b}

	turn, err := q.Start("s1", "hello", "queue-a")
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := q.TurnEnded("s1", turn); done || !q.Busy("s1") {
		t.Fatal("a running turn reads as ended")
	}
	if _, err := q.Start("s1", "again", "queue-b"); !errors.Is(err, chatqueue.ErrNotFree) {
		t.Fatalf("second Start while busy: %v, want ErrNotFree", err)
	}
	// The same key is the same turn: a repeated send never starts a second.
	if again, err := q.Start("s1", "hello", "queue-a"); err != nil || again != turn {
		t.Fatalf("retry = %q %v, want %q", again, err, turn)
	}
	release <- nil
	if s := <-ended; s != "s1" {
		t.Fatalf("OnTurnEnd(%q)", s)
	}
	if done, failed := q.TurnEnded("s1", turn); !done || failed {
		t.Fatalf("TurnEnded = %v %v, want ended, not failed", done, failed)
	}

	turn2, err := q.Start("s1", "next", "queue-c")
	if err != nil {
		t.Fatal(err)
	}
	release <- errors.New("adapter crashed")
	<-ended
	if done, failed := q.TurnEnded("s1", turn2); !done || !failed {
		t.Fatalf("TurnEnded = %v %v, want ended in error", done, failed)
	}
	if f.sends.Load() != 2 {
		t.Fatalf("%d sends, want 2", f.sends.Load())
	}
}

func TestQueueACP_StartErrors(t *testing.T) {
	for in, want := range map[error]error{
		acprun.ErrOpenInTerminal: chatqueue.ErrInTerminal,
		acprun.ErrUnknownSession: chatqueue.ErrNotRunning,
	} {
		f := &fakeBackend{checkF: func(string) error { return in }}
		var b *Broker
		b = NewBroker(f.deps(&b), BrokerOptions{})
		if _, err := (QueueACP{Broker: b}).Start("s1", "x", "k"); !errors.Is(err, want) {
			t.Fatalf("Check %v: Start = %v, want %v", in, err, want)
		}
	}
}

type fakeQueueInput struct {
	sends, submits atomic.Int32
	status         terminput.Status
}

func (f *fakeQueueInput) Status(context.Context, string, string) terminput.Status { return f.status }
func (f *fakeQueueInput) Send(context.Context, string, string, string) error {
	f.sends.Add(1)
	return nil
}
func (f *fakeQueueInput) Submit(context.Context, string, string) error {
	f.submits.Add(1)
	return nil
}

func TestQueueTerminal_OpenAndTurns(t *testing.T) {
	x := localFixture(t, NewBroker(BrokerDeps{}, BrokerOptions{}))
	reg := fmt.Sprintf(`{"pid":%d,"sessionId":"open","status":"busy"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(x.RegistryDir, "1.json"), []byte(reg), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	x.ParseTrace = func(string) ([]trace.Turn, error) {
		return []trace.Turn{
			{Number: 1, Timestamp: at, UserLines: []string{"fix the cache"}},
			{Number: 2, Timestamp: at.Add(time.Minute), UserLines: []string{"Stop hook feedback: lint failed"}},
			{Number: 3, Timestamp: at.Add(2 * time.Minute), UserLines: []string{"<task-notification>done</task-notification>"}},
			{Number: 4, Timestamp: at.Add(3 * time.Minute), UserLines: []string{"[Image #1]"}},
		}, nil
	}
	qt := QueueTerminal{Index: x, Input: &fakeQueueInput{}}
	if st, ok := qt.Open("open"); !ok || st != "busy" {
		t.Fatalf("Open(open) = %q %v, want busy true", st, ok)
	}
	if _, ok := qt.Open("idle"); ok {
		t.Fatal("Open(idle) = true")
	}
	turns, err := qt.Turns(context.Background(), "idle")
	if err != nil {
		t.Fatal(err)
	}
	// Claude Code's own turns (hook feedback, notifications) are not the
	// owner's; an image the owner pasted is.
	if len(turns) != 2 || turns[0].User != "fix the cache" || !turns[0].Time.Equal(at) || turns[1].User != "[Image #1]" {
		t.Fatalf("turns = %+v", turns)
	}
	if _, err := qt.Turns(context.Background(), "missing"); err == nil {
		t.Fatal("Turns of an unknown session: no error")
	}
}
