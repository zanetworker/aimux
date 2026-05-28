package history

import "testing"

func TestInferTaskType_Coding(t *testing.T) {
	skills := []string{"development-tools:crafted-code", "superpowers:test-driven-development"}
	taskType, mult := InferTaskType(skills)
	if taskType != "coding" {
		t.Errorf("expected task type 'coding', got %q", taskType)
	}
	if mult != 1.5 {
		t.Errorf("expected multiplier 1.5, got %f", mult)
	}
}

func TestInferTaskType_Writing(t *testing.T) {
	skills := []string{"writing-tools:humanizer", "writing-tools:lenny-style", "writing-tools:redhat-editorial"}
	taskType, mult := InferTaskType(skills)
	if taskType != "writing" {
		t.Errorf("expected task type 'writing', got %q", taskType)
	}
	if mult != 2.5 {
		t.Errorf("expected multiplier 2.5, got %f", mult)
	}
}

func TestInferTaskType_PMWork(t *testing.T) {
	skills := []string{"planning-tools:rice", "jira-helpers:business-justification"}
	taskType, mult := InferTaskType(skills)
	if taskType != "pm_work" {
		t.Errorf("expected task type 'pm_work', got %q", taskType)
	}
	if mult != 4.0 {
		t.Errorf("expected multiplier 4.0, got %f", mult)
	}
}

func TestInferTaskType_Research(t *testing.T) {
	skills := []string{"communication-tools:social-research", "communication-tools:ai-news-feed"}
	taskType, mult := InferTaskType(skills)
	if taskType != "research" {
		t.Errorf("expected task type 'research', got %q", taskType)
	}
	if mult != 3.0 {
		t.Errorf("expected multiplier 3.0, got %f", mult)
	}
}

func TestInferTaskType_MixedDominance(t *testing.T) {
	skills := []string{
		"development-tools:crafted-code",
		"writing-tools:humanizer",
		"writing-tools:lenny-style",
	}
	taskType, _ := InferTaskType(skills)
	if taskType != "writing" {
		t.Errorf("expected dominant type 'writing' (2 vs 1), got %q", taskType)
	}
}

func TestInferTaskType_Empty(t *testing.T) {
	taskType, mult := InferTaskType(nil)
	if taskType != "" {
		t.Errorf("expected empty task type for nil skills, got %q", taskType)
	}
	if mult != 0 {
		t.Errorf("expected 0 multiplier for nil skills, got %f", mult)
	}
}

func TestInferTaskType_UnknownSkills(t *testing.T) {
	skills := []string{"sync-plugins", "some-unknown-skill"}
	taskType, mult := InferTaskType(skills)
	if taskType != "" {
		t.Errorf("expected empty task type for unknown skills, got %q", taskType)
	}
	if mult != 0 {
		t.Errorf("expected 0 multiplier for unknown skills, got %f", mult)
	}
}

func TestApplyAutoROI_PreservesUserSet(t *testing.T) {
	sessions := []Session{
		{ID: "abc12345-xxxx", ROIMultiplier: 5.0, TaskType: "custom"},
	}
	ApplyAutoROI(sessions)
	if sessions[0].ROIMultiplier != 5.0 {
		t.Errorf("ApplyAutoROI overwrote user-set multiplier: got %f", sessions[0].ROIMultiplier)
	}
	if sessions[0].TaskType != "custom" {
		t.Errorf("ApplyAutoROI overwrote user-set task type: got %q", sessions[0].TaskType)
	}
}

func TestMatchSessionSkills_PrefixMatch(t *testing.T) {
	skillMap := map[string][]string{
		"abc12345": {"development-tools:crafted-code", "superpowers:brainstorming"},
	}
	skills := MatchSessionSkills("abc12345-full-uuid-here", skillMap)
	if len(skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(skills))
	}
}

func TestMatchSessionSkills_NoMatch(t *testing.T) {
	skillMap := map[string][]string{
		"abc12345": {"development-tools:crafted-code"},
	}
	skills := MatchSessionSkills("zzz99999-no-match", skillMap)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills for non-matching ID, got %d", len(skills))
	}
}
