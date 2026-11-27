import { useEffect, useState, type CSSProperties, type ReactNode } from 'react';
import {
  activityText, ago, buildNumber, engineWords, isCommand, meterNotes, shortTokens, verdictLine,
  type DoctorDoc, type EnginesDoc, type MeterDoc, type ServicesDoc,
} from './system';

// The System page: what `aimux status`, `aimux model proxy doctor`, `aimux engine list` and `aimux cost` print,
// read from the server's four endpoints. It shows and never changes anything; where something is wrong it gives
// the command that fixes it.

// at is when data was read, by this browser's clock: ages on the page are counted from it.
interface Loaded<T> { data: T | null; error: string | null; at: number; }
interface Read<T> extends Loaded<T> { url: string; }

// useJSON reads url now and every `every` ms. A failed read keeps the last good answer beside the error. An
// answer is shown only for the url that fetched it: after the url changes, nothing until the new one is read.
function useJSON<T>(url: string, every: number): Loaded<T> {
  const [state, setState] = useState<Read<T>>({ url, data: null, error: null, at: 0 });
  useEffect(() => {
    let live = true;
    const read = () => fetch(url)
      .then(async r => {
        const doc = await r.json().catch(() => null);
        if (!r.ok) throw new Error(doc?.error || `the server answered ${r.status}`);
        return doc as T;
      })
      .then(data => { if (live) setState({ url, data, error: null, at: Date.now() }); })
      .catch(e => { if (live) setState(s => (s.url === url ? { ...s, error: e.message } : { url, data: null, error: e.message, at: 0 })); });
    read();
    const timer = setInterval(read, every);
    return () => { live = false; clearInterval(timer); };
  }, [url, every]);
  return state.url === url ? state : { data: null, error: null, at: 0 };
}

const levelColor: Record<string, string> = {
  ok: 'var(--green)', warn: 'var(--orange)', degraded: 'var(--orange)', fail: 'var(--accent)', down: 'var(--accent)',
};

function Level({ level }: { level: string }) {
  const color = levelColor[level] ?? 'var(--fg-3)';
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: 'var(--fg)' }}>
      <span aria-hidden style={{ width: 7, height: 7, borderRadius: '50%', background: color, flexShrink: 0 }} />
      {level}
    </span>
  );
}

const cell: CSSProperties = { padding: '7px 16px 7px 0', textAlign: 'left', verticalAlign: 'top', fontSize: 12 };
const numeric: CSSProperties = { ...cell, textAlign: 'right', fontFamily: 'var(--mono)', fontVariantNumeric: 'tabular-nums' };
const mono: CSSProperties = { fontFamily: 'var(--mono)', fontSize: 11.5 };

function Table({ head, right = [], children }: { head: string[]; right?: number[]; children: ReactNode }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ borderCollapse: 'collapse', width: '100%' }}>
        <thead>
          <tr>
            {head.map((h, i) => (
              <th key={h} scope="col" style={{ ...cell, textAlign: right.includes(i) ? 'right' : 'left', color: 'var(--fg-3)', fontWeight: 500, borderBottom: '1px solid var(--border-hover)' }}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

const row: CSSProperties = { borderBottom: '1px solid var(--border)' };

function Section({ title, command, error, children }: { title: string; command: string; error?: string | null; children: ReactNode }) {
  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <header style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12 }}>
        <h2 style={{ margin: 0, fontSize: 14, fontWeight: 600, color: 'var(--fg)' }}>{title}</h2>
        <code style={{ ...mono, color: 'var(--fg-3)' }}>{command}</code>
      </header>
      {error && <p role="alert" style={{ margin: 0, fontSize: 12, color: 'var(--accent)' }}>Could not be read: {error}</p>}
      {children}
    </section>
  );
}

const Note = ({ children }: { children: ReactNode }) => (
  <p style={{ margin: 0, fontSize: 12, color: 'var(--fg-2)', lineHeight: 1.5 }}>{children}</p>
);

// A fix: a command is set as one, to be selected whole; advice in words stays words.
const Fix = ({ children }: { children: string }) => (isCommand(children)
  ? <code style={{ ...mono, color: 'var(--fg)', background: 'var(--bg-2)', padding: '1px 6px', borderRadius: 3, userSelect: 'all' }}>{children}</code>
  : <span style={{ color: 'var(--fg)' }}>{children}</span>);

