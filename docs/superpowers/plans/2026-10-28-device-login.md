# Device Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only devices the owner paired reach the remote API; they log in with Face ID (iPhone Secure Enclave key, web passkey), a login ends after 1 hour without use or 12 hours, devices can be listed and revoked, and every changing action is audited.

**Architecture:** A new Go package `internal/remoteapi/devicelogin` holds the device store (file-locked JSON), pairing codes, signature checks (Secure Enclave and WebAuthn, standard library only), the session registry and the audit log, behind one `http.Handler` middleware placed after `RequireTailnetUser`. The CLI gains `aimux remote pair|devices|revoke`. Both clients add a pairing screen, an unlock step and the session credential (Bearer on iPhone, `__Host-` cookie on web).

**Tech Stack:** Go standard library crypto; plain ES modules + WebAuthn; SwiftUI + Security/LocalAuthentication.

**Spec:** `docs/superpowers/specs/2026-10-28-device-login-design.md` (read it whole, including "Builder check: answers" and "Web credential"; those override the body where they differ).

## Global Constraints

- Branch `feat/clients`, worktree `aimux-worktrees/clients`. Commit with `&&` after tests pass; never push; never `git stash`. Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `.claude/` is git-ignored: `git add -f .claude/CLAUDE.md`.
- Every implementer and reviewer follows the `development-tools:crafted-code` principles: test first (see it fail), errors with context, no secrets in errors, simple and minimal, docs match reality. Security-sensitive: invoke the skill before writing code.
- No new Go module, no npm dependency, no iOS package. Go crypto from `crypto/*` and `encoding/*` only.
- Never log, audit, return in errors, or put in a URL: pairing codes, challenges, tokens, private keys. Public keys and device ids may appear in the device file only.
- Files: `~/.aimux/remote-paired-devices.json`, `~/.aimux/remote-pairing.json`, `~/.aimux/remote-paired-seen.json`, `~/.aimux/remote-paired-devices.lock`, `~/.aimux/logs/remote-audit.log`, all mode 0600 (directories 0700). Writes: exclusive `flock` on the lock file, re-read, change, write temp file, rename.
- Values: pairing code 8 Crockford base32 characters shown `XXXX-XXXX`, valid 5 minutes, single use, burned after 5 wrong attempts; challenge 32 random bytes base64url, valid 60 s, single use, at most 5 per device (oldest dropped); session token 32 random bytes, stored only as SHA-256; idle limit 1 hour, absolute 12 hours; device id 16 random bytes base64url; device name 1-32 characters, unique.
- Signed strings (iPhone, UTF-8, `\n` separators, host name without port): pairing `aimux-pair-v1\n<host>\n<code without dash>`, login `aimux-login-v1\n<host>\n<challenge>`. ECDSA P-256 SHA-256, ASN.1 DER signature, SPKI DER public key, base64 (standard) on the wire.
- WebAuthn: `rp.id` = host name without port; origin `https://` + Host as forwarded (with port); rpIdHash = SHA-256(rp.id); flags UP and UV required; alg -7 only; attestation `none`; sign counter ignored; discoverable credential (`residentKey: "required"`, `user.id` = device id bytes).
- What counts as use: any `POST`, and any request with header `Aimux-Interaction: 1`. Event streams never count, and close within 15 s when their session ends (at once on logout or revoke).
- Open without a session: `GET /v1/health`, `GET /v1/openapi.yaml`, `GET /app/*`, `POST /v1/auth/pair`, `POST /v1/auth/challenge`, `POST /v1/auth/login`. Everything else: `401 {"code":"login_required"}` with a fixed message.
- A device's sessions are valid only with the same `Tailscale-User-Login` that paired it.
- Config `remote_access.device_login` default `true`; `false` keeps today's behaviour and prints a warning at start. Health capability `device_login` when on.
- Web: CSP unchanged, no `innerHTML`, Face ID only after a tap ("Unlock with Face ID"). iOS: logic in AimuxKit (no SwiftUI), views in `App/`, Secure Enclave key `.privateKeyUsage` + `.biometryCurrentSet`, tests with a software key behind the same protocol.
- Test data invented; never the owner's tailnet host (use `my-mac.tail1234.ts.net`).
- Docs in the last task.

## Review Focus

- **Replay and cross-use:** a challenge used twice, a pairing signature replayed as a login, a WebAuthn assertion from another origin or rp id, a `webauthn.create` client data sent to login: all refused. Task 2 tests.
- **Concurrent CLI and server writes:** revoke while the server records a pairing: the revoked device stays gone, the new one stays. Task 1 test with two goroutines and the lock.
- **Session end while streaming:** logout or revoke closes an open event stream within 15 s; idle expiry does not happen while the person is active but does when only polls run. Task 3 tests with an injected clock.
- **Lost browser storage:** a web login without `deviceId` (discoverable credential) works. Task 2 and Task 4 tests.
- **Upgrade without pairing:** an existing client against a server with `device_login: true` gets `login_required` and shows pairing, never a crash or a blank screen. Tasks 4 and 5.

