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
        background: '#000000',
        foreground: '#e6e6e6',
        cursor: '#FF3131',
        selectionBackground: '#333333',
        black: '#000000',
        red: '#FF3131',
        green: '#69DF73',
        yellow: '#FFB251',
        blue: '#49D3B4',
        magenta: '#A772EF',
        cyan: '#49D3B4',
        white: '#e6e6e6',
        brightBlack: '#666666',
        brightRed: '#ff5252',
        brightGreen: '#69DF73',
        brightYellow: '#FFB251',
        brightBlue: '#49D3B4',
        brightMagenta: '#A772EF',
        brightCyan: '#49D3B4',
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
        background: '#000000',
        padding: 4,
      }}
    />
  );
}
