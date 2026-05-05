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
  | { type: 'table'; headers: string[]; rows: string[][] }
  | { type: 'paragraph'; text: string };

function parseBlocks(text: string): Block[] {
  const lines = text.split('\n');
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Fenced code block
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

    // Heading
    const headingMatch = line.match(/^(#{1,6})\s+(.+)/);
    if (headingMatch) {
      blocks.push({ type: 'heading', level: headingMatch[1].length, text: headingMatch[2] });
      i++;
      continue;
    }

    // Table: line with pipes, followed by separator row, followed by data rows
    if (line.includes('|') && i + 1 < lines.length && /^\s*\|?[\s:]*-+[\s:]*\|/.test(lines[i + 1])) {
      const headers = parsePipeRow(line);
      i += 2; // skip header + separator
      const rows: string[][] = [];
      while (i < lines.length && lines[i].includes('|') && lines[i].trim() !== '') {
        rows.push(parsePipeRow(lines[i]));
        i++;
      }
      if (headers.length > 0) {
        blocks.push({ type: 'table', headers, rows });
        continue;
      }
    }

    // List item
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

    // Empty line
    if (line.trim() === '') {
      i++;
      continue;
    }

    // Paragraph
    const paraLines: string[] = [line];
    i++;
    while (i < lines.length && lines[i].trim() !== '' && !lines[i].startsWith('```') &&
           !lines[i].match(/^#{1,6}\s/) && !lines[i].match(/^\s*[-*+]\s/) &&
           !lines[i].match(/^\s*\d+\.\s/) &&
           !(lines[i].includes('|') && i + 1 < lines.length && /^\s*\|?[\s:]*-+/.test(lines[i + 1] || ''))) {
      paraLines.push(lines[i]);
      i++;
    }
    blocks.push({ type: 'paragraph', text: paraLines.join('\n') });
  }

  return blocks;
}

function parsePipeRow(line: string): string[] {
  return line.split('|').map(s => s.trim()).filter((s, i, arr) => {
    if (i === 0 && s === '') return false;
    if (i === arr.length - 1 && s === '') return false;
    return true;
  });
}

function renderBlock(block: Block, key: number): React.ReactNode {
  switch (block.type) {
    case 'code':
      return (
        <pre key={key} style={{
          fontFamily: 'var(--mono)', fontSize: 11, lineHeight: '1.5',
          padding: '8px 10px', borderRadius: 4, background: 'var(--bg-0)',
          border: '1px solid var(--border)', color: 'var(--fg)',
          overflowX: 'auto', margin: '6px 0', whiteSpace: 'pre',
        }}>
          {block.lang && (
            <span style={{
              fontSize: 9, color: 'var(--fg-4)', fontWeight: 600,
              textTransform: 'uppercase', letterSpacing: '0.04em',
              display: 'block', marginBottom: 4,
            }}>{block.lang}</span>
          )}
          {block.content}
        </pre>
      );

    case 'heading': {
      const sizes: Record<number, number> = { 1: 16, 2: 14, 3: 13, 4: 12, 5: 12, 6: 11 };
      return (
        <div key={key} style={{
          fontSize: sizes[block.level] || 12, fontWeight: 700,
          color: 'var(--fg)', margin: '10px 0 4px',
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
              <li key={j} style={{ marginBottom: 3 }}>{renderInline(item)}</li>
            ))}
          </ol>
        );
      }
      return (
        <ul key={key} style={{ paddingLeft: 16, margin: '4px 0', listStyleType: 'disc' }}>
          {block.items.map((item, j) => (
            <li key={j} style={{ marginBottom: 3 }}>{renderInline(item)}</li>
          ))}
        </ul>
      );

    case 'table':
      return (
        <div key={key} style={{ overflowX: 'auto', margin: '6px 0' }}>
          <table style={{
            borderCollapse: 'collapse', fontSize: 11, fontFamily: 'var(--mono)',
            width: '100%',
          }}>
            <thead>
              <tr>
                {block.headers.map((h, j) => (
                  <th key={j} style={{
                    padding: '4px 8px', borderBottom: '2px solid var(--border)',
                    textAlign: 'left', color: 'var(--fg)', fontWeight: 600,
                    whiteSpace: 'nowrap', fontSize: 10,
                  }}>
                    {renderInline(h)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.map((row, j) => (
                <tr key={j}>
                  {row.map((cell, k) => (
                    <td key={k} style={{
                      padding: '3px 8px', borderBottom: '1px solid var(--border)',
                      color: 'var(--fg-2)', fontSize: 10, verticalAlign: 'top',
                    }}>
                      {renderInline(cell)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
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
  const segments: React.ReactNode[] = [];
  let remaining = text;
  let key = 0;

  while (remaining.length > 0) {
    // Find the earliest match among all inline patterns
    let earliest: { type: string; index: number; match: RegExpMatchArray } | null = null;

    const patterns: [string, RegExp][] = [
      ['code', /`([^`]+)`/],
      ['bold', /\*\*(.+?)\*\*/],
      ['italic', /(?<!\*)\*([^*]+)\*(?!\*)/],
    ];

    for (const [type, re] of patterns) {
      const m = remaining.match(re);
      if (m && m.index !== undefined) {
        if (!earliest || m.index < earliest.index) {
          earliest = { type, index: m.index, match: m };
        }
      }
    }

    if (!earliest) {
      segments.push(<React.Fragment key={key++}>{remaining}</React.Fragment>);
      break;
    }

    // Text before the match
    if (earliest.index > 0) {
      segments.push(<React.Fragment key={key++}>{remaining.substring(0, earliest.index)}</React.Fragment>);
    }

    const inner = earliest.match[1];
    switch (earliest.type) {
      case 'code':
        segments.push(
          <code key={key++} style={{
            fontFamily: 'var(--mono)', fontSize: '0.9em',
            padding: '1px 4px', borderRadius: 3,
            background: 'var(--bg-2)', color: 'var(--teal)',
          }}>{inner}</code>
        );
        break;
      case 'bold':
        segments.push(<strong key={key++} style={{ color: 'var(--fg)', fontWeight: 600 }}>{renderInline(inner)}</strong>);
        break;
      case 'italic':
        segments.push(<em key={key++}>{inner}</em>);
        break;
    }

    remaining = remaining.substring(earliest.index + earliest.match[0].length);
  }

  return segments.length === 1 ? segments[0] : <>{segments}</>;
}
