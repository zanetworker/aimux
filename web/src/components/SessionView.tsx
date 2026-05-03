import { useEffect, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

interface Props {
  tmuxSession: string;
}

export function SessionView({ tmuxSession }: Props) {
  const termRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    if (!termRef.current || !tmuxSession) return;

    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'SF Mono', 'Fira Code', monospace",
      theme: {
        background: '#09090b',
        foreground: '#e0e0e0',
        cursor: '#ef4444',
        selectionBackground: 'rgba(255,255,255,0.12)',
        black: '#09090b',
        red: '#ef4444',
        green: '#4ade80',
        yellow: '#f59e0b',
        blue: '#34d399',
        magenta: '#a78bfa',
        cyan: '#34d399',
        white: '#e0e0e0',
        brightBlack: '#525252',
        brightRed: '#f87171',
        brightGreen: '#4ade80',
        brightYellow: '#fbbf24',
        brightBlue: '#34d399',
        brightMagenta: '#a78bfa',
        brightCyan: '#34d399',
        brightWhite: '#ffffff',
      },
    });

    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(termRef.current);
    terminalRef.current = terminal;

    // Delay fit so the container has settled its layout dimensions
    requestAnimationFrame(() => {
      fitAddon.fit();
      // Fit again after a short delay to catch late layout shifts
      setTimeout(() => fitAddon.fit(), 100);
    });

    // Connect WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${protocol}//${window.location.host}/api/terminal/${tmuxSession}`);
    ws.binaryType = 'arraybuffer';
    wsRef.current = ws;

    ws.onopen = () => {
      terminal.writeln('\x1b[32mConnected to session\x1b[0m');
    };

    ws.onmessage = (e) => {
      const data = typeof e.data === 'string' ? e.data : new Uint8Array(e.data);
      terminal.write(data);
    };

    ws.onclose = () => {
      terminal.writeln('\x1b[31mDisconnected\x1b[0m');
    };

    ws.onerror = () => {
      terminal.writeln('\x1b[31mConnection error\x1b[0m');
    };

    terminal.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    // Handle resize
    const observer = new ResizeObserver(() => fitAddon.fit());
    observer.observe(termRef.current);

    return () => {
      observer.disconnect();
      ws.close();
      terminal.dispose();
    };
  }, [tmuxSession]);

  return (
    <div
      ref={termRef}
      style={{
        position: 'absolute',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        background: '#09090b',
        padding: 4,
      }}
    />
  );
}
