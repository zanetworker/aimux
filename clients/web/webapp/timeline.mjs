// The chat timeline: history turns (SessionDetail.turns) merged with the event
// log (EventPage.events) into display items. The rules are pinned by
// api/testdata/clients/chat/*.json and stated in api/testdata/clients/README.md;
// the iPhone app implements the same. Pure: no DOM, no clock.
import { apiDate } from './apiDate.mjs';

const WINDOW = 2000; // ms of slack between a turn's events and its history record

// samePrompt matches a history turn's prompt (whitespace-folded, perhaps cut
// short with an ellipsis) to the text a turn was started with.
export function samePrompt(history, text) {
  const fold = (x) => (x || '').replace(/\s+/g, ' ').trim();
  const h = fold(history).replace(/…$/, '');
  return h !== '' && fold(text).startsWith(h);
}

const ACP_KIND = { read: 'read', edit: 'edit', search: 'search', execute: 'run' };
const TOOL_KIND = {
  Read: 'read', Edit: 'edit', Write: 'edit', MultiEdit: 'edit', NotebookEdit: 'edit',
  Grep: 'search', Glob: 'search', WebSearch: 'search', Bash: 'run',
};

const stepStatus = (s) => (s === 'completed' ? 'ok' : s === 'failed' ? 'failed' : 'running');
const seconds = (from, to) => (Number.isFinite(from) && Number.isFinite(to) ? Math.max(0, Math.round((to - from) / 1000)) : null);
const startText = (e) => (e.data && typeof e.data.text === 'string' ? e.data.text : '');
const isEnd = (e) => e.kind === 'turn_done' || e.kind === 'turn_error';

function stepsItem(steps, secs) {
  const files = [];
  for (const s of steps) if (s.kind === 'edit' && s.target && !files.includes(s.target)) files.push(s.target);
  return { kind: 'steps', count: steps.length, seconds: secs, filesChanged: files, steps };
}

const str = (v) => (typeof v === 'string' ? v : '');
const isObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

// A history tool's detail, every field a string ('' when absent).
function historyDetail(d) {
  const x = isObj(d) ? d : {};
  return { input: str(x.input), error: str(x.error), old: str(x.old), new: str(x.new) };
}

const INPUT_KEYS = ['command', 'pattern', 'url', 'query', 'description', 'file_path'];

// A live step's detail from its events (the tool_call and its updates, seq order) and its final
// status. Each field comes from one event, the latest that qualifies; nothing is merged across events.
function liveDetail(us, status) {
  let raw = null;
  let diff = null;
  let content = null;
  for (const u of us) {
    if (isObj(u.rawInput)) raw = u.rawInput;
    if (Array.isArray(u.content)) {
      content = u.content;
      const d = u.content.find((c) => isObj(c) && c.type === 'diff');
      if (d) diff = d;
    }
  }
  const input = raw ? str(INPUT_KEYS.map((k) => raw[k]).find((v) => typeof v === 'string' && v !== '')) : '';
  let old = '';
  let neu = '';
  if (diff) {
    old = str(diff.oldText);
    neu = str(diff.newText);
  } else if (raw) {
    old = str(raw.old_string);
    neu = typeof raw.new_string === 'string' ? raw.new_string : str(raw.content);
  }
  const error = status === 'failed' && content
    ? content.filter((c) => isObj(c) && c.type === 'content' && isObj(c.content) && c.content.type === 'text' && typeof c.content.text === 'string')
      .map((c) => c.content.text).join('\n')
    : '';
  return { input, error, old, new: neu };
}

function historyItems(t) {
  const items = [];
  if (t.user) items.push({ kind: 'user', text: t.user });
  const tools = t.tools || [];
  if (tools.length) {
    items.push(stepsItem(tools.map((x) => ({
      kind: TOOL_KIND[x.name] || 'other',
      target: x.snippet || '',
      status: x.ok ? 'ok' : 'failed',
      detail: historyDetail(x.detail),
    })), null));
  }
  if (t.reply) items.push({ kind: 'reply', text: t.reply });
  return items;
}

// chunkText is the text of a message or thought chunk, '' when it has none.
function chunkText(u) {
  const c = u.content;
  return c && c.type === 'text' && typeof c.text === 'string' ? c.text : '';
}

