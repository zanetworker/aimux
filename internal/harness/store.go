package harness

// SessionStore is where a harness keeps its session transcripts, as far as
// continuing a session needs it: the file of a session, the working
// directory the session recorded and a fingerprint that changes whenever
// the transcript does. Claude Code's is provider.Claude.
type SessionStore interface {
	// SessionFile is the transcript of session id, or "" when there is none.
	SessionFile(id string) string
	// SessionCwd is the first working directory the transcript at path
	// records; an error when it records none or cannot be read.
	SessionCwd(path string) (string, error)
	// Fingerprint changes whenever session id's transcript does; "" when
	// there is no transcript.
	Fingerprint(id string) string
}
