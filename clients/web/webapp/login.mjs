// Device login for the web client: pair this browser with a passkey once,
// then unlock with Face ID. The server keeps the session in an HttpOnly
// cookie, so this page never sees a token. Face ID is asked for only from a
// tap (unlock, pair), never on load or from a poll.
//
// States: 'unknown' (health not read yet), 'unpaired' (pair with a code),
// 'locked' (paired here: unlock), 'live'. A request answered
// 401 login_required is held until the unlock and then sent again, once, in
// order; a held request answered 401 again right after the unlock means the
// device was revoked.

const STORAGE_KEY = 'aimux.device'; // {deviceId, credentialId, name}; none of it is secret
const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

export const PAIR_CODE_FAILED = "That code didn't work. Run aimux remote pair on your Mac for a new one.";
const PAIR_BAD_FORMAT = 'Enter the 8-character code that aimux remote pair shows on your Mac.';
const PAIR_CONFLICT = 'A device with that name is already paired. Revoke it on your Mac, or pair under another name.';
const PAIR_FAILED = 'Pairing failed. Run aimux remote pair on your Mac for a new code.';
const PAIR_CANCELLED = 'Face ID was cancelled. Tap Pair to try again.';
const NO_PASSKEY = 'This browser could not make a passkey.';
const UNLOCK_CANCELLED = 'Unlock was cancelled. Tap to try again.';
const UNLOCK_FAILED = 'Could not unlock. Try again.';
const OFFLINE = 'Could not reach your Mac. Check the connection and try again.';

// Bytes (Uint8Array or ArrayBuffer) as standard base64, with padding.
export function b64(data) {
  let s = '';
  for (const b of new Uint8Array(data)) s += String.fromCharCode(b);
  return btoa(s);
}

