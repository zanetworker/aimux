// Markdown text to the block tree both clients render (api/testdata/clients/README.md, "Markdown").
// GitHub-flavoured Markdown as Claude Code writes it, parsed in two passes like CommonMark: lines
// into blocks (containers: quotes and list items; leaves: paragraphs, headings, code, tables,
// rules), then each leaf's text into inlines with the delimiter-run algorithm. Raw HTML is never
// interpreted; links survive only for http(s). Every prefix of a reply parses (streaming).
//
// parseMarkdown(src, {breaks}) -> blocks. With breaks, a soft line break is a {t: "br"} (user
// bubbles keep the lines as typed); otherwise it is a space, as the fixtures pin.

const EMPH = { em: 1, strong: 2, del: 4 };
const EMPH_ORDER = ['em', 'strong', 'del']; // tie order, outermost first

// ---------------------------------------------------------------------------------------------
// Lines and columns. A line is consumed from the left as containers match; a tab that is only
// partly consumed turns into the spaces that remain of it, so code keeps its tabs otherwise.

function makeLine(s) { return { rest: s, col: 0 }; }

function isBlank(s) { return /^[ \t]*$/.test(s); }

// Columns of leading whitespace in ln.rest.
function indentOf(ln) {
  let col = ln.col;
  for (const ch of ln.rest) {
    if (ch === ' ') col++;
    else if (ch === '\t') col += 4 - (col % 4);
    else break;
  }
  return col - ln.col;
}

// Consumes n columns of leading whitespace (fewer when the line runs out of it).
function consumeCols(ln, n) {
  while (n > 0 && ln.rest.length) {
    const ch = ln.rest[0];
    if (ch === ' ') { ln.rest = ln.rest.slice(1); ln.col++; n--; }
    else if (ch === '\t') {
      const w = 4 - (ln.col % 4);
      if (w <= n) { ln.rest = ln.rest.slice(1); ln.col += w; n -= w; }
      else { ln.rest = ' '.repeat(w - n) + ln.rest.slice(1); ln.col += n; n = 0; }
    } else break;
  }
}

function consumeChars(ln, n) { ln.rest = ln.rest.slice(n); ln.col += n; }

// ---------------------------------------------------------------------------------------------
// Block parser

