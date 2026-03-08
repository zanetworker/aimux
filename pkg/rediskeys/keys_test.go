package rediskeys

import "testing"

func TestTeamKey(t *testing.T) {
	got := TeamKey("myteam", "heartbeat")
	want := "team:myteam:heartbeat"
	if got != want {
		t.Errorf("TeamKey = %q, want %q", got, want)
	}
}

func TestTeamKey_EmptyTeam(t *testing.T) {
	got := TeamKey("", "heartbeat")
	want := "team::heartbeat"
	if got != want {
		t.Errorf("TeamKey empty team = %q, want %q", got, want)
	}
}

func TestInbox(t *testing.T) {
	tests := []struct {
		teamID  string
		agentID string
		want    string
	}{
		{"myteam", "agent-1", "team:myteam:inbox:agent-1"},
		{"research", "claude-abc", "team:research:inbox:claude-abc"},
		{"default", "agent-xyz", "team:default:inbox:agent-xyz"},
	}
	for _, tt := range tests {
		got := Inbox(tt.teamID, tt.agentID)
		if got != tt.want {
			t.Errorf("Inbox(%q, %q) = %q, want %q", tt.teamID, tt.agentID, got, tt.want)
		}
	}
}

func TestEvents(t *testing.T) {
	got := Events("myteam")
	want := "team:myteam:events"
	if got != want {
		t.Errorf("Events = %q, want %q", got, want)
	}
}

func TestTasksPending(t *testing.T) {
	got := TasksPending("myteam")
	want := "team:myteam:tasks:pending"
	if got != want {
		t.Errorf("TasksPending = %q, want %q", got, want)
	}
}

func TestTasksAll(t *testing.T) {
	got := TasksAll("myteam")
	want := "team:myteam:tasks:all"
	if got != want {
		t.Errorf("TasksAll = %q, want %q", got, want)
	}
}

func TestTask(t *testing.T) {
	tests := []struct {
		teamID string
		taskID string
		want   string
	}{
		{"myteam", "abc123", "team:myteam:task:abc123"},
		{"research", "a3f2bc", "team:research:task:a3f2bc"},
		{"default", "00000000", "team:default:task:00000000"},
	}
	for _, tt := range tests {
		got := Task(tt.teamID, tt.taskID)
		if got != tt.want {
			t.Errorf("Task(%q, %q) = %q, want %q", tt.teamID, tt.taskID, got, tt.want)
		}
	}
}

func TestAgent(t *testing.T) {
	got := Agent("myteam", "agent-42")
	want := "team:myteam:agent:agent-42"
	if got != want {
		t.Errorf("Agent = %q, want %q", got, want)
	}
}

func TestHeartbeat(t *testing.T) {
	got := Heartbeat("myteam")
	want := "team:myteam:heartbeat"
	if got != want {
		t.Errorf("Heartbeat = %q, want %q", got, want)
	}
}

func TestCost(t *testing.T) {
	tests := []struct {
		teamID  string
		agentID string
		want    string
	}{
		{"myteam", "agent-1", "team:myteam:cost:agent-1"},
		{"research", "claude-abc", "team:research:cost:claude-abc"},
	}
	for _, tt := range tests {
		got := Cost(tt.teamID, tt.agentID)
		if got != tt.want {
			t.Errorf("Cost(%q, %q) = %q, want %q", tt.teamID, tt.agentID, got, tt.want)
		}
	}
}

func TestConfig(t *testing.T) {
	got := Config("myteam")
	want := "team:myteam:config"
	if got != want {
		t.Errorf("Config = %q, want %q", got, want)
	}
}

// TestKeyFormats verifies the exact format for all keys as documented in §5.
func TestKeyFormats(t *testing.T) {
	const team = "myteam"
	const task = "abc123"
	const agent = "agent-1"

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"TeamKey", TeamKey(team, "heartbeat"), "team:myteam:heartbeat"},
		{"Inbox", Inbox(team, agent), "team:myteam:inbox:agent-1"},
		{"Events", Events(team), "team:myteam:events"},
		{"TasksPending", TasksPending(team), "team:myteam:tasks:pending"},
		{"TasksAll", TasksAll(team), "team:myteam:tasks:all"},
		{"Task", Task(team, task), "team:myteam:task:abc123"},
		{"Agent", Agent(team, agent), "team:myteam:agent:agent-1"},
		{"Heartbeat", Heartbeat(team), "team:myteam:heartbeat"},
		{"Cost", Cost(team, agent), "team:myteam:cost:agent-1"},
		{"Config", Config(team), "team:myteam:config"},
	}

	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}