// Bytes as base64url without padding.
export function b64url(data) {
  return b64(data).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function fromB64url(s) {
  const std = s.replace(/-/g, '+').replace(/_/g, '/');
  const bin = atob(std + '='.repeat((4 - (std.length % 4)) % 4));
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

// canonicalCode reads a typed pairing code as the server does: 8 Crockford
// characters, any case, with or without the dash after four, spaces
// trimmed, I and L as 1, O as 0. Null when it cannot be a code.
export function canonicalCode(typed) {
  let s = String(typed || '').trim().toUpperCase();
  if (s.length === 9 && s[4] === '-') s = s.slice(0, 4) + s.slice(5);
  if (s.length !== 8) return null;
  s = s.replace(/[IL]/g, '1').replace(/O/g, '0');
  return [...s].every((c) => CROCKFORD.includes(c)) ? s : null;
}

// The WebAuthn rp id: the page's host name (location.hostname has no port).
export function rpIdFor(hostname) {
  return hostname.toLowerCase().replace(/\.$/, '');
}

// Options for navigator.credentials.create: a discoverable platform passkey
// behind Face ID, P-256 (alg -7) only, no attestation. user.id is the new
// device id; challenge the SHA-256 of the canonical code.
export function createOptions({ challenge, deviceId, rpId }) {
  return {
    challenge,
    rp: { id: rpId, name: 'aimux' },
    user: { id: deviceId, name: 'aimux on ' + rpId, displayName: 'aimux' },
    pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
    authenticatorSelection: {
      authenticatorAttachment: 'platform', residentKey: 'required', requireResidentKey: true, userVerification: 'required',
    },
    attestation: 'none',
    timeout: 60000, // a challenge lives 60 s
  };
}

// Options for navigator.credentials.get: the server's challenge bytes, Face
// ID required, the passkey stored here if any (else any passkey for this
// host: the browser lost its storage).
export function getOptions({ challenge, rpId, credentialId }) {
  return {
    challenge: fromB64url(challenge),
    rpId,
    userVerification: 'required',
    allowCredentials: credentialId ? [{ type: 'public-key', id: fromB64url(credentialId) }] : [],
    timeout: 60000, // a challenge lives 60 s
  };
}

const isLoginRequired = (e) => e && e.status === 401 && e.code === 'login_required';
const isCancelled = (e) => e && e.name === 'NotAllowedError';

// createLogin returns the login state machine. Dependencies are injected so
// it runs under node --test: fetch, navigator.credentials, a Storage,
// location.hostname, random(n) bytes, a SHA-256 digest, and the page's
// PublicKeyCredential (for signalUnknownCredential where the browser has it).
export function createLogin({
  fetch, credentials, storage, hostname,
  random = (n) => crypto.getRandomValues(new Uint8Array(n)),
  digest = (data) => crypto.subtle.digest('SHA-256', data),
  publicKeyCredential,
}) {
  const rpId = rpIdFor(hostname);
  const listeners = new Set();
  let held = [];            // requests waiting for an unlock: {path, opts, resolve, reject}
  let justUnlocked = false; // replaying after an unlock, until one replayed request goes through
  let generation = 0;       // counts unlocks and pairings, so a late 401 from an older login is told apart

  const login = {
    state: 'unknown',
    error: '',      // why the last pair or unlock failed, for the person
    busy: false,    // a pair or unlock is running
    revoked: false, // unpaired because the server no longer knows this device
    deviceLogin: false, // the server asks for device login (health says so)
    subscribe(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    start, check, request, unlock, pair, lock,
  };

  function notify() { for (const fn of listeners) fn(); }
  function set(changes) { Object.assign(login, changes); notify(); }

  function stored() {
    try {
      const d = JSON.parse(storage.getItem(STORAGE_KEY));
      return d && typeof d.credentialId === 'string' ? d : null;
    } catch { return null; }
  }
  function save(d) { try { storage.setItem(STORAGE_KEY, JSON.stringify(d)); } catch { /* the passkey still works without it */ } }
  function forget() { try { storage.removeItem(STORAGE_KEY); } catch { /* nothing to forget */ } }

  // signedOut: the state for a request without a session.
  const signedOut = () => (stored() ? 'locked' : 'unpaired');

  async function send(path, opts) {
    const r = await fetch(path, opts);
    let data = {};
    try { data = await r.json(); } catch { /* empty or not JSON */ }
    if (!r.ok) {
      const err = new Error(data.error || ('HTTP ' + r.status));
      err.code = data.code || '';
      err.status = r.status;
      throw err;
    }
    return data;
  }

  function options(method, body, interaction) {
    const opts = { method, headers: {} };
    if (interaction) opts.headers['Aimux-Interaction'] = '1';
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return opts;
  }

  const post = (path, body) => send(path, options('POST', body));

  // forgetPasskey tells the browser a passkey the server will never accept
  // (refused pairing, revoked device), so its password manager can drop it.
  // Best effort: older browsers lack the call, and a refusal changes nothing
  // for the login, so errors are ignored.
  function forgetPasskey(credentialId) {
    try {
      const signal = publicKeyCredential && publicKeyCredential.signalUnknownCredential;
      if (typeof signal === 'function') Promise.resolve(signal.call(publicKeyCredential, { rpId, credentialId })).catch(() => {});
    } catch { /* see above */ }
  }

  // request is the page's /v1 call. Without a session it waits for the
  // unlock (or pairing) and then goes out again.
  async function request(method, path, body, { interaction = false } = {}) {
    const opts = options(method, body, interaction);
    if (login.state === 'locked' || login.state === 'unpaired') return hold(path, opts);
    const sentUnder = generation;
    try {
      return await send(path, opts);
    } catch (e) {
      if (!isLoginRequired(e)) throw e;
      // Refused under an older login that a newer unlock replaced: try the new one.
      if (sentUnder !== generation) return request(method, path, body, { interaction });
      if (login.state !== 'locked' && login.state !== 'unpaired') set({ state: signedOut(), error: '' });
      return hold(path, opts);
    }
  }

  function hold(path, opts) {
    return new Promise((resolve, reject) => held.push({ path, opts, resolve, reject }));
  }

  // revoke: the server refused right after an unlock or knows no such
  // device: pair again. Waiting requests fail with the server's error.
  function revoke(e) {
    justUnlocked = false;
    forget();
    const waiting = held;
    held = [];
    for (const h of waiting) h.reject(e);
    set({ state: 'unpaired', revoked: true });
  }

  // replay sends the held requests again, once each, in order.
  async function replay() {
    const waiting = held;
    held = [];
    for (const h of waiting) {
      if (login.revoked) { h.reject(Object.assign(new Error('This device is no longer paired'), { status: 401, code: 'login_required' })); continue; }
      if (login.state !== 'live') { held.push(h); continue; }
      try {
        h.resolve(await send(h.path, h.opts));
        justUnlocked = false;
      } catch (e) {
        if (!isLoginRequired(e)) h.reject(e);
        else if (justUnlocked) { h.reject(e); revoke(e); } // refused again at once: revoked
        else { held.push(h); set({ state: signedOut() }); } // the login ended meanwhile
      }
    }
    justUnlocked = false;
  }

  function loggedIn(ans, credentialId) {
    save({ deviceId: ans.deviceId, credentialId, name: ans.name });
    justUnlocked = true;
    generation++;
    set({ state: 'live', error: '', revoked: false });
    return replay();
  }

  // health reads whether this browser has a live session; null offline.
  async function health() {
    let h;
    try { h = await send('/v1/health', options('GET')); } catch { return null; }
    login.deviceLogin = (h.capabilities || []).includes('device_login');
    return h;
  }

  async function start() {
    const h = await health();
    if (!h) return login.state;
    if (!login.deviceLogin || h.session === 'live') set({ state: 'live' });
    else set({ state: signedOut() });
    return login.state;
  }

  // check, after an event stream failed: 'none' (the login ended: locks),
  // 'live', or 'offline' (the network: nothing changes).
  async function check() {
    const h = await health();
    if (!h) return 'offline';
    if (!login.deviceLogin || h.session === 'live') return 'live';
    if (login.state === 'live' || login.state === 'unknown') set({ state: signedOut() });
    return 'none';
  }

  // unlock runs on a tap only: a challenge, Face ID, the login.
  async function unlock() {
    if (login.busy) return;
    let cred;
    set({ busy: true, error: '' });
    try {
      const { challenge } = await post('/v1/auth/challenge', {});
      const known = stored();
      try {
        cred = await credentials.get({ publicKey: getOptions({ challenge, rpId, credentialId: known ? known.credentialId : '' }) });
      } catch (e) {
        set({ error: isCancelled(e) ? UNLOCK_CANCELLED : UNLOCK_FAILED });
        return;
      }
      const r = cred.response;
      const ans = await post('/v1/auth/login', {
        challenge,
        signature: b64(r.signature),
        credentialId: b64url(cred.rawId),
        // Without a returned handle, the stored device id is the handle.
        userHandle: r.userHandle && r.userHandle.byteLength ? b64url(r.userHandle) : known ? known.deviceId || '' : '',
        clientDataJSON: b64(r.clientDataJSON),
        authenticatorData: b64(r.authenticatorData),
      });
      set({ busy: false });
      await loggedIn(ans, b64url(cred.rawId));
    } catch (e) {
      if (e.code === 'unknown_device') { if (cred) forgetPasskey(b64url(cred.rawId)); revoke(e); }
      else set({ error: e.status ? UNLOCK_FAILED : OFFLINE });
    } finally {
      if (login.busy) set({ busy: false });
    }
  }

  // pair makes the passkey first (Face ID), then sends it with the code.
  async function pair(typed) {
    if (login.busy) return;
    const code = canonicalCode(typed);
    if (!code) { set({ error: PAIR_BAD_FORMAT }); return; }
    set({ busy: true, error: '' });
    try {
      let cred, deviceId;
      try {
        const challenge = new Uint8Array(await digest(new TextEncoder().encode(code)));
        deviceId = random(16);
        cred = await credentials.create({ publicKey: createOptions({ challenge, deviceId, rpId }) });
      } catch (e) {
        set({ error: isCancelled(e) ? PAIR_CANCELLED : NO_PASSKEY });
        return;
      }
      const r = cred.response;
      let ans;
      try {
        ans = await post('/v1/auth/pair', {
          code,
          kind: 'webauthn',
          publicKey: b64(r.getPublicKey()),
          credentialId: b64url(cred.rawId),
          userHandle: b64url(deviceId),
          clientDataJSON: b64(r.clientDataJSON),
          authenticatorData: b64(r.getAuthenticatorData()),
        });
      } catch (e) {
        if (e.code === 'pairing_failed' || e.code === 'pairing_conflict') forgetPasskey(b64url(cred.rawId));
        set({ error: e.code === 'pairing_failed' ? PAIR_CODE_FAILED : e.code === 'pairing_conflict' ? PAIR_CONFLICT : e.status ? PAIR_FAILED : OFFLINE });
        return;
      }
      set({ busy: false });
      await loggedIn(ans, b64url(cred.rawId));
    } finally {
      if (login.busy) set({ busy: false });
    }
  }

  // lock ends the session on the server and shows the lock.
  async function lock() {
    try { await post('/v1/auth/logout', {}); } catch { /* locked here either way; the server ends it within the hour */ }
    justUnlocked = false;
    set({ state: signedOut(), error: '' });
  }

  return login;
}
