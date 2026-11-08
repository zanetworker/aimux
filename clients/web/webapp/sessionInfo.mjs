import { apiDate } from './apiDate.mjs';
import { el } from './rows.mjs';

// The session info sheet: cost, tokens, model and timing from SessionDetail.stats, formatted like
// StatsFormat in the iPhone app.

const MISSING = 'Not available';

// "$0.42"; "<$0.01" for a cost below a cent.
export function formatCost(usd) {
  if (usd > 0 && usd < 0.005) return '<$0.01';
  return '$' + usd.toFixed(2);
}

// "999", "34k", "1.2M".
export function formatTokens(count) {
  if (count < 1000) return String(count);
  if (count < 999_500) return Math.round(count / 1000) + 'k';
  const millions = Math.round(count / 100_000) / 10;
  return (Number.isInteger(millions) ? String(millions) : millions.toFixed(1)) + 'M';
}

// "12m", "1h 12m", "2h"; "<1m" under half a minute.
export function formatDuration(minutes) {
  const whole = Math.round(minutes);
  if (whole < 1) return '<1m';
  const hours = Math.floor(whole / 60), rest = whole % 60;
  if (!hours) return rest + 'm';
  return rest ? `${hours}h ${rest}m` : `${hours}h`;
}

// "just now", "5m ago", "14h ago", "3d ago".
export function ago(ms, now = Date.now()) {
  const minutes = Math.floor((now - ms) / 60000);
  if (!(minutes >= 1)) return 'just now';
  if (minutes < 60) return minutes + 'm ago';
  if (minutes < 60 * 24) return Math.floor(minutes / 60) + 'h ago';
  return Math.floor(minutes / (60 * 24)) + 'd ago';
}

// "Oct 26, 09:14".
export const infoTime = (ms) => new Date(ms).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });

// The sheet's rows in three groups: [{label, value, mono}]; a value the server did not give is
// "Not available". time formats epoch milliseconds.
export function infoSections(detail, time = infoTime) {
  const s = detail && detail.stats;
  const at = (text) => { const ms = apiDate(text || ''); return Number.isFinite(ms) ? time(ms) : null; };
  const row = (label, value, mono = false) => {
    const shown = value === null || value === undefined || value === '' ? null : value;
    return { label, value: shown ?? MISSING, mono: mono && shown !== null };
  };
  return [
    [
      row('Cost', s ? formatCost(s.costUsd) : null),
      row('Tokens in', s ? formatTokens(s.tokensIn) : null),
      row('Tokens out', s ? formatTokens(s.tokensOut) : null),
      row('Cache read', s ? formatTokens(s.cacheReadTokens) : null),
      row('Cache write', s ? formatTokens(s.cacheWriteTokens) : null),
    ],
    [
      row('Model', s ? s.model : null),
      row('Turns', s ? String(s.turns) : null),
      row('Duration', s ? formatDuration(s.durationMin) : null),
      row('Started', s ? at(s.startTime) : null),
      row('Last active', at(detail && detail.lastActive)),
    ],
    [
      row('Branch', s ? s.gitBranch : null, true),
      row('Folder', detail && detail.project, true),
    ],
  ];
}

// Update summary's errors, one fixed line per code (the server's text is never shown).
const DESCRIBE_ERRORS = {
  describe_running: 'An update for this session is already running.',
  owner_set: 'You wrote both the title and the description, so there is nothing to update.',
  no_content: 'This session has no messages to summarise yet.',
  naming_timeout: 'The model took too long. Try again.',
  naming_auth: "The model API refused the Mac's API key.",
  naming_model: 'The naming model was not found. Check sessions.title_model on the Mac.',
  naming_unreadable: "The model's answer could not be read. Try again.",
  naming_failed: 'The model call failed. Try again later.',
  naming_unavailable: 'The Mac has no API key and no claude command to make a summary.',
};

