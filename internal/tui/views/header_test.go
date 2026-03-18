package views

import (
	"strings"
	"testing"
)

func TestSetTaskSummaryStoresCounts(t *testing.T) {
	h := NewHeaderView()
	h.SetTaskSummary(3, 2, 14, 1)

	if h.taskPending != 3 {
		t.Errorf("taskPending = %d, want 3", h.taskPending)
	}
	if h.taskActive != 2 {
		t.Errorf("taskActive = %d, want 2", h.taskActive)
	}
	if h.taskCompleted != 14 {
		t.Errorf("taskCompleted = %d, want 14", h.taskCompleted)
	}
	if h.taskFailed != 1 {
		t.Errorf("taskFailed = %d, want 1", h.taskFailed)
	}
}

func TestTaskSummaryRendersWhenCountsPositive(t *testing.T) {
	h := NewHeaderView()
	h.SetTaskSummary(3, 2, 14, 1)
	h.SetWidth(200)

	view := h.View()

	if !strings.Contains(view, "Tasks") {
		t.Error("expected 'Tasks' label in header view when task counts > 0")
	}
	if !strings.Contains(view, "3 pending") {
		t.Error("expected '3 pending' in header view")
	}
	if !strings.Contains(view, "2 running") {
		t.Error("expected '2 running' in header view")
	}
	if !strings.Contains(view, "14 done") {
		t.Error("expected '14 done' in header view")
	}
	if !strings.Contains(view, "1 failed") {
		t.Error("expected '1 failed' in header view")
	}
}

func TestTaskSummaryHiddenWhenAllCountsZero(t *testing.T) {
	h := NewHeaderView()
	h.SetTaskSummary(0, 0, 0, 0)
	h.SetWidth(200)

	summary := h.renderTaskSummary()
	if summary != "" {
		t.Errorf("renderTaskSummary() = %q, want empty string when all counts are 0", summary)
	}

	view := h.View()
	if strings.Contains(view, "Tasks") {
		t.Error("expected no 'Tasks' label in header when all counts are 0")
	}
}

func TestTaskSummaryOnlyShowsNonZeroCategories(t *testing.T) {
	h := NewHeaderView()
	h.SetTaskSummary(0, 2, 5, 0) // only active and completed
	h.SetWidth(200)

	summary := h.renderTaskSummary()

	if strings.Contains(summary, "pending") {
		t.Error("expected no 'pending' when pending count is 0")
	}
	if !strings.Contains(summary, "2 running") {
		t.Error("expected '2 running' in summary")
	}
	if !strings.Contains(summary, "5 done") {
		t.Error("expected '5 done' in summary")
	}
	if strings.Contains(summary, "failed") {
		t.Error("expected no 'failed' when failed count is 0")
	}
}

func TestTaskSummaryDefaultIsZero(t *testing.T) {
	h := NewHeaderView()

	summary := h.renderTaskSummary()
	if summary != "" {
		t.Errorf("renderTaskSummary() on fresh HeaderView = %q, want empty", summary)
	}
}

func TestK8sStatusInHeader(t *testing.T) {
	h := NewHeaderView()
	h.SetWidth(200)

	// No K8s status by default
	view := h.View()
	if strings.Contains(view, "K8s") {
		t.Error("expected no K8s box when status is empty")
	}

	// Set connected status
	h.SetK8sStatus("connected")
	view = h.View()
	if !strings.Contains(view, "K8s") {
		t.Error("expected 'K8s' label when status is set")
	}
	if !strings.Contains(view, "connected") {
		t.Error("expected 'connected' in header")
	}
}

func TestK8sStatusDisconnected(t *testing.T) {
	h := NewHeaderView()
	h.SetK8sStatus("disconnected (retry in 25s)")
	status := h.renderK8sStatus()
	if !strings.Contains(status, "disconnected") {
		t.Errorf("renderK8sStatus() = %q, want it to contain 'disconnected'", status)
	}
}
