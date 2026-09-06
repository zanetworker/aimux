package sdk

import (
	"context"
	"fmt"

	"github.com/zanetworker/aimux/internal/environment"
)

// Dispatcher routes tool calls to the appropriate execution environment.
// Sandbox tools (bash, read, write, etc.) are executed inside the sandbox
// via env.Exec; all other tools are approved for local SDK execution.
type Dispatcher struct {
	env          environment.Environment
	sandbox      string
	sandboxTools map[string]bool
}

// NewDispatcher creates a Dispatcher that routes sandbox tools to the given
// environment and sandbox name.
func NewDispatcher(env environment.Environment, sandbox string) *Dispatcher {
	return &Dispatcher{
		env:     env,
		sandbox: sandbox,
		sandboxTools: map[string]bool{
			"bash":  true,
			"Bash":  true,
			"read":  true,
			"Read":  true,
			"write": true,
			"Write": true,
			"edit":  true,
			"Edit":  true,
			"glob":  true,
			"Glob":  true,
			"grep":  true,
			"Grep":  true,
		},
	}
}

// Dispatch decides how to handle a tool call. Sandbox tools are executed
// via env.Exec and return (result, Handled). Non-sandbox tools return
// (empty, Approve) so the SDK can execute them locally.
func (d *Dispatcher) Dispatch(ctx context.Context, call ToolCall) (ToolResult, HookDecision) {
	if call.Name == "" || !d.sandboxTools[call.Name] {
		return ToolResult{}, Approve
	}

	command, err := buildCommand(call)
	if err != nil {
		return ToolResult{
			ID:      call.ID,
			Output:  fmt.Sprintf("sdk: failed to build command for %q: %v", call.Name, err),
			IsError: true,
		}, Handled
	}

	output, err := d.env.Exec(ctx, d.sandbox, command)
	if err != nil {
		return ToolResult{
			ID:      call.ID,
			Output:  fmt.Sprintf("sdk: exec %q in sandbox %q: %v", call.Name, d.sandbox, err),
			IsError: true,
		}, Handled
	}

	return ToolResult{
		ID:     call.ID,
		Output: output,
	}, Handled
}

func buildCommand(call ToolCall) (string, error) {
	switch call.Name {
	case "bash", "Bash":
		cmd, ok := call.Input["command"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'command' input")
		}
		return cmd, nil

	case "read", "Read":
		path, ok := call.Input["file_path"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'file_path' input")
		}
		return fmt.Sprintf("cat %q", path), nil

	case "write", "Write":
		path, ok := call.Input["file_path"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'file_path' input")
		}
		content, ok := call.Input["content"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'content' input")
		}
		return fmt.Sprintf("cat > %q << 'AIMUX_EOF'\n%s\nAIMUX_EOF", path, content), nil

	case "edit", "Edit":
		path, _ := call.Input["file_path"].(string)
		oldStr, _ := call.Input["old_string"].(string)
		newStr, _ := call.Input["new_string"].(string)
		if path == "" || oldStr == "" {
			return "", fmt.Errorf("missing 'file_path' or 'old_string' input")
		}
		return fmt.Sprintf("sed -i '' 's/%s/%s/g' %q", escapeForSed(oldStr), escapeForSed(newStr), path), nil

	case "glob", "Glob":
		pattern, ok := call.Input["pattern"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'pattern' input")
		}
		return fmt.Sprintf("find . -name %q", pattern), nil

	case "grep", "Grep":
		pattern, ok := call.Input["pattern"].(string)
		if !ok {
			return "", fmt.Errorf("missing or invalid 'pattern' input")
		}
		path, _ := call.Input["path"].(string)
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("grep -rn %q %q", pattern, path), nil

	default:
		return "", fmt.Errorf("unknown sandbox tool: %s", call.Name)
	}
}

func escapeForSed(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '/', '\\', '&', '\n':
			result = append(result, '\\')
		}
		result = append(result, s[i])
	}
	return string(result)
}
