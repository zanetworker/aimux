// The references pass (api/testdata/clients/README.md, "References"): issue and pull request
// numbers, commits, Jira keys and file paths in a parsed Markdown tree become {t: "ref"} inlines
// linking to the session's repository or Jira. The visible text never changes.
//
// context: {repo: {web, branch, root?} | null, jiraBase, jiraProjects, project}.

// The context from a session detail (its repo and project) and /v1/health (its links).
export function referenceContext(detail, health) {
  const links = (health && health.links) || {};
  return {
    repo: (detail && detail.repo) || null,
    jiraBase: links.jiraBase || null,
    jiraProjects: links.jiraProjects || [],
    project: (detail && detail.project) || null,
  };
}

const WORD = /[A-Za-z0-9_]/;
const isWord = (c) => c !== undefined && WORD.test(c);
const boundary = (s, i) => !isWord(s[i]);
const NOT_ISSUE_WORDS = new Set(['step', 'item', 'option', 'line', 'no.', 'number']);

const stripSlash = (s) => s.replace(/\/+$/, '');
const isGitLab = (web) => {
  try { return new URL(web).hostname.includes('gitlab'); } catch (_) { return false; }
};
const repoPath = (repo, part) => stripSlash(repo.web) + (isGitLab(repo.web) ? '/-/' : '/') + part;

// Each rule takes (s, i, ctx) and returns {len, kind, href} or null.

