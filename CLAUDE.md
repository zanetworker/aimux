# aimux

Multi-agent TUI dashboard. See `.claude/CLAUDE.md` for full project guide.

## Quick Commands

```bash
go build -o aimux ./cmd/aimux
go test ./... -timeout 30s
```

## Single-File Verification

```bash
gofmt internal/agent/agent.go
go vet ./internal/agent/
go test ./internal/agent/ -timeout 30s -run TestName
```

## Pattern References

- New provider: follow the pattern in `internal/provider/claude.go`
- New TUI view: use `internal/tui/views/agents.go` as a template
- New API endpoint: based on `internal/frontend/web/` existing handlers
- New keybinding: see `internal/tui/app.go` for examples
