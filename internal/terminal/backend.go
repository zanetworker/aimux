package terminal

// SessionBackend is the common interface for terminal session backends.
// The direct PTY backend (Session) and the tmux mirror backend (TmuxSession)
// both implement this interface. SessionView works with either.
type SessionBackend interface {
	Read(buf []byte) (int, error)
	Write(data []byte) (int, error)
	Resize(cols, rows int) error
	Close() error
	Alive() bool
}

// DirectRenderer is an optional interface that backends can implement when
// their output is already rendered terminal text (e.g., tmux capture-pane).
// SessionView checks for this: if present, it skips the VT emulator and
// uses Render() directly. If absent, output goes through the VT emulator.
type DirectRenderer interface {
	Render() string
}