export function describeErrorText(e) {
  if (typeof e?.status !== 'number') return 'Could not reach your Mac.';
  return DESCRIBE_ERRORS[e.code] ?? `Could not update the summary (HTTP ${e.status}).`;
}

// "Updated the title and description." / "Updated the description. The title stays as it was."
export function describeResultText(result) {
  const changed = result?.changed ?? [];
  if (!changed.length) return 'Nothing changed.';
  if (changed.length === 2) return 'Updated the title and description.';
  const other = changed[0] === 'title' ? 'description' : 'title';
  return `Updated the ${changed[0]}. The ${other} stays as it was.`;
}

// Health's naming status as one line: "Named 4 of 20 today", or "Naming paused"; null without it.
export function namingLine(naming) {
  if (!naming) return null;
  if (naming.paused) return 'Naming paused';
  return `Named ${naming.today} of ${naming.limit} today`;
}

// Why the server would refuse an owner title or description, or null: it trims, counts
// characters (code points) and refuses control characters.
export function ownerTextError(text, limit) {
  const t = text.trim();
  if (Array.from(t).length > limit) return `At most ${limit} characters.`;
  if (/\p{Cc}/u.test(t)) return 'One line, without control characters.';
  return null;
}

// Pin and Unpin failures as one fixed line (the server's text is never shown).
export function pinErrorText(e, pinned) {
  const verb = pinned ? 'pin' : 'unpin';
  if (typeof e?.status !== 'number') return `Could not ${verb}: your Mac could not be reached.`;
  return `Could not ${verb} (HTTP ${e.status}).`;
}

// Why a Save failed: the server's 400 reasons are fixed text about the field, never its value.
function saveErrorText(e) {
  return typeof e?.status === 'number' ? 'Could not save: ' + e.message : 'Could not reach your Mac.';
}

// What a finished Save announces. An empty value clears only the owner's own: when the shown
// value comes back unchanged, it was the model's or Claude Code's, so nothing was cleared.
export function saveNote(field, draft, original, result) {
  const back = (result?.[field] ?? '').trim();
  if (draft.trim() === '' && back !== '' && back === original.trim()) {
    return `Nothing to clear: this ${field} was not written by you.`;
  }
  return 'Saved';
}

// announce writes a line into a persistent live region; the same line twice still changes the
// text (a no-break space is added or dropped), so a screen reader reads it again.
export function announce(node, text) {
  node.textContent = node.textContent === text ? text + '\u00a0' : text;
}

// The sheet's title, description and pin actions for the one session whose sheet is open. Each
// opened sheet has its own state; an answer that arrives after the sheet was closed or another
// one opened changes only the session (took), never the sheet now shown. Update summary is one at
// a time per session (model.describing), so reopening a sheet while it runs shows it running and
// receives its result.
// model: {editMeta(id, fields), describe(id), describing: Set}; changed(state, focusKey): draw the
// open sheet again; took(id, result): a meta or describe answer for the session; announce(text)
// and alert(text): the persistent live regions.
export function createInfoSheet({ model, changed, took, announce: say, alert }) {
  let current = null;
  const shown = (st) => current === st;
  return {
    get current() { return current; },
    open(id) {
      current = { id, editing: null, draft: '', original: '', saving: false, error: '', note: '', pinning: false, pinError: '' };
      return current;
    },
    close() { current = null; },
    edit(field, value) {
      Object.assign(current, { editing: field, draft: value || '', original: value || '', error: '', note: '' });
      changed(current, 'input:' + field);
    },
    draft(text) { current.draft = text; },
    cancel() {
      const field = current.editing;
      Object.assign(current, { editing: null, error: '' });
      changed(current, 'edit:' + field);
    },
    async save() {
      const st = current;
      if (!st?.editing || st.saving) return;
      const field = st.editing;
      if (st.draft.trim() === st.original.trim()) { // unchanged: never make it the owner's
        st.editing = null;
        changed(st, 'edit:' + field);
        return;
      }
      st.saving = true;
      st.error = '';
      changed(st, 'save:' + field);
      try {
        const result = await model.editMeta(st.id, { [field]: st.draft });
        took(st.id, result);
        st.editing = null;
        if (shown(st)) say(saveNote(field, st.draft, st.original, result));
      } catch (e) {
        st.error = saveErrorText(e);
        if (shown(st)) alert(st.error);
      } finally {
        st.saving = false;
      }
      if (shown(st)) changed(st, st.editing ? 'input:' + field : 'edit:' + field);
    },
    async describe() {
      const st = current;
      if (!st || model.describing.has(st.id)) return;
      st.note = '';
      const running = model.describe(st.id);
      changed(st, 'describe');
      let note;
      try {
        const result = await running;
        took(st.id, result);
        note = describeResultText(result);
      } catch (e) {
        note = describeErrorText(e);
      }
      // The sheet of this session now shown, if any: the one that asked, or the same session reopened.
      const now = current && current.id === st.id ? current : null;
      if (!now) return;
      now.note = note;
      say(note);
      changed(now, 'describe');
    },
    async pin(pinned) {
      const st = current;
      if (!st || st.pinning) return;
      Object.assign(st, { pinning: true, pinError: '', note: '' });
      changed(st, 'pin');
      try {
        took(st.id, await model.editMeta(st.id, { pinned }));
        if (shown(st)) say(pinned ? 'Pinned' : 'Unpinned');
      } catch (e) {
        st.pinError = pinErrorText(e, pinned);
        if (shown(st)) say(st.pinError);
      } finally {
        st.pinning = false;
      }
      if (shown(st)) changed(st, 'pin');
    },
  };
}

