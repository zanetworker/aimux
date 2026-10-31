// The block tree (markdown.mjs, after references.mjs) as DOM. Every node is built with
// createElement and textContent; only http(s) hrefs are ever set, and links open in a new tab.
// Code is always left to right; paragraphs, headings and list items take their direction from
// their text, so Arabic reads right to left.

import { highlight } from './highlight.mjs';
import { icon } from './rows.mjs';
import { linkReferences, parsedHost } from './references.mjs';
import { parseMarkdown } from './markdown.mjs';

const REF_TITLE = { pr: 'Issue or pull request', commit: 'Commit', jira: 'Jira issue', path: 'File in the repository' };

// href if it is an http(s) address, else null.
export function safeHref(href) {
  return typeof href === 'string' && /^https?:/i.test(href) ? href : null;
}

function h(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function anchor(href, title) {
  const a = h('a');
  a.href = href;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  if (title) a.title = title;
  return a;
}

function code(v) {
  const c = h('code', '', v);
  c.dir = 'ltr';
  return c;
}

function inlines(list, parent) {
  for (const n of list) {
    switch (n.t) {
      case 'text': parent.append(n.v); break;
      case 'code': parent.append(code(n.v)); break;
      case 'br': parent.append(h('br')); break;
      case 'em': case 'strong': case 'del': {
        const e = h(n.t);
        inlines(n.c, e);
        parent.append(e);
        break;
      }
      case 'link': {
        const href = safeHref(n.href);
        if (!href) { inlines(n.c, parent); break; }
        const a = anchor(href);
        inlines(n.c, a);
        parent.append(a);
        break;
      }
      case 'ref': {
        const href = safeHref(n.href);
        const inner = n.code ? code(n.v) : n.v;
        if (!href) { parent.append(inner); break; }
        const a = anchor(href, REF_TITLE[n.kind] || 'Link');
        a.className = 'ref ref-' + n.kind;
        a.append(inner);
        parent.append(a);
        break;
      }
      default:
    }
  }
  return parent;
}

const auto = (e) => { e.dir = 'auto'; return e; };

function textLength(list) {
  let n = 0;
  for (const x of list) n += x.v !== undefined ? x.v.length : x.c ? textLength(x.c) : 1;
  return n;
}

function copyButton(text) {
  const b = h('button', 'md-copy', 'Copy');
  b.type = 'button';
  b.setAttribute('aria-label', 'Copy code');
  let timer = null;
  // The label follows what the button shows, so a screen reader hears the outcome too.
  const show = (shown, label) => { b.textContent = shown; b.setAttribute('aria-label', label); };
  b.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(text);
      show('Copied', 'Copied');
    } catch (_) {
      show('Could not copy', 'Could not copy');
    }
    clearTimeout(timer);
    timer = setTimeout(() => show('Copy', 'Copy code'), 1500);
  });
  return b;
}

function codeBlock(b) {
  const wrap = h('div', 'md-code');
  const head = h('div', 'md-code-head');
  head.append(h('span', 'md-lang', b.lang || 'text'), copyButton(b.text));
  const pre = h('pre');
  pre.dir = 'ltr';
  pre.tabIndex = 0; // a keyboard can scroll a long line
  pre.setAttribute('role', 'region');
  pre.setAttribute('aria-label', 'Code');
  // Syntax colours (highlight.mjs): a span per coloured token, plain text between them.
  const c = code('');
  for (const t of highlight(b.text, b.lang)) c.append(t.k === 'plain' ? t.v : h('span', 'hl-' + t.k, t.v));
  pre.append(c);
  wrap.append(head, pre);
  return wrap;
}

