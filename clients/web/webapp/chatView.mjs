import { bidi, dot, el, icon, projectName } from './rows.mjs';
import { ago } from './sessionInfo.mjs';
import { promptItem, terminalPendingText, terminalStatus } from './terminal.mjs';
import { parseMarkdown } from './markdown.mjs';
import { linkReferences, mediaCards } from './references.mjs';
import { bubbleBlocks, formatsAsMarkdown, inlineBlocks, renderBlocks, renderInlines, renderPlain, stepDetail, stepInlines, unlinkedInlines } from './render.mjs';
import { userDisplay } from './timeline.mjs';

// The chat's rendering, like ChatView and TimelineRows in the iPhone app: the header, the
// timeline items from buildTimeline (timeline.mjs), a pending message, the Next bar and the
// read-only line. Server text goes in with textContent; replies and user messages go through the
// Markdown tree (markdown.mjs, references.mjs, render.mjs), which builds DOM nodes, never HTML.

const auto = (node) => { node.dir = 'auto'; return node; };

// The header's status: "Working", "Your turn", "Failed", "On your Mac · Working" or "On your Mac ·
// Your turn" for a terminal that can be typed into, or "Open on your Mac · 14h ago".
export function chatStatus({ detail, readOnly, result, pendingApproval, now = Date.now() }) {
  if (!detail) return null;
  const terminal = terminalStatus(detail, pendingApproval);
  if (terminal) return terminal;
  if (readOnly && detail.state === 'unavailable') return { dot: 'failed', label: 'Did not start' };
  if (readOnly || detail.state === 'open_in_terminal') return { dot: 'idle', label: 'Open on your Mac · ' + ago(Date.parse(detail.lastActive), now) };
  if (pendingApproval) return { dot: 'needs', label: 'Your turn' };
  if (result.working) return { dot: 'working', label: 'Working' };
  const last = result.items[result.items.length - 1];
  if (last && last.kind === 'error') return { dot: 'failed', label: 'Failed' };
  return { dot: 'idle', label: 'Your turn' };
}

// The title (two lines at most) over the status dot, state and project.
export function headerContent(title, status, project) {
  const t = auto(el('span', 'chat-title-text'));
  t.append(bidi(title));
  const out = [t];
  if (status) {
    const line = el('span', 'chat-status');
    line.append(dot(status.dot), status.label + ' · ', bidi(projectName(project)));
    out.push(line);
  }
  return out;
}

// "41s", "2m 10s".
const stepsTime = (s) => (s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's');
const KIND_NAME = { read: 'Read', edit: 'Edit', search: 'Search', run: 'Run', other: 'Tool' };

function toggle(cls, open, onClick, ...content) {
  const b = el('button', cls);
  b.type = 'button';
  b.setAttribute('aria-expanded', String(open));
  b.append(...content);
  b.addEventListener('click', onClick);
  return b;
}

// A chat item's text (a thought, a plan entry, an error) as inline-only Markdown with references.
function inlineText(cls, text, refs) {
  const node = auto(el('div', (cls + ' md').trim()));
  node.append(formatted(text, () => renderBlocks(inlineBlocks(text, refs))));
  return node;
}

function thoughtRow(item, live, open, onToggle, refs) {
  const label = item.seconds !== null ? `Thought for ${item.seconds}s` : live ? 'Thinking…' : 'Thought';
  const wrap = el('div', 'thought');
  wrap.append(toggle('thought-toggle' + (open ? ' open' : ''), open, onToggle, label, icon('chevron')));
  if (open) wrap.append(inlineText('thought-text', item.text, refs));
  return wrap;
}

