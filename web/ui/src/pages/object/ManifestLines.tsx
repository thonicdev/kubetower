import type { ReactNode } from 'react';

import type { ManifestLine, ManifestToken } from '../../api';
import { cut, Mark, type Hit } from '../../components/FindBar';
import { findRanges, type Range } from '../../components/useFind';

/**
 * A manifest's lines as the server colored them: the gutter, the tokens in
 * their classes, and the find bar's matches marked in place.
 *
 * **One renderer for the tab and for the enlarged view.** A read-only text
 * area cannot color a token, so the enlarged view would lose the YAML
 * colors. Both draw these lines, so the same manifest shows the same colors in both, and a
 * change to the colors reaches both at once.
 *
 * Reading only: nothing here is typed into. The editor is CodeMirror
 * (`ManifestCode.tsx`), colored with the same tokens.
 */
export function ManifestLines({
  lines,
  byLine,
  className,
}: {
  lines: readonly ManifestLine[];
  byLine: ReadonlyMap<number, readonly Hit[]>;
  className: string;
}): ReactNode {
  const width = String(lines.length).length;
  return (
    <pre className={`font-mono text-xs leading-5 ${className}`} data-manifest-lines="">
      <code>
        {lines.map((line, index) => (
          <Line key={line.number} line={line} width={width} hits={byLine.get(index) ?? NO_HITS} />
        ))}
      </code>
    </pre>
  );
}

/** The matches of `matchLines`, grouped by line, the current one marked. */
export function hitsByLine(found: readonly (Range & { line: number })[], at: number): ReadonlyMap<number, readonly Hit[]> {
  const byLine = new Map<number, Hit[]>();
  found.forEach((one, index) => {
    const list = byLine.get(one.line) ?? [];
    list.push({ from: one.from, to: one.to, current: index === at });
    byLine.set(one.line, list);
  });
  return byLine;
}

/**
 * One line, with its number in a gutter.
 *
 * The gutter is **server-numbered** — `ManifestLine.number` — rather than the
 * index of a `map`. The two agree today and would stop agreeing the first time
 * the server sends a window over a long document, and a gutter that disagrees
 * with the document is worse than no gutter: it is what somebody quotes in a
 * ticket.
 *
 * It is `aria-hidden` and unselectable. A screen reader reading "one apiVersion
 * two kind three metadata" is reading a different document, and a copy of the
 * manifest with the line numbers in it does not paste back into anything.
 */
function Line({ line, width, hits }: { line: ManifestLine; width: number; hits: readonly Hit[] }): ReactNode {
  // Where each token starts in the line, so the hits - found over the line's
  // whole text - can be cut at the token they fall in.
  const starts: number[] = [];
  let length = 0;
  for (const token of line.tokens) {
    starts.push(length);
    length += token.text.length;
  }
  return (
    <div className="flex">
      <span
        aria-hidden
        className="mr-3 shrink-0 select-none text-right text-muted/60 tabular-nums"
        style={{ width: `${width}ch` }}
      >
        {line.number}
      </span>
      <span className="min-w-0 whitespace-pre-wrap wrap-anywhere">
        {line.tokens.map((token, at) => {
          const from = starts[at] ?? 0;
          const to = from + token.text.length;
          // The index is the key, and it is the right one here: tokens have no
          // identity of their own, the array is replaced whole on every read,
          // and nothing in a line is reordered or removed in place.
          //
          // A token a match reaches into keeps its own span, so the colors of
          // what is not matched are unchanged, with the matched stretch marked
          // inside it.
          return (
            <span key={at} className={`${toneOf(token)}${UNBROKEN.test(token.text) ? ' inline-block max-w-full' : ''}`}>
              {hits.some((hit) => hit.from < to && hit.to > from)
                ? cut(token.text, from, hits).map((piece, i) =>
                    piece.hit === null ? (
                      piece.text
                    ) : (
                      <Mark key={i} hit={piece.hit}>
                        {piece.text}
                      </Mark>
                    ),
                  )
                : token.text}
            </span>
          );
        })}
      </span>
    </div>
  );
}

/** The contract's eight classes, and `plain` for anything a newer server sends
 *  that this build has never heard of. */
const tones: Readonly<Record<ManifestToken['class'], string>> = {
  plain: 'text-fg',
  key: 'text-accent',
  string: 'text-ok',
  number: 'text-warn',
  boolean: 'text-warn',
  comment: 'text-muted italic',
  anchor: 'text-bad',
  punctuation: 'text-muted',
};

/**
 * A token with no space in it - a timestamp, a name, a masked value - is one
 * word, and a narrow screen wraps a line between words: `wrap-anywhere`
 * breaks inside a word only where it is wider than the line. But a browser
 * also breaks after a hyphen, so `"2026-11-05T23:23:47Z"` read `"2026-11-` and
 * `05T23:23:47Z"` at 390px. As its own box (`inline-block`) such a token goes
 * to the next line whole, and wraps inside only past the line's width
 * (`max-w-full`). A token of several words stays inline and wraps between them.
 */
const UNBROKEN = /^\S+$/;

function toneOf(token: ManifestToken): string {
  return tones[token.class] ?? tones.plain;
}

/** The hits on a line with none, shared, so an unmatched line is handed the
 *  same empty list on every render. */
const NO_HITS: readonly Hit[] = [];

/** Every match in the document, line by line and in reading order. The line is
 *  the index in `lines`, which is also the order the lines are drawn in. */
export function matchLines(
  lines: readonly ManifestLine[],
  pattern: RegExp | null,
): readonly (Range & { line: number })[] {
  if (pattern === null) return [];
  return lines.flatMap((line, index) =>
    findRanges(line.tokens.map((token) => token.text).join(''), pattern).map((range) => ({
      line: index,
      ...range,
    })),
  );
}
