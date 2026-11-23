import type { Answer, Cause, Change, ChangeKind, ObjectGroup, SourceState, Tile } from './data';

/**
 * The page's pure logic: the filters, the figures a tile shows, the words of a
 * change and of a cause, and where each bar of the timeline sits. No React,
 * so a large answer is measured without drawing it.
 */

/** A tile's key: what pressing it filters the list down to. */
export type TileKey = 'warnings' | 'crashLoop' | 'oomKilled' | 'failedScheduling' | 'objectsTouched';
export const TILE_KEYS: readonly TileKey[] = ['warnings', 'crashLoop', 'oomKilled', 'failedScheduling', 'objectsTouched'];

export const TILE_LABEL: Readonly<Record<TileKey, string>> = {
  warnings: 'Warnings',
  crashLoop: 'CrashLoop',
  oomKilled: 'OOMKilled',
  failedScheduling: 'FailedScheduling',
  objectsTouched: 'Objects touched',
};

export const TILE_HINT: Readonly<Record<TileKey, string>> = {
  warnings: 'Warning events in the window, counted with their repeats',
  crashLoop: 'Pods whose container keeps restarting, read from the pods',
  oomKilled: 'Pods whose container was last killed for memory, read from the pods',
  failedScheduling: 'Scheduling failures, counted with their repeats',
  objectsTouched: 'Distinct objects with a warning',
};

export interface Filters {
  tile: TileKey | '';
  namespace: string;
  type: string;
  kind: string;
}

export const NO_FILTERS: Filters = { tile: '', namespace: '', type: '', kind: '' };

/** What a tile says: its figure, or the word for why there is none. */
export function tileWord(tile: Tile): string {
  if (tile.state === 'read') return String(tile.count);
  return tile.state === 'failed' ? 'failed' : 'refused';
}

export function tileRead(state: SourceState): boolean {
  return state === 'read';
}

const OOM = /oom/i;

function podKey(namespace: string, name: string): string {
  return `${namespace}/${name}`;
}

/** The group predicate a tile stands for; the pods' own state decides the two
 *  that are not Events. */
function tilePredicate(tile: TileKey, answer: Answer): (cause: Cause, group: ObjectGroup) => boolean {
  const crash = new Set(answer.pods.filter((p) => p.crashLoop).map((p) => podKey(p.namespace, p.name)));
  const oom = new Set(answer.pods.filter((p) => p.oomKilled).map((p) => podKey(p.namespace, p.name)));
  switch (tile) {
    case 'warnings':
    case 'objectsTouched':
      return (cause) => cause.type === 'Warning';
    case 'crashLoop':
      return (cause, g) => g.kind === 'Pod' && (crash.has(podKey(g.namespace, g.name)) || cause.reason === 'BackOff');
    case 'oomKilled':
      return (cause, g) => (g.kind === 'Pod' && oom.has(podKey(g.namespace, g.name))) || OOM.test(cause.reason);
    case 'failedScheduling':
      return (cause) => cause.reason === 'FailedScheduling';
  }
}

export function active(filters: Filters): boolean {
  return filters.tile !== '' || filters.namespace !== '' || filters.type !== '' || filters.kind !== '';
}

/**
 * The causes the filters leave. A cause whose groups all went is dropped; one
 * that lost some is recounted from the groups it keeps, because the server's
 * total covers every object. With no filter the answer is returned as it came.
 */
export function narrow(answer: Answer, filters: Filters): Cause[] {
  if (!active(filters)) return answer.causes;
  const byTile = filters.tile === '' ? undefined : tilePredicate(filters.tile, answer);
  const out: Cause[] = [];
  for (const cause of answer.causes) {
    if (filters.type !== '' && cause.type !== filters.type) continue;
    const groups = cause.groups.filter(
      (g) =>
        (filters.namespace === '' || g.namespace === filters.namespace) &&
        (filters.kind === '' || g.kind === filters.kind) &&
        (byTile === undefined || byTile(cause, g)),
    );
    if (groups.length === 0) continue;
    if (groups.length === cause.groups.length) {
      out.push(cause);
      continue;
    }
    const objects = new Set(groups.map((g) => `${g.kind}/${g.namespace}/${g.name}`));
    out.push({
      ...cause,
      groups,
      count: groups.reduce((n, g) => n + g.count, 0),
      objects: objects.size,
      kinds: [...new Set(groups.map((g) => g.kind))].sort(),
    });
  }
  return out;
}

