package session

import (
	"fmt"
	"time"
)

var validTransitions = map[SessionStatus][]SessionStatus{
	StatusCreated: {StatusRunning},
	StatusRunning: {StatusIdle, StatusTerminated, StatusError},
	StatusIdle:    {StatusRunning, StatusTerminated},
}

// Transition validates and applies a status change to the session.
// Returns an error if the transition is not allowed.
func Transition(sess *Session, to SessionStatus) error {
	allowed, ok := validTransitions[sess.Status]
	if ok {
		for _, s := range allowed {
			if s == to {
				sess.Status = to
				sess.LastActivity = time.Now()
				return nil
			}
		}
	}
	return fmt.Errorf("invalid session transition: %s → %s", sess.Status, to)
}
