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

export function causeKey(c: Pick<Cause, 'reason' | 'message'>): string {
  return `${c.reason}/${c.message}`;
}

/**
 * The causes on screen: Normal ones only once asked for, unless the Type
 * filter already chose a type.
 */
export function shownTypes(causes: readonly Cause[], filters: Pick<Filters, 'type'>, normal: boolean): Cause[] {
  return filters.type !== '' || normal ? [...causes] : causes.filter((c) => c.type !== 'Normal');
}

/** The Events of the Normal causes, for the toggle's figure. */
export function normalCount(causes: readonly Cause[]): number {
  return causes.reduce((n, c) => (c.type === 'Normal' ? n + c.count : n), 0);
}

export interface Axis {
  start: number;
  end: number;
}

/** Share of the span kept clear before the earliest activity. */
const PAD = 0.04;

/**
 * The axis: from the earliest first-seen of the causes drawn or change time, a
 * little before it, to now; never before the window's own start.
 */
export function axisOf(causes: readonly Cause[], changes: readonly Change[], since: string, until: string): Axis {
  const end = Date.parse(until);
  const floor = Date.parse(since);
  let earliest = end;
  for (const c of causes) for (const g of c.groups) earliest = Math.min(earliest, Date.parse(g.firstSeen));
  for (const c of changes) earliest = Math.min(earliest, Date.parse(c.time));
  const span = Math.max(60_000, end - earliest);
  return { start: Math.max(floor, earliest - span * PAD), end };
}

/** The cells each row of the timeline is cut into. */
export const BINS = 40;

/** The cell a time falls in, clamped to the axis. */
export function binOf(ms: number, axis: Axis): number {
  const span = axis.end - axis.start;
  if (span <= 0) return 0;
  return Math.max(0, Math.min(BINS - 1, Math.floor(((ms - axis.start) / span) * BINS)));
}

/**
 * Adds `count` Events to `bins`, spread evenly over the cells from `first` to
 * `last`: the answer holds an object's first and last Event and their count,
 * never the time of each one.
 */
export function spread(bins: number[], first: number, last: number, count: number, axis: Axis): void {
  const a = binOf(first, axis);
  const b = binOf(Math.max(first, last), axis);
  const per = count / (b - a + 1);
  for (let i = a; i <= b; i++) bins[i] = (bins[i] ?? 0) + per;
}

/** What one cell stands for: "−12 to −10 min". */
export function binSpan(i: number, axis: Axis): string {
  const step = (axis.end - axis.start) / BINS;
  const from = tickLabel(axis.start + step * i, axis.end);
  const to = tickLabel(axis.start + step * (i + 1), axis.end);
  return `${from} to ${to.replace(/^−/, '')}`;
}

/** One reason of one workload: a row of the timeline. */
export interface Lane {
  /** `<namespace>/<workload>/<reason>/<type>`. */
  key: string;
  workload: string;
  namespace: string;
  reason: string;
  type: string;
  count: number;
  /** The objects it stands for. */
  objects: number;
  bins: number[];
  /** The changes its groups came after. */
  sinces: Set<string>;
  /** The causes it is part of, as the list keys them. */
  causes: Set<string>;
  first: number;
  last: number;
}

/** The workload a group belongs to: its own, the nodes for a Node, else the object. */
export function workloadOf(g: Pick<ObjectGroup, 'kind' | 'name' | 'workloadName'>): string {
  if (g.workloadName !== undefined && g.workloadName !== '') return g.workloadName;
  return g.kind === 'Node' ? 'nodes' : g.name;
}