## Task 1: Device store, pairing codes and the CLI

**Files:** Create `internal/remoteapi/devicelogin/store.go`, `codes.go`, tests; modify `cmd/aimux/cmd/remote.go` (new `pair`, `devices`, `revoke` subcommands), `cmd/aimux/cmd/remote_test.go`, `internal/config/config.go` (`DeviceLogin *bool` with default true, accessor `DeviceLoginOn() bool`).

**Interfaces:** Produces `type Device struct{ID, Name, Kind, PublicKey, CredentialID, Login string; Created time.Time}`; `type Store struct` with `Open(dir string) *Store`, `Devices() ([]Device, error)`, `Add(Device) error` (name unique), `Remove(name string) (Device, error)`, `SetPending(name string, codeHash [32]byte, expires time.Time) error`, `TakePending(codeHash [32]byte, now time.Time) (name string, ok bool, err error)` (single use), `Seen(id string, t time.Time)`, `LastSeen() map[string]time.Time`, `Changed() bool` (mtime check, at most once a second); `NewCode() (display string, hash [32]byte)`, `ParseCode(s string) ([32]byte, error)` (accepts with or without dash, case-insensitive, Crockford aliases I/L→1, O→0).

- [ ] Tests first: file modes 0600/0700; Add rejects a duplicate name and a name of 0 or 33 characters; TakePending succeeds once, fails after expiry and on a second call; concurrent Remove from one Store and Add from another (two goroutines, same dir) keeps both effects; Changed reports a change made by another Store; ParseCode round-trips NewCode and rejects 7 or 9 characters; CLI: `pair --name x` prints `XXXX-XXXX` and writes a pending hash (not the code); `devices` lists name, kind, created, last seen; `revoke x` removes it and an unknown name exits non-zero with a clear message.
- [ ] Run, see them fail; implement; `go build ./... && go vet ./... && go test ./... -timeout 60s`; commit `feat(remote): device store, pairing codes and the pair, devices and revoke commands`.

## Task 2: Signature checks

**Files:** Create `internal/remoteapi/devicelogin/verify.go`, `verify_test.go`.

**Interfaces:** Produces `VerifyEnclave(publicKeyDER []byte, message []byte, sigDER []byte) error`; `PairMessage(host, code string) []byte`; `LoginMessage(host, challenge string) []byte`; `ParseP256(spkiDER []byte) (*ecdsa.PublicKey, error)` (rejects other curves); `VerifyWebAuthnCreate(origin, rpID string, wantChallenge []byte, clientDataJSON, authenticatorData []byte) error`; `VerifyWebAuthnGet(origin, rpID string, wantChallenge []byte, pub *ecdsa.PublicKey, clientDataJSON, authenticatorData, sigDER []byte) error`.

- [ ] Tests first, with keys generated in the test: good enclave signature passes; wrong message, wrong key, a P-384 key, garbage DER fail; a pairing signature does not verify as a login message; WebAuthn get passes for a well-formed assertion built in the test; fails for type `webauthn.create`, another origin, another port, a wrong challenge, rpIdHash of another host, UP or UV cleared, authenticatorData shorter than 37 bytes, a signature over the data without the clientData hash; WebAuthn create passes and fails on the same axes (type `webauthn.create`).
- [ ] Implement with the standard library only; run; commit `feat(remote): device signature checks for Secure Enclave keys and passkeys`.

## Task 3: Sessions, endpoints, middleware and audit

**Files:** Create `internal/remoteapi/devicelogin/sessions.go`, `handler.go`, `audit.go`, tests; modify `internal/remoteapi/api.go` (routes, health capability, wrap the mux), `internal/remoteapi/api.go` events handler (watch the session), `cmd/aimux/cmd/remote.go` (wire the store and config, warning when off), `api/remote-v1.openapi.yaml` (`/v1/auth/pair`, `/v1/auth/challenge`, `/v1/auth/login`, `/v1/auth/logout`, `login_required`, the capability, the `Aimux-Interaction` header), contract tests.

**Interfaces:** Consumes Tasks 1 and 2. Produces `Sessions` with an injected clock (`New(now func() time.Time)`), `Create(deviceID, login string) (token string)`, `Check(token, login string, interaction bool) (Session, bool)`, `End(token)`, `EndDevice(deviceID)`, `Done(token) <-chan struct{}` for streams; `Middleware(next http.Handler) http.Handler`; `SessionFrom(ctx)`; `Audit(entry)`.

