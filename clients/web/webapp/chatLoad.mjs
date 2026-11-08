// readChat reads an open chat consistently: the event snapshot, then the
// history, then the events since the snapshot. A turn that started after the
// snapshot, or a server restart (a new log), means reading both again, at
// most three times. When the person opened the chat (opened), the first
// history read carries the interaction mark, so opening a chat keeps the
// login alive; the snapshot and the reads after it never do. stale() true
// (the person went elsewhere) stops it and returns null.
export async function readChat(api, id, { opened = false, stale = () => false } = {}) {
  const base = '/v1/sessions/' + encodeURIComponent(id);
  const eventsURL = base + '/events?wait=0';
  for (let attempt = 0; ; attempt++) {
    const page = await api('GET', eventsURL);
    if (stale()) return null;
    const detail = await api('GET', base + '?turns=30', undefined, opened && attempt === 0 ? { interaction: true } : undefined);
    if (stale()) return null;
    if (detail.state === 'open_in_terminal') return { page, detail };
    const seen = page.events.length ? page.events[page.events.length - 1].seq : 0;
    const since = await api('GET', eventsURL + '&after=' + seen + '&log=' + encodeURIComponent(page.log || ''));
    if (stale()) return null;
    const changed = since.log !== page.log || since.events.some((e) => e.kind === 'turn_started');
    if (!changed || attempt >= 2) return { page, detail };
  }
}
