// A session open in a terminal on the Mac that the server can type into (SessionDetail.terminal):
// the reason lines, the pending line, the header, the polling pause and the prompt card, the same
// as ChatPresentation and ChatModelTerminal in the iPhone app. Pure: no DOM, no clock.

import { queueSection } from './queue.mjs';

const REASON = {
  disabled: "Typing into your Mac's terminal is off",
  not_claude: "Claude isn't running in that terminal",
  mac_in_use: 'Your Mac is in use right now',
  no_terminal_driver: "This terminal isn't supported yet",
  automation_denied: 'Allow aimux to control iTerm2 on your Mac',
  not_at_input: "Claude isn't at its input box",
};

// Why the phone cannot type into the terminal now, from terminal.reason.
export function terminalReasonText(reason) {
  return Object.hasOwn(REASON, reason) ? REASON[reason] : "Can't reach the terminal";
}

// The line above the composer for a terminal that cannot take typing now; '' when it can, or when
// its prompt's card says what to do. With the chat queue (queue), the Mac in use reads as the spec's
// line, and not at all while the queue's waiting note says it.
export function terminalReasonLine(detail, { queue = false } = {}) {
  const t = detail && detail.state === 'open_in_terminal' ? detail.terminal : null;
  if (!t || t.canType || t.prompt) return '';
  if (!queue || t.reason !== 'mac_in_use') return terminalReasonText(t.reason);
  const section = queueSection(detail.queue);
  if (section.visible && section.state === 'running' && section.line) return '';
  return "You're using this session on your Mac.";
}

const TERMINAL_CODES = new Set(['disabled', 'not_in_terminal', ...Object.keys(REASON)]);

// The live line at the end of a terminal chat while Claude works on the Mac: "Working · 1m 20s ·
// go test ./…", the time from Claude Code's working line and the step from the latest turn; ''
// when it is not working. The same as terminalWorkingLine in the iPhone app.
export function terminalWorkingLine(detail) {
  const t = detail && detail.state === 'open_in_terminal' ? detail.terminal : null;
  if (!t || !t.working) return '';
  const parts = ['Working'];
  if (t.workingSeconds > 0) parts.push(elapsedText(t.workingSeconds));
  const turns = detail.turns || [];
  const tools = turns.length ? turns[turns.length - 1].tools || [] : [];
  if (tools.length) {
    const step = tools[tools.length - 1];
    parts.push(step.snippet || step.name);
  }
  return parts.join(' · ');
}

// elapsedText: a duration as Claude Code's working line shows it, "42s", "1m 20s", "2h 3m".
export function elapsedText(seconds) {
  const h = Math.floor(seconds / 3600), m = Math.floor((seconds % 3600) / 60), s = seconds % 60;
  if (h) return m ? `${h}h ${m}m` : `${h}h`;
  if (m) return s ? `${m}m ${s}s` : `${m}m`;
  return `${s}s`;
}

// What a refused terminal request says: a terminal refusal as its reason line, bad text and text
// typed but not submitted as their own lines, anything else its message.
export function terminalErrorText(e) {
  if (TERMINAL_CODES.has(e.code)) return terminalReasonText(e.code);
  if (e.code === 'bad_text') return "The terminal can't take that text";
  if (e.code === 'not_submitted') return 'Your message is in the box on your Mac, not sent yet; Retry sends it';
  if (e.code === 'draft_in_box') return 'Your draft on the Mac is kept; nothing was sent';
  if (e.code === 'box_not_cleared') return "The box on your Mac couldn't be cleared; nothing was sent";
  return e.message;
}

const FAST = 2000;
const SLOW = 15000;
const FAST_WINDOW = 120000;

// The line under a message typed into the terminal, until the history shows it.
export function terminalPendingText(sentAt, now) {
  return now - sentAt < FAST_WINDOW ? "Sent to your Mac's terminal" : 'Sent; no answer yet';
}

// How long to wait before reading the session again: 2 s while Claude works there or within
// 2 minutes of a message typed from here, else 15 s.
export function terminalPollInterval({ busy, lastSend, now }) {
  if (busy) return FAST;
  if (lastSend !== null && lastSend !== undefined && now - lastSend < FAST_WINDOW) return FAST;
  return SLOW;
}

// The header's status for a terminal session, or null to use the ordinary one (no terminal field,
// or a status the registry could not tell). pendingPrompt: the prompt card showing, if any.
export function terminalStatus(detail, pendingPrompt) {
  const t = detail && detail.state === 'open_in_terminal' ? detail.terminal : null;
  if (!t || t.status === 'unknown') return null;
  if (pendingPrompt) return { dot: 'needs', label: 'On your Mac · Your turn' };
  if (t.status === 'busy') return { dot: 'working', label: 'On your Mac · Working' };
  return { dot: 'idle', label: 'On your Mac · Your turn' };
}

// The prompt as the chat's approval card: the options keep their keys as ids; the first "Yes" is
// the one-time allow, another "Yes, ..." the lasting one, "No ..." the reject (the kinds only pick
// the buttons' looks). The prompt's detail (the command, or the file and its preview) is the card's
// command, shown in monospace.
export function promptItem(prompt) {
  let allowed = false;
  const options = prompt.options.map((o) => {
    const label = o.label.toLowerCase();
    let kind;
    if (label.startsWith('no')) kind = 'reject_once';
    else if (label.startsWith('yes') && !allowed) { kind = 'allow_once'; allowed = true; } else kind = 'allow_always';
    return { optionId: o.key, name: o.label, kind };
  });
  return { kind: 'approval', approvalId: prompt.promptId, title: prompt.question, command: prompt.detail || null, outcome: null, options };
}
