// Syntax colours for code blocks: highlight(code, lang) splits code into tokens [{k, v}] whose
// texts join back to code exactly. Kinds: plain, keyword, string, comment, number, function, key,
// added, removed, meta. The rules are pinned by api/testdata/clients/highlight/ and are the same
// as Highlight.swift in the iPhone app; an unknown language is one plain token.

const C_LIKE = { line: ['//'], block: ['/*', '*/'] };
const HASH = { line: ['#'], block: null };

const LANGS = {
  go: {
    ...C_LIKE, quotes: '"\'`', raw: '`',
    keywords: 'break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota',
  },
  swift: {
    ...C_LIKE, quotes: '"',
    keywords: 'func let var if else guard return struct class enum protocol extension import for in while switch case default break continue throw throws try catch do async await self Self nil true false public private internal fileprivate static init some any where',
  },
  js: {
    ...C_LIKE, quotes: '"\'`',
    keywords: 'const let var function return if else for while do switch case default break continue new class extends import export from async await try catch finally throw typeof instanceof in of this null undefined true false interface type enum implements as',
  },
  python: {
    ...HASH, quotes: '"\'',
    keywords: 'def class return if elif else for while in not and or is import from as with try except finally raise lambda yield pass break continue None True False async await global nonlocal',
  },
  sh: {
    ...HASH, quotes: '"\'', variables: true,
    keywords: 'if then else elif fi for do done case esac while until function in export local return',
  },
  json: { line: [], block: null, quotes: '"', keys: 'json', keywords: 'true false null' },
  yaml: { ...HASH, quotes: '"\'', keys: 'yaml', keywords: 'true false null yes no' },
};

const ALIASES = {
  go: 'go', golang: 'go', swift: 'swift',
  js: 'js', javascript: 'js', jsx: 'js', mjs: 'js', ts: 'js', typescript: 'js', tsx: 'js',
  py: 'python', python: 'python',
  sh: 'sh', bash: 'sh', zsh: 'sh', shell: 'sh', console: 'sh',
  json: 'json', yaml: 'yaml', yml: 'yaml', diff: 'diff', patch: 'diff',
};

const isIdentStart = (c) => /[A-Za-z_]/.test(c);
const isIdent = (c) => /[A-Za-z0-9_]/.test(c);
const isDigit = (c) => c >= '0' && c <= '9';

// highlight returns the tokens of code in lang (a fence's info string, case-insensitive).
export function highlight(code, lang) {
  const name = ALIASES[String(lang || '').trim().toLowerCase()];
  if (!name) return merge([{ k: 'plain', v: code }]);
  if (name === 'diff') return merge(diff(code));
  return merge(scan(code, LANGS[name]));
}

function diff(code) {
  return code.split(/(?<=\n)/).map((line) => {
    if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('@@')) return { k: 'meta', v: line };
    if (line.startsWith('+')) return { k: 'added', v: line };
    if (line.startsWith('-')) return { k: 'removed', v: line };
    return { k: 'plain', v: line };
  });
}

function scan(code, lang) {
  const keywords = new Set(lang.keywords.split(' '));
  const out = [];
  const push = (k, v) => out.push({ k, v });
  let i = 0;
  let lineStart = true;
  while (i < code.length) {
    const c = code[i];
    if (lang.keys === 'yaml' && lineStart) {
      const m = /^([ \t]*(?:- )?)([A-Za-z0-9_./-]+)(?=:(?:[ \t]|\n|$))/.exec(code.slice(i));
      if (m) {
        if (m[1]) push('plain', m[1]);
        push('key', m[2]);
        i += m[0].length;
        lineStart = false;
        continue;
      }
    }
    lineStart = false;
    if (c === '\n') { push('plain', c); i++; lineStart = true; continue; }
    const line = lang.line.find((p) => code.startsWith(p, i));
    if (line) {
      const end = code.indexOf('\n', i);
      const stop = end < 0 ? code.length : end;
      push('comment', code.slice(i, stop));
      i = stop;
      continue;
    }
    if (lang.block && code.startsWith(lang.block[0], i)) {
      const end = code.indexOf(lang.block[1], i + lang.block[0].length);
      const stop = end < 0 ? code.length : end + lang.block[1].length;
      push('comment', code.slice(i, stop));
      i = stop;
      continue;
    }
    if (lang.quotes.includes(c)) {
      const stop = stringEnd(code, i, c, c === lang.raw || c === '`');
      const v = code.slice(i, stop);
      i = stop;
      if (lang.keys === 'json' && /^[ \t]*:/.test(code.slice(i))) push('key', v);
      else push('string', v);
      continue;
    }
    if (lang.variables && c === '$' && i + 1 < code.length && (isIdentStart(code[i + 1]) || code[i + 1] === '{')) {
      let j = i + 1;
      if (code[j] === '{') {
        const end = code.indexOf('}', j);
        j = end < 0 ? code.length : end + 1;
      } else {
        while (j < code.length && isIdent(code[j])) j++;
      }
      push('key', code.slice(i, j));
      i = j;
      continue;
    }
    if (isDigit(c) && (i === 0 || !isIdent(code[i - 1]))) {
      let j = i + 1;
      while (j < code.length && /[0-9A-Fa-fxX._]/.test(code[j])) j++;
      push('number', code.slice(i, j));
      i = j;
      continue;
    }
    if (isIdentStart(c)) {
      let j = i + 1;
      while (j < code.length && isIdent(code[j])) j++;
      const word = code.slice(i, j);
      if (keywords.has(word)) push('keyword', word);
      else if (code[j] === '(') push('function', word);
      else push('plain', word);
      i = j;
      continue;
    }
    push('plain', c);
    i++;
  }
  return out;
}

// stringEnd is the index just past the string that opens with quote at start: a backslash escapes
// the next character (not in a raw string), and only a multiline (backtick) string crosses a line.
function stringEnd(code, start, quote, multiline) {
  let i = start + 1;
  while (i < code.length) {
    const c = code[i];
    if (c === '\\' && quote !== '`') { i += 2; continue; }
    if (c === quote) return i + 1;
    if (c === '\n' && !multiline) return i;
    i++;
  }
  return code.length;
}

// merge joins neighbouring tokens of one kind and drops empty ones.
function merge(tokens) {
  const out = [];
  for (const t of tokens) {
    if (!t.v) continue;
    const last = out[out.length - 1];
    if (last && last.k === t.k) last.v += t.v;
    else out.push({ k: t.k, v: t.v });
  }
  return out;
}
