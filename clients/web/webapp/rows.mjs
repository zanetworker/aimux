import { descriptionLine } from './now.mjs';

// The pieces Now, All and search share: rows, section labels, status dots and times.
// Everything is built as DOM nodes with textContent; session text is never parsed as HTML.

export function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// Isolated text: a title or folder name (perhaps Arabic) that must not reorder what is around it.
export const bidi = (text, cls) => el('bdi', cls, text);

const DOT_LABEL = { working: 'Working', needs: 'Needs you', failed: 'Failed', idle: 'Idle' };

// The only colour on a row: green working, amber needs you, red failed, grey idle.
export function dot(status) {
  const d = el('span', 'dot ' + status);
  d.setAttribute('role', 'img');
  d.setAttribute('aria-label', DOT_LABEL[status]);
  return d;
}

export const stateDot = (state) => (state === 'busy' ? 'working' : 'idle');

// A session with no real title is "Session <uuid>" or the bare id: show "Session " + the id's first 8 characters.
const UUID = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
const ID_ONLY = new RegExp('^(Session )?' + UUID + '$', 'i');
export function displayTitle(title, id) {
  const t = (title || '').trim();
  return !t || t === id || ID_ONLY.test(t) ? 'Session ' + String(id).slice(0, 8) : t;
}

export function projectName(path) {
  return (path || '').split('/').filter(Boolean).pop() || path || '';
}

const sameDay = (a, b) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

// "now", "5 min", "3 h" (same day), "Yesterday", "Oct 20", "Sep 20, 2025".
export function relativeTime(ms, now = Date.now()) {
  const seconds = (now - ms) / 1000;
  if (!isFinite(seconds)) return '';
  if (seconds < 60) return 'now';
  if (seconds < 3600) return Math.floor(seconds / 60) + ' min';
  const date = new Date(ms), today = new Date(now);
  if (sameDay(date, today)) return Math.floor(seconds / 3600) + ' h';
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  if (sameDay(date, yesterday)) return 'Yesterday';
  const opts = { month: 'short', day: 'numeric' };
  if (date.getFullYear() !== today.getFullYear()) opts.year = 'numeric';
  return date.toLocaleDateString(undefined, opts);
}

// "just now", "4 min ago", "3 h ago"; "Yesterday" and dates as relativeTime has them.
export function sinceText(ms, now = Date.now()) {
  const r = relativeTime(Math.min(ms, now), now);
  if (r === 'now') return 'just now';
  return /^\d+ (min|h)$/.test(r) ? r + ' ago' : r;
}

const SVG = 'http://www.w3.org/2000/svg';
const ICONS = {
  lock: 'M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z',
  chevron: 'M9 6l6 6-6 6',
  // Now's folder rows and the row menu
  folder: 'M3.5 7A1.5 1.5 0 0 1 5 5.5h4l2 2h8A1.5 1.5 0 0 1 20.5 9v8.5A1.5 1.5 0 0 1 19 19H5a1.5 1.5 0 0 1-1.5-1.5z',
  more: 'M5.5 12h.01M12 12h.01M18.5 12h.01',
  search: 'M10.5 4.5a6 6 0 1 0 0 12a6 6 0 0 0 0-12zM15 15l4.5 4.5',
  compose: 'M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z',
  // The chat's icons
  back: 'M15 5l-7 7 7 7',
  info: 'M12 11v6M12 7.6v.1M12 3a9 9 0 1 0 0 18a9 9 0 0 0 0-18z',
  read: 'M7 3h7l4 4v14H7zM14 3v4h4M10 12h5M10 16h5',
  edit: 'M4 20l1-4L16 5l3 3L8 19zM14 7l3 3',
  run: 'M4 5h16v14H4zM8 10l3 2-3 2M13 15h3',
  other: 'M12 4a8 8 0 1 0 0 16a8 8 0 0 0 0-16z',
  up: 'M12 19V5M6 11l6-6 6 6',
  stop: 'M7 7h10v10H7z',
  mic: 'M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zM6 11a6 6 0 0 0 12 0M12 17v4',
  check: 'M5 12.5l4.5 4.5L19 7.5',
  cross: 'M6 6l12 12M18 6L6 18',
  // The chat queue: add to the list, move up or down, delete
  enqueue: 'M4 6h12M4 11h12M4 16h7M17 14v7M13.5 17.5h7',
  moveUp: 'M6 15l6-6 6 6',
  moveDown: 'M6 9l6 6 6-6',
  trash: 'M5 7h14M10 7V4.5h4V7M7 7l1 13h8l1-13',
  // Media cards: a play button in a frame, a picture, a waveform
  play: 'M3 5h18v14H3zM10 9v6l5-3z',
  photo: 'M4 5h16v14H4zM4 16l5-5 4 4 2.5-2.5L20 17M15.5 8.5v.1',
  waveform: 'M4 10v4M8 7v10M12 4v16M16 8v8M20 11v2',
};

