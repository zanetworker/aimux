// Prints, for each case of the fixture given as the first argument, the
// activity the phone web client derives from the session and its events
// with its own functions (now.mjs deriveActivity, search.mjs), mapped onto
// the server's four words. Read by activity_parity_test.go.
import { readFileSync } from 'node:fs';
import { deriveActivity } from '../../../clients/web/webapp/now.mjs';
import { isOpen, isWaitingForPermission, isWorking } from '../../../clients/web/webapp/search.mjs';

const { cases } = JSON.parse(readFileSync(process.argv[2], 'utf8'));
const out = {};
for (const c of cases) {
  const activity = deriveActivity(c.events);
  if (activity.kind === 'needsApproval' || isWaitingForPermission(c.session)) out[c.name] = 'needs_you';
  else if (isWorking(c.session, activity)) out[c.name] = 'working';
  else if (isOpen(c.session, activity)) out[c.name] = 'idle';
  else out[c.name] = 'gone';
}
process.stdout.write(JSON.stringify(out));