// A step's one line; a step with a detail to show is a button that opens it below the line (open
// state keyed "group index:step index", so it survives the re-renders of a streaming turn).
function stepRow(s, live, key, ctx) {
  const d = s.detail || {};
  const canOpen = Boolean(d.input || d.error || d.old || d.new);
  const open = canOpen && ctx.openStepDetails.has(key);
  const cls = 'step' + (s.status === 'failed' ? ' failed' : '') + (open ? ' open' : '');
  const line = canOpen ? toggle(cls, open, () => ctx.onToggleStepDetail(key)) : el('div', cls);
  // The key finds the button again after a render, to give it back its focus.
  if (canOpen) line.setAttribute('data-step-key', key);
  const target = auto(el('span', 'step-target md'));
  // A button holds no link: an openable step's target links are offered in its detail instead.
  let links = [];
  if (s.target) {
    let inl = stepInlines(s.target, ctx.refs);
    if (canOpen) ({ inlines: inl, links } = unlinkedInlines(inl));
    renderInlines(inl, target);
  } else target.textContent = KIND_NAME[s.kind] || 'Tool';
  line.append(icon(s.kind in KIND_NAME ? s.kind : 'other', 'kind ' + s.kind), target);
  if (s.status === 'running' && live) {
    const spin = el('span', 'spinner');
    spin.setAttribute('role', 'img');
    spin.setAttribute('aria-label', 'Running');
    line.append(spin);
  } else if (s.status === 'failed') {
    const x = icon('cross', 'failed-mark');
    x.removeAttribute('aria-hidden');
    x.setAttribute('role', 'img');
    x.setAttribute('aria-label', 'Failed');
    line.append(x);
  }
  if (canOpen) line.append(icon('chevron', 'step-chevron'));
  const shown = open && stepDetail(s, links);
  return shown ? [line, shown] : [line];
}

function stepsCard(item, i, live, open, ctx) {
  const card = el('div', 'steps' + (open ? ' open' : ''));
  const summary = el('span', 'steps-summary');
  summary.append(el('b', '', item.count === 1 ? '1 step' : item.count + ' steps'));
  const parts = [];
  if (item.seconds !== null) parts.push(stepsTime(item.seconds));
  const files = item.filesChanged.length;
  if (files) parts.push(files === 1 ? '1 file changed' : files + ' files changed');
  if (parts.length) summary.append(el('span', 'dim', ' · ' + parts.join(' · ')));
  card.append(toggle('steps-toggle', open, () => ctx.onToggleSteps(i), summary, icon('chevron', 'chevron down')));
  if (open) {
    const list = el('div', 'steps-list');
    item.steps.forEach((s, j) => list.append(...stepRow(s, live, i + ':' + j, ctx)));
    card.append(list);
  }
  return card;
}

function planCard(item, refs) {
  const card = el('div', 'plan');
  const head = el('div', 'plan-head');
  head.append(el('span', '', 'Plan'), el('span', 'num', `${item.done} of ${item.total}`));
  card.append(head);
  for (const e of item.entries) {
    const row = el('div', 'plan-entry ' + (e.status || 'pending'));
    const mark = el('span', 'mark');
    if (e.status === 'completed') mark.append(icon('check'));
    row.append(mark, inlineText('plan-text', e.text, refs));
    card.append(row);
  }
  return card;
}

// A streaming reply is parsed again from its full text on each update. Rendered replies are kept
// by text so an unchanged one keeps its node (and a table or code block its sideways scroll)
// across the re-renders of a working session.
let replyNodes = new Map();
let replyNext = new Map();

function reply(text, refs) {
  let key = refsKey(refs) + '\u0000' + text;
  while (replyNext.has(key)) key += '\u0000'; // the same text twice gets two nodes
  let d = replyNodes.get(key);
  if (!d) {
    d = auto(el('div', 'reply md'));
    d.append(formatted(text, () => renderBlocks(mediaCards(linkReferences(parseMarkdown(text), refs)))));
  }
  replyNext.set(key, d);
  return d;
}

const refsKey = (refs) => (refs ? JSON.stringify(refs) : '');

// The formatted text, or, should formatting ever fail, the text itself as one paragraph: one
// unexpected reply must not stop the chat from drawing.
function formatted(text, render) {
  if (!formatsAsMarkdown(text)) return renderPlain(text);
  try {
    return render();
  } catch (_) {
    return auto(el('p', 'md-plain', text));
  }
}