// turnItems draws one turn from its events (seq order). approvals maps an
// approval id to its item across the whole timeline.
function turnItems(evs, approvals) {
  const items = [];
  const tools = new Map(); // toolCallId -> { step, group }
  const groups = [];
  let group = null; // the steps group a tool call joins; none after a reply
  let run = null; // the open chunk run: { kind, item, start }
  let plan = null;
  for (const e of evs) {
    const d = e.data || {};
    const at = apiDate(e.time);
    const u = e.kind === 'update' ? d : null;
    const su = u && u.sessionUpdate;
    let chunk = null;
    let text = '';
    // An event that changes nothing is ignored: it neither ends a run nor times anything.
    if (su === 'agent_message_chunk' || su === 'agent_thought_chunk') {
      text = chunkText(u);
      if (!text) continue;
      chunk = su === 'agent_thought_chunk' ? 'thought' : 'reply';
    } else if (e.kind === 'update' && su !== 'tool_call' && su !== 'plan' && !(su === 'tool_call_update' && tools.has(u.toolCallId))) {
      continue;
    } else if (e.kind === 'approval_resolved' && !approvals.has(d.approvalId)) {
      continue;
    } else if (!['turn_started', 'update', 'approval', 'approval_resolved', 'turn_done', 'turn_error'].includes(e.kind)) {
      continue;
    }

    if (run && run.kind !== chunk) {
      if (run.kind === 'thought') run.item.seconds = seconds(run.start, at);
      run = null;
    }
    if (chunk) {
      if (run) {
        run.item.text += text;
      } else {
        const item = chunk === 'thought' ? { kind: 'thought', text, seconds: null } : { kind: 'reply', text };
        items.push(item);
        run = { kind: chunk, item, start: at };
        if (chunk === 'reply') group = null;
      }
      continue;
    }

    switch (e.kind) {
      case 'turn_started':
        if (startText(e)) items.push({ kind: 'user', text: startText(e) });
        break;
      case 'approval': {
        const raw = d.toolCall && d.toolCall.rawInput;
        const item = {
          kind: 'approval',
          approvalId: d.approvalId,
          title: d.title || '',
          command: raw && typeof raw.command === 'string' ? raw.command : null,
          options: (d.options || []).map((o) => ({ optionId: o.optionId || '', name: o.name || '', kind: o.kind || '' })),
          outcome: null,
        };
        items.push(item);
        approvals.set(d.approvalId, item);
        break;
      }
      case 'approval_resolved':
        if (d.outcome) approvals.get(d.approvalId).outcome = d.outcome;
        break;
      case 'update':
        if (su === 'plan') {
          const entries = (u.entries || []).map((p) => ({ text: p.content || '', status: p.status || '' }));
          const body = { done: entries.filter((p) => p.status === 'completed').length, total: entries.length, entries };
          if (plan) Object.assign(plan, body);
          else { plan = { kind: 'plan', ...body }; items.push(plan); }
        } else if (su === 'tool_call' && !tools.has(u.toolCallId)) {
          if (!group) {
            group = { steps: [], first: at, last: at, item: {} }; // item filled in at the end
            items.push(group.item);
            groups.push(group);
          }
          const step = { kind: ACP_KIND[u.kind] || 'other', target: u.title || '', status: stepStatus(u.status), detail: null };
          group.steps.push(step);
          group.last = at;
          tools.set(u.toolCallId, { step, group, updates: [u] });
        } else {
          // tool_call_update, or a tool_call repeated for a known id: the fields it carries win
          const { step, group: g, updates } = tools.get(u.toolCallId);
          updates.push(u);
          if (u.title) step.target = u.title;
          if (u.kind) step.kind = ACP_KIND[u.kind] || 'other';
          if (u.status) step.status = stepStatus(u.status);
          g.last = at;
        }
        break;
    }
  }
  for (const { step, updates } of tools.values()) step.detail = liveDetail(updates, step.status);
  for (const g of groups) Object.assign(g.item, stepsItem(g.steps, seconds(g.first, g.last)));
  return items;
}