function Services() {
  const { data, error, at: now } = useJSON<ServicesDoc>('/api/services', 15_000);
  return (
    <Section title="Services" command="aimux status" error={error}>
      {data && (
        <>
          <Table head={['Service', 'State', 'Build', 'Up', 'Address', 'Last activity']}>
            {data.services.map(s => (
              <tr key={s.name} style={row}>
                <td style={cell}>{s.name}</td>
                <td style={cell}><Level level={s.state} />{s.reason && <span style={{ color: 'var(--fg-2)' }}> · {s.reason}</span>}</td>
                <td style={{ ...cell, ...mono }}>{buildNumber(s.build)}{s.build_note && <span style={{ color: 'var(--orange)' }}> ({s.build_note})</span>}</td>
                <td style={{ ...cell, ...mono }}>{ago(s.started_at, now)}</td>
                <td style={{ ...cell, ...mono, color: 'var(--fg-2)' }}>{s.address ?? '-'}</td>
                <td style={{ ...cell, ...mono, color: 'var(--fg-2)' }}>{activityText(s, now)}</td>
              </tr>
            ))}
          </Table>
          {data.attention.length === 0
            ? <Note>Nothing needs attention. This aimux is build {buildNumber(data.cli_build)}.</Note>
            : (
              <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}>
                {data.attention.map(a => (
                  <li key={a.problem} style={{ fontSize: 12, color: 'var(--fg)', lineHeight: 1.6 }}>
                    {a.problem}{a.fix && <> To fix it: <Fix>{a.fix}</Fix></>}
                  </li>
                ))}
              </ul>
            )}
        </>
      )}
    </Section>
  );
}

function Doctor() {
  const { data, error } = useJSON<DoctorDoc>('/api/proxy/doctor', 60_000);
  const open = data?.findings.filter(f => f.level !== 'ok') ?? [];
  const fine = data?.findings.filter(f => f.level === 'ok') ?? [];
  return (
    <Section title="Model proxy" command="aimux model proxy doctor" error={error}>
      {data && (
        <>
          <Note><span style={{ color: 'var(--fg)' }}>{verdictLine(data.findings)}</span> Checked on port {data.port}.</Note>
          {data.allowance?.map(line => (
            <p key={line} style={{ margin: 0, fontSize: 12, color: 'var(--fg)', lineHeight: 1.5 }}>{line}</p>
          ))}
          {open.length > 0 && (
            <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 10 }}>
              {open.map(f => (
                <li key={f.id} style={{ display: 'grid', gridTemplateColumns: '64px 1fr', gap: 8, alignItems: 'start', fontSize: 12, lineHeight: 1.6 }}>
                  <Level level={f.level} />
                  <div>
                    <div style={{ color: 'var(--fg)' }}>{f.detail}</div>
                    {f.fix && <div style={{ color: 'var(--fg-2)', marginTop: 2 }}>What to do: <Fix>{f.fix}</Fix></div>}
                  </div>
                </li>
              ))}
            </ul>
          )}
          {fine.length > 0 && (
            <details>
              <summary style={{ fontSize: 12, color: 'var(--fg-2)', cursor: 'pointer' }}>{fine.length} {fine.length === 1 ? 'check passes' : 'checks pass'}</summary>
              <ul style={{ margin: '8px 0 0', padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 4 }}>
                {fine.map(f => (
                  <li key={f.id} style={{ display: 'grid', gridTemplateColumns: '64px 1fr', gap: 8, fontSize: 12, color: 'var(--fg-2)', lineHeight: 1.6 }}>
                    <Level level="ok" /><span>{f.detail}</span>
                  </li>
                ))}
              </ul>
            </details>
          )}
        </>
      )}
    </Section>
  );
}

