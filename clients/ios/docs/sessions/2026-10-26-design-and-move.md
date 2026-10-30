# Session 2026-10-26: redesign, clients/ layout, move into aimux

## Asked

- Redesign the home screen after 0.1 failed on real data (flat newest-first list of about 330 sessions).
- Design the chat, approvals, attachments and new-session screens; document the design.
- Decide where the app lives; also place the phone web client in the same layout and give it the same design.
- Do step 0 (move), then step 1 (Now and All) and the rest.

## Built or changed

- Design doc `docs/superpowers/specs/2026-10-26-aimux-ios-design.md` with mockups in `docs/superpowers/specs/images/`, reviewed by a builder-role reviewer and a humanizer scan.
- Plan `docs/superpowers/plans/2026-10-26-clients-move-and-now.md`: 10 tasks, step 0 (tasks 1-2) and step 1 (tasks 3-10).
- Step 0 done in the aimux worktree `aimux-worktrees/clients`, branch `feat/clients` from `origin/main` 498c0d9:
  - `16d1e35` Move the iPhone app into clients/ios: app, `.github/workflows/clients-ios.yml` (runs on `clients/ios/**` or `api/**`), swiftlint pre-commit hook for `clients/ios` Swift files, `.gitignore` entries, design doc and plan.
  - `3c5d623` Move the phone web client to clients/web: `internal/remoteclient` becomes `clients/web` (package `webclient`, git renames), `cmd/aimux/cmd/remote.go` import, code map in `.claude/CLAUDE.md`.
- Replaced the owner's tailnet hostname in `AimuxKitTests/APIClientTests.swift` and `docs/brief.md` with `my-mac.tail1234.ts.net` before committing.
- Verified: 57 logic tests and 2 UI tests on the SE 3 simulator, `swiftlint --strict`, `go build ./...`, `go test ./...` (52 packages), all pre-commit hooks. Not verified: the iOS workflow on GitHub's macOS runner (Xcode 27).

## Decisions

- Option 1: native SwiftUI inside aimux (`clients/ios`), recommended by Claude and Codex, confirmed by the owner. Move first, before build step 1.
- All clients under `clients/`; the OpenAPI file is the one contract; CI and pre-push review filter by path.
- The web client follows the same design as the app (same screens, ranking, status dots), plain JS/CSS.
- Ranking rules live once as shared JSON fixtures in `api/testdata/clients/`, run by both the Swift and the web tests; the server reports activity state, clients rank.
- Home screen: Now and All tabs plus a search button, rank by what you can act on, near-monochrome, colour only in status dots.
- `feat/clients` keeps tracking `origin/main` so `git pull` brings in main; use `git pull --rebase` only before the first push, `--no-rebase` after.

## Open

- Step 1, tasks 3-10 of the plan.
- Push `feat/clients` with `git ship` and open a PR when the owner says so.
- Optional: make `git ship` explain a non-fast-forward rejection on a rebased branch instead of suggesting a retry.
