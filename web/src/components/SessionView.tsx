import { useEffect, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

interface Props {
  tmuxSession?: string;
  sessionId?: string;
}

export function SessionView({ tmuxSession, sessionId }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);

  useEffect(() => {
    if (!containerRef.current || (!tmuxSession && !sessionId)) return;

    const container = containerRef.current;

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
    fitAddonRef.current = fitAddon;
    terminal.loadAddon(fitAddon);
    terminal.open(container);
    terminalRef.current = terminal;

    const doFit = () => {
      try {
        fitAddon.fit();
      } catch {
        // container may not be visible yet
      }
    };

    // Fit multiple times to catch layout settling
    requestAnimationFrame(doFit);
    setTimeout(doFit, 50);
    setTimeout(doFit, 200);
    setTimeout(doFit, 500);

    // WebSocket: tmux attach or direct resume
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsPath = tmuxSession
      ? `/api/terminal/${tmuxSession}`
      : `/api/terminal-resume/${sessionId}`;
    const ws = new WebSocket(`${protocol}//${window.location.host}${wsPath}`);
    ws.binaryType = 'arraybuffer';
    wsRef.current = ws;

    ws.onopen = () => {
      terminal.writeln('\x1b[32mConnected to session\x1b[0m');
      // Send initial resize
      doFit();
      sendResize(ws, terminal);
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

    // Send resize when terminal dimensions change
    terminal.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
      }
    });

    // Observe the PARENT container for resize (catches fullscreen toggle, panel resize)
    const parentEl = container.parentElement;
    const observer = new ResizeObserver(() => {
      doFit();
    });
    if (parentEl) observer.observe(parentEl);
    observer.observe(container);

    return () => {
      observer.disconnect();
      ws.close();
      terminal.dispose();
      terminalRef.current = null;
      fitAddonRef.current = null;
      wsRef.current = null;
    };
  }, [tmuxSession, sessionId]);

  return (
    <div
      ref={containerRef}
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

function sendResize(ws: WebSocket, terminal: Terminal) {
  if (ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
  }
}