const ATX = /^(#{1,6})(?=[ \t]|$)(.*)$/;
const RULE = /^([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const SETEXT = /^(=+|-+)[ \t]*$/;
const FENCE_OPEN = /^(`{3,}|~{3,})(.*)$/;
const BULLET = /^([-*+])(?=[ \t]|$)/;
const ORDERED = /^(\d{1,9})([.)])(?=[ \t]|$)/;

function node(type, parent, extra) { return Object.assign({ type, parent, children: [], open: true, depth: 0 }, extra); }

// Quotes and lists nest at most this deep; deeper markers are paragraph text (not pinned beyond
// it). This bounds every recursion over the tree, here and in the renderer.
export const MAX_DEPTH = 100;
const levels = (n) => n.depth;

const isContainer = (n) => n.type === 'doc' || n.type === 'quote' || n.type === 'list' || n.type === 'item';
const canContain = (parent, type) => (parent.type === 'list' ? type === 'item' : type !== 'item');

// Splits a table line into raw cell strings: optional outer pipes, `\|` is a literal pipe.
function splitRow(line) {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '\\' && s[i + 1] === '|') { cur += '|'; i++; } else if (ch === '|') { cells.push(cur.trim()); cur = ''; } else cur += ch;
  }
  cells.push(cur.trim());
  return cells;
}

function delimiterRow(line) {
  if (!/[-]/.test(line) || /^[ \t]{4}/.test(line)) return null;
  const cells = splitRow(line);
  const align = [];
  for (const c of cells) {
    if (!/^:?-+:?$/.test(c)) return null;
    align.push(c.endsWith(':') ? (c.startsWith(':') ? 'center' : 'right') : 'left');
  }
  return align;
}

function parseBlocks(src) {
  const doc = node('doc', null);
  let leaf = null; // the open leaf (paragraph, fenced or indented code, table), last child of its parent

  const closeLeaf = () => { if (leaf) { leaf.open = false; leaf = null; } };
  const close = (n) => { // closes n and everything open inside it
    if (leaf && (leaf.parent === n || isInside(leaf, n))) closeLeaf();
    n.open = false;
    const last = n.children[n.children.length - 1];
    if (last && last.open && isContainer(last)) close(last);
  };
  const isInside = (x, n) => { for (let p = x.parent; p; p = p.parent) if (p === n) return true; return false; };
  const openChain = () => { // open containers below doc, outermost first
    const chain = [];
    let n = doc;
    for (;;) {
      const last = n.children[n.children.length - 1];
      if (!last || !last.open || !isContainer(last)) break;
      chain.push(last);
      n = last;
    }
    return chain;
  };
  const add = (parent, n) => {
    closeLeaf();
    while (!canContain(parent, n.type)) { close(parent); parent = parent.parent; }
    const last = parent.children[parent.children.length - 1];
    if (last && last.open) close(last);
    n.parent = parent;
    n.depth = parent.depth + (n.type === 'quote' || n.type === 'list' ? 1 : 0);
    parent.children.push(n);
    return n;
  };

  const lines = src.split(/\r\n|\r|\n/);
  if (lines.length && lines[lines.length - 1] === '') lines.pop();

  for (const raw of lines) {
    const ln = makeLine(raw);
    const chain = openChain();
    let matched = doc;
    let all = true;
    for (const c of chain) {
      let ok = false;
      if (c.type === 'list') ok = true;
      else if (c.type === 'quote') {
        const ind = indentOf(ln);
        if (ind <= 3 && ln.rest.replace(/^[ \t]*/, '').startsWith('>')) {
          consumeCols(ln, ind);
          consumeChars(ln, 1);
          if (ln.rest[0] === ' ' || ln.rest[0] === '\t') consumeCols(ln, 1);
          ok = true;
        }
      } else if (c.type === 'item') {
        if (isBlank(ln.rest)) ok = c.children.length > 0 || !c.blankStart;
        else if (indentOf(ln) >= c.indent) { consumeCols(ln, c.indent); ok = true; }
      }
      if (!ok) { all = false; break; }
      matched = c;
    }

    // An open fenced or indented code block takes the line when every container matched.
    if (all && leaf && leaf.type === 'code' && leaf.parent === matched) {
      if (leaf.fence) {
        const ind = indentOf(ln);
        const t = ln.rest.replace(/^[ \t]*/, '');
        const m = ind <= 3 && t.match(/^(`{3,}|~{3,})[ \t]*$/);
        if (m && m[1][0] === leaf.fence.ch && m[1].length >= leaf.fence.len) { closeLeaf(); continue; }
        let k = 0;
        while (k < leaf.fence.indent && ln.rest[k] === ' ') k++;
        leaf.lines.push(ln.rest.slice(k));
        continue;
      }
      if (isBlank(ln.rest) || indentOf(ln) >= 4) {
        consumeCols(ln, 4);
        leaf.lines.push(ln.rest);
        continue;
      }
    }

    const paraTip = all && leaf && leaf.type === 'paragraph' && leaf.parent === matched;
    const tableTip = all && leaf && leaf.type === 'table' && leaf.parent === matched;
    let container = matched;
    let opened = false;
    const closeUnmatched = () => {
      if (!all) {
        const idx = chain.indexOf(matched);
        const first = chain[idx + 1];
        if (first) close(first);
        all = true;
      }
    };

    let done = false;
    let indentedCode = false;
    for (;;) {
      if (isBlank(ln.rest)) break;
      const ind = indentOf(ln);
      if (ind >= 4) {
        // Four spaces start indented code, except as a paragraph's (or table's, or a lazy) continuation.
        const continues = !opened && (paraTip || tableTip || (!all && leaf && leaf.type === 'paragraph'));
        indentedCode = !continues;
        break;
      }
      const t = ln.rest.replace(/^[ \t]*/, '');
      if (t[0] === '>') {
        if (levels(container) >= MAX_DEPTH) break;
        closeUnmatched();
        consumeCols(ln, ind);
        consumeChars(ln, 1);
        if (ln.rest[0] === ' ' || ln.rest[0] === '\t') consumeCols(ln, 1);
        container = add(container, node('quote', container));
        opened = true;
        continue;
      }
      let m = t.match(ATX);
      if (m) {
        closeUnmatched();
        const text = m[2].replace(/(^|[ \t]+)#+[ \t]*$/, '').trim();
        const h = add(container, node('heading', container, { level: m[1].length, text }));
        h.open = false;
        done = true;
        break;
      }
      m = t.match(FENCE_OPEN);
      if (m && !(m[1][0] === '`' && m[2].includes('`'))) {
        closeUnmatched();
        const info = m[2].trim();
        const word = info.split(/[ \t]/)[0];
        const lang = word ? decodeText(word) : null;
        leaf = add(container, node('code', container, { fence: { ch: m[1][0], len: m[1].length, indent: ind }, lang, lines: [] }));
        done = true;
        break;
      }
      if (paraTip && !opened && SETEXT.test(t)) {
        leaf.type = 'heading';
        leaf.level = t[0] === '=' ? 1 : 2;
        leaf.text = leaf.lines.join('\n').trim();
        closeLeaf();
        done = true;
        break;
      }
      if (paraTip && !opened) {
        const align = delimiterRow(t);
        const header = align && splitRow(leaf.lines[leaf.lines.length - 1]);
        if (align && header.length === align.length) {
          const para = leaf;
          para.lines.pop();
          const table = node('table', container, { align, header, rows: [] });
          table.parent = container;
          if (para.lines.length) { para.open = false; container.children.push(table); } else container.children.splice(container.children.indexOf(para), 1, table);
          leaf = table;
          done = true;
          break;
        }
      }
      if (RULE.test(t)) {
        closeUnmatched();
        const r = add(container, node('rule', container));
        r.open = false;
        done = true;
        break;
      }
      const bm = t.match(BULLET);
      const om = !bm && t.match(ORDERED);
      if (bm || om) {
        const marker = (bm || om)[0];
        const after = t.slice(marker.length);
        const blankItem = isBlank(after);
        const start = om ? parseInt(om[1], 10) : null;
        if (paraTip && !opened && (blankItem || (om && start !== 1))) break; // cannot interrupt a paragraph
        if (levels(container) >= MAX_DEPTH && container.type !== 'list') break;
        closeUnmatched();
        consumeCols(ln, ind);
        consumeChars(ln, marker.length);
        let pad = 1;
        if (!blankItem) {
          const sp = indentOf(ln);
          pad = sp >= 5 ? 1 : sp;
          consumeCols(ln, pad);
        } else {
          consumeCols(ln, indentOf(ln));
        }
        const kind = bm ? 'b' + bm[1] : 'o' + om[2];
        let list = container.type === 'list' ? container : null;
        if (list && list.kind !== kind) { close(list); container = list.parent; list = null; }
        if (!list) {
          const lastChild = container.children[container.children.length - 1];
          if (lastChild && lastChild.open && lastChild.type === 'list' && lastChild.kind === kind) list = lastChild;
          else list = add(container, node('list', container, { kind, ordered: !!om, start }));
        }
        closeLeaf();
        const lastItem = list.children[list.children.length - 1];
        if (lastItem && lastItem.open) close(lastItem);
        const item = node('item', list, { indent: ind + marker.length + pad, blankStart: blankItem, depth: list.depth });
        list.children.push(item);
        container = item;
        opened = true;
        continue;
      }
      break;
    }
    if (done) continue;

    const blank = isBlank(ln.rest);
    // Lazy continuation: a line that only failed to match containers continues an open paragraph.
    if (!all && !opened && !blank && leaf && leaf.type === 'paragraph') {
      leaf.lines.push(ln.rest.replace(/^[ \t]*/, ''));
      continue;
    }
    closeUnmatched();
    if (blank) {
      if (leaf && (leaf.type === 'paragraph' || leaf.type === 'table')) closeLeaf();
      if (leaf && leaf.type === 'code' && !leaf.fence && leaf.parent === container) { leaf.lines.push(''); }
      continue;
    }
    if (leaf && leaf.parent === container && !opened) {
      if (leaf.type === 'paragraph') { leaf.lines.push(ln.rest.replace(/^[ \t]*/, '')); continue; }
      if (leaf.type === 'table') { leaf.rows.push(splitRow(ln.rest)); continue; }
    }
    if (indentedCode) {
      consumeCols(ln, 4);
      leaf = add(container, node('code', container, { fence: null, lang: null, lines: [ln.rest] }));
      continue;
    }
    leaf = add(container, node('paragraph', container, { lines: [ln.rest.replace(/^[ \t]*/, '')] }));
  }
  return doc;
}

