import { createContext, useContext, useRef, type MouseEvent, type ReactNode } from 'react';
import { Link } from 'react-router';

import { focusRing } from '../focus';

/**
 * # The tile: one thing, at a glance, in a grid of its kind.
 *
 * Drawn by Settings → Extra, the Argo CD page (one tile per Application, as
 * Argo CD's own UI draws them) and the Redis page (one tile per Redis or Valkey
 * deployment). Three private tiles is how a console ends up with three looks,
 * so it lives here once, beside
 * `Pill`, on the argument `Pill.tsx` makes for a graded reading.
 *
 * ## What a tile holds, and what it refuses
 *
 * - **A title**: the thing's name, one line, truncated, the full name on hover.
 * - **A status zone**: a few chips (`Pill`), never a sentence.
 * - **At most one secondary line**: `project · namespace`, `1 primary · 2
 *   replicas`. Truncated, so a long one cannot grow the tile.
 * - **An action slot**: one control of its own beside the title - a switch, a
 *   button. Drawn outside the open control, never inside it: a button inside a
 *   link is invalid HTML and a keyboard trap in every screen reader.
 * - **A tone**: the status as a 3px bar on the left edge, an accent and not a
 *   paragraph.
 *
 * Anything more - an extension's settings - goes in `children`, under a rule,
 * and is also kept out of the open control.
 *
 * ## Opening it
 *
 * `to` (a route inside the console) makes the title a link, `onOpen` makes it a
 * button. Either way the **whole tile** is the target: the control's `::after`
 * is stretched over the tile (the tile is the positioned ancestor), so a click
 * anywhere opens it, while the accessible name stays the title - or `openLabel`
 * where the title alone is not enough. The focus ring is drawn on that
 * stretched box, so it surrounds the tile rather than the word; it is the
 * console's ring (`focus.ts`: 2px accent, offset 2) on a pseudo-element, which
 * is the one place the shared strings cannot reach.
 *
 * The action slot and `children` sit above the stretched box (`relative z-10`),
 * so a switch is pressed, not the tile. **So does the second line**, because a
 * chip's or the detail's own hover sentence is information (Argo CD's
 * condition message, where a Redis reading came from), and under the stretched
 * box it could not be reached: the pointer would land on the link and show the
 * tile's title instead. A click on that line
 * still opens the tile - it is handed to the open control - unless it lands on
 * a control of its own.
 *
 * ## Utilities only
 *
 * Every class is a theme token or a Tailwind step, written out whole: Tailwind
 * finds utilities by scanning source text, so a class built by interpolation
 * exists in the DOM and in no stylesheet. The console has one palette (dark,
 * `theme.css`); the tile reads it through tokens and draws nothing of its own.
 */

/** The status, as an accent. `info` is the console's accent color (a thing in
 *  progress); `off` is a thing that is deliberately not running, drawn quieter. */
export type TileTone = 'ok' | 'warn' | 'bad' | 'info' | 'off';

const BAR: Readonly<Record<TileTone, string>> = {
  ok: 'border-l-ok',
  warn: 'border-l-warn',
  bad: 'border-l-bad',
  info: 'border-l-accent',
  off: 'border-l-line',
};

/** The count's color on a filter tile, by the same tone. */
const COUNT: Readonly<Record<TileTone, string>> = {
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
  info: 'text-accent',
  off: 'text-muted',
};

/** `data-*` attributes, passed to the tile's root element: what tests and the
 *  browser smoke select a tile by. */
export type TileData = { readonly [key: `data-${string}`]: string | undefined };

/** Inside a `TileGrid` a tile is a list item; anywhere else a block. */
const InGrid = createContext(false);

/** The stretched open control: its `::after` covers the tile, and carries the
 *  ring. `focus-visible:outline-none` on the control itself, because the ring
 *  is the stretched box's. */
const STRETCH =
  "after:absolute after:inset-0 after:rounded-lg after:content-[''] focus-visible:outline-none focus-visible:after:outline-solid focus-visible:after:outline-2 focus-visible:after:outline-offset-2 focus-visible:after:outline-accent";

/** What a click on the second line leaves alone: a control of its own. */
const CONTROLS = 'a, button, input, select, textarea, label, [role="switch"]';

const SHELL = 'relative flex flex-col rounded-lg border border-l-3 border-line bg-panel';