function table(b) {
  const wrap = h('div', 'md-table');
  wrap.tabIndex = 0; // a keyboard can scroll a wide table
  wrap.setAttribute('role', 'region');
  wrap.setAttribute('aria-label', 'Table');
  const t = h('table');
  // A short cell keeps to one line (the table scrolls sideways instead); prose wraps.
  const cell = (tag, inl, i) => {
    const cls = [];
    if (b.align[i] && b.align[i] !== 'left') cls.push('al-' + b.align[i]);
    if (textLength(inl) <= 32) cls.push('nw');
    return inlines(inl, auto(h(tag, cls.join(' '))));
  };
  const thead = h('thead');
  const hr = h('tr');
  b.header.forEach((c, i) => hr.append(cell('th', c, i)));
  thead.append(hr);
  const tbody = h('tbody');
  for (const row of b.rows) {
    const tr = h('tr');
    row.forEach((c, i) => tr.append(cell('td', c, i)));
    tbody.append(tr);
  }
  t.append(thead, tbody);
  wrap.append(t);
  return wrap;
}

function list(b) {
  const l = h(b.ordered ? 'ol' : 'ul');
  if (b.ordered && b.start !== 1) l.start = b.start;
  if (b.items.some((it) => it.task !== null)) l.className = 'has-tasks';
  for (const it of b.items) {
    const li = auto(h('li'));
    if (it.task !== null) {
      li.className = 'task';
      const box = h('input');
      box.type = 'checkbox';
      box.checked = it.task;
      box.disabled = true;
      box.setAttribute('aria-label', it.task ? 'Done' : 'Not done');
      li.append(box);
    }
    li.append(blocks(it.blocks));
    l.append(li);
  }
  return l;
}

function block(b) {
  switch (b.type) {
    case 'heading': return inlines(b.inlines, auto(h('h' + b.level, 'md-h')));
    case 'paragraph': {
      const p = auto(h('p', b.code ? 'md-pre' : ''));
      return inlines(b.inlines, p);
    }
    case 'code': return codeBlock(b);
    case 'list': return list(b);
    case 'quote': {
      const q = h('blockquote');
      q.append(blocks(b.blocks));
      return q;
    }
    case 'table': return table(b);
    case 'rule': return h('hr');
    case 'media': return mediaCard(b);
    default: return document.createDocumentFragment();
  }
}

const MEDIA_ICON = { youtube: 'play', vimeo: 'play', video: 'play', image: 'photo', audio: 'waveform' };

// A media card (README, "Media"): the kind's glyph, the title and the host, opening the address
// in a new tab. Nothing is loaded from it until then (no thumbnail, preview or icon).
function mediaCard(b) {
  const href = safeHref(b.href);
  const card = href ? anchor(href) : h('div');
  card.className = 'md-media md-media-' + b.kind;
  const text = h('span', 'md-media-text');
  const host = h('span', 'md-media-host', parsedHost(b.href) || ''); // the host the tap goes to
  host.dir = 'ltr';
  text.append(auto(h('span', 'md-media-title', b.title)), host);
  card.append(icon(MEDIA_ICON[b.kind] || 'play', 'md-media-glyph'), text);
  return card;
}

function blocks(list) {
  const f = document.createDocumentFragment();
  for (const b of list) f.append(block(b));
  return f;
}

// The block tree as a DocumentFragment.
export function renderBlocks(list) {
  return blocks(list);
}

// Text longer than this is not parsed: streaming parses a reply again on every update, so a huge
// one shows as plain preformatted text instead (README: not pinned above 256 KiB).
export const MAX_FORMAT_CHARS = 256 * 1024;
export const formatsAsMarkdown = (text) => text.length <= MAX_FORMAT_CHARS;

// The text as is: preformatted, wrapped, left to right, nothing parsed.
export function renderPlain(text) {
  const pre = h('pre', 'md-raw', text);
  pre.dir = 'ltr';
  return pre;
}