// ---------------------------------------------------------------------------------------------
// Tree to the wire shape

function convert(children, opts) {
  const out = [];
  for (const n of children) {
    switch (n.type) {
      case 'paragraph': {
        const inlines = parseInlines(n.lines.join('\n').trim(), opts, true);
        if (inlines.length) out.push({ type: 'paragraph', inlines });
        break;
      }
      case 'heading':
        out.push({ type: 'heading', level: n.level, inlines: parseInlines(n.text, opts) });
        break;
      case 'code': {
        const lines = n.lines.slice();
        if (!n.fence) while (lines.length && isBlank(lines[lines.length - 1])) lines.pop();
        if (lines.length) out.push({ type: 'code', lang: n.lang || null, text: lines.join('\n') });
        break;
      }
      case 'quote':
        out.push({ type: 'quote', blocks: convert(n.children, opts) });
        break;
      case 'list':
        out.push({
          type: 'list', ordered: n.ordered, start: n.ordered ? n.start : null,
          items: n.children.map((item) => convertItem(item, opts)),
        });
        break;
      case 'table': {
        const cols = n.align.length;
        const cells = (raw) => {
          const row = raw.slice(0, cols).map((c) => parseInlines(c, opts));
          while (row.length < cols) row.push([]);
          return row;
        };
        out.push({ type: 'table', align: n.align, header: cells(n.header), rows: n.rows.map(cells) });
        break;
      }
      case 'rule':
        out.push({ type: 'rule' });
        break;
      default:
    }
  }
  return out;
}

