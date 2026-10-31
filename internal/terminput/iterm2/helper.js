// aimux's iTerm2 helper, run by osascript -l JavaScript for the life of one
// aimux process. One JSON request per line on stdin, one JSON reply per line
// on stdout, in order. Requests are data: nothing from them is evaluated.
// It never creates, focuses, resizes or closes windows, tabs or sessions
// (focus only reads which session has focus),
// and never starts iTerm2: while it is not running, locate lists no sessions
// and every other op answers missing.
ObjC.import('Foundation');

const app = Application('iTerm2');
const input = $.NSFileHandle.fileHandleWithStandardInput;
const output = $.NSFileHandle.fileHandleWithStandardOutput;

function reply(obj) {
  output.writeData($(JSON.stringify(obj) + '\n').dataUsingEncoding($.NSUTF8StringEncoding));
}

// aimux sends ASCII only (non-ASCII escaped as \uXXXX), so a read can never
// split a multi-byte character.
let pending = '';
function readLine() {
  for (;;) {
    const nl = pending.indexOf('\n');
    if (nl >= 0) {
      const line = pending.slice(0, nl);
      pending = pending.slice(nl + 1);
      return line;
    }
    const data = input.availableData;
    if (Number(data.length) === 0) return null; // EOF: aimux closed stdin (length bridges as a string)
    pending += $.NSString.alloc.initWithDataEncoding(data, $.NSASCIIStringEncoding).js;
  }
}

function denied(e) {
  return e && (e.errorNumber === -1743 || String(e.message).indexOf('Not authorized to send Apple events') >= 0);
}

class Missing extends Error {}

// app.running() asks macOS, not iTerm2; any other access to app starts
// iTerm2 when it is not running, so every path checks it first.
function locate() {
  if (!app.running()) return [];
  const wins = app.windows.id();
  const ttys = app.windows.tabs.sessions.tty();
  const ids = app.windows.tabs.sessions.id();
  const sessions = [];
  for (let w = 0; w < wins.length; w++) {
    for (let t = 0; t < ttys[w].length; t++) {
      for (let s = 0; s < ttys[w][t].length; s++) {
        sessions.push({ window: wins[w], session: ids[w][t][s], tty: ttys[w][t][s] });
      }
    }
  }
  return sessions;
}

// focus reports whether iTerm2 is the frontmost app and the tty of the
// current session of its current window, the one keystrokes go to. It only
// reads: asking iTerm2 for frontmost needs no other app's permission.
function focus() {
  if (!app.running()) return { frontmost: false, tty: '' };
  const front = app.frontmost();
  const w = app.currentWindow();
  if (w === null) return { frontmost: front, tty: '' };
  return { frontmost: front, tty: String(w.currentSession().tty()) };
}

function session(req) {
  if (!app.running()) throw new Missing();
  let tabs;
  try {
    tabs = app.windows.byId(req.window).tabs;
    const ids = tabs.sessions.id();
    for (let t = 0; t < ids.length; t++) {
      if (ids[t].indexOf(req.session) >= 0) return tabs[t].sessions.byId(req.session);
    }
  } catch (e) {
    if (denied(e)) throw e;
  }
  throw new Missing();
}

function write(s, text) {
  s.write({ text: text, newline: false });
}

// clear: a space (dismisses Claude Code's grey suggestion), then Ctrl+K and
// Ctrl+U as CSI-u sequences, so Claude Code reads them as keys in one burst.
const KEYS = { escape: '\u001b', enter: '\r', clear: ' \u001b[107;5u\u001b[117;5u' };

function handle(req) {
  switch (req.op) {
    case 'locate':
      return { sessions: locate() };
    case 'focus':
      return focus();
    case 'send': {
      const s = session(req);
      const text = String(req.text);
      // No Enter: aimux reads the screen again and sends it as a key.
      write(s, req.paste ? '\u001b[200~' + text + '\u001b[201~' : text);
      return { ok: true };
    }
    case 'key': {
      const k = String(req.key);
      const bytes = /^[1-9]$/.test(k) ? k : KEYS[k];
      if (!bytes) return { error: 'unknown key' };
      write(session(req), bytes);
      return { ok: true };
    }
    case 'screen': {
      const s = session(req);
      let text;
      try { text = s.contents(); } catch (e) { if (denied(e)) throw e; text = s.text(); }
      return { text: String(text) };
    }
    default:
      return { error: 'unknown op' };
  }
}

for (;;) {
  const line = readLine();
  if (line === null) break;
  if (line.trim() === '') continue;
  let req;
  try { req = JSON.parse(line); } catch (e) { reply({ error: 'bad request' }); continue; }
  let res;
  try {
    res = handle(req);
  } catch (e) {
    // -1728: the session went away between lookup and use.
    if (e instanceof Missing || (e && e.errorNumber === -1728)) res = { error: 'missing' };
    else if (denied(e)) res = { error: 'automation_denied' };
    else res = { error: String(e && e.message ? e.message : e) };
  }
  res.id = req.id;
  reply(res);
}