export const TITLE_LIMIT = 80;
export const DESCRIPTION_LIMIT = 300;

// The sheet's first card: the title and description with Edit, Update summary, Pin, and today's
// naming count. meta: {title, description, pinned} as shown; state: a createInfoSheet state;
// describing: an Update summary runs for the session; canEdit: the server has the describe
// capability; on: {edit(field), draft(text), save(), cancel(), describe(), pin()}. Controls carry
// data-focus so a redraw can give focus back; a control whose action runs stays focusable and
// says so with aria-disabled. The lines here are visible only: announcements go to the page's
// persistent live regions.
export function renderMetaCard({ meta, state, describing, canEdit, naming, on }) {
  const out = [];
  const card = el('div', 'card info-card meta-card');
  card.append(
    metaField('title', 'Title', meta.title, TITLE_LIMIT, { meta, state, canEdit, on }),
    metaField('description', 'Description', meta.description || '', DESCRIPTION_LIMIT, { meta, state, canEdit, on }),
  );
  out.push(card);
  if (!canEdit) return out;

  const actions = el('div', 'card info-card');
  const update = busyButton('info-action', describing ? 'Updating summary…' : 'Update summary', 'describe', describing, on.describe);
  const pin = busyButton('info-action', meta.pinned ? 'Unpin' : 'Pin', 'pin', state.pinning, on.pin);
  actions.append(update, pin);
  out.push(actions);
  const status = el('p', 'info-hint meta-status', state.note || state.pinError || '');
  out.push(status);
  const hint = ['Update summary sends up to 1,200 characters of this session to Anthropic.'];
  const line = namingLine(naming);
  if (line) hint.unshift(line + '.');
  out.push(el('p', 'info-hint', hint.join(' ')));
  return out;
}

// A button whose action may be running: then it says so (aria-disabled) and ignores taps, but
// keeps focus, which a disabled button would drop.
function busyButton(cls, text, focusKey, busy, action) {
  const b = el('button', cls, text);
  b.type = 'button';
  b.dataset.focus = focusKey;
  if (busy) b.setAttribute('aria-disabled', 'true');
  b.addEventListener('click', () => { if (!busy) action(); });
  return b;
}

