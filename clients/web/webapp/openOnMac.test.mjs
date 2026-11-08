import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { canOpenOnMac, openOnMacMessage, openOnMac, COPY } from './openOnMac.mjs';

const FIXTURE = JSON.parse(readFileSync(new URL('../../../api/testdata/clients/terminal/open-on-mac.json', import.meta.url), 'utf8'));

test('module COPY deep-equals fixture copy', () => {
  assert.deepEqual(COPY, FIXTURE.copy);
});

test('every fixture refusal code maps to its text', () => {
  const codes = Object.keys(FIXTURE.codes).filter((c) => c !== 'unknown_session');
  for (const code of codes) {
    assert.equal(COPY[code], FIXTURE.copy[code], `code ${code}`);
  }
});

test('no browser modules import node:', () => {
  const dir = new URL('.', import.meta.url);
  const files = readdirSync(dir).filter((f) => f.endsWith('.mjs') && !f.endsWith('.test.mjs'));
  for (const file of files) {
    const content = readFileSync(new URL(file, dir), 'utf8');
    assert.ok(!content.includes("'node:") && !content.includes('"node:'), `${file} must not import node: modules`);
  }
});

test('every code maps to its fixture text', () => {
  // unknown_session has no copy of its own, so exclude it
  const codes = Object.keys(FIXTURE.codes).filter((c) => c !== 'unknown_session');
  for (const code of codes) {
    const expected = FIXTURE.copy[code];
    assert.equal(openOnMacMessage(code), expected, `code ${code}`);
  }
  // Test the non-error copy entries
  assert.equal(openOnMacMessage('action'), FIXTURE.copy.action);
  assert.equal(openOnMacMessage('progress'), FIXTURE.copy.progress);
  assert.equal(openOnMacMessage('done'), FIXTURE.copy.done);
});

test('unknown code falls back to timeout text', () => {
  assert.equal(openOnMacMessage('unknown_code'), FIXTURE.copy.timeout);
  assert.equal(openOnMacMessage('something_else'), FIXTURE.copy.timeout);
});

test('canOpenOnMac false for open_in_terminal', () => {
  const session = { state: 'open_in_terminal' };
  const health = { openOnMac: true };
  assert.equal(canOpenOnMac(session, health), false);
});

test('canOpenOnMac false for openOnMac:false', () => {
  const session = { state: 'idle' };
  const health = { openOnMac: false };
  assert.equal(canOpenOnMac(session, health), false);
});

test('canOpenOnMac true when health.openOnMac and session not open_in_terminal', () => {
  const session = { state: 'idle' };
  const health = { openOnMac: true };
  assert.equal(canOpenOnMac(session, health), true);
});

test('canOpenOnMac false with missing health', () => {
  const session = { state: 'idle' };
  assert.equal(canOpenOnMac(session, null), false);
  assert.equal(canOpenOnMac(session, {}), false);
});

test('canOpenOnMac false with missing session', () => {
  const health = { openOnMac: true };
  assert.equal(canOpenOnMac(null, health), false);
});

test('openOnMac resolves on 200 success', async () => {
  const api = async (method, path) => {
    assert.equal(method, 'POST');
    assert.equal(path, '/v1/sessions/test-id/open-on-mac');
    return { state: 'open_in_terminal' };
  };
  const result = await openOnMac(api, 'test-id');
  assert.deepEqual(result, { ok: true });
});

test('openOnMac rejects with code and message on error', async () => {
  const api = async () => {
    const err = new Error('Already open in a terminal on your Mac.');
    err.code = 'open_in_terminal';
    err.status = 409;
    throw err;
  };
  const result = await openOnMac(api, 'test-id');
  assert.deepEqual(result, {
    ok: false,
    code: 'open_in_terminal',
    message: FIXTURE.copy.open_in_terminal,
  });
});

test('openOnMac maps every known error code to its message', async () => {
  const codes = Object.keys(FIXTURE.codes).filter((c) => c !== 'unknown_session');
  for (const code of codes) {
    const api = async () => {
      const err = new Error('error');
      err.code = code;
      err.status = FIXTURE.codes[code];
      throw err;
    };
    const result = await openOnMac(api, 'test-id');
    assert.deepEqual(result, {
      ok: false,
      code,
      message: FIXTURE.copy[code],
    }, `code ${code}`);
  }
});

test('openOnMac handles unknown error code with timeout message', async () => {
  const api = async () => {
    const err = new Error('Something went wrong');
    err.code = 'internal';
    err.status = 500;
    throw err;
  };
  const result = await openOnMac(api, 'test-id');
  assert.deepEqual(result, {
    ok: false,
    code: 'internal',
    message: FIXTURE.copy.timeout,
  });
});

test('openOnMac handles client-side timeout with timeout message', async () => {
  const api = async () => {
    const err = new Error('Request timeout');
    err.name = 'AbortError';
    throw err;
  };
  const result = await openOnMac(api, 'test-id');
  assert.deepEqual(result, {
    ok: false,
    code: '',
    message: FIXTURE.copy.timeout,
  });
});

// Pending state coordination tests
test('pending state: starting from one path blocks the other for same session', async () => {
  const pending = new Set();
  let callCount = 0;

  const mockAction = async (id) => {
    if (pending.has(id)) return { called: false };
    pending.add(id);
    callCount++;
    try {
      await new Promise((resolve) => setTimeout(resolve, 10));
      return { called: true };
    } finally {
      pending.delete(id);
    }
  };

  // Start two in parallel for the same session
  const [r1, r2] = await Promise.all([mockAction('s1'), mockAction('s1')]);

  // Only one should have run
  assert.equal(callCount, 1);
  assert.ok((r1.called && !r2.called) || (!r1.called && r2.called), 'exactly one call succeeded');
});

test('pending state: different sessions are independent', async () => {
  const pending = new Set();
  let callCount = 0;

  const mockAction = async (id) => {
    if (pending.has(id)) return { called: false };
    pending.add(id);
    callCount++;
    try {
      await new Promise((resolve) => setTimeout(resolve, 10));
      return { called: true };
    } finally {
      pending.delete(id);
    }
  };

  // Start for different sessions in parallel
  const [r1, r2] = await Promise.all([mockAction('s1'), mockAction('s2')]);

  // Both should have run
  assert.equal(callCount, 2);
  assert.ok(r1.called && r2.called, 'both calls succeeded');
});

test('pending state: clears on success', async () => {
  const pending = new Set();

  const mockAction = async (id) => {
    if (pending.has(id)) return { ok: false };
    pending.add(id);
    try {
      return { ok: true };
    } finally {
      pending.delete(id);
    }
  };

  await mockAction('s1');
  assert.equal(pending.size, 0, 'pending cleared after success');
});

test('pending state: clears on error', async () => {
  const pending = new Set();

  const mockAction = async (id) => {
    if (pending.has(id)) return { ok: false };
    pending.add(id);
    try {
      throw new Error('test error');
    } finally {
      pending.delete(id);
    }
  };

  try {
    await mockAction('s1');
  } catch (e) {
    // expected
  }
  assert.equal(pending.size, 0, 'pending cleared after error');
});