// buildTimeline merges history turns and events into display items, in order.
// now is not used yet: no item's seconds depend on the current time.
export function buildTimeline({ turns = [], events = [], now }) { // eslint-disable-line no-unused-vars
  // A reconnecting client may hand the same event twice (a snapshot overlapping
  // the stream): each seq is kept once, the first copy. An event of no turn
  // (a queue snapshot) belongs to no turn and is left out.
  const seen = new Set();
  const evs = events.filter((e) => e.turn && !seen.has(e.seq) && seen.add(e.seq)).sort((a, b) => a.seq - b.seq);
  const byTurn = new Map();
  for (const e of evs) {
    if (!byTurn.has(e.turn)) byTurn.set(e.turn, []);
    byTurn.get(e.turn).push(e);
  }
  const endOf = (g) => g.find(isEnd);

  // The running turn is the latest one that has not ended.
  let running = null;
  for (let i = evs.length - 1; i >= 0; i--) {
    const g = byTurn.get(evs[i].turn);
    if (!endOf(g)) { running = g; break; }
  }

  const T = (t) => apiDate(t.time);
  let hist = turns.slice();
  const runStart = running && running[0].kind === 'turn_started' ? running[0] : null;
  if (runStart && hist.length) {
    const last = hist[hist.length - 1];
    if (samePrompt(last.user, startText(runStart)) && T(last) >= apiDate(runStart.time) - WINDOW) hist = hist.slice(0, -1);
  }

  const approvals = new Map();
  const after = hist.map(() => []); // items placed after history turn i
  const atEnd = []; // items after all history, before the running turn
  const claimed = new Set();
  const ended = [...byTurn.values()].filter(endOf).sort((a, b) => endOf(a).seq - endOf(b).seq);
  for (const g of ended) {
    const end = endOf(g);
    const endAt = apiDate(end.time);
    const ed = end.data || {};
    const terminal = end.kind === 'turn_error'
      ? [{ kind: 'error', text: ed.error || '' }]
      : ed.stopReason === 'cancelled' ? [{ kind: 'stopped' }] : [];
    const start = g.find((e) => e.kind === 'turn_started');
    let i = -1;
    if (start) {
      const from = apiDate(start.time) - WINDOW;
      i = hist.findIndex((t, j) => !claimed.has(j) && samePrompt(t.user, startText(start)) && T(t) >= from && T(t) <= endAt + WINDOW);
    } else {
      for (let j = hist.length - 1; j >= 0; j--) {
        if (!claimed.has(j) && T(hist[j]) <= endAt + WINDOW) { i = j; break; }
      }
    }
    if (i >= 0) {
      claimed.add(i);
      after[i].push(...terminal);
    } else if (start) {
      // Not in the history: drawn from its events, in time among the turns
      // shown, and left out when it is older than all of them.
      const t0 = apiDate(start.time);
      if (hist.length && t0 < T(hist[0])) continue;
      const k = hist.findIndex((t) => T(t) > t0);
      (k > 0 ? after[k - 1] : atEnd).push(...turnItems(g, approvals), ...terminal);
    } else if (!hist.length || endAt >= T(hist[0])) {
      atEnd.push(...terminal);
    }
  }

  const items = [];
  hist.forEach((t, i) => items.push(...historyItems(t), ...after[i]));
  items.push(...atEnd);
  if (running) items.push(...turnItems(running, approvals));

  let pendingApproval = null;
  for (const it of items) if (it.kind === 'approval' && it.outcome === null) pendingApproval = it.approvalId;
  return { items, working: running !== null, pendingApproval };
}

const WORKING_PREFIX = 'The user sent a new message while you were working:';

// unwrapSavedPrompt is a prompt as Claude Code saved it without the wrappers it adds: the prefix of
// a message typed while it worked, and the tags around a long paste (the pasted text stays). The
// rules are pinned by api/testdata/clients/terminal/saved-prompt-match.json.
export function unwrapSavedPrompt(saved) {
  let text = (saved || '').trim();
  if (text.startsWith(WORKING_PREFIX)) text = text.slice(WORKING_PREFIX.length);
  return text.replace(/<\/?pasted_content\b[^>]*>/g, '').trim();
}

