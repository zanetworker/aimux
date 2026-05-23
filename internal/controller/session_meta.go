package controller

import "github.com/zanetworker/aimux/internal/history"

// ToggleStar flips the starred state for a session and persists it.
// Returns the new starred value.
func ToggleStar(sessionFile string) (bool, error) {
	meta := history.LoadMeta(sessionFile)
	meta.Starred = !meta.Starred
	if err := history.SaveMeta(sessionFile, meta); err != nil {
		return false, err
	}
	return meta.Starred, nil
}

// SetAnnotation updates the annotation field for a session.
func SetAnnotation(sessionFile, annotation string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Annotation = annotation
	return history.SaveMeta(sessionFile, meta)
}

// SetTags updates the tags list for a session.
func SetTags(sessionFile string, tags []string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Tags = tags
	return history.SaveMeta(sessionFile, meta)
}

// SetNote updates the free-text note for a session.
func SetNote(sessionFile, note string) error {
	meta := history.LoadMeta(sessionFile)
	meta.Note = note
	return history.SaveMeta(sessionFile, meta)
}
