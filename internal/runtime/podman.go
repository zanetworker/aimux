package runtime

import (
	"fmt"
	"os/exec"
	"strings"
)

// PodmanBackend implements Backend for local container engines (podman or docker).
type PodmanBackend struct {
	engine string
}

// NewPodmanBackend creates a backend for podman or docker. Empty engine defaults to "podman".
func NewPodmanBackend(engine string) *PodmanBackend {
	if engine == "" {
		engine = "podman"
	}
	return &PodmanBackend{engine: engine}
}

func (p *PodmanBackend) Name() string   { return p.engine }
func (p *PodmanBackend) IsRemote() bool { return false }

func (p *PodmanBackend) Create(name string, opts BackendCreateOpts) error {
	if opts.Image == "" {
		return fmt.Errorf("%s create: image is required", p.engine)
	}
	args := []string{"run", "-d", "--name", name}
	if opts.WorkDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:/workspace:Z", opts.WorkDir), "-w", "/workspace")
	}
	for k, v := range opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	if opts.Resources != nil {
		if opts.Resources.CPULimit != "" {
			args = append(args, "--cpus", opts.Resources.CPULimit)
		}
		if opts.Resources.MemoryLimit != "" {
			args = append(args, "--memory", opts.Resources.MemoryLimit)
		}
	}
	args = append(args, opts.Image, "sleep", "infinity")
	cmd := exec.Command(p.engine, args...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s run: %w\n%s", p.engine, err, out)
	}
	return nil
}

func (p *PodmanBackend) Start(name string) error {
	return p.run("start", name)
}

func (p *PodmanBackend) Stop(name string) error {
	return p.run("stop", name)
}

func (p *PodmanBackend) Delete(name string) error {
	return p.run("rm", "-f", name)
}

func (p *PodmanBackend) Status(name string) (State, error) {
	cmd := exec.Command(p.engine, "inspect", "--format", "{{.State.Status}}", name) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return StateStopped, nil
	}
	if strings.TrimSpace(string(out)) == "running" {
		return StateRunning, nil
	}
	return StateStopped, nil
}

func (p *PodmanBackend) ExecPrefix(name string) []string {
	return []string{p.engine, "exec", "-it", name}
}

func (p *PodmanBackend) run(args ...string) error {
	cmd := exec.Command(p.engine, args...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", p.engine, args[0], err, out)
	}
	return nil
}
