import { test } from 'node:test';
import assert from 'node:assert/strict';
import { b64, b64url, fromB64url, canonicalCode, rpIdFor, createOptions, getOptions, createLogin, PAIR_CODE_FAILED } from './login.mjs';

const bytes = (...v) => new Uint8Array(v);
const buf = (...v) => bytes(...v).buffer;
const sha256 = async (s) => new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s)));

test('base64url has no padding and round-trips; base64 is standard', () => {
  assert.equal(b64url(bytes(0xfb, 0xff)), '-_8');
  assert.equal(b64(bytes(0xfb, 0xff)), '+/8=');
  assert.deepEqual(fromB64url('-_8'), bytes(0xfb, 0xff));
  assert.deepEqual(fromB64url(b64url(bytes())), bytes());
  const all = Uint8Array.from({ length: 256 }, (_, i) => i);
  assert.deepEqual(fromB64url(b64url(all)), all);
  assert.equal(b64(buf(1, 2, 3)), 'AQID'); // ArrayBuffer as well as bytes
});

test('canonicalCode takes the code as typed, the way the server reads it', () => {
  assert.equal(canonicalCode('abcd-efgh'), 'ABCDEFGH');
  assert.equal(canonicalCode('  ABCDEFGH '), 'ABCDEFGH');
  assert.equal(canonicalCode('ilo0-1234'), '11001234'); // I and L read as 1, O as 0
  assert.equal(canonicalCode('ABCD-EFG'), null);
  assert.equal(canonicalCode('ABCD-EFGHJ'), null);
  assert.equal(canonicalCode('ABCDUFGH'), null); // U is not in the alphabet
  assert.equal(canonicalCode('ABC-DEFGH'), null); // the dash only after four
  assert.equal(canonicalCode(''), null);
});

test('rpIdFor is the host name, lowercase, without a trailing dot', () => {
  assert.equal(rpIdFor('My-Mac.tail1234.ts.net.'), 'my-mac.tail1234.ts.net');
  assert.equal(rpIdFor('my-mac.tail1234.ts.net'), 'my-mac.tail1234.ts.net');
});

test('createOptions asks for a discoverable platform passkey with Face ID, P-256 only, no attestation', () => {
  const challenge = bytes(1, 2, 3);
  const deviceId = new Uint8Array(16).fill(7);
  const o = createOptions({ challenge, deviceId, rpId: 'my-mac.tail1234.ts.net' });
  assert.equal(o.challenge, challenge);
  assert.deepEqual(o.rp, { id: 'my-mac.tail1234.ts.net', name: 'aimux' });
  assert.equal(o.user.id, deviceId);
  assert.ok(o.user.name && o.user.displayName);
  assert.deepEqual(o.pubKeyCredParams, [{ type: 'public-key', alg: -7 }]);
  assert.deepEqual(o.authenticatorSelection, {
    authenticatorAttachment: 'platform', residentKey: 'required', requireResidentKey: true, userVerification: 'required',
  });
  assert.equal(o.attestation, 'none');
  assert.equal(o.timeout, 60000); // no longer than a challenge lives
});

test('getOptions signs the challenge bytes with Face ID, naming the stored passkey if there is one', () => {
  const o = getOptions({ challenge: '-_8', rpId: 'my-mac.tail1234.ts.net', credentialId: 'AQID' });
  assert.deepEqual(o.challenge, bytes(0xfb, 0xff));
  assert.equal(o.rpId, 'my-mac.tail1234.ts.net');
  assert.equal(o.userVerification, 'required');
  assert.equal(o.allowCredentials.length, 1);
  assert.equal(o.allowCredentials[0].type, 'public-key');
  assert.deepEqual(o.allowCredentials[0].id, bytes(1, 2, 3));
  // Lost storage: any passkey for this host may answer.
  assert.deepEqual(getOptions({ challenge: '-_8', rpId: 'h', credentialId: '' }).allowCredentials, []);
  assert.equal(o.timeout, 60000);
});

