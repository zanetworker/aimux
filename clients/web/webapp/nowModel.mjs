import { apiDate } from './apiDate.mjs';
import { deriveActivity, rankNow } from './now.mjs';
import { pinErrorText } from './sessionInfo.mjs';
import { savedPromptAsTyped } from './timeline.mjs';

// The Now screen's state, like NowModel in the iPhone app: the session list, what each
// candidate is doing (from its event snapshot), and the ranked view. The page polls refresh().

const RECENT_MS = 900 * 1000;
const SNAPSHOT_LIMIT = 10;
const LIST_LIMIT = 500; // the server's largest; it has no paging
const SEEN_KEY = 'now.seen';

export function newKey() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

// An HTTP answer has a status; a network failure (fetch rejected) has none.
const refused = (e) => typeof e?.status === 'number';

export class NowModel {
  // api(method, path, body) resolves to JSON or throws an Error with `status` for an HTTP error.
  constructor({ api, storage, now = Date.now }) {
    this.api = api;
    this.storage = storage;
    this.now = now;
    this.sessions = []; // as served, with lastActive in epoch milliseconds
    this.lastOnMac = null; // {id, at} with at in epoch milliseconds
    this.health = null; // /v1/health once read: capabilities, naming
    this.pinning = new Set();
    this.pinErrors = new Map(); // session id to the fixed line for the row
    this.describing = new Set(); // sessions with an Update summary on its way
    this.loadError = null;
    this.loaded = false;
    this.answering = new Set();
    this.answerErrors = new Map();
    this.retrying = new Set();
    this.retryErrors = new Map();
    this.activity = {};
    this.answered = new Set();
    this.seen = this.readSeen();
    this.tail = Promise.resolve();
    this.rebuild();
  }

  // Refreshes run one after another, so an older result can never land after a newer one.
  refresh() { return this.serial(() => this.performRefresh()); }

  // The session list alone, for All and search.
  refreshList() { return this.serial(async () => { await this.loadList(); this.rebuild(); }); }

  serial(work) {
    const run = this.tail.then(work);
    this.tail = run.catch(() => {});
    return run;
  }

  async loadList() {
    try {
      const data = await this.api('GET', '/v1/sessions?limit=' + LIST_LIMIT);
      this.sessions = (data.sessions || []).map((s) => ({ ...s, lastActive: apiDate(s.lastActive) }));
      this.lastOnMac = data.lastOnMac ? { id: data.lastOnMac.id, at: apiDate(data.lastOnMac.at) } : null;
      this.loadError = null;
    } catch (e) {
      this.loadError = e; // keep the last list on screen
    }
    this.loaded = true;
  }

  // The server's capabilities and naming status; null until read (a failure is tried again later).
  async readHealth() {
    try {
      this.health = await this.api('GET', '/v1/health');
    } catch { /* keep what we had */ }
    return this.health;
  }

  get canDescribe() { return (this.health?.capabilities || []).includes('describe'); }

  async performRefresh() {
    if (!this.health) await this.readHealth();
    await this.loadList();
    const current = this.now();
    // Every busy session, since any may wait for an approval; the limit is for recent idle ones.
    const newest = [...this.sessions].sort((a, b) => b.lastActive - a.lastActive);
    const recent = newest
      .filter((s) => s.state !== 'open_in_terminal' && s.state !== 'busy' && current - s.lastActive <= RECENT_MS)
      .slice(0, SNAPSHOT_LIMIT);
    const candidates = [...newest.filter((s) => s.state === 'busy'), ...recent];
    const results = await Promise.all(candidates.map(async (s) => {
      try {
        const page = await this.api('GET', '/v1/sessions/' + encodeURIComponent(s.id) + '/events?wait=0');
        return [s.id, deriveActivity(page.events || [])];
      } catch (e) {
        return [s.id, refused(e) ? { kind: 'none' } : this.activity[s.id] ?? { kind: 'none' }];
      }
    }));
    this.activity = Object.fromEntries(results);
    this.rebuild();
  }

