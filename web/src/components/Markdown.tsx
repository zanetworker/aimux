import React from 'react';

interface MarkdownProps {
  text: string;
  color?: string;
}

export function Markdown({ text, color = 'var(--fg-2)' }: MarkdownProps) {
  const blocks = parseBlocks(text);
  return (
    <div style={{ fontSize: 12, lineHeight: '1.6', color }}>
      {blocks.map((block, i) => renderBlock(block, i))}
    </div>
  );
}

type Block =
  | { type: 'code'; lang: string; content: string }
  | { type: 'heading'; level: number; text: string }
  | { type: 'list'; ordered: boolean; items: string[] }
  | { type: 'paragraph'; text: string };

function parseBlocks(text: string): Block[] {
  const lines = text.split('\n');
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (line.startsWith('```')) {
      const lang = line.slice(3).trim();
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith('```')) {
        codeLines.push(lines[i]);
        i++;
      }
      i++;
      blocks.push({ type: 'code', lang, content: codeLines.join('\n') });
      continue;
    }

    const headingMatch = line.match(/^(#{1,6})\s+(.+)/);
    if (headingMatch) {
      blocks.push({ type: 'heading', level: headingMatch[1].length, text: headingMatch[2] });
      i++;
      continue;
    }

    const listMatch = line.match(/^(\s*)([-*+]|\d+\.)\s+(.+)/);
    if (listMatch) {
      const ordered = /^\d+\./.test(listMatch[2]);
      const items: string[] = [listMatch[3]];
      i++;
      while (i < lines.length) {
        const nextMatch = lines[i].match(/^(\s*)([-*+]|\d+\.)\s+(.+)/);
        if (!nextMatch) break;
        items.push(nextMatch[3]);
        i++;
      }
      blocks.push({ type: 'list', ordered, items });
      continue;
    }

    if (line.trim() === '') {
      i++;
      continue;
    }

    const paraLines: string[] = [line];
    i++;
    while (i < lines.length && lines[i].trim() !== '' && !lines[i].startsWith('```') && !lines[i].match(/^#{1,6}\s/) && !lines[i].match(/^\s*[-*+]\s/) && !lines[i].match(/^\s*\d+\.\s/)) {
      paraLines.push(lines[i]);
      i++;
    }
    blocks.push({ type: 'paragraph', text: paraLines.join('\n') });
  }

  return blocks;
}

function renderBlock(block: Block, key: number): React.ReactNode {
  switch (block.type) {
    case 'code':
      return (
        <pre key={key} style={{
          fontFamily: 'var(--mono)',
          fontSize: 11,
          lineHeight: '1.5',
          padding: '8px 10px',
          borderRadius: 4,
          background: 'var(--bg-0)',
          border: '1px solid var(--border)',
          color: 'var(--fg)',
          overflowX: 'auto',
          margin: '6px 0',
          whiteSpace: 'pre',
          wordBreak: 'break-all',
        }}>
          {block.lang && (
            <span style={{
              fontSize: 9, color: 'var(--fg-4)', fontWeight: 600,
              textTransform: 'uppercase', letterSpacing: '0.04em',
              display: 'block', marginBottom: 4,
            }}>
              {block.lang}
            </span>
          )}
          {block.content}
        </pre>
      );

    case 'heading': {
      const sizes: Record<number, number> = { 1: 16, 2: 14, 3: 13, 4: 12, 5: 12, 6: 11 };
      return (
        <div key={key} style={{
          fontSize: sizes[block.level] || 12,
          fontWeight: 700,
          color: 'var(--fg)',
          margin: '8px 0 4px',
        }}>
          {renderInline(block.text)}
        </div>
      );
    }

    case 'list':
      if (block.ordered) {
        return (
          <ol key={key} style={{ paddingLeft: 20, margin: '4px 0' }}>
            {block.items.map((item, j) => (
              <li key={j} style={{ marginBottom: 2 }}>{renderInline(item)}</li>
            ))}
          </ol>
        );
      }
      return (
        <ul key={key} style={{ paddingLeft: 16, margin: '4px 0', listStyleType: 'disc' }}>
          {block.items.map((item, j) => (
            <li key={j} style={{ marginBottom: 2 }}>{renderInline(item)}</li>
          ))}
        </ul>
      );

    case 'paragraph':
      return (
        <p key={key} style={{ margin: '4px 0', wordBreak: 'break-word' }}>
          {renderInline(block.text)}
        </p>
      );
  }
}

function renderInline(text: string): React.ReactNode {
  const parts: React.ReactNode[] = [];
  let remaining = text;
  let key = 0;

  while (remaining.length > 0) {
    // Inline code
    const codeMatch = remaining.match(/^(.*?)`([^`]+)`/s);
    if (codeMatch) {
      if (codeMatch[1]) parts.push(renderPlainInline(codeMatch[1], key++));
      parts.push(
        <code key={key++} style={{
          fontFamily: 'var(--mono)', fontSize: '0.9em',
          padding: '1px 4px', borderRadius: 3,
          background: 'var(--bg-2)', color: 'var(--teal)',
        }}>
          {codeMatch[2]}
        </code>
      );
      remaining = remaining.slice(codeMatch[0].length);
      continue;
    }

    // Bold
    const boldMatch = remaining.match(/^(.*?)\*\*(.+?)\*\*/s);
    if (boldMatch) {
      if (boldMatch[1]) parts.push(renderPlainInline(boldMatch[1], key++));
      parts.push(<strong key={key++} style={{ color: 'var(--fg)', fontWeight: 600 }}>{boldMatch[2]}</strong>);
      remaining = remaining.slice(boldMatch[0].length);
      continue;
    }

    // Italic
    const italicMatch = remaining.match(/^(.*?)(?<!\*)\*([^*]+)\*(?!\*)/s);
    if (italicMatch) {
      if (italicMatch[1]) parts.push(renderPlainInline(italicMatch[1], key++));
      parts.push(<em key={key++}>{italicMatch[2]}</em>);
      remaining = remaining.slice(italicMatch[0].length);
      continue;
    }

    parts.push(<React.Fragment key={key++}>{remaining}</React.Fragment>);
    break;
  }

  return parts.length === 1 ? parts[0] : <>{parts}</>;
}

function renderPlainInline(text: string, key: number): React.ReactNode {
  return <React.Fragment key={key}>{text}</React.Fragment>;
}
