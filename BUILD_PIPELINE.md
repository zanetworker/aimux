# Build Pipeline Documentation

## Overview

The aimux project uses a dual-build system:
1. **Web frontend** (React + TypeScript) built with Vite
2. **Go binary** with embedded web assets

## Build Targets

### `make web-build`
Builds the React frontend and copies it to the embed directory:
```bash
cd web && pnpm install && pnpm build
cp -r web/dist/* internal/frontend/web/dist/
```

### `make web-dev`
Runs the development server with hot reload:
```bash
cd web && pnpm dev
```

### `make build`
Builds the Go binary with embedded web assets:
```bash
go build -ldflags "-X main.version=$(VERSION)" -o aimux ./cmd/aimux
```

### `make build-all`
Complete build from clean state (recommended):
```bash
make build-all
```
This runs `web-build` followed by `build`.

## Directory Structure

```
aimux/
├── web/                          # React source
│   ├── src/
│   ├── dist/                     # Vite build output (gitignored)
│   └── .gitignore               # Excludes node_modules, dist
├── internal/frontend/web/
│   └── dist/                     # Copy of web/dist for go:embed (gitignored)
└── .gitignore                    # Excludes internal/frontend/web/dist
```

## Why Two dist Directories?

Go's `//go:embed` directive cannot reference parent directories (`..`). The build process copies `web/dist/` to `internal/frontend/web/dist/` so the embed directive in `internal/frontend/web/server.go` can reference it:

```go
//go:embed dist
var webDist embed.FS
```

## .gitignore Configuration

### Root `.gitignore`
```
dist/
!web/dist/                        # Allow web/dist to exist (but contents gitignored)
internal/frontend/web/dist/       # Exclude the embed copy
```

### `web/.gitignore`
```
node_modules
dist
```

Both build artifacts (`web/dist/` and `internal/frontend/web/dist/`) are excluded from git, but the directories can exist for the build process.

## Full Build from Clean State

```bash
# Clone the repo
git clone https://github.com/zanetworker/aimux.git
cd aimux

# Build everything
make build-all

# Result: Single binary with embedded dashboard
./aimux --version
```

## Development Workflow

### Frontend-only development
```bash
make web-dev
# Frontend runs on http://localhost:5173 with hot reload
```

### Backend + frontend integration testing
```bash
make web-build  # Rebuild frontend
make build      # Rebuild Go binary with new assets
./aimux         # Run the integrated binary
```

## Binary Size

The embedded web assets add ~600KB (gzipped) to the binary. Final binary size is approximately 77MB on arm64 macOS.