/** The lanes: one per workload, reason and type, binned over the axis, busiest first. */
export function lanesOf(causes: readonly Cause[], axis: Axis): Lane[] {
  const lanes = new Map<string, Lane>();
  for (const c of causes) {
    const cKey = causeKey(c);
    for (const g of c.groups) {
      const workload = workloadOf(g);
      const key = `${g.namespace}/${workload}/${c.reason}/${c.type}`;
      let lane = lanes.get(key);
      if (lane === undefined) {
        lane = { key, workload, namespace: g.namespace, reason: c.reason, type: c.type, count: 0, objects: 0, bins: Array.from({ length: BINS }, () => 0), sinces: new Set(), causes: new Set(), first: Infinity, last: -Infinity };
        lanes.set(key, lane);
      }
      const first = Date.parse(g.firstSeen);
      const last = Math.max(first, Date.parse(g.lastSeen));
      lane.count += g.count;
      lane.objects += 1;
      lane.first = Math.min(lane.first, first);
      lane.last = Math.max(lane.last, last);
      lane.causes.add(cKey);
      if (g.since !== undefined && g.since !== '') lane.sinces.add(g.since);
      spread(lane.bins, first, last, g.count, axis);
    }
  }
  return [...lanes.values()].sort((a, b) => b.count - a.count || a.first - b.first || (a.key < b.key ? -1 : 1));
}

/** A workload's rows: its lanes, and their sum. */
export interface WorkloadRow {
  /** `<namespace>/<workload>`. */
  key: string;
  name: string;
  namespace: string;
  lanes: Lane[];
  count: number;
  bins: number[];
  /** Warning when any lane is. */
  type: string;
}

/** The lanes gathered per workload, busiest first. */
export function workloadsOf(lanes: readonly Lane[]): WorkloadRow[] {
  const out = new Map<string, WorkloadRow>();
  for (const l of lanes) {
    const key = `${l.namespace}/${l.workload}`;
    let w = out.get(key);
    if (w === undefined) {
      w = { key, name: l.workload, namespace: l.namespace, lanes: [], count: 0, bins: Array.from({ length: BINS }, () => 0), type: l.type };
      out.set(key, w);
    }
    w.lanes.push(l);
    w.count += l.count;
    if (l.type === 'Warning') w.type = 'Warning';
    l.bins.forEach((v, i) => (w.bins[i] = (w.bins[i] ?? 0) + v));
  }
  return [...out.values()].sort((a, b) => b.count - a.count || (a.key < b.key ? -1 : 1));
}

/** The most workloads the timeline draws at first, and how many more each press adds. */
export const PAGE_WORKLOADS = 24;

/** How long between two node changes of one kind before they stop being one marker. */
export const NODE_GAP_MS = 5 * 60_000;

export type NodeKind = 'node-added' | 'node-notready';

/** Node changes of one kind, each within NODE_GAP_MS of the one before. */
export interface NodeGroup {
  id: string;
  kind: NodeKind;
  changes: Change[];
  first: number;
  last: number;
}

const isNode = (c: Change): c is Change & { kind: NodeKind } => c.kind === 'node-added' || c.kind === 'node-notready';

/** The node changes in groups, earliest first; every other change as it is. */
export function groupNodes(changes: readonly Change[], gapMs = NODE_GAP_MS): { groups: NodeGroup[]; others: Change[] } {
  const others = changes.filter((c) => !isNode(c));
  const nodes = changes.filter(isNode).sort((a, b) => Date.parse(a.time) - Date.parse(b.time) || (a.id < b.id ? -1 : 1));
  const groups: NodeGroup[] = [];
  const open = new Map<NodeKind, NodeGroup>();
  for (const c of nodes) {
    const t = Date.parse(c.time);
    const g = open.get(c.kind);
    if (g !== undefined && t - g.last <= gapMs) {
      g.changes.push(c);
      g.last = t;
      continue;
    }
    const next: NodeGroup = { id: `nodes/${c.id}`, kind: c.kind, changes: [c], first: t, last: t };
    groups.push(next);
    open.set(c.kind, next);
  }
  return { groups, others };
}

/** A node group in a few words: "+17 nodes", "node ip-10-0-1-5 not ready". */
export function nodeGroupLabel(g: NodeGroup): string {
  if (g.changes.length === 1) return changeLabel(g.changes[0] as Change);
  return `${String(g.changes.length)} nodes ${g.kind === 'node-added' ? 'added' : 'not ready'}`;
}

/** A change and the Warnings that came after it, as the cards above the timeline say it. */
export interface Correlation {
  /** The change's ID, or its node group's. */
  id: string;
  label: string;
  kind: Change['kind'];
  /** Every change it stands for: one, or a node group's. */
  changes: string[];
  reasons: { reason: string; count: number }[];
  objects: number;
  count: number;
}

