import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from 'react';
import { Link } from 'react-router';
import { focusRing, objectPath } from '@kubetower/ui';

import { WINDOWS, ago, type Answer, type Cause, type Change, type Section, type Tiles as TilesData, type Window } from './data';
import {
  CHANGE_TONE,
  TILE_HINT,
  TILE_KEYS,
  TILE_LABEL,
  axisOf,
  bars,
  causeKey,
  changeLabel,
  changeShort,
  changeOf,
  changeTitle,
  objectsWord,
  options,
  pluralOf,
  spot,
  ticks,
  tileWord,
  type Axis,
  type Bar,
  type Filters,
  type TileKey,
} from './model';

/** How many causes the list draws before "more", and how many objects one cause unfolds to. */
export const PAGE_CAUSES = 100;
export const PAGE_OBJECTS = 50;
const LANE_PX = 18;
const LABEL_ROW_PX = 16;
/** Width of a label and of the plot, in pixels, for staggering the labels; an estimate, the layout is fluid. */
const LABEL_CHAR_PX = 6.5;
const PLOT_PX = 700;
const PLOT_COMPACT_PX = 420;

/** The window's words, as the other extras spell a period. */
const WINDOW_WORD: Readonly<Record<Window, string>> = { '15m': '15 min', '1h': '1 h', '3h': '3 h' };

const select = `rounded-md border border-line bg-panel px-2 py-1 text-xs ${focusRing}`;

/**
 * The end of a name that tells it from its siblings: a pod's ReplicaSet and pod
 * hashes (`-7f9c6d8b5-x2k9q`), else its last ten characters. None for a name
 * short enough to need no cut in the middle.
 */
export function tailOf(name: string): string {
  if (name.length <= 24) return '';
  const parts = name.split('-');
  if (parts.length >= 3) {
    const hashes = `-${parts.slice(-2).join('-')}`;
    if (hashes.length <= 17) return hashes;
  }
  return name.slice(-10);
}

/**
 * A name cut in the middle: its start gives way with an ellipsis, its end
 * stays. The whole name is its tooltip, and its text.
 *
 * **The end is never cut.** The box is a grid of a `minmax(0,1fr)` start and
 * an `auto` end: at its smallest (a parent's min-content) the start's track is
 * zero and only the end counts, and at its widest the whole name fits. With no
 * `min-w-0` on the box, a flex or grid parent cannot make it narrower than its
 * end; what sits beside it yields instead.
 */
export function CutName({ name, className = '' }: { name: string; className?: string }): ReactNode {
  const tail = tailOf(name);
  if (tail === '') {
    return (
      <span data-cut-name="" title={name} className={`min-w-0 truncate ${className}`}>
        {name}
      </span>
    );
  }
  return (
    <span data-cut-name="" title={name} className={`grid grid-cols-[minmax(0,1fr)_auto] ${className}`}>
      <span className="min-w-0 truncate">{name.slice(0, name.length - tail.length)}</span>
      <span className="whitespace-nowrap" data-cut-name-end="">
        {tail}
      </span>
    </span>
  );
}

/**
 * A source that was not read whole, in two words: a refusal for this person,
 * a read nobody could review, a failure, or a narrowed answer. Its note is the
 * hover. A source read whole says nothing.
 */
export function SourceNote({ what, section }: { what: string; section: Section | undefined }): ReactNode {
  if (section === undefined || (section.state === 'read' && section.complete)) return null;
  const word = section.state === 'denied' ? 'refused' : section.state === 'unreviewable' ? 'not reviewed' : section.state === 'failed' ? 'failed' : 'partial';
  return (
    <span data-source={what} data-state={section.state} title={section.note ?? ''} className={`text-xs ${section.state === 'read' ? 'text-muted' : 'text-warn'}`}>
      {what}: {word}
    </span>
  );
}

export function Sources({ answer }: { answer: Answer }): ReactNode {
  const s = answer.sources;
  const notes = [
    ['Events', s.events],
    ['ReplicaSets', s.replicasets],
    ['Pods', s.pods],
    ['ConfigMaps', s.configmaps],
    ['Nodes', s.nodes],
  ] as const;
  const shown = notes.filter(([, sec]) => !(sec.state === 'read' && sec.complete));
  if (shown.length === 0 && !answer.truncated) return null;
  return (
    <div data-sources="" className="flex flex-wrap gap-x-3 gap-y-1">
      {notes.map(([what, sec]) => (
        <SourceNote key={what} what={what} section={sec} />
      ))}
      {answer.truncated ? (
        <span data-truncated="" className="text-xs text-muted" title="The answer was cut at its limits: the busiest are kept">
          truncated
        </span>
      ) : null}
    </div>
  );
}

