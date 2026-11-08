// Open on Mac: hand a session the Mac runs in the background to a terminal pane (POST
// /v1/sessions/{id}/open-on-mac). Copy from api/testdata/clients/terminal/open-on-mac.json.

export const COPY = Object.freeze({
  action: 'Open on Mac',
  progress: 'Opening on your Mac…',
  done: 'Open in the aimux window on your Mac',
  busy: 'Claude is answering. Wait for it to finish, or Stop it first.',
  open_in_terminal: 'Already open in a terminal on your Mac.',
  no_terminal: 'Open on Mac needs iTerm2 on your Mac.',
  not_authorized: 'aimux may not control iTerm2. Allow it in System Settings, Privacy & Security, Automation.',
  in_use: 'Another aimux process is using this session.',
  not_started: 'This session has no messages yet, so there is nothing to open.',
  timeout: 'The pane opened, but Claude did not pick up the session. Check the aimux window on your Mac.',
});

// canOpenOnMac: whether the session can be opened on the Mac. Requires health.openOnMac and the
// session not already open_in_terminal.
export function canOpenOnMac(session, health) {
  if (!session || !health || !health.openOnMac) return false;
  return session.state !== 'open_in_terminal';
}

// openOnMacMessage: the text for a given key. Unknown codes fall back to the timeout text.
export function openOnMacMessage(key) {
  return COPY[key] || COPY.timeout;
}

// openOnMac: opens the session on the Mac. Returns {ok:true} on success, or {ok:false, code,
// message} on error. Client-side timeout (AbortError) returns the timeout message.
export async function openOnMac(api, id) {
  try {
    await api('POST', '/v1/sessions/' + encodeURIComponent(id) + '/open-on-mac');
    return { ok: true };
  } catch (e) {
    // Client-side timeout (AbortError) or server timeout both use the timeout message
    if (e.name === 'AbortError') {
      return { ok: false, code: '', message: COPY.timeout };
    }
    return {
      ok: false,
      code: e.code || '',
      message: openOnMacMessage(e.code || ''),
    };
  }
}
