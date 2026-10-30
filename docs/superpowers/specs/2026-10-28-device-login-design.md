# Device login with Face ID for the remote API

Both phone clients (iPhone app, web client) and `aimux remote serve`.

## Intent

Today anything signed in to the owner's tailnet as the owner can drive their agents: the phone, a work laptop, any browser on them, and a login never ends. The owner wants only devices they paired to get in, Face ID to open the door, a login that ends after an hour without use, and a way to cut off a lost phone. aimux is open source, so this must be safe by default for anyone who runs it.

## Goals

- Pair a device once from the Mac; after that the device logs in with Face ID.
- A login ends after 1 hour without use (owner: "Face ID is cheap"), and in any case after 12 hours.
- List paired devices and revoke one; the next request from it fails.
- One audit line for each action that changes something, never the message text.
- Tailscale stays the outer wall: every existing check in `RequireTailnetUser` still runs first.

## Non-goals

- Accounts, passwords, OIDC, invites, sharing between people (single owner).
- Fresh Face ID per approval (the hourly login is the bar for now).
- A QR code in the terminal (a short typed code is enough for a one-time pairing; QR can come later).
- Native iOS passkeys (they need Apple's associated-domains file, which Apple fetches from its public CDN; a private `ts.net` host only works in `?mode=developer`, development builds only).

## Anti-goals

- Any secret in a URL, log or audit line (the remote-claude `?key=` mistake).
- A new Go module dependency or iOS package.
- A login wall that also blocks the Mac's own terminal use of aimux (only the remote API is affected).

## Constraints

- Go standard library crypto only (`crypto/ecdsa`, `crypto/elliptic`, `crypto/sha256`, `crypto/x509`, `crypto/subtle`, `crypto/rand`).
- Web: no framework, no npm deps, CSP unchanged; `EventSource` cannot send headers, so the web client authenticates with a cookie.
- iOS: Security framework and LocalAuthentication only; deployment target iOS 27.
- The existing `POST /v1/devices` and `DELETE /v1/devices/{token}` are push registrations; pairing must not reuse those names.
- Files under `~/.aimux` that hold device data are mode 0600.

## Approaches

1. **Per-device key signs a server challenge; short session token (chosen).** iPhone: a P-256 key made in the Secure Enclave that only signs after Face ID. Web: a WebAuthn passkey (platform authenticator, Face ID in Safari) verified with the standard library using the browser's `getPublicKey()` and `getAuthenticatorData()` (Baseline 2023), so no CBOR parsing. The server issues a random session token after a good signature. No secret ever leaves the device; revoking deletes a public key.
2. **Long-lived bearer token per device in the Keychain, released by Face ID.** Simpler server (compare a hash), but the token is a copyable secret: a backup or a compromised app process could replay it from elsewhere on the tailnet until revoked. Rejected.
3. **Accounts with a password and TOTP.** Familiar, but a password to manage on a single-owner tool and no Face ID on the web without passkeys anyway. Rejected.

## Decided

### Pairing

- `aimux remote pair --name <name>` (name required, 1-32 characters, unique among paired devices) prints a code of 8 characters from the 32-symbol Crockford alphabet, shown as `XXXX-XXXX`, valid 5 minutes, single use. It stores only the code's SHA-256, the name and the expiry in `~/.aimux/remote-pairing.json` (0600), replacing any earlier pending code. The running server reads that file when a pairing request arrives (no IPC; the CLI and the server are separate processes).
- Five wrong codes in a row burn the pending code (the CLI must be run again).
- `POST /v1/auth/pair {code, kind, publicKey, credentialId?, clientDataJSON?, authenticatorData?}`:
  - `kind: "secure-enclave"` (iPhone): `publicKey` is the base64 SPKI DER of a P-256 key.
  - `kind: "webauthn"` (web): `publicKey` from `getPublicKey()` (SPKI DER, must be P-256, alg -7), `credentialId`, `userHandle` = the device id, `clientDataJSON` (type `webauthn.create`, origin `https://<Host>`, challenge = base64url of the SHA-256 of the code), `authenticatorData` (rpIdHash = SHA-256 of the Host name, UP and UV flags set). Attestation is `none`.
  - On success the device is added to `~/.aimux/remote-paired-devices.json` (0600): `{id, name, kind, publicKey, credentialId?, created, lastSeen}`; the answer is a session (as login below), so pairing also logs in.
- The device id is 16 random bytes, base64url.

### Login

- `POST /v1/auth/challenge {deviceId}` → `{challenge}`: 32 random bytes, base64url, valid 60 s, single use, at most 5 outstanding per device (oldest dropped).
- `POST /v1/auth/login {deviceId, challenge, signature, clientDataJSON?, authenticatorData?}`:
  - secure-enclave: ECDSA P-256 SHA-256 (ASN.1 DER signature) over the bytes `aimux-login-v1\n<Host>\n<challenge>`.
  - webauthn: clientDataJSON type `webauthn.get`, challenge matches, origin `https://<Host>`; authenticatorData rpIdHash = SHA-256 of Host, UP and UV set; signature over `authenticatorData || SHA-256(clientDataJSON)`. The sign counter is ignored (synced passkeys report 0).
  - Host is the request's normalized Host, which `RequireTailnetUser` already restricted to the trusted tailnet names.
- A good login creates a session: 32 random bytes, kept in server memory only as its SHA-256 with `{deviceId, created, lastUsed}`. Idle limit 1 hour, absolute limit 12 hours. A server restart ends every session (Face ID again).
- iPhone sends `Authorization: Bearer <token>`. Web receives `Set-Cookie: __Host-aimux_session=<token>; Path=/; Secure; HttpOnly; SameSite=Strict` (no Max-Age, so it ends with the browser; the server enforces the limits). The login answer body for iPhone carries the token; for web it carries no token.
- `POST /v1/auth/logout` ends the current session.
- Failed logins are audited, not locked out (see builder check 6).

### Every request

- Order: `RequireTailnetUser` (unchanged) → device session check → handler.
- Open without a session: `GET /v1/health`, `GET /v1/openapi.yaml`, `GET /app/*` (the web client must load to log in), `POST /v1/auth/pair`, `POST /v1/auth/challenge`, `POST /v1/auth/login`.
- Everything else needs a live session whose device is still in `remote-paired-devices.json`; else `401` with code `login_required` (fixed message). Each good request moves `lastUsed`; `lastSeen` on the device is written at most once a minute.
- Health gains the capability `device_login` and, inside it, nothing that identifies devices.
- The server notices revocations without a restart: it re-reads `remote-paired-devices.json` when its modification time changes (checked at most once a second).

### CLI

- `aimux remote devices` lists name, kind, created, last seen.
- `aimux remote revoke <name>` removes the device; its sessions fail on their next request.
- Config `remote_access.device_login: true` by default. `false` keeps today's behaviour (tailnet only) and the server prints a warning at start. Release notes tell existing users to run `aimux remote pair`.

### Audit

- `~/.aimux/logs/remote-audit.log`, 0600, one JSON line: `{time, login, device, action, session?, result}`. Actions: `pair`, `pair_failed`, `login`, `login_failed`, `logout`, `revoke`, `send`, `new_session`, `cancel`, `approve`, `terminal_message`, `terminal_key`. Never message text, codes, challenges, tokens or keys.

### Clients

- First run with `device_login` in health and no paired key: a pairing screen asks only for the code (the name was given on the Mac), then makes the key (Face ID) and pairs.
- Any `401 login_required`: Face ID, login, retry the request once. A second `401` shows the pairing screen ("This device is no longer paired").
- iPhone: the Secure Enclave key with access control `.privateKeyUsage` and `.biometryCurrentSet`; a change of enrolled faces invalidates it and the app falls back to pairing again. The token lives in memory only. Tests use a software key behind the same protocol (the simulator has no Secure Enclave).
- Web: `navigator.credentials.create` with `rp.id` = the Host, `authenticatorSelection: {authenticatorAttachment: "platform", residentKey: "required", userVerification: "required"}`, `pubKeyCredParams: [{type: "public-key", alg: -7}]`, `attestation: "none"`; login with `navigator.credentials.get` and `allowCredentials` of the stored credential id (kept in `localStorage`, it is not a secret).

## Left to the builder

- File layout inside `internal/remoteapi` (a `devicelogin` sub-package is welcome), error codes beyond `login_required`, exact screen copy, and how the CLI finds `~/.aimux` (reuse existing helpers).

## Builder check: answers (my calls unless marked)

1. **Web passkeys sync through iCloud Keychain.** Owner decision, see "Web credential" below.
2. **Two processes, one file.** Every write to `remote-paired-devices.json` or `remote-pairing.json`, by the CLI or the server, takes an exclusive `flock` on `~/.aimux/remote-paired-devices.lock`, re-reads the file, changes it and renames a temp file over it. `lastSeen` is not written to the devices file: the server keeps it in memory and in its own `remote-paired-seen.json`; `aimux remote devices` reads both. Wrong pairing attempts are counted in the pending record under the lock, so a server restart does not reset them.
3. **Open event streams end with their session.** Sessions live in a registry that handlers can watch; a stream closes when its session is logged out, revoked, idle past the limit or past 12 hours (checked at least every 15 s and at once on revoke or logout). An open stream does not count as use.
4. **What counts as use for the idle hour.** Any `POST`, and any request with the header `Aimux-Interaction: 1`, which clients set only on something the person did (opening a chat or a tab, pull to refresh, Retry). Background polls and streams do not count, so a phone left open logs out after an hour.
5. **Host and port.** The web origin check uses `https://` plus the Host as forwarded (with its port, as `fromOwnPage` does); `rp.id` and the rpIdHash use the host name without port; the iPhone's signed string uses the host name without port.
6. **Device ids are not secret, so nothing keys a lockout on them.** There is no login lockout: signatures cannot be guessed, so failed logins are only audited. Outstanding challenges per device are capped at 5 with the oldest dropped, never refused. Pairing keeps its 5-attempt burn (codes can be guessed); a burned code is fixed by running the CLI again.
7. **The web client never prompts Face ID by itself.** On `401 login_required` it shows "Unlock with Face ID"; the tap starts `navigator.credentials.get`. Requests that failed are retried after the unlock. `EventSource` errors are followed by one `fetch` of health-with-session to tell a lost login from a lost network.
8. **Lost browser storage.** Web passkeys are discoverable (`residentKey: "required"`, `user.id` = the device id). Login may omit `deviceId`: `/v1/auth/challenge {}` gives a challenge, and `/v1/auth/login` finds the device by the credential id and the returned `userHandle`. A Home Screen web app can log in with the passkey made in a Safari tab.
9. **Proof of the key at pairing.** iPhone pairing must sign `aimux-pair-v1\n<host>\n<code>` with the new key, so the server knows the device holds it. The server cannot prove the key is in the Secure Enclave or gated by Face ID (that would need App Attest and Apple's servers); this is accepted and documented: the guarantee is "a key that never left a device the owner paired".
10. **Devices belong to a Tailscale login.** A device records the login that paired it; its sessions are valid only on requests with that same `Tailscale-User-Login`. The audit line carries both.

## Web credential (owner decision)

The owner accepts a synced web passkey. Where it syncs is decided by the passkey provider the device uses, not by aimux: iCloud Keychain (to the owner's Apple devices) by default on Apple devices, another provider such as a password manager when the owner has chosen one, or nowhere when the provider does not sync. Every use still needs Face ID or Touch ID (`userVerification: "required"`). The device list shows one entry per passkey; revoking it cuts off every device that holds it. The iPhone app's Secure Enclave key never leaves the phone.
