import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  elapsedText, terminalWorkingLine, promptItem, terminalErrorText, terminalPendingText, terminalPollInterval, terminalReasonLine, terminalReasonText,
  terminalStatus,
} from './terminal.mjs';

test('terminalReasonText: one line per reason, the iPhone wording', () => {
  assert.equal(terminalReasonText('disabled'), "Typing into your Mac's terminal is off");
  assert.equal(terminalReasonText('not_claude'), "Claude isn't running in that terminal");
  assert.equal(terminalReasonText('mac_in_use'), 'Your Mac is in use right now');
  assert.equal(terminalReasonText('no_terminal_driver'), "This terminal isn't supported yet");
  assert.equal(terminalReasonText('automation_denied'), 'Allow aimux to control iTerm2 on your Mac');
  assert.equal(terminalReasonText('not_at_input'), "Claude isn't at its input box");
  assert.equal(terminalReasonText('not_in_terminal'), "Can't reach the terminal");
  assert.equal(terminalReasonText('error'), "Can't reach the terminal");
  assert.equal(terminalReasonText(undefined), "Can't reach the terminal");
  assert.equal(terminalReasonText('something_new'), "Can't reach the terminal");
});

test('terminalErrorText: terminal refusals use the reason line, bad text its own, others their message', () => {
  const err = (code, message = 'm') => Object.assign(new Error(message), { code });
  assert.equal(terminalErrorText(err('mac_in_use')), 'Your Mac is in use right now');
  assert.equal(terminalErrorText(err('not_in_terminal')), "Can't reach the terminal");
  assert.equal(terminalErrorText(err('bad_text')), "The terminal can't take that text");
  assert.equal(terminalErrorText(err('not_submitted')), 'Your message is in the box on your Mac, not sent yet; Retry sends it');
  assert.equal(terminalErrorText(err('internal', 'the terminal did not answer')), 'the terminal did not answer');
  assert.equal(terminalErrorText(err('', 'Failed to fetch')), 'Failed to fetch');
});

test('terminalPendingText: "Sent to your Mac\'s terminal" for 2 minutes, then "Sent; no answer yet"', () => {
  const sentAt = 1_000_000;
  assert.equal(terminalPendingText(sentAt, sentAt), "Sent to your Mac's terminal");
  assert.equal(terminalPendingText(sentAt, sentAt + 119_999), "Sent to your Mac's terminal");
  assert.equal(terminalPendingText(sentAt, sentAt + 120_000), 'Sent; no answer yet');
  assert.equal(terminalPendingText(sentAt, sentAt + 3_600_000), 'Sent; no answer yet');
});

test('terminalPollInterval: 2 s while busy or within 2 minutes of a send, else 15 s', () => {
  const now = 10_000_000;
  assert.equal(terminalPollInterval({ busy: true, lastSend: null, now }), 2000);
  assert.equal(terminalPollInterval({ busy: false, lastSend: null, now }), 15000);
  assert.equal(terminalPollInterval({ busy: false, lastSend: now - 1000, now }), 2000);
  assert.equal(terminalPollInterval({ busy: false, lastSend: now - 119_999, now }), 2000);
  assert.equal(terminalPollInterval({ busy: false, lastSend: now - 120_000, now }), 15000, 'the window ends at 2 minutes');
  assert.equal(terminalPollInterval({ busy: true, lastSend: now - 600_000, now }), 2000);
});

test('terminalStatus: the header from terminal.status; a prompt is your turn; unknown falls back', () => {
  const detail = (terminal) => ({ state: 'open_in_terminal', lastActive: '2026-10-27T10:00:00Z', terminal });
  assert.deepEqual(terminalStatus(detail({ status: 'busy', canType: true }), null), { dot: 'working', label: 'On your Mac · Working' });
  assert.deepEqual(terminalStatus(detail({ status: 'idle', canType: true }), null), { dot: 'idle', label: 'On your Mac · Your turn' });
  assert.deepEqual(terminalStatus(detail({ status: 'busy', canType: false }), 'p1'), { dot: 'needs', label: 'On your Mac · Your turn' });
  assert.equal(terminalStatus(detail({ status: 'unknown', canType: false }), null), null);
  assert.equal(terminalStatus({ state: 'open_in_terminal' }, null), null, 'an older server sends no terminal');
  assert.equal(terminalStatus({ state: 'idle', terminal: { status: 'busy' } }, null), null);
  assert.equal(terminalStatus(null, null), null);
});