// The whitespace-separated word before position i, lowercased, leading ( [ " ' dropped. Only
// a short look back: the words that matter are short, and a long scan per `#` is quadratic.
function wordBefore(s, i) {
  let end = i;
  while (end > 0 && /\s/.test(s[end - 1])) end--;
  let st = end;
  while (st > 0 && end - st <= 24 && !/\s/.test(s[st - 1])) st--;
  if (st > 0 && !/\s/.test(s[st - 1])) return ''; // longer than any listed word
  return s.slice(st, end).replace(/^[(\["']+/, '').toLowerCase();
}

function issue(s, i, ctx) {
  if (s[i] !== '#' || !ctx.repo) return null;
  const before = s[i - 1];
  if (i > 0 && before !== '(' && !/\s/.test(before)) return null;
  const m = s.slice(i + 1, i + 8).match(/^\d{1,6}/);
  if (!m || !boundary(s, i + 1 + m[0].length)) return null;
  if (i > 0 && before !== '(' && NOT_ISSUE_WORDS.has(wordBefore(s, i))) return null;
  return { len: 1 + m[0].length, kind: 'pr', href: repoPath(ctx.repo, 'issues/' + m[0]) };
}

const OTHER_REPO = /^([A-Za-z0-9][A-Za-z0-9_.-]*)\/([A-Za-z0-9][A-Za-z0-9_.-]*)#(\d{1,6})/;

function otherRepo(s, i, ctx) {
  if (!ctx.repo) return null;
  if (i > 0 && s[i - 1] !== '(' && !/\s/.test(s[i - 1])) return null;
  const m = s.slice(i, i + 300).match(OTHER_REPO);
  if (!m || !boundary(s, i + m[0].length)) return null;
  let origin;
  try { origin = new URL(ctx.repo.web).origin; } catch (_) { return null; }
  const sep = isGitLab(ctx.repo.web) ? '/-/' : '/';
  return { len: m[0].length, kind: 'pr', href: `${origin}/${m[1]}/${m[2]}${sep}issues/${m[3]}` };
}

function commit(s, i, ctx) {
  if (!ctx.repo || isWord(s[i - 1])) return null;
  const m = s.slice(i, i + 41).match(/^[0-9a-f]{7,40}/);
  if (!m || !boundary(s, i + m[0].length)) return null;
  if (!/[0-9]/.test(m[0]) || !/[a-f]/.test(m[0])) return null;
  return { len: m[0].length, kind: 'commit', href: repoPath(ctx.repo, 'commit/' + m[0]) };
}

function jira(s, i, ctx) {
  if (!ctx.jiraBase || !ctx.jiraProjects || !ctx.jiraProjects.length || isWord(s[i - 1])) return null;
  const m = s.slice(i, i + 64).match(/^([A-Z][A-Z0-9]+)-[0-9]+/);
  if (!m || !boundary(s, i + m[0].length) || !ctx.jiraProjects.includes(m[1])) return null;
  return { len: m[0].length, kind: 'jira', href: stripSlash(ctx.jiraBase) + '/browse/' + m[0] };
}

const PATH_CHAR = /[A-Za-z0-9_.@+\-\/]/;
const NOT_BEFORE_PATH = /[A-Za-z0-9_\/.\-@+~:]/;
const FILE_NAME = /\.[A-Za-z][A-Za-z0-9]{0,9}$/;

// A file path at i, with an optional :line: {len, kind, href} or null.
function path(s, i, ctx) {
  if (!ctx.repo || !PATH_CHAR.test(s[i]) || (i > 0 && NOT_BEFORE_PATH.test(s[i - 1]))) return null;
  let end = i;
  while (end < s.length && PATH_CHAR.test(s[end])) end++;
  let text = s.slice(i, end);
  const trimmed = text.replace(/\.+$/, '');
  const hadDots = trimmed.length !== text.length;
  text = trimmed;
  if (!text.includes('/') || !FILE_NAME.test(text)) return null;
  // The extension must end the file name: nothing glued after it within the run.
  const name = text.slice(text.lastIndexOf('/') + 1);
  if (!/^[A-Za-z0-9_.@+-]*\.[A-Za-z][A-Za-z0-9]{0,9}$/.test(name)) return null;

  let rel = text;
  if (rel.startsWith('/')) {
    const base = ctx.repo.root !== undefined && ctx.repo.root !== null ? ctx.repo.root : ctx.project;
    if (!base) return null;
    const prefix = stripSlash(base) + '/';
    if (!rel.startsWith(prefix)) return null;
    rel = rel.slice(prefix.length);
  } else if (rel.startsWith('./')) rel = rel.slice(2);
  const segs = rel.split('/');
  if (segs.some((g) => g === '' || g === '..')) return null;
  if (segs.length > 1 && segs[0].indexOf('.') > 0) return null;

  let len = text.length;
  let line = null;
  if (!hadDots) {
    const lm = s.slice(i + len, i + len + 8).match(/^:(\d{1,6})/);
    if (lm && boundary(s, i + len + lm[0].length)) { line = lm[1]; len += lm[0].length; }
  }
  const enc = (p) => p.split('/').map(encodeURIComponent).join('/'); // per segment, slashes kept
  const href = repoPath(ctx.repo, 'blob/' + enc(ctx.repo.branch) + '/' + enc(rel)) + (line ? '#L' + line : '');
  return { len, kind: 'path', href };
}

const RULES = [issue, otherRepo, commit, jira, path];

// Splits one text into text and ref inlines.
function scan(v, ctx) {
  const out = [];
  let from = 0;
  let i = 0;
  while (i < v.length) {
    let best = null;
    for (const rule of RULES) {
      const m = rule(v, i, ctx);
      if (m && (!best || m.len > best.len)) best = m;
    }
    if (!best) { i++; continue; }
    if (i > from) out.push({ t: 'text', v: v.slice(from, i) });
    out.push({ t: 'ref', kind: best.kind, v: v.slice(i, i + best.len), href: best.href });
    i += best.len;
    from = i;
  }
  if (from === 0) return [{ t: 'text', v }];
  if (from < v.length) out.push({ t: 'text', v: v.slice(from) });
  return out;
}

function linkInlines(inlines, ctx) {
  const out = [];
  for (const n of inlines) {
    if (n.t === 'text') { for (const x of scan(n.v, ctx)) out.push(x); } // a loop: a spread of 100k pieces overflows
    else if (n.t === 'em' || n.t === 'strong' || n.t === 'del') out.push({ t: n.t, c: linkInlines(n.c, ctx) });
    else if (n.t === 'code') {
      const m = path(n.v, 0, ctx);
      out.push(m && m.len === n.v.length ? { t: 'ref', kind: 'path', v: n.v, href: m.href, code: true } : n);
    } else out.push(n);
  }
  return out;
}

function linkBlock(b, ctx) {
  switch (b.type) {
    case 'paragraph': case 'heading': return { ...b, inlines: linkInlines(b.inlines, ctx) };
    case 'table': return {
      ...b, header: b.header.map((c) => linkInlines(c, ctx)),
      rows: b.rows.map((r) => r.map((c) => linkInlines(c, ctx))),
    };
    case 'list': return { ...b, items: b.items.map((it) => ({ ...it, blocks: linkReferences(it.blocks, ctx) })) };
    case 'quote': return { ...b, blocks: linkReferences(b.blocks, ctx) };
    default: return b;
  }
}

export function linkReferences(blocks, ctx) {
  if (!ctx || (!ctx.repo && !ctx.jiraBase)) return blocks;
  return blocks.map((b) => linkBlock(b, ctx));
}

// ---------------------------------------------------------------------------------------------
// Media cards (README, "Media"): a paragraph that is only one link to a video, image or audio
// file, a YouTube or a Vimeo video becomes {type: "media", kind, href, title}. Runs after the
// references pass, on a reply's tree only.

const VIDEO_HOSTS = { youtube: ['youtube.com', 'www.youtube.com', 'm.youtube.com'], vimeo: ['vimeo.com', 'www.vimeo.com'] };
const EXTENSIONS = {
  mp4: 'video', mov: 'video', m4v: 'video', webm: 'video',
  png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image',
  mp3: 'audio', m4a: 'audio', wav: 'audio',
};

// An http(s) address split into its authority (as written), lowercased host, path and query; null otherwise.
export function splitAddress(href) {
  const m = /^https?:\/\//i.exec(href);
  if (!m) return null;
  const rest = href.slice(m[0].length);
  const authEnd = rest.search(/[/?#]/);
  const auth = authEnd < 0 ? rest : rest.slice(0, authEnd);
  const after = authEnd < 0 ? '' : rest.slice(authEnd);
  const host = auth.slice(auth.lastIndexOf('@') + 1).replace(/:\d*$/, '').toLowerCase();
  const hash = after.indexOf('#');
  const beforeHash = hash < 0 ? after : after.slice(0, hash);
  const q = beforeHash.indexOf('?');
  return { auth, host, path: q < 0 ? beforeHash : beforeHash.slice(0, q), query: q < 0 ? '' : beforeHash.slice(q + 1) };
}

const lastSegment = (path) => path.slice(path.lastIndexOf('/') + 1);

// The host a browser goes to: the URL parser's hostname, or null when it cannot parse href.
export function parsedHost(href) {
  try { return new URL(href).hostname; } catch (_) { return null; }
}

// The media kind of an address, or null. A backslash (or %5C) before the path, or a host the URL
// parser reads differently from the split, is no media: the card would name the wrong host.
export function mediaKind(href) {
  const a = splitAddress(href);
  if (!a) return null;
  if (/\\|%5c/i.test(a.auth) || parsedHost(href) !== a.host) return null;
  const oneSegment = (p, prefix) => p.startsWith(prefix) && p.length > prefix.length && !p.slice(prefix.length).includes('/');
  if (VIDEO_HOSTS.youtube.includes(a.host)) {
    if (a.path === '/watch' && a.query.split('&').some((kv) => kv.startsWith('v=') && kv.length > 2)) return 'youtube';
    if (oneSegment(a.path, '/shorts/')) return 'youtube';
  }
  if (a.host === 'youtu.be' && oneSegment(a.path, '/')) return 'youtube';
  if (VIDEO_HOSTS.vimeo.includes(a.host) && /^\/\d+$/.test(a.path)) return 'vimeo';
  const seg = lastSegment(a.path);
  const dot = seg.lastIndexOf('.');
  if (dot < 0) return null;
  return EXTENSIONS[seg.slice(dot + 1).toLowerCase()] || null;
}

function decoded(s) {
  try { return decodeURIComponent(s); } catch (_) { return s; }
}

function mediaTitle(kind, href, text) {
  if (text !== href) return text;
  if (kind === 'youtube') return 'YouTube video';
  if (kind === 'vimeo') return 'Vimeo video';
  return decoded(lastSegment(splitAddress(href).path)); // the file name; the card shows the host below it
}

function mediaBlock(b) {
  switch (b.type) {
    case 'paragraph': {
      const only = b.inlines.length === 1 ? b.inlines[0] : null;
      if (!only || only.t !== 'link') return b;
      const kind = mediaKind(only.href);
      if (!kind) return b;
      const text = only.c.map((x) => x.v || '').join('');
      return { type: 'media', kind, href: only.href, title: mediaTitle(kind, only.href, text) };
    }
    case 'list': return { ...b, items: b.items.map((it) => ({ ...it, blocks: mediaCards(it.blocks) })) };
    case 'quote': return { ...b, blocks: mediaCards(b.blocks) };
    default: return b;
  }
}

export function mediaCards(blocks) {
  return blocks.map(mediaBlock);
}
