package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// TaskMultiplier maps task types to research-backed time multipliers.
// Sources: McKinsey (4,500 devs), DX (135K devs), GitHub RCT, Google RCT.
// Enterprise discount already applied (raw lab gains halved).
var TaskMultiplier = map[string]float64{
	"coding":    1.5,
	"bug_fix":   1.8,
	"refactor":  1.7,
	"research":  3.0,
	"writing":   2.5,
	"pm_work":   4.0,
	"devops":    2.0,
	"learning":  2.0,
	"review":    1.5,
	"automation": 3.0,
}

// skillToTaskType maps skill names to task type categories.
var skillToTaskType = map[string]string{
	"development-tools:crafted-code":              "coding",
	"superpowers:test-driven-development":          "coding",
	"development-tools:ralph-loop":                 "coding",
	"development-tools:openshell-contributor":      "coding",
	"development-tools:llama-stack-contributor":     "coding",
	"development-tools:openai-agents-contributor":   "coding",

	"superpowers:systematic-debugging":             "bug_fix",
	"debugging-and-error-recovery":                 "bug_fix",

	"code-simplification":                          "refactor",
	"code-review-and-quality":                      "review",
	"superpowers:requesting-code-review":            "review",
	"superpowers:receiving-code-review":             "review",

	"communication-tools:social-research":          "research",
	"communication-tools:ai-news-feed":             "research",
	"superpowers:brainstorming":                    "research",
	"development-tools:walkthrough":                "research",
	"planning-tools:competition":                   "research",
	"planning-tools:competitive-research":          "research",
	"planning-tools:oss-project-health":            "research",
	"development-tools:release-analyzer":           "research",

	"writing-tools:humanizer":                      "writing",
	"writing-tools:writing-pipeline":               "writing",
	"writing-tools:lenny-style":                    "writing",
	"writing-tools:redhat-editorial":               "writing",
	"writing-tools:social-post":                    "writing",
	"writing-tools:steve-jobs":                     "writing",
	"writing-tools:linkedin-optimizer":             "writing",
	"writing-tools:x-optimizer":                    "writing",
	"writing-tools:substack-optimizer":             "writing",
	"writing-tools:blog-tweet":                     "writing",

	"planning-tools:rice":                          "pm_work",
	"planning-tools:rice-update":                   "pm_work",
	"planning-tools:prioritize":                    "pm_work",
	"planning-tools:confidence":                    "pm_work",
	"jira-helpers:business-justification":          "pm_work",
	"jira-helpers:feature-refinement-doc":          "pm_work",
	"jira-helpers:jira-orphan-finder":              "pm_work",
	"communication-tools:weekly-report":            "pm_work",
	"communication-tools:slack-ai-team-reporter":   "pm_work",
	"assess-rfe:assess-rfe":                        "pm_work",
	"rfe-creator:rfe-create":                       "pm_work",
	"rfe-creator:rfe-submit":                       "pm_work",
	"rfe-creator:rfe-review":                       "pm_work",
	"rfe-creator:strat-create":                     "pm_work",  //nolint:misspell // "strat" is a skill name, not a typo
	"rfe-creator:strat-review":                     "pm_work",  //nolint:misspell // "strat" is a skill name, not a typo

	"ci-cd-and-automation":                         "devops",
	"development-tools:cron":                       "devops",
	"cluster-tools:rhoai-packages":                 "devops",
	"cluster-tools:container-version-checker":      "devops",
	"cluster-tools:model-route-finder":             "devops",

	"source-driven-development":                    "learning",

	"superpowers:writing-skills":                   "automation",
	"development-tools:autoimprove":                 "automation",
	"development-tools:agentic-cli-builder":         "automation",
}

// skillUsageEntry represents one line from skill-usage.jsonl.
type skillUsageEntry struct {
	Session string `json:"session"`
	Skill   string `json:"skill"`
}

// LoadSkillUsage reads the skill-usage.jsonl file and returns a map
// of session ID prefix to list of skills used.
func LoadSkillUsage() map[string][]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".claude", "skill-usage.jsonl")
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck // read-only file

	result := make(map[string][]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry skillUsageEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Session != "" {
			result[entry.Session] = append(result[entry.Session], entry.Skill)
		}
	}
	return result
}

// InferTaskType determines the dominant task type from a list of skills.
// Returns the task type and its multiplier.
func InferTaskType(skills []string) (string, float64) {
	if len(skills) == 0 {
		return "", 0
	}

	counts := make(map[string]int)
	for _, skill := range skills {
		taskType, ok := skillToTaskType[skill]
		if ok {
			counts[taskType]++
		}
	}

	if len(counts) == 0 {
		return "", 0
	}

	// Find the most frequent task type
	best := ""
	bestCount := 0
	for t, c := range counts {
		if c > bestCount {
			best = t
			bestCount = c
		}
	}

	mult := TaskMultiplier[best]
	return best, mult
}

// BaselineMultiplier is applied to sessions with no skill data but
// enough activity to indicate real work. Enterprise floor from
// Google RCT (21-26% speedup) and DX dataset (2 hrs/week saved).
const BaselineMultiplier = 1.5

// minTurnsForBaseline is the minimum turn count to apply a baseline ROI.
const minTurnsForBaseline = 5

// minCostForBaseline is the minimum cost to apply a baseline ROI.
const minCostForBaseline = 0.50

// ApplyAutoROI sets ROIMultiplier and TaskType on sessions that don't
// already have a user-set value, using skill-usage data for inference.
// Sessions with no skill data but enough activity get a conservative baseline.
func ApplyAutoROI(sessions []Session) {
	skillMap := LoadSkillUsage()

	for i := range sessions {
		s := &sessions[i]
		if s.ROIMultiplier > 0 {
			continue
		}

		prefix := s.ID
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}

		// Try skill-based inference first
		if skillMap != nil {
			skills := skillMap[prefix]
			if len(skills) > 0 {
				taskType, mult := InferTaskType(skills)
				if mult > 0 {
					s.ROIMultiplier = mult
					s.TaskType = taskType
					continue
				}
			}
		}

		// Fall back to baseline for sessions with real activity
		if s.TurnCount >= minTurnsForBaseline && s.CostUSD >= minCostForBaseline {
			s.ROIMultiplier = BaselineMultiplier
			s.TaskType = "general"
		}
	}
}

// MatchSessionSkills returns the skills used in a session by matching
// the session ID prefix against the skill-usage log.
func MatchSessionSkills(sessionID string, skillMap map[string][]string) []string {
	if skillMap == nil {
		return nil
	}
	prefix := sessionID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return skillMap[strings.ToLower(prefix)]
}
