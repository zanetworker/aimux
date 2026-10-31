import { bidi, card, displayTitle, dot, dotRow, el, errorBanner, foldLine, icon, isOpenChoice, projectName, relativeTime, rowPlace, rowState, saveChoice, section, sectionLabel, sessionLine, tabKey } from './rows.mjs';

// Now: what needs you, the pinned sessions that are not open, the Mac's iTerm2 windows and tabs as
// they are on the Mac, the open sessions elsewhere and, with a query, Other sessions. The search box
// and the Working chip sit in the header (tabs.mjs), which keeps them across renders.
// ui: { openCards: Set<approvalId>, menuFor: session id or null, focus: data-focus to focus after
//       the next render, rendering: a render is replacing the rows, pointerDown: a pointer is
//       pressed, announce(text): the persistent live region, open(session), rerender(), refresh(),
//       storage: localStorage or a stand-in, conversations(shownIds): the In conversations section }

const BUSY_ABOVE = 5;
const TABS_KEY = 'openTabs';

export function renderNow(model, ui) {
  const v = model.view;
  const filtered = queryText(model) !== '' || model.workingOnly;
  const busy = v.needsYou.length + v.working > BUSY_ABOVE;
  const body = [];
  if (model.loadError) body.push(errorBanner('Could not reach your Mac', model.loadError.message, () => ui.refresh()));
  body.push(...needsYou(model, ui, busy));
  const row = (session, inPane) => withMenu(model, ui, session, nowRow(model, ui, session, inPane));
  if (v.pinned.length) body.push(section(sectionLabel('Pinned'), ...v.pinned.flatMap((i) => row(i.session, false))));
  v.windows.forEach((w, n) => {
    const s = el('section', 'group');
    if (n === 0) s.append(sectionLabel('On your Mac'));
    const name = el('h2', 'window-name');
    name.append(bidi(w.name));
    s.append(name, card(...w.tabs.flatMap((t) => tabRows(model, ui, w, t, filtered, row))));
    if (n === v.windows.length - 1 && v.hiddenTabs) s.append(hiddenLine(v.hiddenTabs));
    body.push(s);
  });
  if (!v.windows.length && v.hiddenTabs) {
    const s = el('section', 'group');
    s.append(sectionLabel('On your Mac'), hiddenLine(v.hiddenTabs));
    body.push(s);
  }
  if (v.elsewhere.length) {
    const label = !v.windows.length && !v.hiddenTabs ? 'On your Mac' : 'Elsewhere';
    body.push(section(sectionLabel(label), ...v.elsewhere.flatMap((g) => g.sessions).flatMap((s) => row(s, false))));
  }
  if (v.other.length) body.push(section(sectionLabel('Other sessions'), ...v.other.flatMap((s) => row(s, false))));
  body.push(...ui.conversations(shownIds(v)));
  return { summary: summaryLine(model, v), chip: workingChip(model, ui, v), body };
}

const queryText = (model) => model.query.trim();

// A tab as one fold line, then its panes under a guide line when open. A search or the Working chip
// holds every tab left open, and its counts read "1 of 4 panes".
function tabRows(model, ui, w, t, filtered, row) {
  const key = tabKey(w.name, t.index, t.name);
  const open = filtered || isOpenChoice(ui.storage, TABS_KEY, key, t.open);
  const panes = t.panes === 1 ? '1 pane' : t.panes + ' panes';
  const counts = filtered ? t.sessions.length + ' of ' + panes : t.working ? panes + ' · ' + t.working + ' working' : panes;
  const line = foldLine({
    name: t.name, position: 'tab ' + t.index, counts, open, canFold: !filtered, focusKey: 'tab:' + w.id + ':' + t.index,
    onToggle: () => { saveChoice(ui.storage, TABS_KEY, key, !open); ui.rerender(); },
  });
  if (!open) return [line];
  const items = el('div', 'folder-items');
  for (const s of t.sessions) items.append(...row(s, true));
  return [line, items];
}

function hiddenLine(n) {
  return el('p', 'group-note', n === 1 ? '1 tab with no match is hidden' : n + ' tabs with no match are hidden');
}

// A session row: its state line, the quiet place line (with the tab away from its own tab), a pin
// mark when pinned and open in its pane.
function nowRow(model, ui, session, inPane) {
  const activity = model.activity[session.id] ?? { kind: 'none' };
  return sessionLine({
    session, activity, state: rowState(session, activity, model.now()), place: rowPlace(session, !inPane),
    showsPin: inPane && session.pinned === true, onClick: () => ui.open(session),
  });
}