function metaField(field, label, value, limit, { state, canEdit, on }) {
  const box = el('div', 'meta-field');
  const head = el('div', 'meta-head');
  head.append(el('span', 'info-label', label));
  if (state.editing !== field) {
    if (canEdit) {
      const edit = el('button', 'meta-edit', 'Edit');
      edit.type = 'button';
      edit.dataset.focus = 'edit:' + field;
      edit.setAttribute('aria-label', 'Edit ' + label.toLowerCase());
      edit.disabled = state.editing !== null;
      edit.addEventListener('click', () => on.edit(field));
      head.append(edit);
    }
    box.append(head);
    const shown = el('p', 'meta-value' + (value ? '' : ' empty'), value || 'None yet');
    shown.dir = 'auto';
    box.append(shown);
    return box;
  }
  box.append(head);
  const input = el(field === 'title' ? 'input' : 'textarea', 'meta-input');
  if (field === 'title') input.type = 'text';
  else input.rows = 3;
  input.value = state.draft;
  input.dir = 'auto';
  input.dataset.focus = 'input:' + field;
  input.setAttribute('aria-label', label);
  const count = el('span', 'meta-count');
  // Why it can't be saved: tied to the field (read with it), not a live region; a failed Save is
  // announced through the page's alert region.
  const problem = el('p', 'meta-problem');
  problem.id = 'meta-problem-' + field;
  input.setAttribute('aria-describedby', problem.id);
  const save = busyButton('meta-save', state.saving ? 'Saving…' : 'Save', 'save:' + field, state.saving, () => on.save());
  const cancel = el('button', 'meta-cancel', 'Cancel');
  cancel.type = 'button';
  // Updated in place while typing, so the field is never redrawn under the cursor.
  const check = () => {
    const n = Array.from(input.value.trim()).length;
    count.textContent = n + '/' + limit;
    const why = ownerTextError(input.value, limit);
    problem.textContent = why || state.error || '';
    if (why) input.setAttribute('aria-invalid', 'true'); else input.removeAttribute('aria-invalid');
    save.disabled = !!why;
  };
  input.addEventListener('input', () => { state.error = ''; on.draft(input.value); check(); });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !e.isComposing) { e.preventDefault(); e.stopPropagation(); on.cancel(); }
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (!save.disabled) on.save(); }
  });
  cancel.addEventListener('click', on.cancel);
  const buttons = el('div', 'meta-buttons');
  buttons.append(count, cancel, save);
  box.append(input, el('p', 'meta-note', 'Empty uses the automatic ' + label.toLowerCase() + '.'), problem, buttons);
  check();
  return box;
}

// The sheet as a modal dialog: the title, Done, and the grouped rows; `top` (nodes) goes first
// and `extra` (nodes) last.
export function renderInfoSheet({ title, detail, onClose, top = [], extra = [] }) {
  const backdrop = el('div', 'sheet-backdrop');
  const sheet = el('div', 'sheet');
  sheet.setAttribute('role', 'dialog');
  sheet.setAttribute('aria-modal', 'true');
  sheet.setAttribute('aria-label', 'Session info');
  const head = el('div', 'sheet-head');
  const h = el('h2', 'sheet-title');
  h.append(el('bdi', '', title));
  h.dir = 'auto';
  const done = el('button', 'sheet-done', 'Done');
  done.type = 'button';
  done.addEventListener('click', onClose);
  head.append(h, done);
  sheet.append(head, ...top);
  for (const rows of infoSections(detail)) {
    const card = el('div', 'card info-card');
    for (const r of rows) {
      const line = el('div', 'info-row');
      const value = el('span', 'info-value' + (r.mono ? ' mono' : ''));
      value.append(el('bdi', '', r.value));
      line.append(el('span', 'info-label', r.label), value);
      card.append(line);
    }
    sheet.append(card);
  }
  sheet.append(...extra);
  backdrop.append(sheet);
  backdrop.addEventListener('click', (e) => { if (e.target === backdrop) onClose(); });
  backdrop.addEventListener('keydown', (e) => { if (e.key === 'Escape') onClose(); });
  return { node: backdrop, focus: () => done.focus() };
}