// Spreads (push(...xs)) are avoided below: an argument list of 100k items overflows the stack.
// A user's message, inline-only: every block becomes paragraphs, keeping its words. A list item
// keeps its marker as text, a quote its "> ", a table row its cells joined by " | "; code becomes
// one code span (shown preformatted) so pasted code still reads as code.
export function bubbleBlocks(list) {
  const out = [];
  const para = (inl) => {
    const merged = [];
    for (const n of inl) {
      const last = merged[merged.length - 1];
      if (n.t === 'text' && last && last.t === 'text') merged[merged.length - 1] = { t: 'text', v: last.v + n.v };
      else if (n.t !== 'text' || n.v) merged.push(n);
    }
    if (merged.length) out.push({ type: 'paragraph', inlines: merged });
  };
  const prefixed = (prefix, inner) => {
    const first = inner[0];
    if (first && first.type === 'paragraph' && !first.code) {
      para([{ t: 'text', v: prefix }].concat(first.inlines));
      for (let i = 1; i < inner.length; i++) out.push(inner[i]);
    } else {
      para([{ t: 'text', v: prefix.trimEnd() }]);
      for (const b of inner) out.push(b);
    }
  };
  for (const b of list) {
    switch (b.type) {
      case 'heading': case 'paragraph': para(b.inlines); break;
      case 'code': out.push({ type: 'paragraph', code: true, inlines: [{ t: 'code', v: b.text }] }); break;
      case 'list':
        b.items.forEach((it, i) => {
          const marker = (b.ordered ? (b.start + i) + '. ' : '• ') + (it.task === null ? '' : it.task ? '[x] ' : '[ ] ');
          prefixed(marker, bubbleBlocks(it.blocks));
        });
        break;
      case 'quote': prefixed('> ', bubbleBlocks(b.blocks)); break;
      case 'table':
        for (const row of [b.header, ...b.rows]) {
          const inl = [];
          row.forEach((c, i) => { if (i) inl.push({ t: 'text', v: ' | ' }); for (const x of c) inl.push(x); });
          para(inl);
        }
        break;
      default:
    }
  }
  return out;
}

// Text in a chat item (a thought, a plan entry, an error), inline-only like a user's message, with
// references linked: the blocks bubbleBlocks gives.
export function inlineBlocks(text, refs) {
  return bubbleBlocks(linkReferences(parseMarkdown(text, { breaks: true }), refs));
}