// Every session the screen lists, so conversation hits do not repeat them.
function shownIds(v) {
  return new Set([
    ...v.needsYou.map((i) => i.session.id), ...v.pinned.map((i) => i.session.id),
    ...v.windows.flatMap((w) => w.tabs.flatMap((t) => t.sessions.map((s) => s.id))),
    ...v.elsewhere.flatMap((g) => g.sessions.map((s) => s.id)), ...v.other.map((s) => s.id),
  ]);
}

// "2 need you · 25 open"; with a query "2 open match"; "Nothing open" once loaded with nothing open.
// One quiet line: counts in semibold, no colour.
function summaryLine(model, v) {
  const line = el('p', 'summary');
  const part = (n, label) => {
    const p = el('span');
    p.append(el('b', '', String(n)), ' ' + label);
    return p;
  };
  if (queryText(model)) {
    const n = v.windows.reduce((k, w) => k + w.tabs.reduce((j, t) => j + t.sessions.length, 0), 0)
      + v.elsewhere.reduce((k, g) => k + g.sessions.length, 0);
    line.append(n ? part(n, 'open match') : 'No open match');
    return line;
  }
  if (!model.loaded && !v.open && !v.needsYou.length) {
    line.textContent = ' ';
    return line;
  }
  const parts = [];
  if (v.needsYou.length) parts.push(part(v.needsYou.length, 'need you'));
  parts.push(v.open ? part(v.open, 'open') : 'Nothing open');
  parts.forEach((p, i) => line.append(...(i ? [' · ', p] : [p])));
  return line;
}

// "Working · N": narrows Now to working sessions; shown when anything works or it is on.
function workingChip(model, ui, v) {
  if (!v.working && !model.workingOnly) return null;
  const b = el('button', 'chip' + (model.workingOnly ? ' on' : ''), 'Working · ' + v.working);
  b.type = 'button';
  b.dataset.focus = 'working-chip';
  b.setAttribute('aria-pressed', String(model.workingOnly));
  b.addEventListener('click', () => {
    model.setWorkingOnly(!model.workingOnly);
    ui.focus = 'working-chip';
    ui.rerender();
  });
  return b;
}

// The row with its menu button ("…": Pin or Unpin) beside it, when the server can pin; then the
// menu when it is open, and why the last pin failed. Returns the nodes for the card.
function withMenu(model, ui, session, row) {
  row.dataset.focus = 'row:' + session.id; // focus survives a redraw or a poll
  if (!model.canDescribe) return [row];
  const line = el('div', 'row-line');
  const title = displayTitle(session.title, session.id);
  const more = el('button', 'row-more');
  more.type = 'button';
  more.dataset.focus = 'more:' + session.id;
  more.setAttribute('aria-label', 'More for ' + title);
  more.setAttribute('aria-haspopup', 'menu');
  const isOpen = ui.menuFor === session.id;
  more.setAttribute('aria-expanded', String(isOpen));
  more.append(icon('more'));
  const close = () => {
    ui.menuFor = null;
    ui.focus = 'more:' + session.id;
    ui.rerender();
  };
  more.addEventListener('click', () => {
    const wasOpen = ui.menuFor === session.id; // read now: focus leaving may have closed it in place
    ui.menuFor = wasOpen ? null : session.id;
    ui.focus = wasOpen ? 'more:' + session.id : 'menu:' + session.id;
    ui.rerender();
  });
  line.append(row, more);
  const out = [line];
  if (isOpen) {
    const menu = el('div', 'row-menu');
    menu.setAttribute('role', 'menu');
    menu.setAttribute('aria-label', title);
    const pinned = !session.pinned;
    const pin = el('button', 'row-menu-item', pinned ? 'Pin' : 'Unpin');
    pin.type = 'button';
    pin.setAttribute('role', 'menuitem');
    pin.dataset.focus = 'menu:' + session.id;
    pin.addEventListener('click', async () => {
      if (model.pinning.has(session.id)) return;
      const pending = model.setPinned(session, pinned);
      close();
      await pending;
      const err = model.pinErrors.get(session.id);
      ui.announce(err || (pinned ? 'Pinned ' : 'Unpinned ') + title);
      ui.focus = 'more:' + session.id; // the row may have moved to another section
      ui.rerender();
    });
    menu.append(pin);
    // Escape on the menu or its button closes it; so does focus leaving both.
    for (const node of [more, menu]) {
      node.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') return;
        e.stopPropagation();
        close();
      });
    }
    for (const node of [line, menu]) {
      node.addEventListener('focusout', (e) => {
        if (ui.rendering || ui.menuFor !== session.id) return;
        const to = e.relatedTarget;
        if (to && (line.contains(to) || menu.contains(to))) return;
        ui.menuFor = null;
        more.setAttribute('aria-expanded', 'false');
        // A tap elsewhere: the menu goes invisible but keeps its space until the tap's click, so
        // nothing moves under the finger and the tap lands on the row it began on.
        menu.classList.add('closing');
        if (ui.pointerDown) document.addEventListener('click', () => setTimeout(() => ui.rerender()), { once: true });
        else setTimeout(() => ui.rerender()); // after focus has moved, so the render keeps it
      });
    }
    out.push(menu);
  }
  const err = model.pinErrors.get(session.id);
  if (err) out.push(el('p', 'row-error', err));
  return out;
}

