package controller

import "github.com/zanetworker/aimux/internal/history"

// ToggleStar flips the starred state for a session and persists it.
// Returns the new starred value.
func ToggleStar(sessionFile string) (bool, error) {
	var starred bool
	err := history.UpdateMeta(sessionFile, func(m *history.Meta) {
		m.Starred = !m.Starred
		starred = m.Starred
	})
	if err != nil {
		return false, err
	}
	return starred, nil
}

// SetAnnotation updates the annotation field for a session.
func SetAnnotation(sessionFile, annotation string) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) { m.Annotation = annotation })
}

// SetTags updates the tags list for a session.
func SetTags(sessionFile string, tags []string) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) { m.Tags = tags })
}

// SetNote updates the free-text note for a session.
func SetNote(sessionFile, note string) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) { m.Note = note })
}

// SetStarred sets the starred (pinned) state for a session.
func SetStarred(sessionFile string, starred bool) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) { m.Starred = starred })
}

// SetROI updates the ROI multiplier and task type for a session.
func SetROI(sessionFile string, multiplier float64, taskType string) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) {
		m.ROIMultiplier = multiplier
		m.TaskType = taskType
	})
}
