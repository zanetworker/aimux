import './styles/theme.css';

export default function App() {
  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}>
      <header style={{
        padding: '12px 24px',
        background: 'var(--bg-1)',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
      }}>
        <span style={{ fontSize: 18, fontWeight: 700 }}>
          <span style={{ color: 'var(--accent)' }}>ai</span>
          <span>mux</span>
        </span>
        <span style={{ color: 'var(--fg-3)', fontSize: 12 }}>Dashboard loading...</span>
      </header>
      <main style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <p style={{ color: 'var(--fg-3)' }}>Components will be added in subsequent tasks.</p>
      </main>
    </div>
  );
}
