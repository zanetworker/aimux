package runtime

// Backend is the container engine that manages container lifecycle.
// Podman, Docker, and Kubernetes all implement this interface.
type Backend interface {
	Name() string
	IsRemote() bool
	Create(name string, opts BackendCreateOpts) error
	Start(name string) error
	Stop(name string) error
	Delete(name string) error
	Status(name string) (State, error)
	ExecPrefix(name string) []string
}

// BackendCreateOpts configures container creation at the backend level.
type BackendCreateOpts struct {
	Image     string
	WorkDir   string
	Env       map[string]string
	Resources *Resources
	Labels    map[string]string
}