function needsYou(model, ui, busy) {
  const v = model.view;
  const approvals = v.needsYou.filter((i) => i.activity.kind === 'needsApproval');
  const failures = v.needsYou.filter((i) => i.activity.kind === 'failed');
  if (busy) {
    if (!v.needsYou.length) return [];
    const rows = approvals.map((item) => {
      const a = item.activity.approval;
      if (ui.openCards.has(a.approvalId)) return approvalCard(model, ui, item, true);
      return dotRow({
        status: 'needs', title: displayTitle(item.session.title, item.session.id), time: item.session.lastActive, cls: 'approval-row',
        detail: el('span', 'detail strong', a.title || 'a tool call'),
        onClick: () => { ui.openCards.add(a.approvalId); ui.rerender(); },
      });
    });
    rows.push(...failures.map((item) => failedRow(model, ui, item)));
    return [section(sectionLabel('Needs you', String(v.needsYou.length)), ...rows)];
  }
  const out = approvals.map((item) => {
    const s = el('section', 'group');
    s.append(approvalCard(model, ui, item, false));
    return s;
  });
  if (failures.length) out.push(section(null, ...failures.map((item) => failedRow(model, ui, item))));
  return out;
}

// The signature element: the waiting request in large type, its answers as buttons on the card.
function approvalCard(model, ui, item, inList) {
  const { session } = item;
  const a = item.activity.approval;
  const c = el('div', inList ? 'approval-card' : 'card approval-card');
  c.dataset.approval = a.approvalId;

  const top = el('div', 'approval-top');
  const label = el('span', 'needs-label');
  label.append(dot('needs'), el('span', 'amber', 'Needs you'));
  const where = el('span', 'approval-where');
  where.append(bidi(projectName(session.project)), ' · ' + relativeTime(session.lastActive));
  top.append(label, where);

  const titleRow = el('div', 'approval-session');
  const name = el('span', 'approval-session-title', displayTitle(session.title, session.id));
  name.dir = 'auto';
  const open = el('button', 'link', 'Open ›');
  open.type = 'button';
  open.addEventListener('click', () => ui.open(session));
  titleRow.append(name, open);

  const ask = el('p', 'approval-title', a.title || 'a tool call');
  ask.dir = 'auto';
  c.append(top, titleRow, ask);
  const command = a.toolCall && a.toolCall.rawInput && a.toolCall.rawInput.command;
  if (typeof command === 'string' && command) c.append(el('pre', 'approval-command', command));

  const options = el('div', 'options');
  const primary = (a.options || []).find((o) => (o.kind || '').startsWith('allow'));
  const busy = model.answering.has(a.approvalId);
  for (const o of a.options || []) {
    const kind = o === primary ? 'primary' : (o.kind || '').startsWith('reject') ? 'reject' : 'plain';
    const b = el('button', 'option ' + kind, o.name || o.optionId);
    b.type = 'button';
    b.disabled = busy;
    b.addEventListener('click', async () => {
      const pending = model.answer(a, o.optionId, session.id);
      ui.rerender(); // shows the buttons disabled while the answer is on its way
      await pending;
      ui.rerender();
    });
    options.append(b);
  }
  c.append(options);
  const err = model.answerErrors.get(a.approvalId);
  if (err) c.append(el('p', 'row-error', 'Could not answer: ' + err.message));
  return c;
}

// A session whose last turn failed: red dot, the error in one line, and Retry when there is a prompt to resend.
function failedRow(model, ui, item) {
  const { session } = item;
  const wrap = el('div', 'row-wrap');
  const row = el('div', 'row failed');
  const main = dotRow({
    status: 'failed', title: displayTitle(session.title, session.id), tag: 'button', cls: 'row-main',
    detail: el('span', 'detail', 'Failed · ' + (item.activity.error || 'the turn failed')),
    onClick: () => ui.open(session),
  });
  row.append(main);
  if ((session.lastPrompt || '').trim()) {
    const retry = el('button', 'retry', 'Retry');
    retry.type = 'button';
    retry.disabled = model.retrying.has(session.id);
    retry.addEventListener('click', async () => {
      const pending = model.retry(session);
      ui.rerender();
      await pending;
      ui.rerender();
    });
    row.append(retry);
  }
  wrap.append(row);
  const err = model.retryErrors.get(session.id);
  if (err) wrap.append(el('p', 'row-error', 'Could not retry: ' + err.message));
  return wrap;
}
