package runtime

import "fmt"

// Container represents a containerized runtime environment.
// It delegates all lifecycle operations to a Backend (podman, docker, or kubernetes).
type Container struct {
	name    string
	backend Backend
}

// NewContainer creates a Container runtime backed by the given Backend.
func NewContainer(name string, backend Backend) *Container {
	return &Container{name: name, backend: backend}
}

func (c *Container) Type() string { return "container" }
func (c *Container) Name() string { return c.name }

func (c *Container) Create(opts CreateOpts) error {
	if opts.Image == "" {
		return fmt.Errorf("container runtime requires an image")
	}
	return c.backend.Create(c.name, BackendCreateOpts{
		Image:     opts.Image,
		WorkDir:   opts.WorkDir,
		Env:       opts.Env,
		Resources: &opts.Resources,
	})
}

func (c *Container) Start() error              { return c.backend.Start(c.name) }
func (c *Container) Stop() error               { return c.backend.Stop(c.name) }
func (c *Container) Delete() error             { return c.backend.Delete(c.name) }

func (c *Container) Status() RuntimeStatus {
	state, _ := c.backend.Status(c.name)
	return RuntimeStatus{State: state}
}

func (c *Container) ExecPrefix() []string { return c.backend.ExecPrefix(c.name) }
func (c *Container) Attach() error        { return nil }

// Backend returns the underlying backend.
func (c *Container) Backend() Backend { return c.backend }
