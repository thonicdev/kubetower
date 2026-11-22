import type { ReactNode } from 'react';

import { runClass, runStyle } from './ansi';
import { pieces, type LogBuffer, type LogLine } from './logBuffer';

/**
 * A key per line, for the line's whole life in the buffer.
 *
 * Not its index: a page backwards or a trim moves every index, and React then
 * hands each kept row another line's text instead of moving it, which under a
 * selection is a selection over text nobody chose. A
 * `LogLine` is one object from arrival to eviction - the decoded caches are
 * keyed by it for the same reason - so a weak map numbers it once and forgets
 * it with the line.
 */
const rowKeys = new WeakMap<LogLine, number>();
let nextRowKey = 0;
export function rowKey(line: LogLine | undefined, index: number): string {
  if (line === undefined) return `at-${String(index)}`;
  let key = rowKeys.get(line);
  if (key === undefined) {
    key = nextRowKey++;
    rowKeys.set(line, key);
  }
  return String(key);
}

/** One line of the buffer, its colors and its find matches. */
export function LogRow({
  buffer, index, matcher, stamped, current,
}: {
  buffer: LogBuffer; index: number; matcher: RegExp | null; stamped: boolean; current: boolean;
}): ReactNode {
  const text = buffer.shownText(index);
  const decoded = buffer.decoded(index);
  const line = buffer.lines[index];

  return (
    <div data-line={index} className={current ? 'bg-panel-2' : undefined}>
      {stamped && line?.at !== undefined && (
        // The instant is its own element and is **not** part of the text, so
        // nothing has to shift a color offset past it; a shift forgotten would
        // paint a line's colors one stamp's width early - only with this
        // toggle on.
        <span className="mr-2 text-muted">{line.at}</span>
      )}
      {pieces(text, buffer.jsonOn ? [] : decoded.runs, matcher).map((piece, i) => {
        const body = piece.style === null
          ? piece.text
          : (
            <span style={runStyle(piece.style)} className={runClass(piece.style)}>
              {piece.text}
            </span>
          );
        // **The mark goes outside the colored span.** Color is inherited, so a
        // span carrying `color` inside a mark would override the mark's own -
        // the current match on a red ERR line would come out red on amber.
        return piece.match ? <mark key={i} className="bg-warn text-bg">{body}</mark> : <span key={i}>{body}</span>;
      })}
    </div>
  );
}
