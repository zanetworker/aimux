import { descriptionLine } from './now.mjs';
import { displayTitle, el, bidi, dot, dotRow, descriptionNode, projectLine, projectName, relativeTime, sectionLabel, section, sinceText, icon, errorBanner, screenHeader } from './rows.mjs';

// Now: the session last in front on the Mac, the pinned ones, what needs you, what just finished,
// what is working, and what sits open on your Mac.
// ui: { collapsed: Set<project>, openCards: Set<approvalId>, menuFor: session id or null,
//       focus: data-focus to focus after the next render, rendering: a render is replacing the
//       rows, pointerDown: a pointer is pressed, announce(text): the persistent live region, open(session, seen), showAll(filter),
//       rerender(), headerButton: Node }

const BUSY_ABOVE = 5;

export function renderNow(model, ui) {
  const v = model.view;
  const quiet = !v.needsYou.length && !v.finished.length && !v.summary.working;
  const busy = v.needsYou.length + v.finished.length + v.summary.working > BUSY_ABOVE;
  const out = [screenHeader('Now', ui.headerButton, summaryLine(v.summary, quiet, model.loaded))];
  if (model.loadError) {
    out.push(errorBanner('Could not reach your Mac', model.loadError.message, () => ui.refresh()));
  }
  if (v.lastOnMac) {
    const { session } = v.lastOnMac;
    out.push(section(sectionLabel('Last on your Mac', sinceText(model.lastOnMac.at)), ...withMenu(model, ui, session, placedRow(v.lastOnMac, ui))));
  }
  if (v.pinned.length) {
    out.push(section(sectionLabel('Pinned'), ...v.pinned.flatMap((item) => withMenu(model, ui, item.session, placedRow(item, ui)))));
  }
  out.push(...needsYou(model, ui, busy));
  if (v.finished.length) {
    out.push(section(sectionLabel('Just finished'), ...v.finished.flatMap((item) => withMenu(model, ui, item.session, dotRow({
      status: 'idle', title: displayTitle(item.session.title, item.session.id), description: descriptionLine(item.session),
      time: item.activity.at, detail: finishedLine(item.session.project), onClick: () => ui.open(item.session, true),
    })))));
  }
  out.push(...working(model, v, ui));
  if (v.onMacTotal > 0) {
    const all = el('button', 'link', 'All ' + v.onMacTotal);
    all.type = 'button';
    all.addEventListener('click', () => ui.showAll({ onMacOnly: true }));
    const label = sectionLabel('On your Mac', 'read-only, by project', all);
    if (v.onMacFolders.length) {
      out.push(section(label, ...v.onMacFolders.map((g) => (g.count === 1
        ? singleFolder(model, ui, g, ON_MAC_ROWS) : folder(model, ui, g, ON_MAC_ROWS)))));
    } else {
      const s = el('section', 'group');
      s.append(label);
      out.push(s);
    }
  }
  return out;
}