// A fake server: routes['METHOD path'] is a reply {status, body}, a list of
// replies used in turn, or a function; an Error is a network failure.
function fakeServer(routes) {
  const calls = [];
  const fetch = async (path, opts = {}) => {
    const call = { method: opts.method || 'GET', path, headers: opts.headers || {}, body: opts.body ? JSON.parse(opts.body) : undefined };
    calls.push(call);
    let r = routes[call.method + ' ' + path];
    if (Array.isArray(r)) r = r.length > 1 ? r.shift() : r[0];
    if (typeof r === 'function') r = r(call);
    if (!r) r = { status: 404, body: { error: 'no route', code: 'not_supported' } };
    if (r instanceof Error) throw r;
    return { ok: r.status < 300, status: r.status, json: async () => r.body ?? {} };
  };
  return { fetch, calls };
}

const LOGIN_REQUIRED = { status: 401, body: { error: 'log in with a paired device', code: 'login_required' } };
const health = (session, caps = ['sessions', 'device_login']) => ({ status: 200, body: { status: 'ok', capabilities: caps, ...(session ? { session } : {}) } });

function fakeCredentials({ create, get } = {}) {
  const calls = { create: [], get: [] };
  return {
    calls,
    create: async (o) => { calls.create.push(o.publicKey); if (create instanceof Error) throw create; return create || createdCred(); },
    get: async (o) => { calls.get.push(o.publicKey); if (get instanceof Error) throw get; return get || assertion(); },
  };
}

function createdCred() {
  return {
    rawId: buf(9, 9, 9),
    response: { clientDataJSON: buf(1), getPublicKey: () => buf(2), getAuthenticatorData: () => buf(3) },
  };
}

function assertion(userHandle = new Uint8Array(16).fill(5).buffer) {
  return { rawId: buf(9, 9, 9), response: { clientDataJSON: buf(4), authenticatorData: buf(5), signature: buf(6), userHandle } };
}

function memoryStorage(initial = {}) {
  const m = new Map(Object.entries(initial));
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), map: m };
}

const STORED = { 'aimux.device': JSON.stringify({ deviceId: 'BQUFBQUFBQUFBQUFBQUFBQ', credentialId: 'CQkJ', name: 'phone' }) };
const notAllowed = () => Object.assign(new Error('The operation either timed out or was not allowed.'), { name: 'NotAllowedError' });

function rig({ routes, storage = memoryStorage(STORED), credentials = fakeCredentials(), ...more }) {
  const server = fakeServer(routes);
  const login = createLogin({
    fetch: server.fetch, credentials, storage, hostname: 'My-Mac.tail1234.ts.net',
    random: (n) => new Uint8Array(n).fill(5), ...more,
  });
  return { login, server, credentials, storage };
}

const settle = () => new Promise((r) => setTimeout(r, 0));

test('start reads health: no device login is live, a live cookie is live, else locked or unpaired', async () => {
  const off = rig({ routes: { 'GET /v1/health': health(undefined, ['sessions']) } }).login;
  assert.equal(await off.start(), 'live');
  assert.equal(off.deviceLogin, false); // no Lock to offer
  const on = rig({ routes: { 'GET /v1/health': health('live') } }).login;
  assert.equal(await on.start(), 'live');
  assert.equal(on.deviceLogin, true);
  assert.equal((await rig({ routes: { 'GET /v1/health': health('none') } }).login.start()), 'locked');
  assert.equal((await rig({ routes: { 'GET /v1/health': health('none') }, storage: memoryStorage() }).login.start()), 'unpaired');
  // Health unreachable: unknown, and requests go out as usual.
  const r = rig({ routes: { 'GET /v1/health': new TypeError('Load failed') } });
  assert.equal(await r.login.start(), 'unknown');
});

test('request sends JSON, Aimux-Interaction only when the person asked, and throws errors with status and code', async () => {
  const { login, server } = rig({ routes: {
    'GET /v1/sessions': { status: 200, body: { sessions: [] } },
    'POST /v1/sessions/x/messages': { status: 409, body: { error: 'busy', code: 'busy' } },
  } });
  assert.deepEqual(await login.request('GET', '/v1/sessions'), { sessions: [] });
  assert.equal(server.calls[0].headers['Aimux-Interaction'], undefined);
  await login.request('GET', '/v1/sessions', undefined, { interaction: true });
  assert.equal(server.calls[1].headers['Aimux-Interaction'], '1');
  await assert.rejects(login.request('POST', '/v1/sessions/x/messages', { text: 'hi' }), (e) => e.status === 409 && e.code === 'busy');
  assert.equal(server.calls[2].headers['Content-Type'], 'application/json');
  assert.deepEqual(server.calls[2].body, { text: 'hi' });
});

