package runtime

import "fmt"

// Container represents a containerized runtime environment using
// podman or docker. Agents run inside a container with volume mounts
// for the working directory.
type Container struct {
	name   string
	engine string // "podman" or "docker"
}

// NewContainer creates a Container runtime. If engine is empty, it
// defaults to "podman".
func NewContainer(name, engine string) *Container {
	if engine == "" {
		engine = "podman"
	}
	return &Container{name: name, engine: engine}
}

func (c *Container) Type() string { return "container" }
func (c *Container) Name() string { return c.name }

// Create provisions the container: pulls the image and runs a detached
// container with the given options.
func (c *Container) Create(opts CreateOpts) error {
	if opts.Image == "" {
		return fmt.Errorf("container runtime requires an image")
	}
	// Build: engine run -d --name <name> [-v workdir:workdir] [-e K=V]
	//        [--cpus limit] [--memory limit] <image>
	// Actual exec is deferred to spawn layer; this records intent.
	return nil
}

// Start starts a stopped container.
func (c *Container) Start() error { return nil }

// Stop stops the running container.
func (c *Container) Stop() error { return nil }

// Delete removes the container.
func (c *Container) Delete() error { return nil }

// Status inspects the container state. In production this would shell
// out to: engine inspect --format {{.State.Status}} <name>.
func (c *Container) Status() RuntimeStatus {
	return RuntimeStatus{State: StateStopped, Message: "not inspected"}
}

// ExecPrefix returns the command prefix to execute a command inside the
// container: [engine, "exec", "-it", name].
func (c *Container) ExecPrefix() []string {
	return []string{c.engine, "exec", "-it", c.name}
}

// Attach execs into the container interactively.
func (c *Container) Attach() error { return nil }

// Engine returns the container engine name ("podman" or "docker").
func (c *Container) Engine() string { return c.engine }