// A Last on your Mac or Pinned row: the session's own status dot, its description, "project · step".
function placedRow(item, ui) {
  const { session } = item;
  const detail = item.status === 'working' ? stepLine(item, true) : projectLine(session.project);
  return dotRow({
    status: item.status, title: displayTitle(session.title, session.id), description: descriptionLine(session),
    detail, time: session.lastActive, locked: session.state === 'open_in_terminal' && item.status !== 'working',
    onClick: () => ui.open(session),
  });
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

// "2 need you · 2 working · 14 on your Mac": counts in bold, only "need you" amber;
// "Nothing needs you" when nothing needs you, finished or is working.
function summaryLine(counts, quiet, loaded) {
  const line = el('p', 'summary');
  if (quiet) {
    line.textContent = loaded ? 'Nothing needs you' : ' ';
    return line;
  }
  const parts = [];
  const part = (n, label, cls) => {
    const p = el('span', cls);
    p.append(el('b', '', String(n)), ' ' + label);
    return p;
  };
  if (counts.needYou) parts.push(part(counts.needYou, 'need you', 'amber'));
  if (counts.working) parts.push(part(counts.working, 'working'));
  if (counts.onMac) parts.push(part(counts.onMac, 'on your Mac'));
  parts.forEach((p, i) => line.append(...(i ? [' · ', p] : [p])));
  return line;
}

function finishedLine(project) {
  const d = el('span', 'detail', 'Finished · ');
  d.append(bidi(projectName(project)));
  return d;
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
      const pending = model.answer(a, o.optionId);
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

// "project · live step": the step in monospace, or "Thinking", or "Active".
function stepLine(item, withProject) {
  const step = item.activity.kind === 'working' ? item.activity.step : undefined;
  const text = step ?? 'Active';
  const cls = step && step !== 'Thinking' ? 'mono' : '';
  if (withProject) return projectLine(item.session.project, text, cls);
  const d = el('span', 'detail');
  d.append(bidi(text, cls));
  return d;
}

function working(model, v, ui) {
  const w = v.working;
  if (w.list) {
    if (!w.list.length) return [];
    return [section(sectionLabel('Working'), ...w.list.flatMap((item) => withMenu(model, ui, item.session, dotRow({
      status: 'working', title: displayTitle(item.session.title, item.session.id), description: descriptionLine(item.session),
      time: item.session.lastActive, detail: stepLine(item, true), onClick: () => ui.open(item.session),
    }))))];
  }
  const rows = w.groups.map((g) => (g.count === 1 ? singleFolder(model, ui, g) : folder(model, ui, g)));
  if (w.more) {
    const more = el('button', 'row folder-row');
    more.type = 'button';
    more.dataset.focus = 'more-projects';
    const text = el('span', 'text one');
    text.append(el('span', 'group-name', w.more.projects === 1 ? '1 more project' : w.more.projects + ' more projects'));
    more.append(icon('folder'), text, el('span', 'count', String(w.more.sessions)), icon('chevron'));
    more.addEventListener('click', () => ui.showAll({ sort: 'mostActive' }));
    rows.push(more);
  }
  return [section(sectionLabel('Working', v.summary.working + ', by project'), ...rows)];
}

// How folder rows show their sessions: Working's with the working dot and the current step;
// On your Mac's idle, without a step, and collapsed apart from a Working folder of the same project.
const WORKING_ROWS = { status: 'working', steps: true, key: '' };
const ON_MAC_ROWS = { status: 'idle', steps: false, key: 'mac:' };

// A folder: its icon, the name in semibold, the count and a chevron; open, its sessions indented
// under a guide line. Folders start open; ui.collapsed holds the ones closed.
function folder(model, ui, g, rows = WORKING_ROWS) {
  const key = rows.key + g.project;
  const isOpen = !ui.collapsed.has(key);
  const box = el('div', 'folder');
  const head = el('button', 'row folder-row' + (isOpen ? ' open' : ''));
  head.type = 'button';
  head.dataset.focus = 'folder:' + key;
  head.setAttribute('aria-expanded', String(isOpen));
  const text = el('span', 'text one');
  text.append(bidi(g.name, 'group-name'));
  if (!isOpen) {
    const newest = el('span', 'detail inline');
    newest.append(' · ', bidi(displayTitle(g.newest.session.title, g.newest.session.id)));
    text.append(newest);
  }
  head.append(icon('folder'), text, el('span', 'count', String(g.count)), icon('chevron'));
  head.addEventListener('click', () => {
    if (isOpen) ui.collapsed.add(key); else ui.collapsed.delete(key);
    ui.rerender();
  });
  box.append(head);
  if (isOpen) {
    const items = el('div', 'folder-items');
    for (const item of g.items) items.append(...withMenu(model, ui, item.session, compactRow(item, ui, rows)));
    box.append(items);
  }
  return box;
}

// A folder of one session: the folder icon, its name and the session inline, opening the session.
function singleFolder(model, ui, g, rows = WORKING_ROWS) {
  const item = g.items[0];
  const r = el('button', 'row folder-row single');
  r.type = 'button';
  const t = el('span', 'text');
  const line = el('span', 'one');
  const title = bidi(displayTitle(item.session.title, item.session.id), 'compact-title');
  title.dir = 'auto';
  line.append(bidi(g.name, 'group-name'), el('span', 'detail inline', ' · '), title);
  t.append(line);
  const desc = descriptionNode(descriptionLine(item.session));
  if (desc) t.append(desc);
  if (rows.steps) t.append(stepLine(item, false));
  r.append(icon('folder'), t, el('span', 'time', relativeTime(item.session.lastActive)));
  r.addEventListener('click', () => ui.open(item.session));
  const box = el('div', 'folder');
  box.append(...withMenu(model, ui, item.session, r));
  return box;
}

// A session inside an open folder: dot, title · step, its description under them, the time.
function compactRow(item, ui, rows = WORKING_ROWS) {
  const r = el('button', 'row compact');
  r.type = 'button';
  const t = el('span', 'text');
  const line = el('span', 'one');
  const title = bidi(displayTitle(item.session.title, item.session.id), 'compact-title');
  title.dir = 'auto';
  line.append(title);
  if (rows.steps) {
    const step = stepLine(item, false);
    step.className = 'detail inline';
    step.prepend(' · ');
    line.append(step);
  }
  t.append(line);
  const desc = descriptionNode(descriptionLine(item.session));
  if (desc) t.append(desc);
  r.append(dot(rows.status), t, el('span', 'time', relativeTime(item.session.lastActive)));
  r.addEventListener('click', () => ui.open(item.session));
  return r;
}
