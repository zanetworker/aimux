package correlator

import (
	"testing"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
)

func TestTagFromProcessTree(t *testing.T) {
	parentLookup := func(pid int) int {
		switch pid {
		case 200:
			return 100
		case 300:
			return 100
		default:
			return 1
		}
	}
	agents := []agent.Agent{
		{PID: 100, Name: "main"},
		{PID: 200, Name: "sub1"},
		{PID: 300, Name: "sub2"},
		{PID: 400, Name: "independent"},
	}
	TagFromProcessTree(agents, parentLookup)

	if agents[0].IsSubagent() {
		t.Error("PID 100 should not be a subagent")
	}
	if !agents[1].IsSubagent() || agents[1].ParentPID != 100 {
		t.Errorf("PID 200: IsSubagent=%v ParentPID=%d, want true/100", agents[1].IsSubagent(), agents[1].ParentPID)
	}
	if !agents[2].IsSubagent() || agents[2].ParentPID != 100 {
		t.Errorf("PID 300: IsSubagent=%v ParentPID=%d, want true/100", agents[2].IsSubagent(), agents[2].ParentPID)
	}
	if agents[3].IsSubagent() {
		t.Error("PID 400 should not be a subagent")
	}
}

func TestTagFromProcessTreeEmpty(t *testing.T) {
	TagFromProcessTree(nil, func(pid int) int { return 0 })
}

func TestTagFromProcessTreeSingle(t *testing.T) {
	agents := []agent.Agent{{PID: 100}}
	TagFromProcessTree(agents, func(pid int) int { return 1 })
	if agents[0].IsSubagent() {
		t.Error("single agent should not be a subagent")
	}
}

func TestTagFromProcessTreeMultiLevel(t *testing.T) {
	parentLookup := func(pid int) int {
		switch pid {
		case 200:
			return 100
		case 300:
			return 200
		default:
			return 1
		}
	}
	agents := []agent.Agent{
		{PID: 100, Name: "parent"},
		{PID: 300, Name: "grandchild"},
	}
	TagFromProcessTree(agents, parentLookup)

	if agents[0].IsSubagent() {
		t.Error("PID 100 should not be a subagent")
	}
	if !agents[1].IsSubagent() || agents[1].ParentPID != 100 {
		t.Errorf("PID 300: IsSubagent=%v ParentPID=%d, want true/100", agents[1].IsSubagent(), agents[1].ParentPID)
	}
}

func TestEnrichFromOTELNilStore(t *testing.T) {
	agents := []agent.Agent{{PID: 100, SessionID: "s1"}}
	EnrichFromOTEL(agents, nil) // should not panic
}

type mockOTELStore struct {
	info map[string]subagent.Info
	subs map[string][]subagent.Info
}

func (m *mockOTELStore) SubagentInfoBySession(sessionID string) subagent.Info {
	return m.info[sessionID]
}

func (m *mockOTELStore) SubagentsBySession(sessionID string) []subagent.Info {
	return m.subs[sessionID]
}

func TestEnrichFromOTELWithData(t *testing.T) {
	store := &mockOTELStore{
		info: map[string]subagent.Info{
			"s1": {ID: "sub-1", Type: "Explore"},
		},
		subs: map[string][]subagent.Info{},
	}
	agents := []agent.Agent{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
		{PID: 300, SessionID: ""},
	}
	result := EnrichFromOTEL(agents, store)

	if result[0].Subagent.Type != "Explore" {
		t.Errorf("agent[0].Subagent.Type = %q, want %q", result[0].Subagent.Type, "Explore")
	}
	if result[1].Subagent.HasIdentity() {
		t.Error("agent[1] should not have subagent identity (no OTEL data)")
	}
	if result[2].Subagent.HasIdentity() {
		t.Error("agent[2] should not have subagent identity (no session ID)")
	}
}

func TestEnrichFromOTELCreatesVirtualSubagents(t *testing.T) {
	store := &mockOTELStore{
		info: map[string]subagent.Info{},
		subs: map[string][]subagent.Info{
			"s1": {
				{ID: "explore-1", Type: "Explore"},
				{ID: "plan-1", Type: "Plan"},
			},
		},
	}
	agents := []agent.Agent{
		{PID: 100, SessionID: "s1", ProviderName: "claude", Model: "opus-4.6", WorkingDir: "/tmp/test"},
	}
	result := EnrichFromOTEL(agents, store)

	if len(result) != 3 {
		t.Fatalf("expected 3 agents (1 parent + 2 virtual), got %d", len(result))
	}

	// Virtual subagents should have ParentPID = parent's PID
	explore := result[1]
	if explore.Subagent.Type != "Explore" {
		t.Errorf("virtual[0].Type = %q, want Explore", explore.Subagent.Type)
	}
	if explore.ParentPID != 100 {
		t.Errorf("virtual[0].ParentPID = %d, want 100", explore.ParentPID)
	}
	if !explore.IsSubagent() {
		t.Error("virtual agent should be a subagent")
	}
	if explore.Name != "Explore" {
		t.Errorf("virtual[0].Name = %q, want Explore", explore.Name)
	}

	plan := result[2]
	if plan.Subagent.Type != "Plan" {
		t.Errorf("virtual[1].Type = %q, want Plan", plan.Subagent.Type)
	}
}

func TestEnrichFromOTELNoDuplicateVirtuals(t *testing.T) {
	store := &mockOTELStore{
		info: map[string]subagent.Info{},
		subs: map[string][]subagent.Info{
			"s1": {{ID: "explore-1", Type: "Explore"}},
		},
	}
	// Agent already tagged from process tree with same subagent ID
	agents := []agent.Agent{
		{PID: 100, SessionID: "s1"},
		{PID: 200, ParentPID: 100, Subagent: subagent.Info{ID: "explore-1", Type: "Explore"}},
	}
	result := EnrichFromOTEL(agents, store)

	// Should NOT create a duplicate virtual entry
	if len(result) != 2 {
		t.Errorf("expected 2 agents (no duplicate virtual), got %d", len(result))
	}
}