test('a 401 locks and holds the request; only a tap prompts; the unlock replays held requests once, in order', async () => {
  let unlocked = false;
  const { login, server, credentials } = rig({ routes: {
    'GET /v1/sessions': () => (unlocked ? { status: 200, body: { sessions: ['a'] } } : LOGIN_REQUIRED),
    'GET /v1/projects': () => (unlocked ? { status: 200, body: { projects: ['p'] } } : LOGIN_REQUIRED),
    'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
    'POST /v1/auth/login': () => { unlocked = true; return { status: 200, body: { deviceId: 'BQUFBQUFBQUFBQUFBQUFBQ', name: 'phone' } }; },
  } });
  const changes = [];
  login.subscribe(() => { if (changes.at(-1) !== login.state) changes.push(login.state); });
  const first = login.request('GET', '/v1/sessions', undefined, { interaction: true });
  await settle();
  assert.equal(login.state, 'locked');
  const second = login.request('GET', '/v1/projects'); // held without going out
  await settle();
  assert.equal(server.calls.filter((c) => c.path === '/v1/projects').length, 0);
  assert.equal(credentials.calls.get.length, 0, 'no Face ID without a tap');

  await login.unlock();
  assert.equal(credentials.calls.get.length, 1);
  assert.equal(credentials.calls.get[0].rpId, 'my-mac.tail1234.ts.net');
  assert.equal(credentials.calls.get[0].userVerification, 'required');
  const challengeCall = server.calls.find((c) => c.path === '/v1/auth/challenge');
  assert.deepEqual(challengeCall.body, {}); // discoverable: no device id
  const loginCall = server.calls.find((c) => c.path === '/v1/auth/login');
  assert.deepEqual(loginCall.body, {
    challenge: '-_8', signature: 'Bg==', credentialId: 'CQkJ', userHandle: 'BQUFBQUFBQUFBQUFBQUFBQ',
    clientDataJSON: 'BA==', authenticatorData: 'BQ==',
  });
  assert.deepEqual(await first, { sessions: ['a'] });
  assert.deepEqual(await second, { projects: ['p'] });
  const after = server.calls.slice(server.calls.indexOf(loginCall) + 1).map((c) => c.path);
  assert.deepEqual(after, ['/v1/sessions', '/v1/projects']); // once each, in order
  assert.equal(server.calls.at(-2).headers['Aimux-Interaction'], '1'); // the replay keeps its headers
  assert.equal(login.state, 'live');
  assert.deepEqual(changes, ['locked', 'live']);
});

test('a second 401 right after the unlock means the device is no longer paired', async () => {
  const { login } = rig({ routes: {
    'GET /v1/sessions': LOGIN_REQUIRED,
    'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
    'POST /v1/auth/login': { status: 200, body: { deviceId: 'd', name: 'phone' } },
  } });
  const held = login.request('GET', '/v1/sessions');
  await settle();
  await login.unlock();
  await assert.rejects(held, (e) => e.status === 401 && e.code === 'login_required');
  assert.equal(login.state, 'unpaired');
  assert.equal(login.revoked, true);
});

test('a 401 on a request sent after the unlock locks again (an expiry, not a revoke)', async () => {
  let live = false;
  const { login } = rig({ routes: {
    'GET /v1/sessions': () => (live ? { status: 200, body: { sessions: [] } } : LOGIN_REQUIRED),
    'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
    'POST /v1/auth/login': () => { live = true; return { status: 200, body: { deviceId: 'd', name: 'phone' } }; },
    'POST /v1/auth/logout': { status: 200, body: { status: 'logged_out' } },
  } });
  await login.lock();
  await login.unlock(); // nothing held: no request has gone through since
  live = false;
  login.request('GET', '/v1/sessions');
  await settle();
  assert.equal(login.state, 'locked');
  assert.equal(login.revoked, false);
});

