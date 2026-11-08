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

// MetaPatch is a partial .meta.json update: nil fields are left as they are,
// a non-nil field (an empty string or empty tag list too) replaces the value.
type MetaPatch struct {
	Annotation    *string
	Tags          []string
	Note          *string
	Starred       *bool
	ROIMultiplier *float64
	TaskType      *string
}

// UpdateSessionMeta applies a patch to a session's metadata in one write.
func UpdateSessionMeta(sessionFile string, p MetaPatch) error {
	return history.UpdateMeta(sessionFile, func(m *history.Meta) {
		if p.Annotation != nil {
			m.Annotation = *p.Annotation
		}
		if p.Tags != nil {
			m.Tags = p.Tags
		}
		if p.Note != nil {
			m.Note = *p.Note
		}
		if p.Starred != nil {
			m.Starred = *p.Starred
		}
		if p.ROIMultiplier != nil {
			m.ROIMultiplier = *p.ROIMultiplier
		}
		if p.TaskType != nil {
			m.TaskType = *p.TaskType
		}
	})
}
