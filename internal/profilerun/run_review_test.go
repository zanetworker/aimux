package profilerun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/acprun"
	"github.com/zanetworker/aimux/internal/profile"
	"github.com/zanetworker/aimux/internal/runcheck"
	"github.com/zanetworker/aimux/internal/toolreg"
)

// compiledStage is a planner engine that reports the Compiled its turn ran
// under, as the sandbox engine does.
type compiledStage struct {
	*fakeStage
	by map[string]profile.Compiled // by child session id
}

func (s compiledStage) TurnCompiled(id string) (profile.Compiled, bool) {
	c, ok := s.by[id]
	return c, ok
}

// The outside flags come from the Compiled the planner's turn ran under,
// not from a fresh compile that may differ (a source reclassified since).
func TestRun_OutsideFlagsFromPlannerTurnCompile(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply(nil, "notes")}, tools: []ToolResult{{Name: "mcp__jira__search", Output: "ok"}}},
		stageScript{}, stageScript{deltas: []string{"answer"}},
	)
	turn, _ := runCompile("work-readonly")
	reg := turn.Snapshot.Registry()
	src := reg.Sources["jira"]
	src.Authorship = toolreg.AuthorshipOutside
	reg.Sources["jira"] = src
	turn.Snapshot = toolreg.NewSnapshot(reg, nil, nil)
	// Only the planner child's own id carries the turn's Compiled; any other
	// id falls back to runCompile, where jira is internal.
	r.e.Planner = compiledStage{r.planner, map[string]profile.Compiled{"work-readonly-child": turn}}
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(context.Background(), id, "q"); err != nil {
		t.Fatal(err)
	}
	m, _ := r.store.Get(id)
	if !reflect.DeepEqual(m.Run.OutsideAttempted, []string{"jira"}) || !reflect.DeepEqual(m.Run.OutsideSucceeded, []string{"jira"}) {
		t.Fatalf("outside %v / %v, want jira from the turn's compile", m.Run.OutsideAttempted, m.Run.OutsideSucceeded)
	}
}

// With no turn Compiled and a compile that fails, every server the planner
// called counts as outside: the flags fail closed, never empty.
func TestRun_OutsideFlagsFailClosedOnCompileError(t *testing.T) {
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply(nil, "notes")}, tools: []ToolResult{
			{Name: "mcp__jira__search", Output: "ok"},
			{Name: "mcp__gh__issue_view", Output: "boom", Failed: true},
			{Name: "Read", Output: "local"},
		}},
		stageScript{}, stageScript{deltas: []string{"answer"}},
	)
	r.e.Compile = func(string) (profile.Compiled, error) { return profile.Compiled{}, errors.New("registry unreadable") }
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(context.Background(), id, "q"); err != nil {
		t.Fatal(err)
	}
	m, _ := r.store.Get(id)
	if !reflect.DeepEqual(m.Run.OutsideAttempted, []string{"gh", "jira"}) || !reflect.DeepEqual(m.Run.OutsideSucceeded, []string{"jira"}) {
		t.Fatalf("outside %v / %v, want [gh jira] / [jira]", m.Run.OutsideAttempted, m.Run.OutsideSucceeded)
	}
}

// The page's earlier questions are private to the query check.
func TestRun_PriorQuestionsReachTheCheck(t *testing.T) {
	prior := "how does the customer acme rollout plan for the egress proxy look"
	r := newRunRig(t,
		stageScript{deltas: []string{plannerReply([]string{"customer acme rollout plan for the egress proxy look"}, "")}},
		stageScript{}, stageScript{deltas: []string{"answer"}},
	)
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(WithPrior(context.Background(), []string{prior}), id, "q"); err != nil {
		t.Fatal(err)
	}
	m, _ := r.store.Get(id)
	if len(m.Run.Accepted) != 0 || len(m.Run.Rejected) != 1 || m.Run.Rejected[0].Reason != runcheck.ReasonOverlap {
		t.Fatalf("accepted %v rejected %v, want the prior-copying query rejected as overlap", m.Run.Accepted, m.Run.Rejected)
	}
}

// A turn cancelled before a stage starts creates no child session.
func TestRun_CancelledTurnStartsNoChild(t *testing.T) {
	r := newRunRig(t, stageScript{deltas: []string{plannerReply(nil, "")}}, stageScript{}, stageScript{})
	id := r.start(t, Meta{Client: "wingman"}, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.send(ctx, id, "q"); err == nil {
		t.Fatal("a cancelled turn succeeded")
	}
	if len(r.planner.metas) != 0 {
		t.Fatalf("planner child created %d times for a cancelled turn", len(r.planner.metas))
	}
}

// A run session lost in a restart is reported by Check (so the API says
// so before a turn), and cancelling it is not an error.
func TestRun_CheckerReportsLostSession(t *testing.T) {
	r := newRunRig(t, stageScript{}, stageScript{}, stageScript{})
	var c Checker = r.e
	id := r.start(t, Meta{Client: "wingman"}, "")
	if err := c.Check(id); err != nil {
		t.Fatalf("Check(known) = %v", err)
	}
	err := c.Check("lost")
	if err == nil || !strings.Contains(err.Error(), "ask again from the page") {
		t.Fatalf("Check(lost) = %v, want ask again from the page", err)
	}
	if err := r.e.Cancel("lost"); err != nil {
		t.Fatalf("Cancel(lost) = %v, want nil", err)
	}
}

// The planner stage runs with web forbidden for its launch: the engine
// that compiles it refuses a profile that reaches the web, whatever the
// role check saw at admission.
func TestRun_PlannerStageForbidsWeb(t *testing.T) {
	r := newRunRig(t, stageScript{deltas: []string{plannerReply(nil, "")}}, stageScript{}, stageScript{deltas: []string{"a"}})
	var plannerNoWeb, writerNoWeb bool
	r.e.Planner = ctxStage{r.planner, &plannerNoWeb}
	r.e.Writer = ctxStage{r.write, &writerNoWeb}
	id := r.start(t, Meta{Client: "wingman"}, "")
	if _, err := r.send(context.Background(), id, "q"); err != nil {
		t.Fatal(err)
	}
	if !plannerNoWeb || writerNoWeb {
		t.Fatalf("no-web: planner %v writer %v, want planner only", plannerNoWeb, writerNoWeb)
	}
}

type ctxStage struct {
	*fakeStage
	noWeb *bool
}

func (s ctxStage) Send(ctx context.Context, id, text string, onEvent func(acprun.Event)) (string, error) {
	*s.noWeb = NoWebFrom(ctx)
	return s.fakeStage.Send(ctx, id, text, onEvent)
}
