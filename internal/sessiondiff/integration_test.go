package sessiondiff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zanetworker/aimux/internal/provider"
)

func TestIntegration_ExtractDiffsFromFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "sample_session.jsonl")
	if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
		t.Skip("fixture not found: ", fixturePath)
	}

	// Parse the fixture with the real Claude provider parser.
	claude := &provider.Claude{}
	turns, err := claude.ParseTrace(fixturePath)
	if err != nil {
		t.Fatalf("ParseTrace failed: %v", err)
	}

	if len(turns) == 0 {
		t.Fatal("ParseTrace returned zero turns from fixture")
	}

	// Extract diffs from the parsed turns.
	diffs := Extract(turns)

	// Verify at least 2 diffs returned (one Edit, one Write).
	if len(diffs) < 2 {
		t.Fatalf("expected at least 2 diffs, got %d", len(diffs))
	}

	// Find the Edit diff (main.go) and the Write diff (config.yaml).
	var mainGoDiff, configYamlDiff *FileDiff
	for i := range diffs {
		if strings.Contains(diffs[i].Path, "main.go") {
			mainGoDiff = &diffs[i]
		}
		if strings.Contains(diffs[i].Path, "config.yaml") {
			configYamlDiff = &diffs[i]
		}
	}

	// Verify main.go diff (Edit tool call).
	if mainGoDiff == nil {
		t.Fatal("no diff found with 'main.go' in path")
	}
	if mainGoDiff.Status != "modified" {
		t.Errorf("main.go status=%q, want %q", mainGoDiff.Status, "modified")
	}
	if mainGoDiff.Added == 0 {
		t.Error("main.go Added should be > 0")
	}
	if mainGoDiff.Removed == 0 {
		t.Error("main.go Removed should be > 0")
	}
	if mainGoDiff.ShortPath != "main.go" {
		t.Errorf("main.go ShortPath=%q, want %q", mainGoDiff.ShortPath, "main.go")
	}

	// Verify config.yaml diff (Write tool call).
	if configYamlDiff == nil {
		t.Fatal("no diff found with 'config.yaml' in path")
	}
	if configYamlDiff.Status != "added" {
		t.Errorf("config.yaml status=%q, want %q", configYamlDiff.Status, "added")
	}
	if configYamlDiff.Added == 0 {
		t.Error("config.yaml Added should be > 0")
	}
	if configYamlDiff.ShortPath != "config.yaml" {
		t.Errorf("config.yaml ShortPath=%q, want %q", configYamlDiff.ShortPath, "config.yaml")
	}

	// Verify hunks are populated with DiffLines.
	if len(mainGoDiff.Hunks) == 0 {
		t.Error("main.go should have at least one hunk")
	} else {
		if len(mainGoDiff.Hunks[0].Lines) == 0 {
			t.Error("main.go hunk[0] should have DiffLines")
		}
		// Edit produces both "del" and "add" lines.
		var hasDel, hasAdd bool
		for _, line := range mainGoDiff.Hunks[0].Lines {
			if line.Type == "del" {
				hasDel = true
			}
			if line.Type == "add" {
				hasAdd = true
			}
		}
		if !hasDel {
			t.Error("main.go hunk should contain 'del' lines from Edit old_string")
		}
		if !hasAdd {
			t.Error("main.go hunk should contain 'add' lines from Edit new_string")
		}
	}

	if len(configYamlDiff.Hunks) == 0 {
		t.Error("config.yaml should have at least one hunk")
	} else {
		if len(configYamlDiff.Hunks[0].Lines) == 0 {
			t.Error("config.yaml hunk[0] should have DiffLines")
		}
		// Write only produces "add" lines.
		for _, line := range configYamlDiff.Hunks[0].Lines {
			if line.Type != "add" {
				t.Errorf("config.yaml hunk line type=%q, want only 'add' for Write", line.Type)
			}
		}
	}
}
