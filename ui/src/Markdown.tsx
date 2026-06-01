import { Fragment, type ReactNode } from "react";

/**
 * Focused, dependency-free Markdown renderer for agent synthesis output.
 *
 * Agent syntheses are LLM-generated GitHub-Flavored Markdown with a regular
 * shape: headings, **bold**, *italic*, `code`, bullet/numbered lists, GFM
 * tables, and inline `[n]` citation markers. Rather than pull in react-markdown
 * + remark-gfm, we parse exactly those constructs and render them with the
 * Big Michael theme (gold accents, serif prose, mono labels). Anything we don't
 * recognise degrades gracefully to a paragraph.
 */

// ─── Inline: bold / italic / code / citation markers ────────────────────────

const INLINE = /(\*\*[^*]+\*\*|\*[^*]+\*|_[^_]+_|`[^`]+`|\[\d+(?:\s*,\s*\d+)*\])/g;

function renderInline(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  INLINE.lastIndex = 0;
  let i = 0;
  while ((m = INLINE.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const tok = m[0];
    const key = `${keyBase}-${i++}`;
    if (tok.startsWith("**")) out.push(<strong key={key}>{tok.slice(2, -2)}</strong>);
    else if (tok.startsWith("`")) out.push(<code key={key} className="md-code">{tok.slice(1, -1)}</code>);
    else if (tok.startsWith("[")) out.push(<sup key={key} className="md-cite">{tok.slice(1, -1)}</sup>);
    else out.push(<em key={key}>{tok.slice(1, -1)}</em>); // *italic* or _italic_
    last = m.index + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

// ─── Block parsing ──────────────────────────────────────────────────────────

const splitRow = (line: string): string[] =>
  line.replace(/^\s*\|/, "").replace(/\|\s*$/, "").split("|").map((c) => c.trim());

const isTableSep = (line: string): boolean => /^\s*\|?[\s:|-]*-{2,}[\s:|-]*\|?\s*$/.test(line);

export function Markdown({ source }: { source: string }): ReactNode {
  const lines = source.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;
  let key = 0;

  while (i < lines.length) {
    const line = lines[i];

    // blank line → block separator
    if (!line.trim()) { i++; continue; }

    // heading
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      const level = h[1].length;
      const Tag = (`h${Math.min(level + 1, 6)}`) as keyof JSX.IntrinsicElements;
      blocks.push(<Tag key={key++} className={`md-h md-h${level}`}>{renderInline(h[2], `h${key}`)}</Tag>);
      i++;
      continue;
    }

    // table: a header row followed by a separator row
    if (line.includes("|") && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      const header = splitRow(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i].includes("|") && lines[i].trim()) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      blocks.push(
        <div key={key++} className="md-table-wrap">
          <table className="md-table">
            <thead>
              <tr>{header.map((c, j) => <th key={j}>{renderInline(c, `th${key}-${j}`)}</th>)}</tr>
            </thead>
            <tbody>
              {rows.map((r, ri) => (
                <tr key={ri}>{header.map((_, ci) => <td key={ci}>{renderInline(r[ci] ?? "", `td${key}-${ri}-${ci}`)}</td>)}</tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
      continue;
    }

    // list (bullet or numbered) — consume consecutive list lines
    if (/^\s*([*-]|\d+\.)\s+/.test(line)) {
      const ordered = /^\s*\d+\.\s+/.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && /^\s*([*-]|\d+\.)\s+/.test(lines[i])) {
        const text = lines[i].replace(/^\s*([*-]|\d+\.)\s+/, "");
        items.push(<li key={items.length}>{renderInline(text, `li${key}-${items.length}`)}</li>);
        i++;
      }
      blocks.push(ordered
        ? <ol key={key++} className="md-list">{items}</ol>
        : <ul key={key++} className="md-list">{items}</ul>);
      continue;
    }

    // blockquote
    if (/^\s*>\s?/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
        quote.push(lines[i].replace(/^\s*>\s?/, ""));
        i++;
      }
      blocks.push(<blockquote key={key++} className="md-quote">{renderInline(quote.join(" "), `q${key}`)}</blockquote>);
      continue;
    }

    // paragraph — gather until blank line or a block-starting line
    const para: string[] = [];
    while (
      i < lines.length && lines[i].trim() &&
      !/^(#{1,6})\s/.test(lines[i]) &&
      !/^\s*([*-]|\d+\.)\s+/.test(lines[i]) &&
      !/^\s*>\s?/.test(lines[i]) &&
      !(lines[i].includes("|") && i + 1 < lines.length && isTableSep(lines[i + 1]))
    ) {
      para.push(lines[i]);
      i++;
    }
    blocks.push(<p key={key++} className="md-p">{renderInline(para.join(" "), `p${key}`)}</p>);
  }

  return <Fragment>{blocks}</Fragment>;
}
