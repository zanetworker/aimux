// aimux's one-shot iTerm2 pane opener, run as
//   osascript -l JavaScript opentab.js <maxPanes> <title> <line>
// The three arguments are data: nothing from them is evaluated.
// aimux's panes carry the session variables user.aimux = "1",
// user.aimux_col = "0" or "1" and user.aimux_seq (open order); a window
// holding such panes is an aimux window. Nothing is remembered: each run scans every window for them, and
// panes without the variable (the owner's) are neither counted nor split.
// The newest aimux window (highest id) with fewer than maxPanes aimux panes
// gets a new pane (plan below), else a new window is created with the
// default profile and its first session is the pane. The pane is marked,
// named "aimux · <title>", and line and a newline are written into it. It
// prints {"window": <id>, "pane": <aimux panes in that window now>}. It
// never activates, focuses or closes anything. Apple event denial prints
// {"error":"not_authorized"}; other failures {"error": "..."}, with
// "window" when the window is known.

const MARK = 'user.aimux';
const COL = 'user.aimux_col';
const SEQ = 'user.aimux_seq';

// plan decides where the next pane goes. windows: [{id, panes: [{col, seq,
// ref}]}] with only aimux panes, in any order: iTerm2's listing order is not
// relied on, seq (user.aimux_seq, 1 for a window's first pane, then the
// window's highest + 1) orders them. Returns {action: 'new', col: 0, seq: 1,
// count: 1} or {action: 'vertical' | 'horizontal', window, target, col, seq,
// count}: split target that way and mark the new pane col and seq; count is
// the window's aimux panes after the open. Layout: 2 columns of up to 5;
// pane 2 splits pane 1 vertically, later ones split horizontally the
// highest-seq pane (the bottom) of the column with fewer panes (left on a
// tie). When a column is empty (pane 2, or the owner closed a whole column)
// the highest-seq pane left splits vertically and the new pane takes the
// empty column.
function plan(windows, maxPanes) {
  let best = null;
  for (const w of windows) {
    const n = w.panes.length;
    if (n === 0 || n >= maxPanes) continue;
    if (best === null || w.id > best.id) best = w;
  }
  if (best === null) return { action: 'new', col: 0, seq: 1, count: 1 };
  const cols = [[], []];
  let top = null;
  for (const p of best.panes) {
    cols[p.col === 1 ? 1 : 0].push(p);
    if (top === null || p.seq > top.seq) top = p;
  }
  const seq = top.seq + 1;
  const count = best.panes.length + 1;
  if (cols[0].length === 0 || cols[1].length === 0) {
    const empty = cols[0].length === 0 ? 0 : 1;
    return { action: 'vertical', window: best.id, target: top.ref, col: empty, seq: seq, count: count };
  }
  const c = cols[1].length < cols[0].length ? 1 : 0;
  let bottom = cols[c][0];
  for (const p of cols[c]) if (p.seq > bottom.seq) bottom = p;
  return { action: 'horizontal', window: best.id, target: bottom.ref, col: c, seq: seq, count: count };
}

function denied(e) {
  return e && (e.errorNumber === -1743 || String(e.message).indexOf('Not authorized to send Apple events') >= 0);
}

function message(e) {
  return String(e && e.message ? e.message : e);
}

// readVar is a session variable's value, '' when unset or unreadable.
function readVar(session, name) {
  try {
    const v = session.variable({ named: name });
    return v === null || v === undefined ? '' : String(v);
  } catch (e) {
    if (denied(e)) throw e;
    return '';
  }
}

// scan lists every window with its aimux panes, for plan.
function scan(app) {
  const windows = [];
  const ids = app.windows.id();
  for (let w = 0; w < ids.length; w++) {
    const panes = [];
    const tabs = app.windows.byId(ids[w]).tabs();
    for (let t = 0; t < tabs.length; t++) {
      const sessions = tabs[t].sessions();
      for (let s = 0; s < sessions.length; s++) {
        if (readVar(sessions[s], MARK) !== '1') continue;
        panes.push({
          col: readVar(sessions[s], COL) === '1' ? 1 : 0,
          seq: Math.max(0, parseInt(readVar(sessions[s], SEQ), 10) || 0),
          ref: sessions[s],
        });
      }
    }
    windows.push({ id: ids[w], panes: panes });
  }
  return windows;
}

function run(argv) {
  const maxPanes = Number(argv[0]) || 10;
  const title = String(argv[1]);
  const line = String(argv[2]);
  let id = 0;
  try {
    const app = Application('iTerm2');
    const p = plan(scan(app), maxPanes);
    let session;
    if (p.action === 'new') {
      const win = app.createWindowWithDefaultProfile();
      id = win.id();
      session = win.currentTab().currentSession();
    } else {
      id = p.window;
      session = p.action === 'vertical'
        ? p.target.splitVerticallyWithDefaultProfile()
        : p.target.splitHorizontallyWithDefaultProfile();
    }
    session.setVariable({ named: MARK, to: '1' });
    session.setVariable({ named: COL, to: String(p.col) });
    session.setVariable({ named: SEQ, to: String(p.seq) });
    session.name = 'aimux · ' + title;
    session.write({ text: line, newline: true });
    return JSON.stringify({ window: id, pane: p.count });
  } catch (e) {
    if (denied(e)) return JSON.stringify({ error: 'not_authorized' });
    if (id > 0) return JSON.stringify({ window: id, error: message(e) });
    return JSON.stringify({ error: message(e) });
  }
}

if (typeof module !== 'undefined') module.exports = { plan: plan };
