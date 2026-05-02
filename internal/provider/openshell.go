package provider

import (
	"os/exec"

	"github.com/zanetworker/aimux/internal/agent"
	"github.com/zanetworker/aimux/internal/subagent"
	"github.com/zanetworker/aimux/internal/trace"
)

// OpenShell discovers agents running in NVIDIA OpenShell sandboxes.
// Architecture placeholder. Functional implementation follows once
// OpenShell's API stabilizes. Integration plan: discover sandboxes
// via openshell sandbox list, connect via openshell sandbox connect.
type OpenShell struct{}

// Compile-time interface check
var _ Provider = (*OpenShell)(nil)

func (o *OpenShell) Name() string {
	return "openshell"
}

func (o *OpenShell) Discover() ([]agent.Agent, error) {
	return nil, nil
}

func (o *OpenShell) ResumeCommand(a agent.Agent) *exec.Cmd {
	return nil
}

func (o *OpenShell) CanEmbed() bool {
	return false
}

func (o *OpenShell) FindSessionFile(a agent.Agent) string {
	return ""
}

func (o *OpenShell) RecentDirs(max int) []RecentDir {
	return nil
}

func (o *OpenShell) SpawnCommand(dir, model, mode string) *exec.Cmd {
	return nil
}

func (o *OpenShell) SpawnArgs() SpawnArgs {
	return SpawnArgs{}
}

func (o *OpenShell) ParseTrace(filePath string) ([]trace.Turn, error) {
	return nil, nil
}

func (o *OpenShell) OTELEnv(endpoint string) string {
	return ""
}

func (o *OpenShell) OTELServiceName() string {
	return "openshell"
}

func (o *OpenShell) SubagentAttrKeys() subagent.AttrKeys {
	return subagent.AttrKeys{}
}

func (o *OpenShell) Kill(a agent.Agent) error {
	return nil
}
