import type { ReactNode } from 'react';

import { applePlatform } from '../dock/terminalClipboard';
import { focusRing } from '../focus';
import { CloseButton } from './CloseButton';
import { Icon } from './Icon';
import { SearchField } from './SearchField';
import { toolbarSquare } from './toolbar';
import type { Find, Range } from './useFind';

/**
 * The find bar: the log pane's search box (`SearchField`), with the case toggle
 * and the `‹ n/m ›` stepper, and a way to close it. Drawn only while the search
 * is open; `useFind` says what opens it and what closes it.
 *
 * `data-find-bar` is how `useFind` knows the keyboard is in the bar when it
 * closes, so that it gives the focus back only then.
 */
export function FindBar({
  find,
  count,
  label,
  more,
}: {
  find: Find;
  /** How many matches the view has for the settled text. */
  count: number;
  /** Set when the view marked fewer hits than its text holds: why, for the
   *  counter's hover. The counter reads `count+`. */
  more?: string | undefined;
  /** The box's accessible name: what it searches. */
  label: string;
}): ReactNode {
  if (!find.open) return null;
  return (
    <div data-find-bar="" role="search" className="flex min-w-0 flex-1 flex-wrap items-center gap-1 text-xs">
      <SearchField
        label={label}
        placeholder="Find"
        query={find.typed}
        onQuery={find.type}
        onEnter={(backwards) => find.enter(backwards, count)}
        matchCase={find.matchCase}
        onMatchCase={find.setMatchCase}
        error={null}
        matches={count}
        more={more}
        at={find.current(count)}
        onStep={(by) => find.step(by, count)}
        inputRef={find.input}
      />
      <CloseButton onClick={find.close} label="Close the search" size="inline" escape />
    </div>
  );
}

/**
 * The control that opens the bar, for whoever does not know the key - and the
 * key is on its hover, in the platform's own spelling, so that it is learnt
 * from the control rather than from a manual.
 */
export function FindButton({ find, what }: { find: Find; what: string }): ReactNode {
  const key = applePlatform() ? '⌘F' : 'Ctrl+F';
  return (
    <button
      type="button"
      aria-label={`Search ${what}`}
      aria-expanded={find.open}
      title={`Search ${what} (${key})`}
      onClick={find.show}
      className={`${toolbarSquare} text-muted hover:text-fg ${focusRing}`}
    >
      <Icon name="search" />
    </button>
  );
}

/** A match in a text, and whether it is the one the bar is on. */
export interface Hit extends Range {
  readonly current: boolean;
}

/** One stretch of a text, cut at the edges of the hits over it. */
export interface Piece {
  readonly text: string;
  readonly hit: Hit | null;
}

/**
 * Cut `text` - which starts at `offset` in the string the hits were found in -
 * at every edge of a hit that falls inside it.
 *
 * The offset is what lets a manifest line be searched whole and drawn token by
 * token: a key and its value are separate spans, and a match that runs across
 * the `: ` between them is cut into a piece per token and is still one match.
 */
export function cut(text: string, offset: number, hits: readonly Hit[]): Piece[] {
  const end = offset + text.length;
  const out: Piece[] = [];
  let at = offset;
  for (const hit of hits) {
    if (hit.to <= at || hit.from >= end) continue;
    const from = Math.max(hit.from, at);
    const to = Math.min(hit.to, end);
    if (from > at) out.push({ text: text.slice(at - offset, from - offset), hit: null });
    out.push({ text: text.slice(from - offset, to - offset), hit });
    at = to;
  }
  if (at < end || out.length === 0) out.push({ text: text.slice(at - offset), hit: null });
  return out;
}

/**
 * A matched stretch, drawn.
 *
 * Every match the view hands it is marked (`Data.tsx` hands at most a cap of
 * them for the whole data block), and the current one is marked differently enough to
 * be found at a glance: the full warning color under the background's ink, as
 * the log pane marks a match, where the others are a tint of it under the
 * ordinary ink. The token's own color is not kept inside the mark - a key's
 * accent on amber is the illegible case - and the stretch is still the token's
 * text, character for character, so a selection copies what the document says.
 *
 * `data-find-hit` and `data-find-current` are what `useFind` scrolls to and
 * what a test counts; a match cut across two tokens carries them on both
 * pieces, and the first is the one scrolled to.
 */
export function Mark({ hit, children }: { hit: Hit; children: ReactNode }): ReactNode {
  return (
    <mark
      data-find-hit=""
      data-find-current={hit.current ? '' : undefined}
      className={`rounded-sm ${hit.current ? 'bg-warn text-bg' : 'bg-warn/30 text-fg'}`}
    >
      {children}
    </mark>
  );
}

/** A plain text with its hits marked, for a view that has no tokens of its own. */
export function Marked({ text, hits }: { text: string; hits: readonly Hit[] }): ReactNode {
  if (hits.length === 0) return text;
  return cut(text, 0, hits).map((piece, at) =>
    // The index is the key: the pieces are rebuilt whole on every render and
    // nothing in them moves in place.
    piece.hit === null ? <span key={at}>{piece.text}</span> : <Mark key={at} hit={piece.hit}>{piece.text}</Mark>,
  );
}
