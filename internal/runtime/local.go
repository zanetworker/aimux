package runtime

// Local represents the default runtime where agents run as local
// processes. All lifecycle methods are no-ops because the agent process
// lifecycle is managed by spawn.Launch, not by the runtime abstraction.
type Local struct {
	name string
}

// NewLocal creates a Local runtime with the given instance name.
func NewLocal(name string) *Local {
	return &Local{name: name}
}

func (l *Local) Type() string            { return "local" }
func (l *Local) Name() string            { return l.name }
func (l *Local) Create(_ CreateOpts) error { return nil }
func (l *Local) Start() error            { return nil }
func (l *Local) Stop() error             { return nil }
func (l *Local) Delete() error           { return nil }

// Status always returns StateRunning for local processes. The actual
// process health is tracked by the discovery layer, not the runtime.
func (l *Local) Status() RuntimeStatus {
	return RuntimeStatus{State: StateRunning}
}

// ExecPrefix returns nil because local processes need no command prefix.
func (l *Local) ExecPrefix() []string { return nil }

// Attach is a no-op for local runtimes.
func (l *Local) Attach() error { return nil }
