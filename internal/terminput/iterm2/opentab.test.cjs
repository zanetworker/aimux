// Unit tests for opentab.js's layout decision (plan), run by
// TestOpentabPlan_Node or directly: node --test internal/terminput/iterm2/
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { plan } = require('./opentab.js');

// win builds a scanned window from [col, seq] pairs in listing order; each
// pane's ref is "<window>:s<seq>".
function win(id, panes) {
  return { id, panes: panes.map(([col, seq]) => ({ col, seq, ref: id + ':s' + seq })) };
}

// grid is n panes opened in order into an untouched window: seq 1..n, pane 1
// left, 2 right, then left, right, ...
function grid(n) {
  const out = [];
  for (let s = 1; s <= n; s++) out.push([s === 1 ? 0 : (s % 2 === 0 ? 1 : 0), s]);
  return out;
}

// shuffled is a fixed non-layout listing order of the panes.
function shuffled(panes) {
  return panes.slice().reverse().sort((a, b) => (a[1] * 7) % 5 - (b[1] * 7) % 5);
}

const NEW = { action: 'new', col: 0, seq: 1, count: 1 };

test('no aimux window opens a new one', () => {
  assert.deepEqual(plan([], 10), NEW);
});

test('windows with no aimux panes are not aimux windows', () => {
  assert.deepEqual(plan([win(3, []), win(4, [])], 10), NEW);
});

test('one pane splits vertically into the right column', () => {
  assert.deepEqual(plan([win(7, [[0, 1]])], 10),
    { action: 'vertical', window: 7, target: '7:s1', col: 1, seq: 2, count: 2 });
});

test('two panes split the left column (tie goes left)', () => {
  assert.deepEqual(plan([win(7, grid(2))], 10),
    { action: 'horizontal', window: 7, target: '7:s1', col: 0, seq: 3, count: 3 });
});

test('three panes split the right column, the shorter one', () => {
  assert.deepEqual(plan([win(7, grid(3))], 10),
    { action: 'horizontal', window: 7, target: '7:s2', col: 1, seq: 4, count: 4 });
});

test('the target is the column pane with the highest seq', () => {
  assert.deepEqual(plan([win(7, grid(5))], 10),
    { action: 'horizontal', window: 7, target: '7:s4', col: 1, seq: 6, count: 6 });
  assert.deepEqual(plan([win(7, grid(8))], 10),
    { action: 'horizontal', window: 7, target: '7:s7', col: 0, seq: 9, count: 9 });
});

test('listing order does not matter, only seq', () => {
  for (let n = 1; n <= 9; n++) {
    assert.deepEqual(plan([win(7, shuffled(grid(n)))], 10), plan([win(7, grid(n))], 10), 'n=' + n);
  }
  assert.deepEqual(plan([win(7, shuffled(grid(6)))], 10),
    { action: 'horizontal', window: 7, target: '7:s5', col: 0, seq: 7, count: 7 });
});

test('nine panes take the tenth, the last that fits', () => {
  assert.deepEqual(plan([win(7, grid(9))], 10),
    { action: 'horizontal', window: 7, target: '7:s8', col: 1, seq: 10, count: 10 });
});

test('a full window (10) opens a new window', () => {
  assert.deepEqual(plan([win(7, grid(10))], 10), NEW);
});

test('maxPanes is the limit, whatever its value', () => {
  assert.equal(plan([win(7, grid(2))], 2).action, 'new');
  assert.equal(plan([win(7, grid(1))], 2).action, 'vertical');
});

test('the newest aimux window with room is filled', () => {
  assert.deepEqual(plan([win(2, grid(1)), win(9, grid(10)), win(5, grid(2))], 10),
    { action: 'horizontal', window: 5, target: '5:s1', col: 0, seq: 3, count: 3 });
});

test('owner panes are not in the scan, so they never count or get split', () => {
  // The scan keeps only sessions with user.aimux = 1: an owner pane between
  // aimux panes is simply absent.
  assert.deepEqual(plan([win(7, [[0, 1], [1, 2], [1, 4]])], 10),
    { action: 'horizontal', window: 7, target: '7:s1', col: 0, seq: 5, count: 4 });
});

test('holes after closing are filled into the shorter column', () => {
  // 10 panes; the owner closed right panes seq 4, 6 and 10.
  const panes = grid(10).filter(([, s]) => s !== 4 && s !== 6 && s !== 10);
  assert.deepEqual(plan([win(7, shuffled(panes))], 10),
    { action: 'horizontal', window: 7, target: '7:s8', col: 1, seq: 10, count: 8 });
});

test('seq continues after the highest, even when that pane is gone', () => {
  // Closing seq 9 leaves max 8; seq never collides with a live pane.
  const panes = grid(9).filter(([, s]) => s !== 9);
  assert.equal(plan([win(7, panes)], 10).seq, 9);
});

test('a whole column closed: the highest-seq pane splits vertically into it', () => {
  // Right column (2, 4, 6) closed; left has 1, 3, 5.
  assert.deepEqual(plan([win(7, shuffled([[0, 1], [0, 3], [0, 5]]))], 10),
    { action: 'vertical', window: 7, target: '7:s5', col: 1, seq: 6, count: 4 });
  // Left column closed; right has 2, 4.
  assert.deepEqual(plan([win(7, [[1, 4], [1, 2]])], 10),
    { action: 'vertical', window: 7, target: '7:s4', col: 0, seq: 5, count: 3 });
});

test('a pane without seq counts as seq 0', () => {
  assert.deepEqual(plan([win(7, [[0, 0], [0, 2]])], 10),
    { action: 'vertical', window: 7, target: '7:s2', col: 1, seq: 3, count: 3 });
});
