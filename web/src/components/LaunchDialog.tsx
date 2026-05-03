import { useState, useEffect } from 'react';

interface Props {
  open: boolean;
  onClose: () => void;
}

export function LaunchDialog({ open, onClose }: Props) {
  const [provider, setProvider] = useState('claude');
  const [dir, setDir] = useState('');
  const [model, setModel] = useState('');
  const [mode, setMode] = useState('auto');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    if (open) window.addEventListener('keydown', handleKey);
    return () => window.removeEventListener('keydown', handleKey);
  }, [open, onClose]);

  if (!open) return null;

  const handleSubmit = async () => {
    if (!dir) return;
    setSubmitting(true);
    try {
      await fetch('/api/agents/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider, dir, model, mode }),
      });
      onClose();
      // Reset form
      setDir('');
      setModel('');
      setProvider('claude');
      setMode('auto');
    } finally {
      setSubmitting(false);
    }
  };

  const providerColors = {
    claude: {
      background: 'var(--accent-dim)',
      color: 'var(--accent)',
      border: '1px solid var(--accent)',
    },
    codex: {
      background: 'var(--green-dim)',
      color: 'var(--green)',
      border: '1px solid rgba(105,223,115,0.3)',
    },
    gemini: {
      background: 'var(--purple-dim)',
      color: 'var(--purple)',
      border: '1px solid rgba(167,114,239,0.3)',
    },
  };

  const ProviderButton = ({ name }: { name: 'claude' | 'codex' | 'gemini' }) => {
    const isSelected = provider === name;
    const style = providerColors[name];
    return (
      <button
        type="button"
        onClick={() => setProvider(name)}
        style={{
          padding: '6px 12px',
          borderRadius: 4,
          fontSize: 11,
          fontWeight: 600,
          textTransform: 'uppercase',
          letterSpacing: '0.05em',
          cursor: 'pointer',
          opacity: isSelected ? 1 : 0.5,
          transition: 'opacity 0.15s ease',
          ...style,
        }}
        onMouseEnter={(e) => {
          e.currentTarget.style.opacity = '1';
        }}
        onMouseLeave={(e) => {
          e.currentTarget.style.opacity = isSelected ? '1' : '0.5';
        }}
      >
        {name}
      </button>
    );
  };

  const ModeButton = ({ name, label }: { name: string; label: string }) => {
    const isSelected = mode === name;
    return (
      <button
        type="button"
        onClick={() => setMode(name)}
        style={{
          padding: '6px 12px',
          borderRadius: 4,
          fontSize: 11,
          fontWeight: 600,
          cursor: 'pointer',
          border: `1px solid ${isSelected ? 'var(--accent)' : 'var(--border)'}`,
          background: isSelected ? 'var(--accent-dim)' : 'var(--bg-3)',
          color: isSelected ? 'var(--accent)' : 'var(--fg-3)',
          transition: 'all 0.15s ease',
        }}
      >
        {label}
      </button>
    );
  };

  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.7)',
        zIndex: 1000,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        style={{
          background: 'var(--bg-1)',
          border: '1px solid var(--border)',
          borderRadius: 8,
          padding: 24,
          width: 400,
        }}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 style={{ fontSize: 16, fontWeight: 600, marginBottom: 20, color: 'var(--fg)' }}>
          Launch New Agent
        </h2>

        {/* Provider selector */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block',
            fontSize: 11,
            textTransform: 'uppercase',
            letterSpacing: '0.06em',
            color: 'var(--fg-3)',
            marginBottom: 6,
          }}>
            Provider
          </label>
          <div style={{ display: 'flex', gap: 8 }}>
            <ProviderButton name="claude" />
            <ProviderButton name="codex" />
            <ProviderButton name="gemini" />
          </div>
        </div>

        {/* Directory input */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block',
            fontSize: 11,
            textTransform: 'uppercase',
            letterSpacing: '0.06em',
            color: 'var(--fg-3)',
            marginBottom: 6,
          }}>
            Directory
          </label>
          <input
            type="text"
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            placeholder="Working directory path..."
            style={{
              background: 'var(--bg-2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              color: 'var(--fg)',
              padding: '8px 12px',
              width: '100%',
              fontSize: 13,
              outline: 'none',
            }}
            onFocus={(e) => {
              e.currentTarget.style.borderColor = 'var(--accent)';
            }}
            onBlur={(e) => {
              e.currentTarget.style.borderColor = 'var(--border)';
            }}
          />
        </div>

        {/* Model input */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block',
            fontSize: 11,
            textTransform: 'uppercase',
            letterSpacing: '0.06em',
            color: 'var(--fg-3)',
            marginBottom: 6,
          }}>
            Model
          </label>
          <input
            type="text"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="Model name (e.g., opus, sonnet, haiku)"
            style={{
              background: 'var(--bg-2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              color: 'var(--fg)',
              padding: '8px 12px',
              width: '100%',
              fontSize: 13,
              outline: 'none',
            }}
            onFocus={(e) => {
              e.currentTarget.style.borderColor = 'var(--accent)';
            }}
            onBlur={(e) => {
              e.currentTarget.style.borderColor = 'var(--border)';
            }}
          />
        </div>

        {/* Mode selector */}
        <div style={{ marginBottom: 16 }}>
          <label style={{
            display: 'block',
            fontSize: 11,
            textTransform: 'uppercase',
            letterSpacing: '0.06em',
            color: 'var(--fg-3)',
            marginBottom: 6,
          }}>
            Mode
          </label>
          <div style={{ display: 'flex', gap: 8 }}>
            <ModeButton name="auto" label="Auto" />
            <ModeButton name="plan" label="Plan" />
            <ModeButton name="bypassPermissions" label="Bypass" />
          </div>
        </div>

        {/* Submit button */}
        <button
          onClick={handleSubmit}
          disabled={!dir || submitting}
          style={{
            background: !dir || submitting ? 'var(--bg-3)' : 'var(--accent)',
            color: !dir || submitting ? 'var(--fg-3)' : '#fff',
            border: 'none',
            borderRadius: 4,
            padding: '8px 16px',
            fontWeight: 600,
            cursor: !dir || submitting ? 'not-allowed' : 'pointer',
            width: '100%',
            marginTop: 16,
            fontSize: 13,
          }}
        >
          {submitting ? 'Launching...' : 'Launch Agent'}
        </button>

        {/* Cancel link */}
        <div
          onClick={onClose}
          style={{
            color: 'var(--fg-3)',
            fontSize: 12,
            textAlign: 'center',
            marginTop: 8,
            cursor: 'pointer',
          }}
        >
          Cancel
        </div>
      </div>
    </div>
  );
}