  async answer(approval, optionId) {
    const id = approval.approvalId;
    if (this.answering.has(id)) return;
    this.answering.add(id);
    this.answerErrors.delete(id);
    try {
      await this.api('POST', '/v1/approvals/' + encodeURIComponent(id), { option: optionId });
    } catch (e) {
      // no_such_approval: already answered elsewhere, so the card is stale, not failed.
      if (e.status !== 404 || e.code !== 'no_such_approval') {
        this.answerErrors.set(id, e);
        return;
      }
    } finally {
      this.answering.delete(id);
    }
    this.answered.add(id);
    this.rebuild();
  }

  // Sends the session's last prompt again, whole and as typed from its history: the list's
  // lastPrompt is a shortened preview. Each tap is a new attempt, so it gets a new key.
  async retry(session) {
    const id = session.id;
    const prompt = (session.lastPrompt || '').trim();
    if (!prompt || this.retrying.has(id)) return;
    this.retrying.add(id);
    this.retryErrors.delete(id);
    try {
      // Held through the refresh, like the app's defer, so a second tap cannot send twice.
      try {
        const base = '/v1/sessions/' + encodeURIComponent(id);
        const detail = await this.api('GET', base + '?turns=3');
        const text = lastTyped(detail);
        if (!text) return;
        await this.api('POST', base + '/messages', { text, key: newKey() });
      } catch (e) {
        this.retryErrors.set(id, e);
        return;
      }
      await this.refresh();
    } finally {
      this.retrying.delete(id);
    }
  }

  // Pin or unpin from the row menu; a refusal is kept for the row until the next try.
  async setPinned(session, pinned) {
    const id = session.id;
    if (this.pinning.has(id)) return;
    this.pinning.add(id);
    this.pinErrors.delete(id);
    try {
      await this.editMeta(id, { pinned });
    } catch (e) {
      this.pinErrors.set(id, pinErrorText(e, pinned));
    } finally {
      this.pinning.delete(id);
    }
  }

  // PATCH meta with the fields given ({title?, description?, pinned?}); the answer is what clients
  // show now, so the session takes it. Errors go to the caller.
  async editMeta(id, fields) {
    const result = await this.api('PATCH', '/v1/sessions/' + encodeURIComponent(id) + '/meta', fields);
    this.take(id, result);
    return result;
  }

  // Update summary: a new title and description from the model now; one at a time per session, so
  // a call while one runs sends nothing and answers null. Errors go to the caller.
  async describe(id) {
    if (this.describing.has(id)) return null;
    this.describing.add(id);
    try {
      const result = await this.api('POST', '/v1/sessions/' + encodeURIComponent(id) + '/describe');
      this.take(id, result);
      return result;
    } finally {
      this.describing.delete(id);
    }
  }

  // take copies a meta or describe answer into the listed session (description absent: none).
  take(id, result) {
    const s = this.sessions.find((x) => x.id === id);
    if (!s) return;
    s.title = result.title;
    if (typeof result.description === 'string') s.description = result.description; else delete s.description;
    if (typeof result.pinned === 'boolean') s.pinned = result.pinned;
    this.rebuild();
  }

  markSeen(id) {
    this.seen[id] = this.now();
    try { this.storage.setItem(SEEN_KEY, JSON.stringify(this.seen)); } catch { /* private mode: seen for this visit only */ }
    this.rebuild();
  }

  readSeen() {
    try {
      const parsed = JSON.parse(this.storage.getItem(SEEN_KEY) || '{}');
      return parsed && typeof parsed === 'object' ? parsed : {};
    } catch {
      return {};
    }
  }

  rebuild() {
    const shown = { ...this.activity };
    for (const [id, a] of Object.entries(this.activity)) {
      if (a.kind === 'needsApproval' && this.answered.has(a.approval.approvalId)) shown[id] = { kind: 'none' };
    }
    this.view = rankNow({ sessions: this.sessions, activity: shown, seen: this.seen, now: this.now(), lastOnMac: this.lastOnMac });
  }
}

// lastTyped is the latest message the person sent in a session detail, as they typed it.
function lastTyped(detail) {
  const turns = (detail && detail.turns) || [];
  for (let i = turns.length - 1; i >= 0; i--) {
    const text = savedPromptAsTyped(turns[i].user || '').trim();
    if (text) return text;
  }
  return '';
}