/** Every namespace, type and kind the answer holds, for the filter controls. */
export function options(answer: Answer): { namespaces: string[]; types: string[]; kinds: string[] } {
  const namespaces = new Set<string>();
  const types = new Set<string>();
  const kinds = new Set<string>();
  for (const cause of answer.causes) {
    types.add(cause.type);
    for (const g of cause.groups) {
      if (g.namespace !== '') namespaces.add(g.namespace);
      kinds.add(g.kind);
    }
  }
  return { namespaces: [...namespaces].sort(), types: [...types].sort(), kinds: [...kinds].sort() };
}

/** One color each, from the console's plates. */
export const CHANGE_TONE: Readonly<Record<ChangeKind, string>> = {
  rollout: 'bg-plate-blue',
  rollback: 'bg-plate-violet',
  config: 'bg-plate-amber',
  scaling: 'bg-plate-teal',
  'node-added': 'bg-plate-green',
  'node-notready': 'bg-plate-red',
};

/** A change in a few words: "rollout checkout-api rev. 7". */
export function changeLabel(c: Change): string {
  const rev = c.revision === undefined || c.revision === 0 ? '' : ` rev. ${String(c.revision)}`;
  switch (c.kind) {
    case 'rollout':
    case 'rollback':
      return `${c.kind} ${c.name}${rev}`;
    case 'config':
      return `config ${c.name}`;
    case 'scaling':
      return `scaling ${c.name}`;
    case 'node-added':
      return `node ${c.name} added`;
    case 'node-notready':
      return `node ${c.name} not ready`;
  }
}

/** The hover of a change: the details the label leaves out. */
export function changeTitle(c: Change, at: string): string {
  const parts = [changeLabel(c), at];
  if (c.imagesAfter !== undefined && c.imagesAfter.length > 0) parts.push(`${(c.imagesBefore ?? []).join(', ') || 'none'} → ${c.imagesAfter.join(', ')}`);
  if (c.manager !== undefined && c.manager !== '') parts.push(`by ${c.manager}`);
  if (c.detail !== undefined && c.detail !== '') parts.push(c.detail);
  if (c.namespace !== undefined && c.namespace !== '') parts.push(c.namespace);
  return parts.join(' · ');
}

/** "37 pods", "1 object": the cause's objects, by kind when they are one. */
export function objectsWord(cause: Pick<Cause, 'objects' | 'kinds'>): string {
  const only = cause.kinds.length === 1 ? cause.kinds[0] : undefined;
  const noun = only === undefined ? 'object' : only.toLowerCase();
  return `${String(cause.objects)} ${noun}${cause.objects === 1 ? '' : 's'}`;
}

/** Where on the timeline a time falls, 0 to 100, clamped to the window. */
export function place(time: string, since: string, until: string): number {
  const from = Date.parse(since);
  const span = Date.parse(until) - from;
  if (!Number.isFinite(span) || span <= 0) return 0;
  return Math.max(0, Math.min(100, ((Date.parse(time) - from) / span) * 100));
}

/** A bar's thickness in pixels, growing with the count and bounded. */
export function thickness(count: number): number {
  return Math.round(Math.min(10, 2 + Math.log2(Math.max(1, count))));
}

/** A lane bar's thickness from the density of its Events: the count over its span in minutes. */
export function densityPx(count: number, first: number, last: number): number {
  const minutes = Math.max(1, (last - first) / 60_000);
  return Math.round(Math.min(14, 4 + 3 * Math.log2(1 + count / minutes)));
}

export interface Bar {
  /** The workload the objects belong to, or the object itself. */
  name: string;
  namespace: string;
  /** How many objects the lane stands for. */
  objects: number;
  count: number;
  reason: string;
  type: string;
  /** The cause it belongs to: reason and message, as the list keys it. */
  cause: string;
  /** The changes its groups came after. */
  sinces: string[];
  first: number;
  last: number;
  px: number;
}

/** The most lanes the timeline draws; the busiest, the rest stay in the list. */
export const MAX_BARS = 24;

export function causeKey(c: Pick<Cause, 'reason' | 'message'>): string {
  return `${c.reason}/${c.message}`;
}

/**
 * The timeline's lanes: one per workload (or object) and cause, from the
 * first to the last seen of its groups, earliest first, the busiest kept.
 */
