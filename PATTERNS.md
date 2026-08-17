# Common Change Patterns

Recipes for the most frequent types of changes in aimux.

## Add a New Provider

1. Create `internal/provider/yourprovider.go` implementing all `Provider` interface methods
2. Add compile-time check: `var _ Provider = (*YourProvider)(nil)`
3. Register in `tui/app.go` `NewApp()` providers list
4. Add default config in `config/config.go` `Default()`
5. Add model pricing in `cost/tracker.go`
6. Add tests in `internal/provider/yourprovider_test.go`

Example: see `internal/provider/claude.go` for a complete implementation.

## Add a New TUI View

1. Create `internal/tui/views/yourview.go` with a Bubble Tea `Model` struct
2. Implement `Init()`, `Update()`, `View()` methods
3. Add navigation key in `tui/app.go` `Update()` switch
4. Add hint bar entry in `tui/app.go` `updateHints()`
5. Add tests (view logic should be in core packages, not in the view itself)

## Add a New API Endpoint (Web UI)

1. Add handler in `internal/frontend/web/` using core packages for logic
2. Register route in the HTTP mux setup
3. Add corresponding React component in `web/src/`
4. Never reimplement Go logic in TypeScript; fetch from the API instead

## Add a New Keybinding

1. Add key handler in the relevant view's `Update()` method
2. Update the hint bar in `tui/app.go` `updateHints()`
3. Document in `docs-site/src/content/docs/` if user-facing

## Fix a Bug Across All Providers

1. Check if the bug exists in both providers (Claude, Codex)
2. Fix in shared helpers (`provider/helpers.go`) if possible
3. If provider-specific, fix in each provider file
4. Run `go test ./internal/provider/... -timeout 30s` to verify both