function convertItem(item, opts) {
  let task = null;
  const first = item.children[0];
  if (first && first.type === 'paragraph') {
    const m = first.lines[0].match(/^\[([ xX])\][ \t]/);
    if (m) {
      task = m[1] !== ' ';
      first.lines[0] = first.lines[0].slice(4);
    }
  }
  return { task, blocks: convert(item.children, opts) };
}

export function parseMarkdown(src, opts = {}) {
  return convert(parseBlocks(String(src || '')).children, opts);
}

// ---------------------------------------------------------------------------------------------
// Entities and escapes

const NAMED = {
  amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ', copy: '©', reg: '®', trade: '™',
  hellip: '…', mdash: '—', ndash: '–', lsquo: '‘', rsquo: '’', ldquo: '“', rdquo: '”', laquo: '«',
  raquo: '»', bull: '•', middot: '·', deg: '°', plusmn: '±', times: '×', divide: '÷', micro: 'µ',
  para: '¶', sect: '§', cent: '¢', pound: '£', euro: '€', yen: '¥', curren: '¤', iexcl: '¡',
  iquest: '¿', frac12: '½', frac14: '¼', frac34: '¾', sup1: '¹', sup2: '²', sup3: '³', not: '¬',
  shy: '­', larr: '←', rarr: '→', uarr: '↑', darr: '↓', harr: '↔', lArr: '⇐', rArr: '⇒',
  hArr: '⇔', le: '≤', ge: '≥', ne: '≠', asymp: '≈', equiv: '≡', infin: '∞', minus: '−', radic: '√',
  sum: '∑', prod: '∏', part: '∂', nabla: '∇', isin: '∈', notin: '∉', cap: '∩', cup: '∪', and: '∧',
  or: '∨', forall: '∀', exist: '∃', empty: '∅', check: '✓', cross: '✗', star: '☆', starf: '★',
  hearts: '♥', spades: '♠', clubs: '♣', diams: '♦', alpha: 'α', beta: 'β', gamma: 'γ', delta: 'δ',
  epsilon: 'ε', lambda: 'λ', mu: 'μ', pi: 'π', sigma: 'σ', tau: 'τ', phi: 'φ', omega: 'ω',
  Delta: 'Δ', Sigma: 'Σ', Omega: 'Ω', dagger: '†', Dagger: '‡', permil: '‰', prime: '′', Prime: '″',
  thinsp: ' ', ensp: ' ', emsp: ' ', zwj: '‍', zwnj: '‌', lrm: '‎',
  rlm: '‏', grave: '`', acute: '´', tilde: '˜', circ: 'ˆ', num: '#', excl: '!', percnt: '%',
  dollar: '$', lpar: '(', rpar: ')', ast: '*', plus: '+', comma: ',', period: '.', sol: '/',
  colon: ':', semi: ';', equals: '=', quest: '?', commat: '@', lsqb: '[', rsqb: ']', bsol: '\\',
  lowbar: '_', lcub: '{', rcub: '}', verbar: '|', vert: '|', Hat: '^', auml: 'ä', ouml: 'ö', uuml: 'ü',
  Auml: 'Ä', Ouml: 'Ö', Uuml: 'Ü', szlig: 'ß', eacute: 'é', egrave: 'è', ecirc: 'ê', aacute: 'á',
  agrave: 'à', acirc: 'â', ccedil: 'ç', ntilde: 'ñ', oacute: 'ó', iacute: 'í', uacute: 'ú',
  Eacute: 'É', aring: 'å', aelig: 'æ', oslash: 'ø', Aring: 'Å', AElig: 'Æ', Oslash: 'Ø',
};