test('promptItem: the prompt as an approval card, keys as option ids; first Yes is the one-time allow', () => {
  const item = promptItem({
    promptId: 'p1', question: 'Do you want to make this edit to CHANGELOG.md?',
    options: [{ key: '1', label: 'Yes' }, { key: '2', label: 'Yes, allow all edits during this session' }, { key: '3', label: 'No, and tell Claude what to do differently' }],
  });
  assert.deepEqual(item, {
    kind: 'approval', approvalId: 'p1', title: 'Do you want to make this edit to CHANGELOG.md?', command: null, outcome: null,
    options: [
      { optionId: '1', name: 'Yes', kind: 'allow_once' },
      { optionId: '2', name: 'Yes, allow all edits during this session', kind: 'allow_always' },
      { optionId: '3', name: 'No, and tell Claude what to do differently', kind: 'reject_once' },
    ],
  });
  assert.deepEqual(promptItem({ promptId: 'p2', question: 'q', options: [] }).options, []);
});

test('promptItem: the prompt detail is the card command, shown in monospace', () => {
  const detail = 'Bash command\n\n  ls -la\n  List files in the current directory';
  assert.equal(promptItem({ promptId: 'p3', question: 'Do you want to proceed?', detail, options: [] }).command, detail);
  assert.equal(promptItem({ promptId: 'p4', question: 'q', detail: '', options: [] }).command, null);
});

test('terminalReasonLine: with the queue the Mac in use is one line (the spec wording, or the waiting note alone)', () => {
  const blocked = (reason, extra = {}) => ({
    state: 'open_in_terminal', terminal: { status: 'idle', canType: false, working: false, reason, ...extra.terminal }, ...extra.detail,
  });
  const note = { queue: { state: 'running', note: "Waiting: you're using this session on your Mac", items: [{ id: 'q1', text: 'Next', state: 'waiting', created: '2026-03-02T09:01:00Z' }] } };
  assert.equal(terminalReasonLine(blocked('mac_in_use'), { queue: true }), "You're using this session on your Mac.");
  assert.equal(terminalReasonLine(blocked('mac_in_use', { detail: note }), { queue: true }), '', "the queue's waiting note says it");
  assert.equal(terminalReasonLine(blocked('mac_in_use', { detail: note }), { queue: false }), 'Your Mac is in use right now', 'a server without the queue keeps the old line');
  assert.equal(terminalReasonLine(blocked('disabled', { detail: note }), { queue: true }), "Typing into your Mac's terminal is off");
  assert.equal(terminalReasonLine({ state: 'open_in_terminal', terminal: { status: 'idle', canType: true, working: false } }, { queue: true }), '');
  assert.equal(terminalReasonLine(blocked('not_at_input', { terminal: { prompt: { promptId: 'p', question: 'Q?', options: [] } } }), { queue: false }), '');
  assert.equal(terminalReasonLine(null, { queue: true }), '');
});

test('a kept draft and a box that would not clear have their own lines', () => {
  assert.equal(terminalErrorText({ code: 'draft_in_box', message: 'm' }), 'Your draft on the Mac is kept; nothing was sent');
  assert.equal(terminalErrorText({ code: 'box_not_cleared', message: 'm' }), "The box on your Mac couldn't be cleared; nothing was sent");
});

test('the working line: how long, then the current step; nothing while idle', () => {
  const detail = (terminal, tools = []) => ({ state: 'open_in_terminal', terminal, turns: [{ number: 1, user: 'go', reply: '', tools }] });
  const tools = [{ name: 'Read', snippet: 'notes.md' }, { name: 'Bash', snippet: 'go test ./remote/...' }];
  assert.equal(terminalWorkingLine(detail({ working: true, workingSeconds: 80 }, tools)), 'Working · 1m 20s · go test ./remote/...');
  assert.equal(terminalWorkingLine(detail({ working: true })), 'Working');
  assert.equal(terminalWorkingLine(detail({ working: true }, [{ name: 'WebSearch', snippet: '' }])), 'Working · WebSearch');
  assert.equal(terminalWorkingLine(detail({ working: false, workingSeconds: 9 })), '');
  assert.equal(terminalWorkingLine(null), '');
  assert.deepEqual([0, 59, 60, 3599, 3600, 7380].map(elapsedText), ['0s', '59s', '1m', '59m 59s', '1h', '2h 3m']);
});
