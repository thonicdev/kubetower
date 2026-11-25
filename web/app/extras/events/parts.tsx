import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { btnToggle, focusRing, objectPath } from '@kubetower/ui';

import { WINDOWS, ago, type Answer, type Cause, type Change, type Section, type Tiles as TilesData, type Window } from './data';

export type { Focus } from './model';
import {
  BINS,
  CHANGE_TONE,
  PAGE_WORKLOADS,
  TILE_HINT,
  TILE_KEYS,
  TILE_LABEL,
  axisOf,
  binOf,
  binSpan,
  causeKey,
  causeLit,
  changeLabel,
  changeLit,
  changeOf,
  changeShort,
  changeTitle,
  correlations,
  figure,
  groupNodes,
  heldOf,
  laneLit,
  lanesOf,
  maxOf,
  nodeGroupLabel,
  nodeTracks,
  objectsWord,
  options,
  pluralOf,
  spot,
  ticks,
  tileWord,
  workloadsOf,
  type Axis,
  type Filters,
  type Focus,
  type NodeGroup,
  type TileKey,
} from './model';

/** How many causes the list draws before "more", and how many objects one cause unfolds to. */
export const PAGE_CAUSES = 100;
export const PAGE_OBJECTS = 50;

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
  if (shown.length === 0) return null;
  return (
    <div data-sources="" className="flex flex-wrap gap-x-3 gap-y-1">
      {notes.map(([what, sec]) => (
        <SourceNote key={what} what={what} section={sec} />
      ))}
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

const same = (a: Focus, kind: NonNullable<Focus>['kind'], id: string): boolean => a !== null && a.kind === kind && a.id === id;

/** A cell's figure: "<1" for a sliver, "≈ 37" where the spread split an Event. */
function approx(v: number): string {
  if (v > 0 && v < 1) return '<1';
  return Number.isInteger(v) ? figure(v) : `≈ ${figure(v)}`;
}

/** Why a cell holds a fraction: the answer has each object's first and last Event, not each Event's time. */
const SPREAD = 'Spread evenly from each object’s first to last Event';

/** A row's cells, darker where its Events are more; each cell's hover says how many, and when. */
function Strip({ bins, max, warning, axis }: { bins: readonly number[]; max: number; warning: boolean; axis: Axis }): ReactNode {
  return (
    <div data-strip="" className="flex h-3 min-w-0 flex-1 gap-px">
      {bins.map((v, i) => (
        <span
          key={i}
          data-cell={i}
          data-count={v === 0 ? undefined : Math.round(v * 100) / 100}
          title={v === 0 ? binSpan(i, axis) : `${approx(v)} · ${binSpan(i, axis)}\n${SPREAD}`}
          className={`h-full min-w-0 flex-1 rounded-xs ${v === 0 ? 'bg-panel-2' : warning ? 'bg-warn' : 'bg-muted'}`}
          style={v === 0 ? undefined : { opacity: 0.2 + 0.8 * Math.sqrt(v / max) }}
        />
      ))}
    </div>
  );
}

/** The toggle that adds the Normal Events back, with how many there are. */
export function NormalToggle({ on, count, onToggle }: { on: boolean; count: number; onToggle: () => void }): ReactNode {
  if (count === 0) return null;
  return (
    <button
      type="button"
      data-normal-toggle=""
      aria-pressed={on}
      onClick={onToggle}
      className={`${btnToggle(on)} gap-1.5`}
    >
      <span className={`size-2 rounded-full ${on ? 'bg-muted' : 'border border-muted'}`} />
      Normal
      <span className="tabular-nums text-muted">{figure(count)}</span>
    </button>
  );
}

/**
 * The changes Warnings came after, a card each, most Events first: what
 * changed, the reason it brought most, and the others. A card lights its rows
 * and its markers, as a change's marker does.
 */
export function Correlations({ answer, causes, highlight, compact = false }: { answer: Answer; causes: readonly Cause[]; highlight: Highlight; compact?: boolean }): ReactNode {
  const [more, setMore] = useState(false);
  const list = useMemo(() => correlations(causes, answer.changes, groupNodes(answer.changes).groups), [causes, answer.changes]);
  if (list.length === 0) return null;
  const first = compact ? 2 : 3;
  const shown = more ? list : list.slice(0, first);
  const { focus } = highlight;
  return (
    <section aria-label="Correlations" data-correlations="" className="flex flex-wrap gap-2">
      {shown.map((c) => {
        const lead = c.reasons[0];
        const on = same(focus, 'correlation', c.id);
        const lit = focus !== null && c.changes.some((id) => changeLit(focus, id, causes));
        const dim = focus !== null && !lit && !on;
        const to: NonNullable<Focus> = { kind: 'correlation', id: c.id, ids: c.changes };
        return (
          <button
            key={c.id}
            type="button"
            data-correlation={c.id}
            data-lit={lit || on}
            aria-pressed={on}
            title={`${c.label} → ${c.reasons.map((r) => `${r.reason} × ${String(r.count)} · ${objectsWord({ objects: r.objects, kinds: [] })}`).join('\n')}`}
            onMouseEnter={() => highlight.hover(to)}
            onMouseLeave={() => highlight.hover(null)}
            onClick={() => highlight.pin(to)}
            className={`flex min-w-0 grow basis-64 flex-col gap-1 rounded-md border px-3 py-2 text-left ${on || lit ? 'border-accent bg-panel-2' : 'border-line bg-panel hover:bg-panel-2'} ${dim ? 'opacity-50' : ''} ${focusRing}`}
          >
            <span className="flex min-w-0 items-center gap-2 text-xs text-muted">
              <span className={`size-2.5 shrink-0 rounded-full ${CHANGE_TONE[c.kind]}`} />
              <CutName name={c.label} />
            </span>
            {lead === undefined ? null : (
              <span className="flex min-w-0 items-baseline gap-2">
                <span className="text-xl font-semibold tabular-nums text-warn">{figure(lead.count)}</span>
                <span className="truncate text-sm font-semibold">{lead.reason}</span>
                <span className="shrink-0 text-xs text-muted">{objectsWord({ objects: lead.objects, kinds: [] })}</span>
              </span>
            )}
            {c.reasons.length > 1 ? (
              <span className="flex flex-wrap gap-1">
                {c.reasons.slice(1).map((r) => (
                  <span key={r.reason} className="rounded bg-panel-2 px-1.5 py-0.5 text-2xs tabular-nums text-muted">
                    {r.reason} {figure(r.count)}
                  </span>
                ))}
              </span>
            ) : null}
          </button>
        );
      })}
      {list.length > first && !more ? (
        <button type="button" className={`self-center text-xs text-accent underline ${focusRing}`} onClick={() => setMore(true)}>
          +{list.length - first}
        </button>
      ) : null}
    </section>
  );
}

/** A row of the grid: its name at the left, its cells, its total at the right. */
function Row({ gutter, name, total, children, ...rest }: { gutter: string; name: ReactNode; total: ReactNode; children: ReactNode } & Record<`data-${string}`, string | boolean | undefined> & { className?: string; title?: string }): ReactNode {
  const { className = '', title, ...data } = rest;
  return (
    <div {...data} title={title} className={`flex h-5 items-center ${className}`}>
      <span className={`${gutter} flex min-w-0 shrink-0 items-center gap-1.5 pr-2 text-xs`}>{name}</span>
      {children}
      <span className="w-12 shrink-0 text-right text-xs tabular-nums">{total}</span>
    </div>
  );
}

/**
 * The timeline: a grid of cells over the window, one row of the nodes that
 * came and went, one of the other changes, then one row per workload that
 * unfolds to its reasons, each darker where its Events are dense, with its
 * total at the end. The axis starts where the activity does.
 */
export function Timeline({
  answer,
  causes,
  highlight,
  normal,
  compact = false,
  scope,
}: {
  answer: Answer;
  causes: readonly Cause[];
  highlight: Highlight;
  /** A namespace the page is filtered to, and its tiles: null while they are read. */
  scope?: { namespace: string; tiles: TilesData | null | undefined } | undefined;
  /** The Normal toggle, where the Type filter has not chosen. */
  normal?: { on: boolean; count: number; onToggle: () => void } | undefined;
  /** A narrow container, a panel's: a shorter name column. */
  compact?: boolean;
}): ReactNode {
  const gutter = compact ? 'w-36' : 'w-36 sm:w-56';
  const changes = useMemo(() => answer.changes.filter((c) => c.kind !== 'config' || c.referenced), [answer.changes]);
  const axis = useMemo(() => axisOf(causes, changes, answer.since, answer.generatedAt), [causes, changes, answer.since, answer.generatedAt]);
  const nodes = useMemo(() => groupNodes(changes), [changes]);
  const lanes = useMemo(() => lanesOf(causes, axis), [causes, axis]);
  const workloads = useMemo(() => workloadsOf(lanes), [lanes]);
  const [pages, setPages] = useState(1);
  const [unfolded, setUnfolded] = useState<ReadonlySet<string> | null>(null);
  const open = unfolded ?? new Set(workloads.slice(0, 1).map((w) => w.key));
  const toggle = (key: string) => setUnfolded(() => {
    const next = new Set(open);
    if (next.has(key)) next.delete(key);
    else next.add(key);
    return next;
  });
  const drawn = workloads.slice(0, pages * PAGE_WORKLOADS);
  const laneMax = useMemo(() => maxOf(lanes.flatMap((l) => l.bins), 1), [lanes]);
  const rowMax = maxOf(drawn.flatMap((w) => w.bins), 1);
  const nodeBins = useMemo(() => {
    const added = Array.from({ length: BINS }, () => 0);
    const notReady = Array.from({ length: BINS }, () => 0);
    for (const g of nodes.groups) for (const c of g.changes) (g.kind === 'node-added' ? added : notReady)[binOf(Date.parse(c.time), axis)]! += 1;
    return { added, notReady, max: maxOf([...added, ...notReady], 1) };
  }, [nodes.groups, axis]);
  const tracks = useMemo(() => nodeTracks(nodes.groups, axis), [nodes.groups, axis]);
  const { focus } = highlight;
  const held = scope === undefined ? heldOf(answer) : scope.tiles ? heldOf(answer, scope.namespace, scope.tiles) : undefined;
  const tick = ticks(axis);
  const addedCount = nodes.groups.filter((g) => g.kind === 'node-added').reduce((n, g) => n + g.changes.length, 0);
  const notReadyCount = nodes.groups.filter((g) => g.kind === 'node-notready').reduce((n, g) => n + g.changes.length, 0);
  const hoverable = (to: NonNullable<Focus>) => ({
    onMouseEnter: () => highlight.hover(to),
    onMouseLeave: () => highlight.hover(null),
    onClick: () => highlight.pin(to),
  });
  const pct = (bin: number) => `${String((bin / BINS) * 100)}%`;
  return (
    <section aria-label="Timeline" data-timeline="" data-axis-start={axis.start} className="flex flex-col gap-1 overflow-hidden rounded-lg border border-line bg-panel px-3.5 py-3">
      {nodes.groups.length > 0 ? (
        <Row
          gutter={gutter}
          data-nodes-row=""
          name={
            <>
              <span className="size-2 shrink-0 rounded-full bg-plate-green" />
              <span className="font-semibold">Nodes</span>
              {addedCount > 0 ? <span className="rounded bg-plate-green/25 px-1 text-2xs tabular-nums text-fg">+{addedCount}</span> : null}
              {notReadyCount > 0 ? <span className="rounded bg-plate-red/30 px-1 text-2xs tabular-nums text-fg">!{notReadyCount}</span> : null}
            </>
          }
          total={<span className="text-muted">{addedCount + notReadyCount}</span>}
        >
          <div className="relative flex h-3 min-w-0 flex-1 gap-px">
            {nodeBins.added.map((v, i) => {
              const red = nodeBins.notReady[i] ?? 0;
              return (
                <span
                  key={i}
                  className={`h-full min-w-0 flex-1 rounded-xs ${red > 0 ? 'bg-plate-red-pale' : v > 0 ? 'bg-plate-green-pale' : 'bg-panel-2'}`}
                  style={v + red === 0 ? undefined : { opacity: 0.4 + 0.6 * ((v + red) / nodeBins.max) }}
                />
              );
            })}
            <div className="pointer-events-none absolute -inset-y-1 inset-x-0">
            {nodes.groups.map((g) => {
              const from = binOf(g.first, axis);
              const track = tracks.track.get(g.id) ?? 0;
              const to = binOf(g.last, axis) + 1;
              const lit = focus !== null && g.changes.some((c) => changeLit(focus, c.id, causes));
              const id = g.changes.length === 1 ? (g.changes[0] as Change).id : g.id;
              const target: NonNullable<Focus> = g.changes.length === 1 ? { kind: 'change', id } : { kind: 'correlation', id: g.id, ids: g.changes.map((c) => c.id) };
              return (
                <button
                  key={g.id}
                  type="button"
                  data-node-group={g.id}
                  data-nodes={g.changes.length}
                  data-lit={lit}
                  aria-label={nodeGroupLabel(g)}
                  aria-pressed={same(focus, target.kind, target.id)}
                  title={[nodeGroupLabel(g), ...g.changes.map((c) => `${c.name} · ${ago(c.time, answer.generatedAt)}`)].join('\n')}
                  {...hoverable(target)}
                  data-track={track}
                  className={`pointer-events-auto absolute rounded-sm ${lit ? 'ring-1 ring-fg' : 'hover:ring-1 hover:ring-muted'} ${focusRing}`}
                  style={{ left: pct(from), width: pct(to - from), top: `${String((track / tracks.tracks) * 100)}%`, height: `${String(100 / tracks.tracks)}%` }}
                />
              );
            })}
            </div>
          </div>
        </Row>
      ) : null}
      {nodes.others.length > 0 ? (
        <Row
          gutter={gutter}
          data-changes=""
          name={
            <>
              <span className="size-2 shrink-0 rounded-full bg-plate-blue" />
              <span className="font-semibold">Changes</span>
            </>
          }
          total={<span className="text-muted">{nodes.others.length}</span>}
        >
          <div className="relative h-full min-w-0 flex-1">
            {nodes.others.map((c) => {
              const lit = changeLit(focus, c.id, causes);
              return (
                <button
                  key={c.id}
                  type="button"
                  data-change={c.id}
                  data-kind={c.kind}
                  data-lit={lit}
                  data-dim={focus !== null && !lit}
                  aria-label={changeShort(c)}
                  aria-pressed={same(focus, 'change', c.id)}
                  title={changeTitle(c, ago(c.time, answer.generatedAt))}
                  {...hoverable({ kind: 'change', id: c.id })}
                  className={`absolute top-1/2 size-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full ${CHANGE_TONE[c.kind]} ${lit ? 'ring-2 ring-fg' : ''} ${focus !== null && !lit ? 'opacity-30' : ''} ${focusRing}`}
                  style={{ left: `${String(spot(Date.parse(c.time), axis))}%` }}
                />
              );
            })}
          </div>
        </Row>
      ) : null}
      <div data-rows="" className="flex flex-col gap-1 border-t border-line pt-1.5">
        {drawn.map((w) => {
          const lit = focus !== null && w.lanes.some((l) => laneLit(focus, l));
          const dim = focus !== null && !lit;
          const unfoldedHere = open.has(w.key);
          return (
            <div key={w.key} className="flex flex-col gap-1">
              <Row
                gutter={gutter}
                data-workload={w.key}
                data-lit={focus !== null && lit}
                data-dim={dim}
                className={dim ? 'opacity-35' : ''}
                title={`${w.namespace === '' ? w.name : `${w.namespace}/${w.name}`} · ${w.lanes.map((l) => `${l.reason} × ${String(l.count)}`).join(' · ')}`}
                name={
                  <button type="button" aria-expanded={unfoldedHere} onClick={() => toggle(w.key)} className={`flex min-w-0 items-center gap-1.5 ${focusRing}`}>
                    <span className="w-3 shrink-0 text-muted">{unfoldedHere ? '▾' : '▸'}</span>
                    <CutName name={w.name} className="font-semibold" />
                    {/* The namespace gives way before the name. */}
                    {w.namespace === '' ? null : <span className="min-w-0 shrink-[999] truncate text-2xs text-muted">{w.namespace}</span>}
                  </button>
                }
                total={<span className="font-semibold">{figure(w.count)}</span>}
              >
                <Strip bins={w.bins} max={rowMax} warning={w.type === 'Warning'} axis={axis} />
              </Row>
              {unfoldedHere
                ? w.lanes.map((l) => {
                    const laneOn = focus !== null && laneLit(focus, l);
                    return (
                      <Row
                        key={l.key}
                        gutter={gutter}
                        data-lane={`${l.namespace}/${l.workload}/${l.reason}`}
                        data-since={[...l.sinces][0]}
                        data-lit={laneOn}
                        data-dim={focus !== null && !laneOn}
                        className={focus !== null && !laneOn ? 'opacity-35' : ''}
                        title={`${l.reason} × ${String(l.count)} · ${objectsWord({ objects: l.objects, kinds: [] })}`}
                        name={
                          <span data-lane-name="" className="flex min-w-0 items-center gap-1 pl-4.5 text-muted">
                            <span className={`size-1.5 shrink-0 rounded-full ${l.type === 'Warning' ? 'bg-warn' : 'bg-muted'}`} />
                            <span className="min-w-0 truncate">{l.reason}</span>
                            <span className="shrink-0 text-2xs">· {l.objects}</span>
                          </span>
                        }
                        total={<span className="text-muted">{figure(l.count)}</span>}
                      >
                        <Strip bins={l.bins} max={laneMax} warning={l.type === 'Warning'} axis={axis} />
                      </Row>
                    );
                  })
                : null}
            </div>
          );
        })}
      </div>
      <div className="relative flex border-t border-line pt-1">
        <div className={`${gutter} shrink-0`} />
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
        <div className="w-12 shrink-0" />
      </div>
      <div className="flex flex-wrap items-center gap-2 pt-1">
        {normal === undefined ? null : <NormalToggle on={normal.on} count={normal.count} onToggle={normal.onToggle} />}
        <span className="ml-auto flex items-center gap-2 text-2xs tabular-nums text-muted">
          {held === undefined ? null : (
            <span data-held="" title="Warnings in the lists · Warnings counted" className="rounded bg-panel-2 px-1.5 py-0.5">
              {figure(held.held)} of {figure(held.total)}
            </span>
          )}
          {workloads.length > drawn.length ? (
            <>
              <span data-rows-shown="" title="Workloads drawn · Workloads">
                {drawn.length} of {workloads.length}
              </span>
              <button type="button" className={`text-accent underline ${focusRing}`} onClick={() => setPages((p) => p + 1)}>
                +{Math.min(PAGE_WORKLOADS, workloads.length - drawn.length)}
              </button>
            </>
          ) : null}
        </span>
      </div>
    </section>
  );
}

function objectHref(cluster: string, kind: string, namespace: string, name: string): string | undefined {
  const plural = pluralOf(kind);
  return plural === undefined ? undefined : objectPath({ cluster, kind: plural, namespace, name });
}

function CauseRow({ cluster, answer, cause, highlight, nodeGroups }: { cluster: string; answer: Answer; cause: Cause; highlight: Highlight; nodeGroups: ReadonlyMap<string, NodeGroup> }): ReactNode {
  const [open, setOpen] = useState(false);
  const [pages, setPages] = useState(1);
  const since = changeOf(answer, cause.since);
  // A node of a group that came together is said as the group.
  const group = since === undefined ? undefined : nodeGroups.get(since.id);
  const sinceWord = since === undefined ? '' : group !== undefined && group.changes.length > 1 ? nodeGroupLabel(group) : changeLabel(since);
  const age = ago(since === undefined ? cause.lastSeen : since.time, answer.generatedAt);
  const shown = cause.groups.slice(0, pages * PAGE_OBJECTS);
  const key = causeKey(cause);
  const on = same(highlight.focus, 'cause', key);
  const dim = highlight.focus !== null && !on && !causeLit(highlight.focus, cause);
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
              <span className="min-w-0 truncate" title={`since ${sinceWord}`}>
                since {sinceWord}
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
export function Causes({ cluster, answer, causes, highlight, hidden = 0 }: { cluster: string; answer: Answer; causes: readonly Cause[]; highlight: Highlight; /** The Normal Events behind the toggle. */ hidden?: number }): ReactNode {
  const [pages, setPages] = useState(1);
  const nodeGroups = useMemo(() => new Map(groupNodes(answer.changes).groups.flatMap((g) => g.changes.map((c) => [c.id, g] as const))), [answer.changes]);
  const shown = causes.slice(0, pages * PAGE_CAUSES);
  if (causes.length === 0) {
    // An empty answer over a source that was read is a quiet cluster; over one that was not, there is nothing to call quiet;
    // with Normal Events behind the toggle, the toggle says it.
    return answer.sources.events.state === 'read' && hidden === 0 ? (
      <p data-none="" className="text-xs text-muted">
        None
      </p>
    ) : null;
  }
  return (
    <section aria-label="Causes" className="flex flex-col rounded-lg border border-line bg-panel px-3.5 py-1">
      <ul className="flex flex-col divide-y divide-line">
        {shown.map((c) => (
          <CauseRow key={causeKey(c)} cluster={cluster} answer={answer} cause={c} highlight={highlight} nodeGroups={nodeGroups} />
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