/** A filter tile's width: sized by its content, never under 7rem nor over
 *  12rem, and never grown to fill the row - a lone filter on a wide screen
 *  stays a tile, not a bar. */
const FILTER_WIDTH = 'min-w-28 max-w-48 shrink-0 grow-0';

export interface TileProps extends TileData {
  /** The thing's name. One line; the whole of it on hover. */
  title: string;
  /** The status zone: a few `Pill`s, drawn at the start of the second line. */
  badges?: ReactNode;
  /** At most one short secondary line, after the badges. Truncated. */
  detail?: ReactNode;
  /** The status, as the left bar. None: a plain border. */
  tone?: TileTone | undefined;
  /** Drawn grayed: a tile whose subject is not there, still pressable. */
  dim?: boolean | undefined;
  /** A route inside the console: the tile is a link to it. */
  to?: string | undefined;
  /** Or what pressing the tile does: the tile is a button. Ignored with `to`. */
  onOpen?: (() => void) | undefined;
  /** The open control's accessible name, where the title alone is not enough
   *  ("Open Application guestbook"). Defaults to the title. */
  openLabel?: string | undefined;
  /** Where the tile folds and unfolds its `children`: whether they are shown,
   *  set as `aria-expanded` on the open control. Absent: the tile does not fold. */
  expanded?: boolean | undefined;
  /** One control of its own, beside the title: a switch, a button. */
  action?: ReactNode;
  /** Anything under the two lines - settings, a refusal. Under a rule. */
  children?: ReactNode;
}

/**
 * One thing, at a glance: title and action on the first line, status chips and
 * one short secondary line on the second, the tone as the left bar.
 */
export function Tile({
  title,
  badges,
  detail,
  tone,
  to,
  onOpen,
  openLabel,
  expanded,
  dim,
  action,
  children,
  ...data
}: TileProps): ReactNode {
  const Root = useContext(InGrid) ? 'li' : 'div';
  const opens = to !== undefined || onOpen !== undefined;
  const quiet = tone === 'off';
  const name = `min-w-0 truncate text-sm font-medium ${quiet ? 'text-muted' : 'text-fg'}`;
  const label = openLabel === undefined || openLabel === title ? {} : { 'aria-label': openLabel };
  const link = useRef<HTMLAnchorElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  // The second line is above the stretched box, so a click on it is handed on
  // to the open control: a mouse user still opens the tile from anywhere on it.
  // Keyboard users reach the open control itself, which is the same action.
  const handOn = (event: MouseEvent<HTMLDivElement>): void => {
    if (event.target instanceof Element && event.target.closest(CONTROLS) !== null) return;
    (link.current ?? button.current)?.click();
  };

  let head: ReactNode;
  if (to !== undefined) {
    head = (
      <Link ref={link} to={to} title={title} className={`${name} ${STRETCH}`} data-tile-open="" {...label}>
        {title}
      </Link>
    );
  } else if (onOpen !== undefined) {
    head = (
      <button
        ref={button}
        type="button"
        title={title}
        onClick={onOpen}
        className={`${name} cursor-pointer text-left ${STRETCH}`}
        data-tile-open=""
        {...(expanded === undefined ? {} : { 'aria-expanded': expanded })}
        {...label}
      >
        {title}
      </button>
    );
  } else {
    head = (
      <span title={title} className={name}>
        {title}
      </span>
    );
  }

  const second = badges !== undefined || detail !== undefined;

  return (
    <Root
      {...data}
      data-tile=""
      data-tone={tone ?? 'none'}
      className={`${SHELL} min-w-0 gap-1.5 px-3 py-2.5 ${tone === undefined ? '' : BAR[tone]} ${
        opens ? 'transition-colors hover:bg-panel-2' : ''
      } ${dim === true ? 'opacity-70' : ''}`}
    >
      <div className="flex min-h-6 items-center gap-2">
        <div className="flex min-w-0 flex-1">{head}</div>
        {action === undefined ? null : <div className="relative z-10 flex shrink-0 items-center">{action}</div>}
      </div>
      {second ? (
        // Wraps rather than overflows: on a narrow tile the chips take as many
        // lines as they need and the detail goes to a line of its own, where
        // it truncates (on a 390px screen the third chip would be clipped).
        // Above the stretched box where the tile opens, so hover sentences
        // in it are reachable; a click on it is handed on (`handOn`).
        <div
          className={`flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted ${
            opens ? 'relative z-10 cursor-pointer' : ''
          }`}
          data-tile-second=""
          {...(opens ? { onClick: handOn } : {})}
        >
          {badges === undefined ? null : (
            <div className="flex max-w-full min-w-0 flex-wrap items-center gap-1">{badges}</div>
          )}
          {detail === undefined ? null : (
            <span className="max-w-full min-w-0 truncate" data-tile-detail="">
              {detail}
            </span>
          )}
        </div>
      ) : null}
      {children === undefined ? null : (
        <div className="relative z-10 mt-1 flex flex-col gap-1.5 border-t border-line pt-2">{children}</div>
      )}
    </Root>
  );
}