- [ ] Tests first (handler tests through `httptest`, clock injected): pair with a good code returns a session (iPhone: token in body; web: `Set-Cookie: __Host-aimux_session=...; Path=/; Secure; HttpOnly; SameSite=Strict`, no token in body); pairing without the proof signature, with a burned code, after 5 wrong codes, or with a duplicate name fails; challenge then login works for both kinds; a challenge works once and not after 60 s; the 6th challenge drops the oldest; login without `deviceId` finds a web device by credential id and userHandle; every protected route returns `401 login_required` without a session, with an expired one (idle 1 h with only GETs without the header; absolute 12 h with interactions), with another Tailscale login, and after revoke (the file changed by another Store); a GET with `Aimux-Interaction: 1` and any POST move the idle clock; an open events stream closes after logout and after revoke; open routes stay open; health lists `device_login`; with `device_login: false` everything behaves as before; audit lines for pair, pair_failed, login, login_failed, logout, revoke, send, new_session, cancel, approve, terminal_message, terminal_key carry no code, challenge, token or message text (assert by scanning the log for those values).
- [ ] Implement; run `go build ./... && go vet ./... && go test ./... -timeout 60s`; commit `feat(remote): device login sessions, endpoints and audit`.

## Task 4: Web client

**Files:** Create `clients/web/webapp/login.mjs` (pure helpers: base64url, building `create`/`get` options from server values, the state machine for unlock and retry) and `login.test.mjs`; modify `app.js` (pairing screen, "Unlock with Face ID", retry of failed requests, `Aimux-Interaction: 1` on user actions, EventSource error check via one `fetch`), `index.html`, `app.css`.

- [ ] Tests first for `login.mjs`: option builders give the exact values in Global Constraints (rp id without port, alg -7, `userVerification: "required"`, `residentKey: "required"`, attestation `none`); the state machine: a `401 login_required` moves to `locked` and queues the request, a tap triggers exactly one `credentials.get`, success replays queued requests once, a second `401` after login moves to `unpaired`; no prompt without a tap.
- [ ] Implement; full web suite; Playwright with a virtual authenticator (Chromium's WebAuthn virtual authenticator) against a test server or a stub: pair, lock after a forced expiry, unlock, retry; screenshots light and dark at 390 px to the session scratchpad `device-login/`; look at them.
- [ ] Commit `feat(web): pair with a passkey and unlock with Face ID`.

## Task 5: iPhone app

**Files:** Create `clients/ios/AimuxKit/Sources/DeviceKey.swift` (protocol `DeviceKey { publicKeyDER; sign(_ message: Data) async throws -> Data }`, `EnclaveKey`, `SoftwareKey` for tests), `DeviceLogin.swift` (pairing and login calls, signed strings, session token in memory, 401 handling and single retry), `clients/ios/App/PairingView.swift`, `UnlockView.swift`; modify `APIClient.swift` and `EventStream.swift` (Bearer header, `Aimux-Interaction` on user actions), `RootTabs.swift` (show pairing or unlock), tests, `project.yml` (`NSFaceIDUsageDescription`, build number +1).

- [ ] Tests first (Swift Testing with `SoftwareKey` and a fake transport): the signed strings match Global Constraints byte for byte; pairing sends SPKI DER and a valid signature (verified in the test with CryptoKit or Security); a `401 login_required` triggers one login and one retry; a second `401` reports unpaired; the token is never written to UserDefaults or disk; EventStream sends the Bearer header and ends on 401.
- [ ] Implement; kit tests, UI tests (pairing screen with a stub server path or demo mode), `swiftlint --strict`; screenshots of pairing and unlock, light and dark, to the scratchpad `device-login/`; look at them.
- [ ] Commit `feat(ios): pair with a Secure Enclave key and unlock with Face ID`.

## Task 6: Docs and live check

**Files:** `docs-site/src/content/docs/guides/remote-access.mdx` (pairing, unlock, revoke, audit log, `device_login`, the new endpoints, the guarantee and its limit), `RELEASE_NOTES.md` (upgrade note: run `aimux remote pair`), `clients/ios/README.md`, `clients/ios/AGENTS.md`, `clients/ios/docs/project-context.md`, `.claude/CLAUDE.md` (devicelogin package, CLI commands, files).

- [ ] Update; `cd docs-site && npm run build`.
- [ ] Live check on the Mac with a scratch config dir (never the owner's `~/.aimux`): start a server on another port with `device_login: true`, `pair`, log in with a software key script, confirm 401 before and 200 after, revoke and confirm 401, read the audit log for absence of secrets. Record commands and output in the report.
- [ ] Commit `docs: device login`.