test('cancelled Face ID stays locked with the button; a later tap asks again', async () => {
  const credentials = fakeCredentials({ get: notAllowed() });
  const { login, server } = rig({ credentials, routes: {
    'GET /v1/sessions': LOGIN_REQUIRED,
    'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
  } });
  login.request('GET', '/v1/sessions');
  await settle();
  await login.unlock();
  assert.equal(login.state, 'locked');
  assert.equal(login.error, 'Unlock was cancelled. Tap to try again.');
  assert.equal(server.calls.filter((c) => c.path === '/v1/auth/login').length, 0);
  await login.unlock();
  assert.equal(credentials.calls.get.length, 2);
});

test('unlock failures: a revoked device goes to pairing, a refused login and the network stay locked', async () => {
  const base = { 'GET /v1/sessions': LOGIN_REQUIRED, 'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } } };
  let r = rig({ routes: { ...base, 'POST /v1/auth/login': { status: 404, body: { error: 'x', code: 'unknown_device' } } } });
  const failed = r.login.request('GET', '/v1/sessions').catch((e) => e); await settle();
  await r.login.unlock();
  assert.equal((await failed).code, 'unknown_device'); // held requests fail with the server's answer
  assert.equal(r.login.state, 'unpaired');
  assert.equal(r.login.revoked, true);

  r = rig({ routes: { ...base, 'POST /v1/auth/login': { status: 401, body: { error: 'login failed', code: 'login_failed' } } } });
  r.login.request('GET', '/v1/sessions'); await settle();
  await r.login.unlock();
  assert.equal(r.login.state, 'locked');
  assert.equal(r.login.error, 'Could not unlock. Try again.');

  r = rig({ routes: { ...base, 'POST /v1/auth/challenge': new TypeError('Load failed') } });
  r.login.request('GET', '/v1/sessions'); await settle();
  await r.login.unlock();
  assert.equal(r.login.state, 'locked');
  assert.equal(r.login.error, 'Could not reach your Mac. Check the connection and try again.');
  assert.equal(r.credentials.calls.get.length, 0);
});

test('a 401 with nothing stored here asks to pair', async () => {
  const { login } = rig({ storage: memoryStorage(), routes: { 'GET /v1/sessions': LOGIN_REQUIRED } });
  login.request('GET', '/v1/sessions');
  await settle();
  assert.equal(login.state, 'unpaired');
  assert.equal(login.revoked, false);
});

test('pair makes the passkey first, over the hash of the code, and pairs with its fields', async () => {
  let paired = false;
  const storage = memoryStorage();
  const { login, server, credentials } = rig({ storage, routes: {
    'GET /v1/health': health('none'),
    'GET /v1/sessions': () => (paired ? { status: 200, body: { sessions: [] } } : LOGIN_REQUIRED),
    'POST /v1/auth/pair': () => { paired = true; return { status: 200, body: { deviceId: 'BQUFBQUFBQUFBQUFBQUFBQ', name: 'phone' } }; },
  } });
  await login.start();
  const held = login.request('GET', '/v1/sessions');
  await settle();
  await login.pair(' abcd-efgh ');
  const o = credentials.calls.create[0];
  assert.deepEqual(new Uint8Array(o.challenge), await sha256('ABCDEFGH'));
  assert.equal(o.rp.id, 'my-mac.tail1234.ts.net');
  assert.deepEqual(new Uint8Array(o.user.id), new Uint8Array(16).fill(5));
  const pairCall = server.calls.find((c) => c.path === '/v1/auth/pair');
  assert.deepEqual(pairCall.body, {
    code: 'ABCDEFGH', kind: 'webauthn', publicKey: 'Ag==', credentialId: 'CQkJ', userHandle: 'BQUFBQUFBQUFBQUFBQUFBQ',
    clientDataJSON: 'AQ==', authenticatorData: 'Aw==',
  });
  assert.equal(login.state, 'live');
  assert.deepEqual(JSON.parse(storage.getItem('aimux.device')), { deviceId: 'BQUFBQUFBQUFBQUFBQUFBQ', credentialId: 'CQkJ', name: 'phone' });
  assert.deepEqual(await held, { sessions: [] });
});

test('pair errors: a malformed code asks nothing, a refused code, a cancelled Face ID, the network', async () => {
  const pairWith = (reply, credentials) => rig({ storage: memoryStorage(), credentials, routes: { 'POST /v1/auth/pair': reply } });
  let r = pairWith({ status: 200, body: {} });
  await r.login.pair('ABC');
  assert.equal(r.login.error, 'Enter the 8-character code that aimux remote pair shows on your Mac.');
  assert.equal(r.credentials.calls.create.length, 0);
  assert.equal(r.server.calls.length, 0);

  r = pairWith({ status: 401, body: { error: 'pairing failed', code: 'pairing_failed' } });
  await r.login.pair('ABCD-EFGH');
  assert.equal(r.login.error, PAIR_CODE_FAILED);
  assert.equal(PAIR_CODE_FAILED, "That code didn't work. Run aimux remote pair on your Mac for a new one.");
  assert.notEqual(r.login.state, 'live');

  r = pairWith({ status: 409, body: { error: 'x', code: 'pairing_conflict' } });
  await r.login.pair('ABCD-EFGH');
  assert.equal(r.login.error, 'A device with that name is already paired. Revoke it on your Mac, or pair under another name.');

  r = pairWith({ status: 200, body: {} }, fakeCredentials({ create: notAllowed() }));
  await r.login.pair('ABCD-EFGH');
  assert.equal(r.login.error, 'Face ID was cancelled. Tap Pair to try again.');
  assert.equal(r.server.calls.length, 0);

  r = pairWith(new TypeError('Load failed'));
  await r.login.pair('ABCD-EFGH');
  assert.equal(r.login.error, 'Could not reach your Mac. Check the connection and try again.');
  for (const e of [r.login.error, PAIR_CODE_FAILED]) assert.ok(!e.includes('ABCD'), 'errors never repeat the code');
});

test('lock logs out and shows the lock', async () => {
  const { login, server } = rig({ routes: { 'GET /v1/health': health('live'), 'POST /v1/auth/logout': { status: 200, body: { status: 'logged_out' } } } });
  await login.start();
  await login.lock();
  assert.equal(server.calls.at(-1).path, '/v1/auth/logout');
  assert.equal(login.state, 'locked');
});

test('check tells a lost login from a lost network', async () => {
  const { login, server } = rig({ routes: { 'GET /v1/health': [health('live'), new TypeError('Load failed')] } });
  await login.start();
  assert.equal(await login.check(), 'offline');
  assert.ok(server.calls.every((c) => c.headers['Aimux-Interaction'] === undefined));
  assert.equal(login.state, 'live', 'offline keeps the state');
});

test('check after a lost login locks', async () => {
  const { login } = rig({ routes: { 'GET /v1/health': [health('live'), health('none')] } });
  await login.start();
  assert.equal(await login.check(), 'none');
  assert.equal(login.state, 'locked');
});

test('storage that throws is treated as empty', async () => {
  const broken = { getItem: () => { throw new Error('denied'); }, setItem: () => { throw new Error('denied'); }, removeItem: () => {} };
  const { login } = rig({ storage: broken, routes: { 'GET /v1/health': health('none') } });
  assert.equal(await login.start(), 'unpaired');
});

const loginRoutes = (loginReply) => ({
  'GET /v1/sessions': LOGIN_REQUIRED,
  'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
  'POST /v1/auth/login': loginReply,
});

test('an assertion without a user handle sends the stored device id as the handle', async () => {
  const credentials = fakeCredentials({ get: assertion(null) });
  const { login, server } = rig({ credentials, routes: loginRoutes({ status: 200, body: { deviceId: 'BQUFBQUFBQUFBQUFBQUFBQ', name: 'phone' } }) });
  login.request('GET', '/v1/sessions').catch(() => {});
  await settle();
  await login.unlock();
  assert.equal(server.calls.find((c) => c.path === '/v1/auth/login').body.userHandle, 'BQUFBQUFBQUFBQUFBQUFBQ');
  // Nothing stored either: an empty handle (the server then refuses it).
  const bare = rig({ credentials: fakeCredentials({ get: assertion(new ArrayBuffer(0)) }), storage: memoryStorage(), routes: loginRoutes({ status: 404, body: { code: 'unknown_device' } }) });
  await bare.login.unlock();
  assert.equal(bare.server.calls.find((c) => c.path === '/v1/auth/login').body.userHandle, '');
});

test('pairing without crypto.subtle shows the fixed error, never an unhandled rejection', async () => {
  const { login, credentials } = rig({ storage: memoryStorage(), routes: {}, digest: () => Promise.reject(new TypeError('subtle is undefined')) });
  await login.pair('ABCD-EFGH');
  assert.equal(login.error, 'This browser could not make a passkey.');
  assert.equal(login.busy, false);
  assert.equal(credentials.calls.create.length, 0);
});

// A fake PublicKeyCredential with signalUnknownCredential; `fails` makes it throw.
function fakeSignal(fails = false) {
  const calls = [];
  return { calls, PublicKeyCredential: { signalUnknownCredential: async (o) => { calls.push(o); if (fails) throw new Error('not allowed'); } } };
}

test('a passkey the server will never know is reported to the browser, best effort', async () => {
  for (const code of ['pairing_failed', 'pairing_conflict']) {
    const sig = fakeSignal();
    const { login } = rig({ storage: memoryStorage(), publicKeyCredential: sig.PublicKeyCredential, routes: { 'POST /v1/auth/pair': { status: code === 'pairing_failed' ? 401 : 409, body: { code } } } });
    await login.pair('ABCD-EFGH');
    assert.deepEqual(sig.calls, [{ rpId: 'my-mac.tail1234.ts.net', credentialId: 'CQkJ' }], code);
  }
  const sig = fakeSignal(true); // a throwing signal changes nothing
  const { login } = rig({ publicKeyCredential: sig.PublicKeyCredential, routes: loginRoutes({ status: 404, body: { code: 'unknown_device' } }) });
  login.request('GET', '/v1/sessions').catch(() => {});
  await settle();
  await login.unlock();
  assert.deepEqual(sig.calls, [{ rpId: 'my-mac.tail1234.ts.net', credentialId: 'CQkJ' }]);
  assert.equal(login.state, 'unpaired');
  // Other failures keep the passkey; no API (older browsers) is fine.
  const keep = fakeSignal();
  const r = rig({ storage: memoryStorage(), publicKeyCredential: keep.PublicKeyCredential, routes: { 'POST /v1/auth/pair': new TypeError('Load failed') } });
  await r.login.pair('ABCD-EFGH');
  assert.deepEqual(keep.calls, []);
  const old = rig({ storage: memoryStorage(), publicKeyCredential: {}, routes: { 'POST /v1/auth/pair': { status: 401, body: { code: 'pairing_failed' } } } });
  await old.login.pair('ABCD-EFGH');
  assert.equal(old.login.error, PAIR_CODE_FAILED);
});

test('a 401 that arrives after a newer unlock is retried on the new login, not locked', async () => {
  let unlocked = false;
  let releaseSlow;
  const slow = new Promise((r) => { releaseSlow = r; });
  const { server } = rig({ routes: {
    'GET /v1/health': health('live'),
    'GET /v1/projects': () => (unlocked ? { status: 200, body: { projects: ['p'] } } : LOGIN_REQUIRED),
    'GET /v1/sessions': () => (unlocked ? { status: 200, body: { sessions: ['a'] } } : LOGIN_REQUIRED),
    'POST /v1/auth/challenge': { status: 200, body: { challenge: '-_8' } },
    'POST /v1/auth/login': () => { unlocked = true; return { status: 200, body: { deviceId: 'd', name: 'phone' } }; },
  } });
  // The slow request's 401 was answered under the old login, but reaches the
  // page only after the unlock.
  const fetch = server.fetch;
  const delayed = async (path, opts) => {
    if (path === '/v1/projects' && !unlocked) { const r = await fetch(path, opts); await slow; return r; }
    return fetch(path, opts);
  };
  const late = createLogin({ fetch: delayed, credentials: fakeCredentials(), storage: memoryStorage(STORED), hostname: 'My-Mac.tail1234.ts.net', random: (n) => new Uint8Array(n).fill(5) });
  await late.start();
  assert.equal(late.state, 'live');
  const projects = late.request('GET', '/v1/projects');
  const sessions = late.request('GET', '/v1/sessions');
  await settle();
  assert.equal(late.state, 'locked');
  await late.unlock();
  assert.deepEqual(await sessions, { sessions: ['a'] });
  releaseSlow();
  assert.deepEqual(await projects, { projects: ['p'] });
  assert.equal(late.state, 'live');
});