// A user's message: Markdown, inline-only, with the lines as typed; images (chip labels, from
// userDisplay) sit above the text as small "Image" chips. parts (from userDisplay), when given,
// replace text: text parts as above, a command in monospace with its args as plain text, an
// output as a plain monospace block, labelled when it is error output.
export function userBubble(text, cls = '', refs = null, images = [], parts = null) {
  const row = el('div', ('user-row ' + cls).trim());
  const bubble = auto(el('div', 'bubble md'));
  if (images.length) {
    const chips = el('div', 'image-chips');
    for (const label of images) {
      const chip = el('span', 'image-chip');
      chip.append(icon('photo', 'image-chip-glyph'), label);
      chips.append(chip);
    }
    bubble.append(chips);
  }
  for (const p of parts || (text ? [{ kind: 'text', text }] : [])) bubble.append(bubblePart(p, refs));
  row.append(bubble);
  return row;
}

function bubblePart(p, refs) {
  if (p.kind === 'command') {
    const line = auto(el('div', 'bubble-command'));
    line.append(el('code', 'bubble-command-name', p.text));
    if (p.args) line.append(' ', el('span', 'bubble-command-args', p.args));
    return line;
  }
  if (p.kind === 'output') {
    const box = el('div', 'bubble-output' + (p.error ? ' error' : ''));
    if (p.error) box.append(el('div', 'bubble-output-label', 'Error output'));
    box.append(el('pre', 'bubble-output-text', p.text));
    return box;
  }
  return formatted(p.text, () => renderBlocks(bubbleBlocks(linkReferences(parseMarkdown(p.text, { breaks: true }), refs))));
}

// A history or event prompt without Claude Code's wrappers; null when nothing is left to show.
export function userItemNode(saved, refs = null) {
  const d = userDisplay(saved);
  return d.parts.length || d.images.length ? userBubble(d.text, '', refs, d.images, d.parts) : null;
}

// "Allowed", "Denied", "Expired" once resolved, or answered here; null while it waits.
export function approvalOutcome(item, answered, chosen) {
  if (item.outcome) {
    return { allowed: 'Allowed', denied: 'Denied', expired: 'Expired' }[item.outcome]
      || item.outcome.charAt(0).toUpperCase() + item.outcome.slice(1);
  }
  if (!answered.has(item.approvalId)) return null;
  const option = chosen.get(item.approvalId);
  if (!option) return 'Answered';
  return option.kind.startsWith('reject') ? 'Denied' : 'Allowed';
}

function approvalCard(item, ctx) {
  const card = el('div', 'approval-card chat-approval');
  const top = el('div', 'needs-label');
  top.append(dot('needs'), 'Needs your approval');
  card.append(top, auto(el('div', 'approval-title', item.title)));
  if (item.command) card.append(auto(el('pre', 'approval-command', item.command)));
  const options = el('div', 'options');
  // The one-time allow is the strong button; "always" stays plain so it is not the easy tap.
  const primary = item.options.find((o) => o.kind === 'allow_once') || item.options.find((o) => o.kind.startsWith('allow'));
  for (const o of item.options) {
    const b = el('button', 'option' + (o === primary ? ' primary' : o.kind.startsWith('reject') ? ' reject' : ''), o.name || o.optionId);
    b.type = 'button';
    b.disabled = ctx.answering.has(item.approvalId);
    b.addEventListener('click', () => ctx.onAnswer(item, o));
    options.append(b);
  }
  card.append(options);
  const err = ctx.answerErrors.get(item.approvalId);
  if (err) card.append(el('div', 'row-error', err));
  return card;
}

function resolvedLine(outcome, detail) {
  const line = el('div', 'resolved');
  line.append(icon(outcome === 'Allowed' ? 'check' : 'cross'), el('b', '', outcome));
  if (detail) line.append(auto(el('span', 'resolved-detail', detail)));
  return line;
}

function errorLine(text, refs) {
  const line = el('div', 'timeline-error');
  line.append(dot('failed'), text ? inlineText('timeline-error-text', text, refs) : auto(el('span', '', 'The turn failed.')));
  return line;
}