/**
 * The Warning groups that came after a change, gathered per change (a node
 * group's changes as one), most Events first.
 */
export function correlations(causes: readonly Cause[], changes: readonly Change[], groups: readonly NodeGroup[]): Correlation[] {
  const groupOf = new Map<string, NodeGroup>();
  for (const g of groups) for (const c of g.changes) groupOf.set(c.id, g);
  const byId = new Map(changes.map((c) => [c.id, c]));
  const out = new Map<string, { c: Correlation; reasons: Map<string, number>; objects: Set<string> }>();
  for (const cause of causes) {
    if (cause.type !== 'Warning') continue;
    for (const g of cause.groups) {
      const change = g.since === undefined ? undefined : byId.get(g.since);
      if (change === undefined) continue;
      const ng = groupOf.get(change.id);
      const id = ng?.id ?? change.id;
      let entry = out.get(id);
      if (entry === undefined) {
        entry = {
          c: { id, label: ng === undefined ? changeLabel(change) : nodeGroupLabel(ng), kind: change.kind, changes: ng?.changes.map((x) => x.id) ?? [change.id], reasons: [], objects: 0, count: 0 },
          reasons: new Map(),
          objects: new Set(),
        };
        out.set(id, entry);
      }
      entry.c.count += g.count;
      entry.objects.add(`${g.kind}/${g.namespace}/${g.name}`);
      entry.reasons.set(cause.reason, (entry.reasons.get(cause.reason) ?? 0) + g.count);
    }
  }
  return [...out.values()]
    .map(({ c, reasons, objects }) => ({
      ...c,
      objects: objects.size,
      reasons: [...reasons].map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count || (a.reason < b.reason ? -1 : 1)),
    }))
    .sort((a, b) => b.count - a.count || (a.id < b.id ? -1 : 1));
}

/**
 * The Warnings the lists hold against those the tile counted, when the answer
 * was cut: undefined when it was not, or when the tile has no figure.
 */
export function heldOf(answer: Pick<Answer, 'truncated' | 'causes' | 'tiles'>): { held: number; total: number } | undefined {
  if (!answer.truncated || answer.tiles.warnings.state !== 'read') return undefined;
  const held = answer.causes.reduce((n, c) => (c.type === 'Warning' ? n + c.count : n), 0);
  return { held, total: Math.max(held, answer.tiles.warnings.count) };
}

/** What is lit: a change, a cause, or the changes of a correlation. Nothing lit means nothing dims. */
export type Focus = { kind: 'change' | 'cause'; id: string } | { kind: 'correlation'; id: string; ids: readonly string[] } | null;

/** Whether a change the focus names: itself, one of a correlation's, or the change a focused cause came after. */
export function changeLit(focus: Focus, id: string, causes: readonly Cause[]): boolean {
  if (focus === null) return false;
  if (focus.kind === 'change') return focus.id === id;
  if (focus.kind === 'correlation') return focus.ids.includes(id);
  return causes.find((c) => causeKey(c) === focus.id)?.since === id;
}

/** Whether a lane is lit: it is of the focused cause, or came after a focused change. */
export function laneLit(focus: Focus, lane: Pick<Lane, 'sinces' | 'causes'>): boolean {
  if (focus === null) return false;
  if (focus.kind === 'cause') return lane.causes.has(focus.id);
  const ids = 'ids' in focus ? focus.ids : [focus.id];
  for (const s of lane.sinces) if (ids.includes(s)) return true;
  return false;
}

/** Whether a cause of the list is lit, or only not dimmed: itself, or after the focused change. */
export function causeLit(focus: Focus, cause: Cause): boolean {
  if (focus === null) return false;
  if (focus.kind === 'cause') return focus.id === causeKey(cause);
  const ids = 'ids' in focus ? focus.ids : [focus.id];
  return (cause.since !== undefined && ids.includes(cause.since)) || cause.groups.some((g) => g.since !== undefined && ids.includes(g.since));
}

/** "1 650", "2 012": thousands apart with a space. */
export function figure(n: number): string {
  return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
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
