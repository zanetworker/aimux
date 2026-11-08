package tui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/discovery"
)

// slowProvider is a discovery provider that takes delay per Discover and
// records how many run at once.
type slowProvider struct {
	delay             time.Duration
	inFlight, maxSeen int32
	calls             int32
}

func (p *slowProvider) Name() string { return "slow" }

func (p *slowProvider) Discover() ([]agent.Agent, error) {
	atomic.AddInt32(&p.calls, 1)
	n := atomic.AddInt32(&p.inFlight, 1)
	for {
		m := atomic.LoadInt32(&p.maxSeen)
		if n <= m || atomic.CompareAndSwapInt32(&p.maxSeen, m, n) {
			break
		}
	}
	time.Sleep(p.delay)
	atomic.AddInt32(&p.inFlight, -1)
	return nil, nil
}

// tickCmds returns the commands of Update(tickMsg) other than the next tick.
func tickCmds(t *testing.T, app App) []tea.Cmd {
	t.Helper()
	_, cmd := app.Update(tickMsg(time.Now()))
	if cmd == nil {
		t.Fatal("tick returned no command")
	}
	// The discovery is the first command of the batch; the tick's own timer
	// is not run here (it would sleep 2 s).
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("tick returned %T, want a batch", batch)
	}
	return batch[:1]
}

func TestTick_NoOverlappingDiscoveries(t *testing.T) {
	prov := &slowProvider{delay: 300 * time.Millisecond}
	app := newTestAppForPending(map[string]agent.Agent{})
	app.orchestrator = discovery.NewOrchestrator(prov)
	app.refreshing = new(atomic.Bool)

	// Three ticks fire while the first discovery is still running.
	var cmds []tea.Cmd
	for i := 0; i < 3; i++ {
		cmds = append(cmds, tickCmds(t, app)...)
	}
	var wg sync.WaitGroup
	results := make([]tea.Msg, len(cmds))
	for i, c := range cmds {
		wg.Add(1)
		go func(i int, c tea.Cmd) {
			defer wg.Done()
			results[i] = c()
		}(i, c)
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()

	if m := atomic.LoadInt32(&prov.maxSeen); m != 1 {
		t.Errorf("max concurrent discoveries = %d, want 1", m)
	}
	if c := atomic.LoadInt32(&prov.calls); c != 1 {
		t.Errorf("discoveries run = %d, want 1 (the others skipped)", c)
	}
	delivered := 0
	for _, r := range results {
		if _, ok := r.(instancesMsg); ok {
			delivered++
		} else if r != nil {
			t.Errorf("skipped tick returned %T, want nil", r)
		}
	}
	if delivered != 1 {
		t.Errorf("instancesMsg delivered %d times, want 1", delivered)
	}

	// Once it finished, the next tick discovers again.
	for _, c := range tickCmds(t, app) {
		if _, ok := c().(instancesMsg); !ok {
			t.Error("tick after the discovery finished did not discover")
		}
	}
	if c := atomic.LoadInt32(&prov.calls); c != 2 {
		t.Errorf("discoveries run = %d, want 2", c)
	}
}