// The timeline's nodes. ctx: {working, openSteps, openThoughts (Sets of item index), openStepDetails
// (Set of "item index:step index"), answered,
// answering (Sets of approval id), answerErrors (Map id -> text), chosen (Map id -> option),
// refs (the reference context, references.mjs), onToggleSteps(i), onToggleThought(i), onToggleStepDetail(key),
// onAnswer(item, option)}.
export function timelineNodes(items, ctx) {
  replyNext = new Map();
  const nodes = timelineItems(items, ctx);
  replyNodes = replyNext;
  return nodes;
}

function timelineItems(items, ctx) {
  let lastUser = -1;
  items.forEach((it, i) => { if (it.kind === 'user') lastUser = i; });
  // A turn that ended shows no spinner, even on a step that never reported back.
  const live = (i) => ctx.working && i > lastUser;
  return items.map((it, i) => {
    switch (it.kind) {
      case 'user': return userItemNode(it.text, ctx.refs);
      case 'thought': return thoughtRow(it, live(i), ctx.openThoughts.has(i), () => ctx.onToggleThought(i), ctx.refs);
      case 'steps': return stepsCard(it, i, live(i), ctx.openSteps.has(i), ctx);
      case 'plan': return planCard(it, ctx.refs);
      case 'reply': return reply(it.text, ctx.refs);
      case 'approval': {
        const outcome = approvalOutcome(it, ctx.answered, ctx.chosen);
        return outcome ? resolvedLine(outcome, it.command || it.title) : approvalCard(it, ctx);
      }
      case 'error': return errorLine(it.text, ctx.refs);
      case 'stopped': return el('div', 'stopped', 'Stopped');
      default: return el('div');
    }
  }).filter(Boolean); // a user item with nothing left to show adds no node
}

// The permission prompt showing in the terminal on the Mac, as an approval card; ctx as for
// timelineNodes, with onAnswer(item, option) given the card's option ({optionId: key, ...}).
export function promptCard(prompt, ctx) {
  return approvalCard(promptItem(prompt), ctx);
}

// A message sent from here that no event shows yet; a failed one says why, with Retry. In a terminal
// session a sent one says it went to the Mac's terminal, and Retry waits until it can be typed into.
// opts: {terminal, canRetry, now, refs, retryLabel (a send refused busy offers "Add to queue")}.
export function pendingNode(pending, onRetry, { terminal = false, canRetry = true, now = Date.now(), refs = null, retryLabel = 'Retry' } = {}) {
  const row = userBubble(pending.text, pending.failure ? 'failed' : 'sending', refs);
  if (!pending.failure && terminal) row.append(el('div', 'pending-note', terminalPendingText(pending.sentAt, now)));
  if (pending.failure) {
    const note = el('div', 'not-sent');
    const retry = el('button', 'link', retryLabel);
    retry.type = 'button';
    retry.disabled = !canRetry;
    retry.addEventListener('click', onRetry);
    note.append(auto(el('span', '', 'Not sent · ' + pending.failure)), retry);
    row.append(note);
  }
  return row;
}

const NEXT_STATE = { needsApproval: ['needs', 'your turn'], failed: ['failed', 'failed'] };

// "Next · <title> · your turn ›".
export function nextBar(item, title, onClick) {
  const [status, label] = NEXT_STATE[item.activity.kind] || ['working', 'working'];
  const b = el('button', 'next-bar');
  b.type = 'button';
  b.setAttribute('aria-label', `Next: ${title}, ${label}`);
  const text = el('span', 'next-text');
  text.append(el('span', 'dim', 'Next · '), bidi(title), el('span', 'dim', ' · ' + label));
  b.append(dot(status), text, icon('chevron'));
  b.addEventListener('click', onClick);
  return b;
}

// Replaces the composer: a session open in a terminal (and why it cannot be typed into), or one
// that never started.
export function readOnlyLine(text) {
  const line = el('div', 'readonly-line');
  line.append(icon('lock'), el('span', '', text));
  return line;
}
