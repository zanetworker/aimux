# aimux Documentation Site Design

## Overview

Build a documentation website for aimux using Starlight (Astro) with custom dark/light theming matching aimux's palette. Deploy to GitHub Pages via GitHub Actions.

## Technology

- **Framework**: Starlight (Astro)
- **Search**: Pagefind (built into Starlight)
- **Deploy**: GitHub Actions to `gh-pages` branch
- **Hosting**: GitHub Pages at `zanetworker.github.io/aimux`

## Site Structure

```
docs-site/
  astro.config.mjs          # Starlight config, sidebar, site metadata
  src/
    content/docs/
      index.mdx              # Landing/hero page
      getting-started.mdx     # Install + first run (TUI and web)
      configuration.mdx       # Full config.yaml reference
      guides/
        web-dashboard.mdx     # Web UI: cards, filters, sessions, trace panel, themes
        tui-keybindings.mdx   # TUI keybindings and views
        launch-modes.mdx      # Direct vs tmux, task-driven launch
        tracing.mdx           # Trace view, annotations, export (JSONL/OTEL)
        cost-tracking.mdx     # Per-agent/turn cost, model pricing
        notifications.mdx     # Bell, desktop, per-event config
        plugins.mdx           # Skill dashboard, plugin system
        tasks.mdx             # Google Tasks integration
      advanced/
        adding-a-provider.mdx # Provider interface guide
        k8s-quickstart.mdx    # Kubernetes multi-agent setup
    styles/
      custom.css              # aimux theme overrides (dark + light palettes)
  public/
    favicon.svg               # aimux logo
```

## Sidebar Navigation

```
- Home
- Getting Started
- Configuration
- Guides
  - Web Dashboard
  - TUI Keybindings
  - Launch Modes
  - Tracing & Annotations
  - Cost Tracking
  - Notifications
  - Plugins
  - Tasks Integration
- Advanced
  - Adding a Provider
  - Kubernetes Quickstart
```

## Custom Theme

Override Starlight's CSS custom properties to match aimux palettes.

### Dark theme (Starlight default = dark)

| Starlight Variable | Value | Source |
|-------------------|-------|--------|
| `--sl-color-accent` | `#FF3131` | aimux accent |
| `--sl-color-accent-low` | `#3b1010` | aimux accent-dim |
| `--sl-color-accent-high` | `#FF6B6B` | lighter accent for links |
| `--sl-color-bg` | `#000000` | aimux bg-0 |
| `--sl-color-bg-nav` | `#0d0d0d` | aimux bg-1 |
| `--sl-color-bg-sidebar` | `#0d0d0d` | aimux bg-1 |
| `--sl-color-hairline` | `#1a1a1a` | aimux border |
| `--sl-color-text` | `#e6e6e6` | aimux fg |
| `--sl-color-text-accent` | `#FF3131` | aimux accent |
| `--sl-color-gray-1` | `#e6e6e6` | aimux fg |
| `--sl-color-gray-2` | `#a6a6a6` | aimux fg-2 |
| `--sl-color-gray-3` | `#666666` | aimux fg-3 |
| `--sl-color-gray-4` | `#404040` | aimux fg-4 |
| `--sl-color-gray-5` | `#1a1a1a` | aimux bg-2 |

### Light theme

| Starlight Variable | Value | Source |
|-------------------|-------|--------|
| `--sl-color-accent` | `#EE0000` | Red Hat Red |
| `--sl-color-accent-low` | `#FCE3E3` | RH red-10 |
| `--sl-color-accent-high` | `#A60000` | RH red-60 |
| `--sl-color-bg` | `#F5F5F5` | aimux light bg-0 |
| `--sl-color-bg-nav` | `#FFFFFF` | aimux light bg-1 |
| `--sl-color-bg-sidebar` | `#FFFFFF` | aimux light bg-1 |
| `--sl-color-hairline` | `#D4D4D4` | aimux light border |
| `--sl-color-text` | `#151515` | aimux light fg |
| `--sl-color-text-accent` | `#EE0000` | Red Hat Red |

## Page Content Sources

| Page | Content from |
|------|-------------|
| index.mdx | README hero, tagline, feature highlights |
| getting-started.mdx | README install + new `aimux web` command docs |
| configuration.mdx | internal/config/config.go structs, all fields documented |
| web-dashboard.mdx | New: describe card grid, filters, sessions table, trace panel, dark/light toggle |
| tui-keybindings.mdx | README keybindings table + split view, command palette |
| launch-modes.mdx | New: direct vs tmux modes, quick launch dirs, task-driven launch |
| tracing.mdx | README trace/annotate/export + OTEL dual-mode details |
| cost-tracking.mdx | internal/cost/tracker.go model pricing table |
| notifications.mdx | NotificationsConfig fields, macOS integration |
| plugins.mdx | New: plugin system, skill dashboard, plugin data API |
| tasks.mdx | New: Google Tasks integration, MCP/GWS backends, prompt templates |
| adding-a-provider.mdx | Existing docs/adding-a-provider.md |
| k8s-quickstart.mdx | Existing docs/k8s-quickstart.md |

## GitHub Actions Deploy

Workflow at `.github/workflows/docs.yml`:
- Trigger: push to main that touches `docs-site/**`
- Steps: checkout, setup Node 20, npm ci, npm run build, deploy to gh-pages
- Uses: `actions/deploy-pages@v4`

## CLI Subcommands to Document

| Command | Description |
|---------|-------------|
| `aimux` | Launch TUI dashboard |
| `aimux web` | Launch web dashboard (default port 9090) |
| `aimux --web` | Launch both TUI and web |
| `aimux sessions` | List/search session history |
| `aimux resume` | Resume a previous session |
| `aimux --version` | Print version |

## Web API Endpoints to Document

30 endpoints under `/api/` covering: agents SSE stream, launch, archive, trace, annotations, session meta, history, search, export (JSONL/OTEL), plugins, directories, tasks.

## What This Does NOT Include

- Blog
- Versioned docs (single version, latest)
- i18n
- API reference auto-generation