const ENTITY = /^&(?:#[xX]([0-9a-fA-F]{1,6})|#([0-9]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));/;

// The entity at the start of s: [decoded text, length] or null.
function entityAt(s) {
  const m = s.match(ENTITY);
  if (!m) return null;
  if (m[3]) return Object.prototype.hasOwnProperty.call(NAMED, m[3]) ? [NAMED[m[3]], m[0].length] : null;
  let cp = m[1] ? parseInt(m[1], 16) : parseInt(m[2], 10);
  if (cp === 0 || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) cp = 0xfffd;
  return [String.fromCodePoint(cp), m[0].length];
}

const ASCII_PUNCT = /[!-\/:-@\[-`{-~]/;

// Backslash escapes and entities, for link destinations and info strings.
function decodeText(s) {
  let out = '';
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '\\' && i + 1 < s.length && ASCII_PUNCT.test(s[i + 1])) { out += s[++i]; continue; }
    if (ch === '&') {
      const e = entityAt(s.slice(i));
      if (e) { out += e[0]; i += e[1] - 1; continue; }
    }
    out += ch;
  }
  return out;
}

// ---------------------------------------------------------------------------------------------
// Inline parser

const PUNCT = /[\p{P}\p{S}]/u;
const SPACE = /\s/u;

function charBefore(s, i) {
  if (i <= 0) return '\n';
  const c = s.charCodeAt(i - 1);
  if (c >= 0xdc00 && c <= 0xdfff && i >= 2) return s.slice(i - 2, i);
  return s[i - 1];
}
function charAfter(s, i) {
  if (i >= s.length) return '\n';
  return String.fromCodePoint(s.codePointAt(i));
}

const SPECIAL = /[\\`*_~\[\]!<&\n]/g;

// Parses the inline destination after `](`: {href, end} or null. p is the index of `(`.
// Parentheses in a bare destination nest at most 32 deep (as in cmark), which also keeps a run
// of unclosed `[a](` from rescanning the rest of the text each time.
const MAX_PARENS = 32;
function linkTail(s, p) {
  let i = p + 1;
  const skip = () => { let nl = 0; while (i < s.length && (s[i] === ' ' || s[i] === '\t' || (s[i] === '\n' && nl++ === 0))) i++; };
  skip();
  let dest = '';
  if (s[i] === '<') {
    let j = i + 1;
    for (; j < s.length; j++) {
      if (s[j] === '\\' && j + 1 < s.length && ASCII_PUNCT.test(s[j + 1])) { j++; continue; }
      if (s[j] === '>' || s[j] === '<' || s[j] === '\n') break;
    }
    if (s[j] !== '>') return null;
    dest = s.slice(i + 1, j);
    i = j + 1;
  } else {
    let depth = 0;
    const st = i;
    for (; i < s.length; i++) {
      const ch = s[i];
      if (ch === '\\' && i + 1 < s.length && ASCII_PUNCT.test(s[i + 1])) { i++; continue; }
      if (ch === '(') { if (++depth > MAX_PARENS) return null; } else if (ch === ')') { if (depth === 0) break; depth--; } else if (ch.charCodeAt(0) <= 0x20 || ch === '\u007f') break;
    }
    if (depth !== 0) return null;
    dest = s.slice(st, i);
  }
  const beforeTitle = i;
  skip();
  if (i > beforeTitle && (s[i] === '"' || s[i] === "'" || s[i] === '(')) {
    const closeCh = s[i] === '(' ? ')' : s[i];
    let j = i + 1;
    for (; j < s.length; j++) {
      if (s[j] === '\\' && j + 1 < s.length) { j++; continue; }
      if (s[j] === closeCh) break;
      if (closeCh === ')' && s[j] === '(') return null;
    }
    if (j >= s.length) return null;
    i = j + 1;
    skip();
  }
  if (s[i] !== ')') return null;
  return { href: decodeText(dest), end: i + 1 };
}

const AUTOLINK = /^<([A-Za-z][A-Za-z0-9+.\-]{1,31}:[^\s<>]*)>/;
const EMAIL = /^<([a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*)>/;

// Inline nodes live in doubly linked lists (as in commonmark.js) so that wrapping a run in
// emphasis or a link is constant time. A container node (em, strong, del, link, img) holds its
// children as a list from `head`. Delimiters are a second linked list over the delimiter runs.

function list() { const head = { t: 'head', prev: null, next: null }; return { head, last: head }; }
function append(l, n) { n.prev = l.last; n.next = null; l.last.next = n; l.last = n; return n; }
function unlink(l, n) {
  n.prev.next = n.next;
  if (n.next) n.next.prev = n.prev; else l.last = n.prev;
}
// Moves the nodes after `from` up to `to` (inclusive) out of l into a new list.
function cut(l, from, to) {
  const out = list();
  const first = from.next;
  if (!first || from === to) return out;
  from.next = to.next;
  if (to.next) to.next.prev = from; else l.last = from;
  first.prev = out.head;
  out.head.next = first;
  to.next = null;
  out.last = to;
  return out;
}

function tokenize(s) {
  const nodes = list();
  const ds = { top: null, pos: 0 }; // delimiter stack, newest last; pos orders delimiters
  const brackets = [];
  let linkSeq = 0, cutSeq = 0; // brackets older than the last link are inactive (links do not nest)
  const noCloser = new Set(); // backtick run lengths with no closer further on
  const text = (v) => append(nodes, { t: 'text', v });
  let i = 0;
  while (i < s.length) {
    const ch = s[i];
    switch (ch) {
      case '\\': {
        const nx = s[i + 1];
        if (nx === '\n') { append(nodes, { t: 'br' }); i += 2; while (s[i] === ' ' || s[i] === '\t') i++; } else if (nx !== undefined && ASCII_PUNCT.test(nx)) { text(nx); i += 2; } else { text('\\'); i++; }
        break;
      }
      case '`': {
        let n = 1;
        while (s[i + n] === '`') n++;
        let found = -1;
        if (!noCloser.has(n)) {
          let j = i + n;
          while (j < s.length) {
            const k = s.indexOf('`', j);
            if (k < 0) break;
            let m = 1;
            while (s[k + m] === '`') m++;
            if (m === n) { found = k; break; }
            j = k + m;
          }
          if (found < 0) noCloser.add(n);
        }
        if (found < 0) { text('`'.repeat(n)); i += n; break; }
        let v = s.slice(i + n, found).replace(/\n/g, ' ');
        if (v.length >= 2 && v[0] === ' ' && v[v.length - 1] === ' ' && /[^ ]/.test(v)) v = v.slice(1, -1);
        append(nodes, { t: 'code', v });
        i = found + n;
        break;
      }
      case '*': case '_': case '~': {
        let n = 1;
        while (s[i + n] === ch) n++;
        const before = charBefore(s, i), after = charAfter(s, i + n);
        const bs = SPACE.test(before), as = SPACE.test(after);
        const bp = PUNCT.test(before), ap = PUNCT.test(after);
        const left = !as && (!ap || bs || bp);
        const right = !bs && (!bp || as || ap);
        let canOpen = left, canClose = right;
        if (ch === '_') { canOpen = left && (!right || bp); canClose = right && (!left || ap); }
        const nd = append(nodes, { t: 'text', v: ch.repeat(n) });
        if ((canOpen || canClose) && !(ch === '~' && n > 2)) {
          nd.delim = true;
          const d = { ch, node: nd, count: n, orig: n, canOpen, canClose, pos: ds.pos++, prev: ds.top, next: null };
          if (ds.top) ds.top.next = d;
          ds.top = d;
        }
        i += n;
        break;
      }
      case '!':
        if (s[i + 1] === '[') {
          const nd = append(nodes, { t: 'text', v: '![' });
          brackets.push({ node: nd, image: true, seq: linkSeq++, bottom: ds.top });
          i += 2;
        } else { text('!'); i++; }
        break;
      case '[': {
        const nd = append(nodes, { t: 'text', v: '[' });
        brackets.push({ node: nd, image: false, seq: linkSeq++, bottom: ds.top });
        i++;
        break;
      }
      case ']': {
        const op = brackets[brackets.length - 1];
        if (!op) { text(']'); i++; break; }
        if (!op.image && op.seq < cutSeq) { brackets.pop(); text(']'); i++; break; }
        const tail = s[i + 1] === '(' ? linkTail(s, i + 1) : null;
        if (!tail) { brackets.pop(); text(']'); i++; break; }
        processEmphasis(nodes, ds, op.bottom);
        const children = cut(nodes, op.node, nodes.last);
        unlink(nodes, op.node);
        append(nodes, { t: op.image ? 'img' : 'link', href: tail.href, head: children.head });
        brackets.pop();
        if (!op.image) cutSeq = linkSeq;
        i = tail.end;
        break;
      }
      case '<': {
        const rest = s.slice(i, i + 2100);
        let m = rest.match(AUTOLINK);
        if (m) {
          const c = list();
          append(c, { t: 'text', v: m[1] });
          append(nodes, { t: 'link', href: m[1], head: c.head });
          i += m[0].length;
          break;
        }
        m = rest.match(EMAIL);
        if (m) { text(m[1]); i += m[0].length; break; }
        text('<');
        i++;
        break;
      }
      case '&': {
        const e = entityAt(s.slice(i, i + 40));
        if (e) { text(e[0]); i += e[1]; } else { text('&'); i++; }
        break;
      }
      case '\n': {
        const last = nodes.last;
        let hard = false;
        if (last.t === 'text' && !last.delim) {
          hard = / {2,}$/.test(last.v);
          last.v = last.v.replace(/[ \t]+$/, '');
        }
        append(nodes, hard ? { t: 'br' } : { t: 'soft' });
        i++;
        while (s[i] === ' ' || s[i] === '\t') i++;
        break;
      }
      default: {
        SPECIAL.lastIndex = i;
        const m = SPECIAL.exec(s);
        const end = m ? m.index : s.length;
        text(s.slice(i, end));
        i = end;
      }
    }
  }
  processEmphasis(nodes, ds, null);
  return nodes;
}

function removeDelim(ds, d) {
  if (d.prev) d.prev.next = d.next;
  if (d.next) d.next.prev = d.prev; else ds.top = d.prev;
}

// The CommonMark "process emphasis" procedure over the delimiters newer than `bottom` (null: all).
function processEmphasis(nodes, ds, bottom) {
  const bottomPos = bottom ? bottom.pos : -1;
  let closer = ds.top && ds.top.pos > bottomPos ? ds.top : null; // the first delimiter above bottom
  while (closer && closer.prev && closer.prev.pos > bottomPos) closer = closer.prev;
  const openersBottom = new Map();
  while (closer) {
    if (!closer.canClose) { closer = closer.next; continue; }
    const key = closer.ch + (closer.canOpen ? '1' : '0') + (closer.orig % 3);
    // Searches stop at a position, not a delimiter, which may since have been removed.
    const floor = Math.max(bottomPos, openersBottom.has(key) ? openersBottom.get(key) : -1);
    let o = closer.prev;
    let found = false;
    for (; o && o.pos > floor; o = o.prev) {
      if (o.ch !== closer.ch || !o.canOpen) continue;
      if (closer.ch === '~') { if (o.count === closer.count) { found = true; break; } continue; }
      const odd = (o.canClose || closer.canOpen) && (o.orig + closer.orig) % 3 === 0 && !(o.orig % 3 === 0 && closer.orig % 3 === 0);
      if (!odd) { found = true; break; }
    }
    if (!found) {
      openersBottom.set(key, closer.prev ? closer.prev.pos : -1);
      const next = closer.next;
      if (!closer.canOpen) removeDelim(ds, closer);
      closer = next;
      continue;
    }
    let use, type;
    if (closer.ch === '~') { use = closer.count; type = 'del'; } else {
      use = o.count >= 2 && closer.count >= 2 ? 2 : 1;
      type = use === 2 ? 'strong' : 'em';
    }
    const inner = closer.node.prev === o.node ? list() : cut(nodes, o.node, closer.node.prev);
    const wrap = { t: type, head: inner.head, prev: o.node, next: closer.node };
    o.node.next = wrap;
    closer.node.prev = wrap;
    o.count -= use; closer.count -= use;
    o.node.v = o.node.v.slice(use); closer.node.v = closer.node.v.slice(use);
    o.next = closer; closer.prev = o; // the delimiters in between are gone
    if (o.count === 0) { unlink(nodes, o.node); removeDelim(ds, o); }
    if (closer.count === 0) {
      const next = closer.next;
      unlink(nodes, closer.node);
      removeDelim(ds, closer);
      closer = next;
    }
  }
  while (ds.top && ds.top.pos > bottomPos) removeDelim(ds, ds.top);
}

// ---------------------------------------------------------------------------------------------
// Leaves: the parsed inlines flattened into text, code, link and br, each with its styles (a
// bitmask of EMPH), then bare URLs, then the canonical nesting. The walks use an explicit stack,
// so nesting depth costs no call stack.

function plain(head) {
  let out = '';
  const stack = [head.next];
  while (stack.length) {
    const n = stack[stack.length - 1];
    if (!n) { stack.pop(); continue; }
    stack[stack.length - 1] = n.next;
    if (n.t === 'text' || n.t === 'code') out += n.v;
    else if (n.t === 'soft' || n.t === 'br') out += ' ';
    else if (n.head) stack.push(n.head.next);
  }
  return out;
}

const isWeb = (href) => /^https?:/i.test(href);

function toLeaves(nodes, opts) {
  const out = [];
  const push = (leaf, mask) => {
    const last = out[out.length - 1];
    if (leaf.t === 'text') {
      if (!leaf.v) return;
      if (last && last.leaf.t === 'text' && last.mask === mask) { last.leaf = { t: 'text', v: last.leaf.v + leaf.v }; return; }
    }
    out.push({ leaf, mask });
  };
  const stack = [{ n: nodes.head.next, mask: 0 }];
  while (stack.length) {
    const f = stack[stack.length - 1];
    const n = f.n;
    if (!n) { stack.pop(); continue; }
    f.n = n.next;
    switch (n.t) {
      case 'text': push({ t: 'text', v: n.v }, f.mask); break;
      case 'soft': push(opts.breaks ? { t: 'br' } : { t: 'text', v: ' ' }, f.mask); break;
      case 'br': push({ t: 'br' }, f.mask); break;
      case 'code': push({ t: 'code', v: n.v }, f.mask); break;
      case 'em': case 'strong': case 'del': stack.push({ n: n.head.next, mask: f.mask | EMPH[n.t] }); break;
      case 'link':
        if (isWeb(n.href)) push({ t: 'link', href: n.href, c: [{ t: 'text', v: plain(n.head) || n.href }] }, f.mask);
        else stack.push({ n: n.head.next, mask: f.mask });
        break;
      case 'img': push({ t: 'text', v: plain(n.head) }, f.mask); break;
      default:
    }
  }
  return out;
}

const URL_START = /https?:\/\//gi;
const URL_TRAIL = /[.,;:!?*_~'"]/;

// Splits one text into text and link pieces at bare http(s) URLs.
function bareURLs(v) {
  const out = [];
  let from = 0;
  URL_START.lastIndex = 0;
  let m;
  while ((m = URL_START.exec(v))) {
    const st = m.index;
    if (st > 0 && /[A-Za-z]/.test(v[st - 1])) { URL_START.lastIndex = st + 1; continue; }
    let end = st + m[0].length;
    let open = 0, close = 0;
    while (end < v.length && !/[\s<]/u.test(v[end])) {
      if (v[end] === '(') open++; else if (v[end] === ')') close++;
      end++;
    }
    for (;;) {
      const last = v[end - 1];
      if (URL_TRAIL.test(last)) { end--; continue; }
      if (last === ')' && close > open) { end--; close--; continue; }
      break;
    }
    if (end <= st + m[0].length) { URL_START.lastIndex = st + 1; continue; }
    const url = v.slice(st, end);
    if (st > from) out.push({ t: 'text', v: v.slice(from, st) });
    out.push({ t: 'link', href: url, c: [{ t: 'text', v: url }] });
    from = end;
    URL_START.lastIndex = from;
  }
  if (from === 0) return null;
  if (from < v.length) out.push({ t: 'text', v: v.slice(from) });
  return out;
}

function rebuild(leaves, from, to, open) {
  const out = [];
  const emit = (n) => {
    const last = out[out.length - 1];
    if (n.t === 'text' && last && last.t === 'text') last.v += n.v;
    else out.push(n.t === 'text' ? { t: 'text', v: n.v } : n);
  };
  let i = from;
  while (i < to) {
    const { leaf, mask } = leaves[i];
    const remaining = mask & ~open;
    if (!remaining) { emit(leaf); i++; continue; }
    let best = null, bestEnd = -1;
    for (const name of EMPH_ORDER) {
      const bit = EMPH[name];
      if (!(remaining & bit)) continue;
      let j = i;
      while (j < to && (leaves[j].mask & bit)) j++;
      if (j > bestEnd) { best = name; bestEnd = j; }
    }
    out.push({ t: best, c: rebuild(leaves, i, bestEnd, open | EMPH[best]) });
    i = bestEnd;
  }
  return out;
}

// A paragraph that is only one image with an http(s) source is that image as a link (README,
// "Links"), so the media pass can make it a card; anywhere else an image is its alt text.
function parseInlines(src, opts = {}, paragraph = false) {
  if (!src) return [];
  const nodes = tokenize(src);
  const only = nodes.head.next;
  if (paragraph && only && only.t === 'img' && !only.next && isWeb(only.href)) {
    return [{ t: 'link', href: only.href, c: [{ t: 'text', v: plain(only.head) || only.href }] }];
  }
  const flat = toLeaves(nodes, opts);
  const leaves = [];
  for (const l of flat) {
    const parts = l.leaf.t === 'text' ? bareURLs(l.leaf.v) : null;
    if (parts) for (const p of parts) leaves.push({ leaf: p, mask: l.mask });
    else leaves.push(l);
  }
  return rebuild(leaves, 0, leaves.length, 0);
}