function Engines() {
  const { data, error } = useJSON<EnginesDoc>('/api/engines', 60_000);
  const words = data?.engines.map(engineWords) ?? [];
  const silent = words.length > 0 && words.every(w => w.running === '-');
  return (
    <Section title="Engines and roles" command="aimux engine list" error={error}>
      {data && (
        <>
          <Table head={['Engine', 'In the file', 'Running', 'Type', 'Key']}>
            {data.engines.map((e, i) => (
              <tr key={e.name} style={row}>
                <td style={cell}>{e.name}</td>
                <td style={cell}>{words[i].file}</td>
                <td style={{ ...cell, color: words[i].differs ? 'var(--orange)' : undefined }}>{words[i].running}</td>
                <td style={{ ...cell, ...mono, color: 'var(--fg-2)' }}>{e.type}</td>
                <td style={{ ...cell, color: 'var(--fg-2)' }}>{e.key_at ?? '-'}</td>
              </tr>
            ))}
          </Table>
          {silent && <Note>The inference proxy on port {data.port} did not say which engines it runs: it does not answer, or it is a build from before it said. Only the file is shown.</Note>}
          {words.some(w => w.differs) && <Note>The running proxy and the file differ. To make them match: <Fix>aimux setup --apply</Fix></Note>}

          <Table head={['Role', 'Uses', 'Model', 'Answered by']}>
            {data.roles.map(r => (
              <tr key={r.role} style={row}>
                <td style={cell}>{r.role}</td>
                <td style={{ ...cell, ...mono }}>{r.entry ?? '-'}</td>
                <td style={{ ...cell, ...mono }}>{r.model ?? (r.role === 'main' ? 'chosen in the session' : '-')}</td>
                <td style={cell}>{r.engine ?? '-'}{r.note && <span style={{ color: 'var(--orange)' }}> · {r.note}</span>}</td>
              </tr>
            ))}
          </Table>

          {data.subagents.length > 0 && (
            <details>
              <summary style={{ fontSize: 12, color: 'var(--fg-2)', cursor: 'pointer' }}>
                {data.subagents.length} subagent {data.subagents.length === 1 ? 'definition' : 'definitions'}
              </summary>
              <div style={{ marginTop: 8 }}>
                <Table head={['Subagent', 'Defined in', 'Names the model', 'Runs on']}>
                  {data.subagents.map(s => (
                    <tr key={`${s.defined_in}/${s.name}`} style={row}>
                      <td style={cell}>{s.name}</td>
                      <td style={{ ...cell, ...mono, color: 'var(--fg-2)' }}>{s.defined_in}</td>
                      <td style={{ ...cell, ...mono }}>{s.model ?? '-'}</td>
                      <td style={cell}>{s.runs_on ? `${s.runs_on}${s.from_role ? ' (the subagent role)' : ''}` : "the session's model"}</td>
                    </tr>
                  ))}
                </Table>
                <div style={{ marginTop: 8 }}>
                  <Note>What each would run on going by the config file. A definition that names a model keeps it; the others follow the subagent role when one is set.</Note>
                </div>
              </div>
            </details>
          )}
        </>
      )}
    </Section>
  );
}

const SINCE = ['24h', '7d', '30d', 'all'] as const;
const BY = ['role', 'model', 'session', 'day'] as const;

function Choice<T extends string>({ label, options, value, onChange }: { label: string; options: readonly T[]; value: T; onChange: (v: T) => void }) {
  return (
    <label style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--fg-2)' }}>
      {label}
      <select value={value} onChange={e => onChange(e.target.value as T)} style={{
        background: 'var(--bg-1)', color: 'var(--fg)', border: '1px solid var(--border-hover)', borderRadius: 4, padding: '3px 6px', fontSize: 12,
      }}>
        {options.map(o => <option key={o} value={o}>{o}</option>)}
      </select>
    </label>
  );
}

function Spend() {
  const [since, setSince] = useState<typeof SINCE[number]>('24h');
  const [by, setBy] = useState<typeof BY[number]>('role');
  const { data, error } = useJSON<MeterDoc>(`/api/meter?since=${since}&by=${by}`, 60_000);
  const rows = data?.rows ?? [];
  const approx = (r: { estimated?: boolean }, n: number | undefined) => (r.estimated ? '~' : '') + shortTokens(n ?? 0);
  return (
    <Section title="Model calls" command={`aimux cost --since ${since} --by ${by}`} error={error}>
      <div style={{ display: 'flex', gap: 16 }}>
        <Choice label="Since" options={SINCE} value={since} onChange={setSince} />
        <Choice label="By" options={BY} value={by} onChange={setBy} />
      </div>
      {rows.length > 0 && (
        <Table head={[by[0].toUpperCase() + by.slice(1), 'Calls', 'Failed', 'Input', 'Cached', 'Output', 'Engines']} right={[1, 2, 3, 4, 5]}>
          {rows.map(r => (
            <tr key={r.key} style={row}>
              <td style={{ ...cell, ...mono }} title={r.key}>{by === 'session' ? r.key.slice(0, 8) : r.key || <span style={{ color: 'var(--fg-3)' }}>none named</span>}</td>
              <td style={numeric}>{r.requests}</td>
              <td style={{ ...numeric, color: r.failed ? 'var(--orange)' : 'var(--fg-3)' }}>{r.failed ?? 0}</td>
              <td style={numeric}>{approx(r, r.input_tokens)}</td>
              <td style={numeric}>{approx(r, r.cached_input_tokens)}</td>
              <td style={numeric}>{approx(r, r.output_tokens)}</td>
              <td style={{ ...cell, color: 'var(--fg-2)' }}>{r.engines?.join(', ') ?? '-'}</td>
            </tr>
          ))}
        </Table>
      )}
      {data && (
        <Note>
          Counted from the ledger: calls and tokens, not money.
          {meterNotes(data).map(n => <span key={n}> {n}</span>)}
        </Note>
      )}
    </Section>
  );
}

export function SystemView() {
  return (
    <div style={{ flex: 1, overflowY: 'auto', padding: '20px 24px 40px' }}>
      <div style={{ maxWidth: 1080, display: 'flex', flexDirection: 'column', gap: 36 }}>
        <Services />
        <Doctor />
        <Engines />
        <Spend />
      </div>
    </div>
  );
}