/** The page's five figures; pressing one filters the list, pressing it again does not. */
export function Tiles({
  tiles,
  filters,
  onTile,
}: {
  /** The figures for the chosen namespace; undefined while they are being read. */
  tiles: TilesData | undefined;
  filters: Filters;
  onTile: (key: TileKey | '') => void;
}): ReactNode {
  return (
    <ul aria-label="Tiles" aria-busy={tiles === undefined} className="flex flex-wrap gap-2">
      {TILE_KEYS.map((key) => {
        const tile = tiles?.[key] ?? { count: 0, state: 'failed' as const };
        const waiting = tiles === undefined;
        const on = filters.tile === key;
        const read = tile.state === 'read';
        return (
          <li key={key}>
            <button
              type="button"
              data-tile={key}
              data-state={waiting ? 'pending' : tile.state}
              data-count={read && !waiting ? tile.count : undefined}
              aria-pressed={on}
              title={waiting ? TILE_LABEL[key] : read ? TILE_HINT[key] : `${TILE_LABEL[key]}: ${tile.state}`}
              onClick={() => onTile(on ? '' : key)}
              className={`flex items-center gap-2 rounded-md border px-3 py-1.5 ${on ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'} ${focusRing}`}
            >
              <span className={`text-2xl font-semibold tabular-nums ${waiting ? 'text-muted' : !read ? 'text-warn' : tile.count === 0 ? 'text-muted' : ''}`}>{waiting ? '…' : tileWord(tile)}</span>
              <span className="text-xs text-muted">{TILE_LABEL[key]}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

/** The window, 15 minutes to 3 hours, as a row of buttons. */
export function WindowPicker({ window, onWindow }: { window: Window; onWindow: (w: Window) => void }): ReactNode {
  return (
    <div role="group" aria-label="Window" className="flex overflow-hidden rounded-md border border-line">
      {WINDOWS.map((w) => (
        <button
          key={w}
          type="button"
          data-window={w}
          aria-pressed={window === w}
          onClick={() => onWindow(w)}
          className={`px-2.5 py-1 text-xs tabular-nums ${window === w ? 'bg-panel-2 text-fg' : 'bg-panel text-muted hover:bg-panel-2'} ${focusRing}`}
        >
          {WINDOW_WORD[w]}
        </button>
      ))}
    </div>
  );
}

/** Window, namespace, type and kind: the controls over the list. */
export function Controls({
  answer,
  filters,
  onFilters,
  window,
  onWindow,
}: {
  answer: Answer;
  filters: Filters;
  onFilters: (next: Filters) => void;
  window: Window;
  onWindow: (w: Window) => void;
}): ReactNode {
  const opts = useMemo(() => options(answer), [answer]);
  const set = (name: 'namespace' | 'type' | 'kind', value: string) => onFilters({ ...filters, [name]: value });
  return (
    <div className="flex flex-wrap items-center gap-2">
      <WindowPicker window={window} onWindow={onWindow} />
      <select
        aria-label="Namespace"
        title={filters.namespace === '' ? 'Namespace' : filters.namespace}
        value={filters.namespace}
        onChange={(e) => set('namespace', e.target.value)}
        className={`${select} max-w-full min-w-0 sm:max-w-72`}
      >
        <option value="">Namespace</option>
        {opts.namespaces.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
      <select aria-label="Type" value={filters.type} onChange={(e) => set('type', e.target.value)} className={select}>
        <option value="">Type</option>
        {opts.types.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
      <select aria-label="Kind" value={filters.kind} onChange={(e) => set('kind', e.target.value)} className={select}>
        <option value="">Kind</option>
        {opts.kinds.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </div>
  );
}

/** What is lit: a change, or a cause. Nothing lit means nothing dims. */
export type Focus = { kind: 'change' | 'cause'; id: string } | null;

export interface Highlight {
  focus: Focus;
  hover: (next: Focus) => void;
  pin: (next: NonNullable<Focus>) => void;
  clear: () => void;
}

/** Hover lights while the pointer is on a change; a click pins it until the next click or Escape. */
export function useHighlight(): Highlight {
  const [pinned, setPinned] = useState<Focus>(null);
  const [hovered, setHovered] = useState<Focus>(null);
  const clear = useCallback(() => {
    setPinned(null);
    setHovered(null);
  }, []);
  const pin = useCallback((next: NonNullable<Focus>) => setPinned((cur) => (cur !== null && cur.kind === next.kind && cur.id === next.id ? null : next)), []);
  useEffect(() => {
    if (pinned === null && hovered === null) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') clear();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [pinned, hovered, clear]);
  return { focus: hovered ?? pinned, hover: setHovered, pin, clear };
}

const same = (a: Focus, kind: 'change' | 'cause', id: string): boolean => a !== null && a.kind === kind && a.id === id;

/**
 * Labels in rows, deterministically. A label spans from its dot to its end (to
 * the dot, when flipped), and a change's line runs down from its dot's row. A
 * mark takes the first row where its span meets no other label in that row, no
 * lower mark's line crosses its label, and no line of its own crosses the
 * label of a mark below it.
 */
export function staggered(marks: readonly { id: string; at: number; width: number; flip: boolean }[]): Map<string, number> {
  const placed: { at: number; from: number; to: number; row: number }[] = [];
  const out = new Map<string, number>();
  for (const m of [...marks].sort((a, b) => a.at - b.at || (a.id < b.id ? -1 : 1))) {
    const from = m.flip ? m.at - m.width : m.at;
    const to = m.flip ? m.at : m.at + m.width;
    const overlaps = (row: number): boolean => placed.some((p) => p.row === row && p.from < to && from < p.to);
    const crosses = (row: number): boolean =>
      placed.some((p) => (p.row > row && p.from <= m.at && m.at <= p.to) || (p.row < row && from <= p.at && p.at <= to));
    // A line runs from its label down to the bars, so a label whose span covers an
    // earlier line may have no clean row at all: past the rows already used, fall
    // back to the first row where no label overlaps it.
    let row = 0;
    while (row <= placed.length && (overlaps(row) || crosses(row))) row++;
    if (row > placed.length) {
      row = 0;
      while (overlaps(row)) row++;
    }
    placed.push({ at: m.at, from, to, row });
    out.set(m.id, row);
  }
  return out;
}

function ChangeMark({
  change,
  answer,
  axis,
  row,
  flip,
  dim,
  lit,
  highlight,
}: {
  change: Change;
  answer: Answer;
  axis: Axis;
  row: number;
  flip: boolean;
  dim: boolean;
  lit: boolean;
  highlight: Highlight;
}): ReactNode {
  const pct = spot(Date.parse(change.time), axis);
  const id = change.id;
  return (
    <button
      type="button"
      data-change={id}
      data-kind={change.kind}
      data-dim={dim}
      data-lit={lit}
      aria-pressed={same(highlight.focus, 'change', id)}
      title={changeTitle(change, ago(change.time, answer.generatedAt))}
      style={{ left: `${String(pct)}%`, top: `${String(row * LABEL_ROW_PX)}px` }}
      onMouseEnter={() => highlight.hover({ kind: 'change', id })}
      onMouseLeave={() => highlight.hover(null)}
      onClick={() => highlight.pin({ kind: 'change', id })}
      className={`absolute z-10 flex h-4 items-center gap-1 whitespace-nowrap rounded-sm px-1 text-2xs leading-none ${flip ? '-translate-x-full flex-row-reverse' : ''} ${dim ? 'opacity-25' : ''} ${lit ? 'bg-panel-2 text-fg' : 'bg-panel text-muted hover:text-fg'} ${focusRing}`}
    >
      <span className={`size-2 shrink-0 rounded-full ${CHANGE_TONE[change.kind]}`} />
      {changeShort(change)}
    </button>
  );
}

/**
 * The timeline: a row of labeled changes over lanes, one per object and
 * cause, named at the left and as thick as their Events are dense. A change
 * runs down through the lanes as a line; the axis starts where the activity
 * does.
 */
export function Timeline({
  answer,
  causes,
  highlight,
  compact = false,
}: {
  answer: Answer;
  causes: readonly Cause[];
  highlight: Highlight;
  /** A narrow container, a panel's: a shorter name column and a plot sized for it. */
  compact?: boolean;
}): ReactNode {
  // The names' column: narrower on a phone, so the plot keeps its width.
  const gutter = compact ? 'w-40' : 'w-36 sm:w-56';
  // The plot's real width where it has been measured: the labels' rows and
  // which way each one opens are worked out against it.
  const plotBox = useRef<HTMLDivElement>(null);
  const plot = useWidth(plotBox) ?? (compact ? PLOT_COMPACT_PX : PLOT_PX);
  const { drawn, total } = useMemo(() => bars(causes), [causes]);
  const shown = useMemo(() => answer.changes.filter((c) => c.kind !== 'config' || c.referenced), [answer.changes]);
  const axis = useMemo(() => axisOf(drawn, shown, answer.since, answer.generatedAt), [drawn, shown, answer.since, answer.generatedAt]);
  const marks = useMemo(() => {
    const list = shown.map((c) => {
      const at = spot(Date.parse(c.time), axis);
      return { id: c.id, at, width: ((changeShort(c).length * LABEL_CHAR_PX + 22) / plot) * 100, flip: at + ((changeShort(c).length * LABEL_CHAR_PX + 22) / plot) * 100 > 100 };
    });
    return { rows: staggered(list), flip: new Map(list.map((m) => [m.id, m.flip])) };
  }, [shown, axis, plot]);
  const rows = marks.rows.size === 0 ? 0 : Math.max(...marks.rows.values()) + 1;
  const { focus } = highlight;
  const focusedCause = focus?.kind === 'cause' ? causes.find((c) => causeKey(c) === focus.id) : undefined;
  const litGroup = (b: Bar): boolean => (focus === null ? true : focus.kind === 'change' ? b.sinces.includes(focus.id) : b.cause === focus.id);
  const litChange = (id: string): boolean => (focus === null ? false : focus.kind === 'change' ? focus.id === id : focusedCause?.since === id);
  const tick = ticks(axis);
  return (
    <section aria-label="Timeline" data-timeline="" data-axis-start={axis.start} className="flex flex-col overflow-hidden rounded-lg border border-line bg-panel px-3.5 py-3">
      <div className="relative">
        <div className="relative">
        <div className={`pointer-events-none absolute inset-y-0 right-0 ${compact ? 'left-40' : 'left-36 sm:left-56'}`}>
          {tick.map((t) => (
            <span key={t.at} className="absolute inset-y-0 border-l border-line" style={{ left: `${String(t.at)}%` }} />
          ))}
          {shown.map((c) => (
            <span
              key={c.id}
              data-line={c.id}
              className={`absolute bottom-0 border-l ${litChange(c.id) ? 'border-fg' : 'border-muted'} ${focus !== null && !litChange(c.id) ? 'opacity-20' : 'opacity-60'}`}
              style={{ left: `${String(spot(Date.parse(c.time), axis))}%`, top: `${String((marks.rows.get(c.id) ?? 0) * LABEL_ROW_PX + 8)}px` }}
            />
          ))}
        </div>
        <div className="relative flex">
          <div className={`${gutter} shrink-0`} />
          <div ref={plotBox} data-changes="" className="relative min-w-0 flex-1" style={{ height: `${String(Math.max(1, rows) * LABEL_ROW_PX + 4)}px` }}>
            {shown.map((c) => (
              <ChangeMark
                key={c.id}
                change={c}
                answer={answer}
                axis={axis}
                row={marks.rows.get(c.id) ?? 0}
                flip={marks.flip.get(c.id) === true}
                dim={focus !== null && !litChange(c.id)}
                lit={litChange(c.id)}
                highlight={highlight}
              />
            ))}
          </div>
        </div>
        <div data-bars="" className="relative border-t border-line">
          {drawn.map((b, i) => {
            const left = spot(b.first, axis);
            const lit = litGroup(b);
            const path = b.namespace === '' ? b.name : `${b.namespace}/${b.name}`;
            const detail = `${path} · ${b.reason} × ${String(b.count)}${b.objects > 1 ? ` · ${String(b.objects)} objects` : ''}`;
            return (
              <div
                key={`${b.cause}/${path}/${String(i)}`}
                data-lane={`${b.namespace}/${b.name}`}
                data-lit={focus !== null && lit}
                data-dim={focus !== null && !lit}
                title={detail}
                style={{ height: `${String(LANE_PX)}px` }}
                className={`flex items-center ${focus !== null && !lit ? 'opacity-25' : ''}`}
              >
                <span
                  data-lane-name=""
                  title={detail}
                  className={`${gutter} flex min-w-0 shrink-0 overflow-hidden pr-2 text-xs ${lit && focus !== null ? 'text-fg' : 'text-muted'}`}
                >
                  {/* The name gives way in its middle first, the cause beside it last. */}
                  <CutName name={b.name} className="shrink-[999]" />
                  <span className="min-w-0 truncate whitespace-pre">{` · ${b.reason}`}</span>
                </span>
                <div className="relative h-full min-w-0 flex-1">
                  <span
                    data-bar={`${b.namespace}/${b.name}`}
                    data-since={b.sinces[0]}
                    title={detail}
                    style={{ left: `${String(left)}%`, width: `${String(Math.max(0.6, spot(b.last, axis) - left))}%`, height: `${String(b.px)}px`, top: `${String((LANE_PX - b.px) / 2)}px` }}
                    className={`absolute rounded-full ${b.type === 'Warning' ? 'bg-warn' : 'bg-muted'}`}
                  />
                </div>
              </div>
            );
          })}
        </div>
        </div>
        <div className="relative flex border-t border-line pt-1">
          <div className={`flex ${gutter} shrink-0 items-center pr-2 text-2xs tabular-nums text-muted`}>
            {total > drawn.length ? (
              <span title={`The ${String(drawn.length)} busiest of ${String(total)} groups are drawn; the list holds them all`}>
                {drawn.length} of {total}
              </span>
            ) : null}
          </div>
          <div data-axis="" className="relative h-4 min-w-0 flex-1 text-2xs tabular-nums text-muted">
            {tick.map((t, i) => (
              <span
                key={t.at}
                data-tick=""
                // On a phone every other inner tick is left out, or their labels meet.
                className={`absolute whitespace-nowrap ${i === 0 ? '' : i === tick.length - 1 ? '-translate-x-full' : '-translate-x-1/2'} ${i % 2 === 1 && i < tick.length - 1 ? 'max-sm:hidden' : ''}`}
                style={{ left: `${String(t.at)}%` }}
              >
                {t.label}
              </span>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

function objectHref(cluster: string, kind: string, namespace: string, name: string): string | undefined {
  const plural = pluralOf(kind);
  return plural === undefined ? undefined : objectPath({ cluster, kind: plural, namespace, name });
}

function CauseRow({ cluster, answer, cause, highlight }: { cluster: string; answer: Answer; cause: Cause; highlight: Highlight }): ReactNode {
  const [open, setOpen] = useState(false);
  const [pages, setPages] = useState(1);
  const since = changeOf(answer, cause.since);
  const age = ago(since === undefined ? cause.lastSeen : since.time, answer.generatedAt);
  const shown = cause.groups.slice(0, pages * PAGE_OBJECTS);
  const key = causeKey(cause);
  const on = same(highlight.focus, 'cause', key);
  const dim = highlight.focus !== null && !on && !(highlight.focus.kind === 'change' && cause.since === highlight.focus.id);
  return (
    <li data-cause={cause.reason} data-type={cause.type} data-active={on} className={`flex flex-col ${dim ? 'opacity-50' : ''}`}>
      <button
        type="button"
        aria-expanded={open}
        aria-pressed={on}
        title={cause.message}
        onClick={() => {
          setOpen(!open);
          highlight.pin({ kind: 'cause', id: key });
        }}
        className={`flex items-center gap-2 py-2 text-left text-sm ${on ? 'text-fg' : ''} ${focusRing}`}
      >
        <span className="w-3 shrink-0 text-xs text-muted">{open ? '▾' : '▸'}</span>
        <span className={`size-2 shrink-0 rounded-full ${cause.type === 'Warning' ? 'bg-warn' : 'bg-muted'}`} />
        <span className="shrink-0 font-semibold">
          {cause.reason} <span className="tabular-nums">× {cause.count}</span>
        </span>
        <span className="flex min-w-0 text-xs text-muted" data-cause-summary="">
          <span className="shrink-0 whitespace-pre">{`${objectsWord(cause)} · `}</span>
          {since === undefined ? (
            <span className="shrink-0">last {age}</span>
          ) : (
            <>
              <span className="min-w-0 truncate" title={`since ${changeLabel(since)}`}>
                since {changeLabel(since)}
              </span>
              <span className="shrink-0 whitespace-pre">{` · ${age}`}</span>
            </>
          )}
        </span>
      </button>
      {open ? (
        <ul
          data-groups=""
          className="mb-2 ml-3 grid grid-cols-[auto_minmax(0,1fr)_minmax(min-content,2fr)_auto] divide-y divide-line rounded-md border border-line sm:ml-8"
        >
          {shown.map((g) => {
            const href = objectHref(cluster, g.kind, g.namespace, g.name);
            const where = g.namespace === '' ? g.name : `${g.namespace}/${g.name}`;
            return (
              <li
                key={`${g.kind}/${g.namespace}/${g.name}`}
                data-group={`${g.namespace}/${g.name}`}
                className="col-span-4 grid grid-cols-subgrid items-center gap-x-2 px-2 py-1 text-xs"
                title={`${where}\n${g.message}`}
              >
                <span className="text-2xs text-muted">{g.kind}</span>
                <span data-group-namespace="" className="min-w-0 truncate text-muted" title={g.namespace}>
                  {g.namespace}
                </span>
                {href === undefined ? (
                  <CutName name={g.name} />
                ) : (
                  <Link to={href} className={`flex text-accent hover:underline ${focusRing}`}>
                    <CutName name={g.name} />
                  </Link>
                )}
                <span data-group-count="" className="whitespace-nowrap text-right tabular-nums text-muted">
                  × {g.count} · {ago(g.lastSeen, answer.generatedAt)}
                </span>
              </li>
            );
          })}
          {cause.groups.length > shown.length ? (
            <li className="col-span-4 px-2 py-1">
              <button type="button" className={`text-xs text-accent underline ${focusRing}`} onClick={() => setPages((p) => p + 1)}>
                +{cause.groups.length - shown.length}
              </button>
            </li>
          ) : null}
        </ul>
      ) : null}
    </li>
  );
}

/** The causes, most-warned first as the server sorts them, PAGE_CAUSES at a time. */
export function Causes({ cluster, answer, causes, highlight }: { cluster: string; answer: Answer; causes: readonly Cause[]; highlight: Highlight }): ReactNode {
  const [pages, setPages] = useState(1);
  const shown = causes.slice(0, pages * PAGE_CAUSES);
  if (causes.length === 0) {
    // An empty answer over a source that was read is a quiet cluster; over one that was not, there is nothing to call quiet.
    return answer.sources.events.state === 'read' ? (
      <p data-none="" className="text-xs text-muted">
        None
      </p>
    ) : null;
  }
  return (
    <section aria-label="Causes" className="flex flex-col rounded-lg border border-line bg-panel px-3.5 py-1">
      <ul className="flex flex-col divide-y divide-line">
        {shown.map((c) => (
          <CauseRow key={causeKey(c)} cluster={cluster} answer={answer} cause={c} highlight={highlight} />
        ))}
      </ul>
      {causes.length > shown.length ? (
        <button type="button" className={`self-start py-2 text-xs text-accent underline ${focusRing}`} onClick={() => setPages((p) => p + 1)}>
          +{causes.length - shown.length}
        </button>
      ) : null}
    </section>
  );
}

/** An element's width in pixels, followed as it changes; none before it is laid out. */
function useWidth(box: RefObject<HTMLElement | null>): number | undefined {
  const [width, setWidth] = useState<number | undefined>(undefined);
  useEffect(() => {
    const element = box.current;
    if (element === null || typeof ResizeObserver === 'undefined') return;
    const read = (): void => {
      const next = element.getBoundingClientRect().width;
      setWidth(next > 0 ? next : undefined);
    };
    read();
    const watch = new ResizeObserver(read);
    watch.observe(element);
    return () => watch.disconnect();
  }, [box]);
  return width;
}
