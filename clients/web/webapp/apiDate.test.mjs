import { test } from 'node:test';
import assert from 'node:assert/strict';
import { apiDate } from './apiDate.mjs';

test('apiDate: whole seconds and milliseconds match Date.parse', () => {
  assert.equal(apiDate('2026-10-26T09:00:00Z'), Date.parse('2026-10-26T09:00:00Z'));
  assert.equal(apiDate('2026-10-26T09:00:00.606Z'), Date.parse('2026-10-26T09:00:00Z') + 606);
});

test('apiDate: digits below a millisecond are kept', () => {
  const base = Date.parse('2026-10-26T09:00:00Z');
  assert.ok(Math.abs(apiDate('2026-10-26T09:00:00.0004Z') - (base + 0.4)) < 1e-3);
  assert.ok(apiDate('2026-10-26T09:00:00.0014Z') - apiDate('2026-10-26T09:00:00.0010Z') > 0.3);
  assert.ok(Math.abs(apiDate('2026-10-26T09:00:00.123456789Z') - (base + 123.456789)) < 1e-3);
});

test('apiDate: offsets work and garbage is NaN', () => {
  assert.equal(apiDate('2026-10-26T11:00:00.5+02:00'), Date.parse('2026-10-26T09:00:00Z') + 500);
  assert.ok(Number.isNaN(apiDate('yesterday')));
});