export interface FilterTileProps extends TileData {
  /** How many, drawn large. A string where the count is not a number yet. */
  count: number | string;
  /** What is counted: one or two words, drawn small and upper-case. */
  label: string;
  /** The status, as the left bar and the count's color. */
  tone?: TileTone | undefined;
  /** Whether this filter is the one applied (`aria-pressed`). */
  pressed?: boolean | undefined;
  /** What pressing it does. None: the tile is a reading, not a control. */
  onClick?: (() => void) | undefined;
  /** The hover sentence: what pressing it shows. */
  hint?: string | undefined;
}

/**
 * The compact variant: a count and a word, for a row of filters over a list
 * ("3 Degraded"). Same shell and tone as `Tile`; a toggle button with
 * `aria-pressed` when it filters, a plain reading when it does not.
 */
export function FilterTile({ count, label, tone, pressed, onClick, hint, ...data }: FilterTileProps): ReactNode {
  const Root = useContext(InGrid) ? 'li' : 'div';
  const body = (
    <>
      <span
        className={`text-lg leading-tight font-semibold tabular-nums ${tone === undefined ? 'text-fg' : COUNT[tone]}`}
      >
        {count}
      </span>
      {/* A space for the accessible name ("3 Degraded"); a flex box draws none. */}{' '}
      <span className="truncate text-2xs tracking-wide text-muted uppercase">{label}</span>
    </>
  );
  const bar = tone === undefined ? '' : BAR[tone];
  const hover = hint === undefined ? {} : { title: hint };

  if (onClick === undefined) {
    return (
      <Root {...data} data-tile="" data-tone={tone ?? 'none'} className={`${SHELL} ${FILTER_WIDTH} px-2.5 py-1.5 ${bar}`}
        {...hover}
      >
        {body}
      </Root>
    );
  }
  return (
    <Root {...data} data-tile="" data-tone={tone ?? 'none'} className={`flex ${FILTER_WIDTH}`}>
      <button
        type="button"
        aria-pressed={pressed === true}
        onClick={onClick}
        className={`${SHELL} w-full min-w-0 cursor-pointer px-2.5 py-1.5 text-left transition-colors hover:bg-panel-2 ${focusRing} ${bar} ${
          pressed === true ? 'bg-panel-2 ring-1 ring-accent' : ''
        }`}
        {...hover}
      >
        {body}
      </button>
    </Root>
  );
}

export interface TileGridProps {
  children: ReactNode;
  /** The list's accessible name, where the heading above it does not give one. */
  label?: string | undefined;
  /** `start` keeps each tile at its own height, so one that grows (a folded tile
   *  opened) does not stretch its neighbors in the row; `stretch` is the default. */
  align?: 'stretch' | 'start';
  /** `regular` for `Tile`s (1 to 4 columns with the width); `compact` for a row
   *  of `FilterTile`s (a wrapping row of content-sized tiles, 7 to 12rem,
   *  that never stretch to fill it). */
  density?: 'regular' | 'compact';
}

/** A grid of tiles, as a list: each `Tile` or `FilterTile` in it is an item. */
export function TileGrid({ children, label, density = 'regular', align = 'stretch' }: TileGridProps): ReactNode {
  const columns =
    density === 'compact'
      ? 'flex flex-wrap gap-2'
      : `grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4${align === 'start' ? ' items-start' : ''}`;
  return (
    <InGrid.Provider value={true}>
      <ul className={columns} data-tile-grid={density} {...(label === undefined ? {} : { 'aria-label': label })}>
        {children}
      </ul>
    </InGrid.Provider>
  );
}
