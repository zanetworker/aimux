// The pure parts of the System page: the shapes the server answers with (GET /api/services, /api/proxy/doctor,
// /api/engines and /api/meter, the documents `aimux status`, `aimux model proxy doctor`, `aimux engine list` and
// `aimux cost` print as JSON) and the words the page puts on them, which are the commands' words.

export interface Attention { problem: string; fix: string; }

export interface Service {
  name: string;
  state: 'ok' | 'degraded' | 'down' | 'not installed' | string;
  reason?: string;
  build?: string;
  build_note?: string;
  started_at?: string;
  address?: string;
  last_activity?: string;
  activity_note?: string;
}

export interface ServicesDoc { cli_build: string; services: Service[]; attention: Attention[]; }

export interface Finding { id: string; level: 'ok' | 'warn' | 'fail' | string; detail: string; fix?: string; }

export interface DoctorDoc { port: number; verdict: string; findings: Finding[]; }

export interface Engine {
  name: string;
  on: boolean;
  type: string;
  auth?: string;
  url?: string;
  key_at?: string;
  running?: boolean; // absent when the proxy did not say
  stated: boolean;
}

export interface Role { role: string; entry?: string; model?: string; engine?: string; note?: string; }

export interface Subagent { name: string; defined_in: string; model?: string; runs_on?: string; from_role?: boolean; }

export interface EnginesDoc { port: number; engines: Engine[]; roles: Role[]; subagents: Subagent[]; }

export interface MeterRow {
  key: string;
  requests: number;
  failed?: number;
  input_tokens?: number;
  cached_input_tokens?: number;
  output_tokens?: number;
  estimated?: boolean; // a call in the row had estimated usage
  models?: string[];
  engines?: string[];
}

export interface MeterDoc {
  rows: MeterRow[] | null;
  total?: MeterRow;
  unmatched?: number;
  skipped?: number;
  torn?: number;
  no_usage?: number;
  incomplete?: boolean; // lines were skipped, torn or missing
  files?: number; // ledger files read; 0: nothing has written a ledger here yet
}

// A token count short enough for a table cell: 950, 1.0k, 136k, 7.9M, 43M.
export function shortTokens(n: number | undefined): string {
  if (n === undefined || n === null) return '-';
  for (const [size, unit] of [[1e6, 'M'], [1e3, 'k']] as const) {
    if (n >= size) {
      const v = n / size;
      return (v >= 10 ? String(Math.round(v)) : v.toFixed(1)) + unit;
    }
  }
  return String(n);
}

// How long ago a time was, largest unit only.
export function ago(iso: string | undefined, now: number): string {
  const at = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(at)) return '-';
  const seconds = Math.max(0, Math.floor((now - at) / 1000));
  if (seconds >= 86400) return `${Math.floor(seconds / 86400)}d`;
  if (seconds >= 3600) return `${Math.floor(seconds / 3600)}h`;
  if (seconds >= 60) return `${Math.floor(seconds / 60)}m`;
  return `${seconds}s`;
}

// A build as `aimux status` shows it: the commit count of v0.14.0-326-g2b30bc09, else the version itself.
export function buildNumber(version: string | undefined): string {
  if (!version) return '-';
  return /^v?[\d.]+-(\d+)-g[0-9a-f]+/.exec(version)?.[1] ?? version;
}

// What an engine's row says, in the words of `aimux engine list`. differs: the file and the running proxy
// disagree, which `aimux setup --apply` settles.
export function engineWords(e: Engine): { file: string; running: string; differs: boolean } {
  let file = 'off';
  if (e.type === 'not in the file') file = '-';
  else if (!e.stated) file = 'not stated';
  else if (e.on) file = 'on';
  if (e.running === undefined || e.running === null) return { file, running: '-', differs: false };
  return { file, running: e.running ? 'yes' : 'no', differs: e.stated && e.running !== e.on };
}

const things = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

// The diagnosis in one line, as `aimux model proxy doctor` heads its findings.
export function verdictLine(findings: Finding[]): string {
  const fails = findings.filter(f => f.level === 'fail').length;
  const warns = findings.filter(f => f.level === 'warn').length;
  if (fails > 0 && warns > 0) return `${things(fails, 'thing is', 'things are')} failing, ${things(warns, 'thing', 'things')} to look at.`;
  if (fails > 0) return `${things(fails, 'thing is', 'things are')} failing.`;
  if (warns > 0) return `${things(warns, 'thing', 'things')} to look at; nothing is failing.`;
  return 'Nothing to fix.';
}

// Whether a fix is a command to run, rather than advice in words: it starts with a program this machine has.
export function isCommand(fix: string): boolean {
  return /^(aimux|claude|codex) \S/.test(fix);
}

// A service's last activity: how long ago and what it was, or whichever of the two there is.
export function activityText(s: Pick<Service, 'last_activity' | 'activity_note'>, now: number): string {
  const parts = [s.last_activity ? `${ago(s.last_activity, now)} ago` : '', s.activity_note ?? ''].filter(Boolean);
  return parts.length > 0 ? parts.join(' · ') : '-';
}

// What the meter's figures leave out or cannot vouch for, a sentence each; empty when there is nothing to say.
export function meterNotes(doc: MeterDoc): string[] {
  const rows = doc.rows ?? [];
  if (doc.files === 0) return ['No ledger has been written on this machine yet.'];
  const notes: string[] = [];
  if (rows.length === 0) notes.push('No model calls passed the proxy in this time.');
  if (doc.incomplete) notes.push('The ledger has lines that could not be counted, so these figures are low.');
  const outside = [
    doc.unmatched ? `${doc.unmatched} with no session` : '',
    doc.skipped ? `${doc.skipped} of another schema` : '',
    doc.torn ? `${doc.torn} torn` : '',
  ].filter(Boolean);
  if (outside.length > 0) notes.push(`Not in the table: ${outside.join(', ')}.`);
  if (doc.no_usage) notes.push(`${doc.no_usage} of the calls counted carried no token figures.`);
  if (rows.some(r => r.estimated)) notes.push('~ marks tokens that are partly estimated.');
  return notes;
}