// The tags Claude Code wraps a slash command, bash mode and their output in, written exactly so.
const COMMAND_TAG = /<(command-name|command-message|command-args|local-command-stdout|local-command-stderr|command-stdout|command-stderr|bash-input|bash-stdout|bash-stderr|local-command-caveat)>/g;
const ANSI_COLOUR = /\x1b\[[0-9;]*m/g;

// savedParts splits an unwrapped message into text runs (as saved, untrimmed), commands
// ({text, args}) and outputs ({text, error}), in order. Pinned by display/user-bubble.json.
function savedParts(message) {
  const parts = [];
  let run = '';
  let pos = 0;
  let argsFor = null; // the command from <command-name> that may still take <command-args>
  const push = (part) => {
    if (run.trim()) parts.push({ kind: 'text', text: run });
    run = '';
    parts.push(part);
    argsFor = null;
  };
  const re = new RegExp(COMMAND_TAG.source, 'g');
  for (let m = re.exec(message); m; m = re.exec(message)) {
    const tag = m[1];
    const close = '</' + tag + '>';
    const end = message.indexOf(close, re.lastIndex);
    if (end < 0) continue; // never closed: the tag stays text
    const body = message.slice(re.lastIndex, end);
    run += message.slice(pos, m.index);
    pos = end + close.length;
    re.lastIndex = pos;
    if (tag === 'command-message' || tag === 'local-command-caveat') continue;
    if (tag === 'command-args') {
      if (argsFor && !run.trim()) {
        argsFor.args = body.trim();
        argsFor = null;
        run = '';
      } else {
        run += body;
      }
      continue;
    }
    if (tag === 'command-name' || tag === 'bash-input') {
      const name = body.trim();
      if (!name) continue;
      const command = { kind: 'command', text: tag === 'bash-input' ? '!' + name : name, args: '' };
      push(command);
      if (tag === 'command-name') argsFor = command;
      continue;
    }
    const out = body.replace(ANSI_COLOUR, '').trim();
    if (out) push({ kind: 'output', text: out, error: tag.endsWith('stderr') });
  }
  run += message.slice(pos);
  if (run.trim()) parts.push({ kind: 'text', text: run });
  return parts;
}

// savedPromptAsTyped is a saved prompt as the person typed it: unwrapped, each command as its name
// and args ("/tidy docs only", "!git status"), outputs left out.
export function savedPromptAsTyped(saved) {
  return savedParts(unwrapSavedPrompt(saved))
    .filter((p) => p.kind !== 'output')
    .map((p) => (p.kind === 'command' && p.args ? p.text + ' ' + p.args : p.text))
    .join('\n');
}

// An image Claude Code saved as a placeholder, with the spaces and tabs after it.
const IMAGE_PLACEHOLDER = /\[Image(?: #([0-9]+)|:[^\]\n]*)\][ \t]*/g;

// withoutImages takes each image placeholder out of a text run, adding its chip label ("Image 2",
// or "Image" when it has no number) to images. A line left blank by that is dropped.
function withoutImages(text, images) {
  const lines = [];
  for (const line of text.split('\n')) {
    let found = false;
    const rest = line.replace(IMAGE_PLACEHOLDER, (_, n) => {
      found = true;
      images.push(n === undefined ? 'Image' : 'Image ' + n);
      return '';
    });
    if (!found) { lines.push(line); continue; }
    const kept = rest.replace(/[ \t]+$/, '');
    if (kept) lines.push(kept);
  }
  return lines.join('\n').trim();
}

// userDisplay is how a saved user message shows in its bubble: unwrapped as above, split into
// parts (text, a command, an output), image placeholders out of the text as chip labels. text is
// the text parts joined by a blank line. Pinned by api/testdata/clients/display/user-bubble.json.
export function userDisplay(saved) {
  const images = [];
  const parts = [];
  for (const p of savedParts(unwrapSavedPrompt(saved))) {
    if (p.kind !== 'text') { parts.push(p); continue; }
    const text = withoutImages(p.text, images);
    if (text) parts.push({ kind: 'text', text });
  }
  const text = parts.filter((p) => p.kind === 'text').map((p) => p.text).join('\n\n');
  return { text, images, parts };
}

// pendingShownInHistory says whether a history prompt is the message sent from here, once Claude
// Code's wrappers are removed.
export function pendingShownInHistory(history, text) {
  return samePrompt(savedPromptAsTyped(history), text);
}

// pendingShown says whether the timeline now shows a message sent from here, so it is no longer
// drawn as pending: its turn (from the send's answer) is in the events, or a history turn with its
// prompt (Claude Code's wrappers removed) was recorded no more than 2 s before it was sent, or
// later. A failed send stays.
// pending: {text, turn, sentAt (ms), failure}.
export function pendingShown(pending, events, turns) {
  if (pending.failure) return false;
  if (pending.turn && events.some((e) => e.turn === pending.turn)) return true;
  return turns.some((t) => apiDate(t.time) >= pending.sentAt - WINDOW && pendingShownInHistory(t.user, pending.text));
}