// A step's target as one line of inlines. A target with no whitespace and no backtick (a path, a
// pattern, a word) is not parsed, so pkg/__init__.py keeps its underscores and **/*.go its stars;
// when it is a path as a whole it is that path reference, drawn in the step's monospace (so without
// the code look). Any other target is inline Markdown, its lines and blocks joined by spaces.
export function stepInlines(target, refs) {
  if (!/[\s`]/.test(target)) {
    const whole = linkReferences([{ type: 'paragraph', inlines: [{ t: 'code', v: target }] }], refs)[0].inlines;
    if (whole.length === 1 && whole[0].t === 'ref') {
      const { code: _, ...ref } = whole[0]; // eslint-disable-line no-unused-vars
      return [ref];
    }
    return target ? [{ t: 'text', v: target }] : [];
  }
  if (!formatsAsMarkdown(target)) return target ? [{ t: 'text', v: target }] : [];
  const out = [];
  const add = (n) => {
    const last = out[out.length - 1];
    if (n.t === 'text' && last && last.t === 'text') out[out.length - 1] = { t: 'text', v: last.v + n.v };
    else out.push(n);
  };
  for (const b of inlineBlocks(target, refs)) {
    if (out.length) add({ t: 'text', v: ' ' });
    for (const n of b.inlines) add(n.t === 'br' ? { t: 'text', v: ' ' } : n);
  }
  return out;
}

// Step inlines for the inside of a button, which may hold no link: references and links become
// their text (code keeps its look), and the http(s) ones are returned to offer elsewhere.
// -> {inlines, links: [{href, text, kind}]}
export function unlinkedInlines(list) {
  const links = [];
  const flat = (xs) => xs.flatMap((n) => {
    if (n.t === 'ref') {
      const href = safeHref(n.href);
      if (href) links.push({ href, text: n.v, kind: n.kind });
      return [n.code ? { t: 'code', v: n.v } : { t: 'text', v: n.v }];
    }
    if (n.t === 'link') {
      const c = flat(n.c);
      const href = safeHref(n.href);
      if (href) links.push({ href, text: textOf(c), kind: null });
      return c;
    }
    if (n.c) return [{ ...n, c: flat(n.c) }];
    return [n];
  });
  return { inlines: flat(list), links };
}

const textOf = (xs) => xs.map((n) => (n.v !== undefined ? n.v : n.c ? textOf(n.c) : '')).join('');

// Inlines (from inlineBlocks or stepInlines) into an element.
export function renderInlines(list, parent) {
  return inlines(list, parent);
}

const DIFF_LINES = 40;
// Caps on what an opened step draws, in code points (the iPhone app counts Unicode scalars): live
// text from the event stream has no bound of its own.
const CAP_INPUT = 4000;
const CAP_ERROR = 2000;
const CAP_LINE = 2000;

// The first n code points of text, then "…"; text within the cap is returned whole.
const capped = (text, n) => {
  if (text.length <= n) return text; // UTF-16 units: never fewer than code points
  const points = Array.from(text);
  return points.length > n ? points.slice(0, n).join('') + '…' : text;
};

// A text's lines; a final line break ends the last line rather than starting an empty one.
const lines = (text) => (text ? text.replace(/\n$/, '').split('\n') : []);

// What an opened step shows (its detail: {input, error, old, new}): the input in monospace (a
// command after a dimmed $), the error under "Failed", then an edit's removed then added lines,
// at most 40, then "N more lines"; links (from unlinkedInlines) go first. null when there is
// nothing to show, so the step cannot open.
// All of it is server text, set with textContent only.
export function stepDetail(step, links = []) {
  const d = step.detail || {};
  const input = d.input || '';
  const error = d.error || '';
  const removed = lines(d.old || '');
  const added = lines(d.new || '');
  if (!input && !error && !removed.length && !added.length) return null;
  const box = h('div', 'step-detail');
  // The links of the line's target (a button holds none), each on its own line.
  for (const l of links) {
    const a = anchor(l.href, REF_TITLE[l.kind] || 'Link');
    a.className = 'step-link';
    a.dir = 'ltr';
    a.textContent = l.text;
    box.append(a);
  }
  // An edit's input is its path; when the line already shows that path, the lines say enough.
  const repeats = step.kind === 'edit' && input === step.target && (removed.length || added.length);
  if (input && !repeats) {
    const pre = h('pre', 'step-input');
    pre.dir = 'ltr';
    if (step.kind === 'run') pre.append(h('span', 'dim', '$ '));
    pre.append(capped(input, CAP_INPUT));
    box.append(pre);
  }
  if (error) {
    const err = h('div', 'step-error failed');
    const text = h('pre', 'step-error-text', capped(error, CAP_ERROR));
    text.dir = 'ltr';
    err.append(h('div', 'step-error-label', 'Failed'), text);
    box.append(err);
  }
  if (removed.length || added.length) {
    const diff = h('div', 'diff');
    diff.dir = 'ltr';
    const all = [...removed.map((t) => ['del', '-', 'Removed: ', t]), ...added.map((t) => ['add', '+', 'Added: ', t])];
    for (const [cls, mark, spoken, text] of all.slice(0, DIFF_LINES)) {
      const line = h('div', 'diff-line ' + cls);
      // A screen reader hears "Removed: " or "Added: " instead of the mark, as on the iPhone.
      const markEl = h('span', 'diff-mark', mark);
      markEl.setAttribute('aria-hidden', 'true');
      line.append(markEl, h('span', 'visually-hidden', spoken), h('span', 'diff-text', capped(text, CAP_LINE)));
      diff.append(line);
    }
    const more = all.length - DIFF_LINES;
    if (more > 0) diff.append(h('div', 'diff-more', more === 1 ? '1 more line' : more + ' more lines'));
    box.append(diff);
  }
  return box;
}