export function icon(name, cls) {
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('class', 'icon ' + (cls || name));
  const path = document.createElementNS(SVG, 'path');
  path.setAttribute('d', ICONS[name]);
  svg.append(path);
  return svg;
}

// "project · rest": each part isolated, the rest optionally in monospace.
export function projectLine(project, rest, restClass) {
  const line = el('span', 'detail');
  line.append(bidi(projectName(project)));
  if (rest) line.append(' · ', bidi(rest, restClass));
  return line;
}

// The session's description as one quiet line under the title (truncated by CSS); null for none.
export function descriptionNode(text) {
  if (!text) return null;
  const d = el('span', 'desc', text);
  d.dir = 'auto';
  return d;
}

// Dot, title, the description, one secondary line and the time: the shape every session row shares.
export function dotRow({ status, title, description, detail, time, locked, onClick, tag = 'button', cls = '' }) {
  const row = el(tag, ('row ' + cls).trim());
  if (tag === 'button') row.type = 'button';
  const text = el('span', 'text');
  const t = el('span', 'title', title);
  t.dir = 'auto';
  text.append(t);
  const desc = descriptionNode(description);
  if (desc) text.append(desc);
  if (detail) text.append(typeof detail === 'string' ? el('span', 'detail', detail) : detail);
  row.append(dot(status), text);
  if (locked) {
    const lock = icon('lock');
    lock.setAttribute('aria-label', 'Open on your Mac');
    lock.removeAttribute('aria-hidden');
    row.append(lock);
  }
  if (time !== undefined) row.append(el('span', 'time', relativeTime(time)));
  if (onClick) row.addEventListener('click', onClick);
  return row;
}

// A row in All and in search: title, then "project · last prompt"; a lock when open in a terminal.
export function sessionRow(session, onClick) {
  return dotRow({
    status: stateDot(session.state), title: displayTitle(session.title, session.id), description: descriptionLine(session),
    detail: projectLine(session.project, session.lastPrompt), time: session.lastActive,
    locked: session.state === 'open_in_terminal', onClick,
  });
}

// A small uppercase label over a group, with an optional detail and something on the right.
export function sectionLabel(title, detail, trailing) {
  const label = el('div', 'label');
  const left = el('span', 'label-text');
  left.append(el('span', 'label-title', title));
  if (detail) left.append(' · ' + detail);
  label.append(left);
  if (trailing !== undefined && trailing !== null) label.append(typeof trailing === 'string' ? el('span', 'label-trailing', trailing) : trailing);
  return label;
}

// A grouped list: rounded rows on the grouped background.
export function card(...rows) {
  const c = el('div', 'card');
  c.append(...rows);
  return c;
}

export function section(label, ...rows) {
  const s = el('section', 'group');
  if (label) s.append(label);
  s.append(card(...rows));
  return s;
}

export function errorBanner(title, detail, retry) {
  const b = el('div', 'card banner');
  b.setAttribute('role', 'alert');
  b.append(el('div', 'banner-title', title), el('div', 'banner-detail', detail));
  const again = el('button', 'pill', 'Try again');
  again.type = 'button';
  again.addEventListener('click', retry);
  b.append(again);
  return b;
}

// The top of Now, All and search: the large title with its button on one line, then what sits under it.
export function screenHeader(title, trailing, below) {
  const h = el('div', 'screen-header');
  const top = el('div', 'screen-title-row');
  top.append(el('h1', 'screen-title', title));
  if (trailing) top.append(trailing);
  h.append(top);
  if (below) h.append(below);
  return h;
}