export function bars(causes: readonly Cause[]): { drawn: Bar[]; total: number } {
  const lanes = new Map<string, Bar>();
  for (const cause of causes) {
    const cKey = causeKey(cause);
    for (const group of cause.groups) {
      const name = group.workloadName !== undefined && group.workloadName !== '' ? group.workloadName : group.name;
      const key = `${cKey}\u0000${group.namespace}\u0000${name}`;
      const first = Date.parse(group.firstSeen);
      const last = Math.max(first, Date.parse(group.lastSeen));
      const lane = lanes.get(key);
      if (lane === undefined) {
        lanes.set(key, { name, namespace: group.namespace, objects: 1, count: group.count, reason: cause.reason, type: cause.type, cause: cKey, sinces: group.since === undefined ? [] : [group.since], first, last, px: 0 });
        continue;
      }
      lane.objects += 1;
      lane.count += group.count;
      lane.first = Math.min(lane.first, first);
      lane.last = Math.max(lane.last, last);
      if (group.since !== undefined && !lane.sinces.includes(group.since)) lane.sinces.push(group.since);
    }
  }
  const all = [...lanes.values()];
  for (const b of all) b.px = densityPx(b.count, b.first, b.last);
  const drawn = all.length > MAX_BARS ? [...all].sort((a, b) => b.count - a.count).slice(0, MAX_BARS) : all;
  drawn.sort((a, b) => a.first - b.first || b.count - a.count);
  return { drawn, total: all.length };
}

export interface Axis {
  start: number;
  end: number;
}

/** Share of the span kept clear before the earliest activity. */
const PAD = 0.04;

/**
 * The axis: from the earliest drawn first-seen or change time, a little
 * before it, to now; never before the window's own start.
 */
export function axisOf(drawn: readonly Bar[], changes: readonly Change[], since: string, until: string): Axis {
  const end = Date.parse(until);
  const floor = Date.parse(since);
  let earliest = end;
  for (const b of drawn) earliest = Math.min(earliest, b.first);
  for (const c of changes) earliest = Math.min(earliest, Date.parse(c.time));
  const span = Math.max(60_000, end - earliest);
  return { start: Math.max(floor, earliest - span * PAD), end };
}

/** Where a time falls on the axis, 0 to 100. */
export function spot(ms: number, axis: Axis): number {
  const span = axis.end - axis.start;
  return span <= 0 ? 0 : Math.max(0, Math.min(100, ((ms - axis.start) / span) * 100));
}

/** A tick's text: minutes before now, then hours and minutes. */
export function tickLabel(ms: number, end: number): string {
  const min = Math.round((end - ms) / 60_000);
  if (min <= 0) return 'now';
  if (min < 60) return `−${String(min)} min`;
  const rest = min % 60;
  return `−${String(Math.floor(min / 60))} h${rest === 0 ? '' : ` ${String(rest)} min`}`;
}

/** Five ticks from the axis's start to now. */
export function ticks(axis: Axis): { at: number; label: string }[] {
  return [0, 1, 2, 3, 4].map((i) => {
    const ms = axis.start + ((axis.end - axis.start) * i) / 4;
    return { at: i * 25, label: tickLabel(ms, axis.end) };
  });
}

/** The longest workload name a marker spells out; the title holds the whole one. */
export const MARK_NAME_MAX = 24;

function clip(name: string): string {
  return name.length > MARK_NAME_MAX ? `${name.slice(0, MARK_NAME_MAX - 1)}…` : name;
}

/** A change as a marker's label: "rollout api rev.7", "scale →8", "config checkout-config", "node node-2". */
export function changeShort(c: Change): string {
  const rev = c.revision === undefined || c.revision === 0 ? '' : ` rev.${String(c.revision)}`;
  switch (c.kind) {
    case 'rollout':
    case 'rollback':
      return `${c.kind} ${clip(c.name)}${rev}`;
    case 'config':
      return `config ${c.name}`;
    case 'scaling': {
      const size = /New size: (\d+)/.exec(c.detail ?? '')?.[1];
      return size === undefined ? `scale ${c.name}` : `scale →${size}`;
    }
    case 'node-added':
    case 'node-notready':
      return `node ${c.name}`;
  }
}

/** The change a cause says it came after, when the answer holds it. */
export function changeOf(answer: Pick<Answer, 'changes'>, id: string | undefined): Change | undefined {
  return id === undefined || id === '' ? undefined : answer.changes.find((c) => c.id === id);
}

/** The address of an object's own page, for the kinds the console lists. */
const PLURAL: Readonly<Record<string, string>> = {
  Pod: 'pods',
  Deployment: 'deployments',
  ReplicaSet: 'replicasets',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  Job: 'jobs',
  CronJob: 'cronjobs',
  Node: 'nodes',
  Service: 'services',
  PersistentVolumeClaim: 'persistentvolumeclaims',
};

export function pluralOf(kind: string): string | undefined {
  return PLURAL[kind];
}
