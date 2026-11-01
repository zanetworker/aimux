// Screenshot the aimux web dashboard served by `DEMO_MODE=web` (see record.sh).
import { chromium } from 'playwright';
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: 1440, height: 860 }, deviceScaleFactor: 2 });
await p.goto('http://localhost:3000/', { waitUntil: 'load' });
await p.waitForTimeout(3500);
await p.getByText('Add idempotency keys to POST').first().click();
await p.waitForTimeout(3000);
await p.screenshot({ path: process.argv[2] || 'out/web.png' });
await b.close();
