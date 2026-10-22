// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
//
// Inline SVG icons (24-unit grid, stroked with currentColor): the run log's tool marks and a few
// interface glyphs. No icon font, no network.
const PATHS: Record<string, string> = {
  ledger: 'M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01',
  outline: 'M6 3h9l4 4v14H6zM14 3v5h5M9 12h7M9 16h5',
  search: 'M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM20 20l-4-4',
  extract_facts: 'M7 7h4v4c0 2-1 4-3 5M14 7h4v4c0 2-1 4-3 5',
  draft_section: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
  finish: 'M5 21V4M5 4h11l-2 4 2 4H5',
  lead: 'M12 3v3M12 18v3M3 12h3M18 12h3M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z',
  tool: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z',
  check: 'M5 12.5l4.5 4.5L19 7.5',
  x: 'M6 6l12 12M18 6L6 18',
  doc: 'M6 3h9l4 4v14H6zM14 3v5h5',
  scan: 'M4 8V4h4M16 4h4v4M20 16v4h-4M8 20H4v-4M8 12h8',
  arrow: 'M5 12h14M13 6l6 6-6 6',
  back: 'M19 12H5M11 6l-6 6 6 6',
  gate: 'M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z',
  external: 'M14 4h6v6M20 4l-9 9M18 14v6H4V6h6',
};

export function Icon(props: { name: string; size?: number; label?: string }) {
  const s = props.size ?? 16;
  return (
    <svg className="icon" width={s} height={s} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" role={props.label ? 'img' : undefined} aria-label={props.label} aria-hidden={props.label ? undefined : true}>
      <path d={PATHS[props.name] ?? PATHS.tool} />
    </svg>
  );
}

/** stepIcon picks the run-log mark: retrieve is an outline read or a search, by what it did. */
export function stepIcon(tool: string, summary: string): string {
  if (tool === 'retrieve') return summary.startsWith('search') ? 'search' : 'outline';
  return tool in PATHS ? tool : 'tool';
}
