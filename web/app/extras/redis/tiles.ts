import type { TileTone } from '@kubetower/ui';

import { plural } from './parts';
import { roleOf, stateOf, type Deployment, type Door, type Member, type ProbeState, type StateTile } from './data';

/**
 * What one deployment's tile says (the operator's review: one
 * tile per Redis or Valkey deployment, as Settings → Extra draws its
 * extensions). Everything here is derived from the inventory and from the
 * probes the person pressed; nothing is asked of a server to draw a tile.
 *
 * **Each reading says where it came from**, because two of them can be known
 * before any probe and only from a label:
 *
 * - the engine is the server's own `INFO server` once probed, else the chart
 *   the Helm labels name (`valkey-2.1.0`), else unknown - **never a Service's
 *   name**;
 * - the mode is the server's own once probed (a Cluster node), else the chart
 *   (`redis-cluster-…`), else the members' shape (a Sentinel, a replica), else
 *   unknown where one Service fronts several pods nothing labels.
 *
 * Unknown is its own value and its own filter tile, never folded into a count
 * of zero (R5 §5).
 */

export type Said = 'info' | 'chart' | 'members';

export const UNKNOWN = 'unknown';

/** The member the tile opens and probes: the primary when one is said, else
 *  the first Redis that is not a replica or a headless Service. */
export function mainMember(d: Deployment): Member | undefined {
  const redis = d.members.filter((m) => m.sentinel !== true);
  return (
    redis.find((m) => m.role === 'primary' && m.reachable) ??
    redis.find((m) => m.role !== 'headless' && m.role !== 'replica' && m.reachable) ??
    redis.find((m) => m.reachable) ??
    redis[0]
  );
}

/** The door the tile opens: the main member's first. */
export function mainDoor(d: Deployment): Door | undefined {
  return mainMember(d)?.doors[0];
}

/** The chart without its version: `redis` from `redis-19.6.4`. */
export function chartName(chart: string | undefined): string {
  if (chart === undefined) return '';
  return chart.replace(/-\d[^-]*$/, '').toLowerCase();
}

function answered(d: Deployment, probes: Readonly<Record<string, ProbeState>>) {
  return d.members.flatMap((m) => {
    const p = probes[m.key];
    return p?.status === 'answered' ? [p.answer] : [];
  });
}

export interface Reading {
  value: string;
  /** Where it was read; absent for `unknown`. */
  from?: Said;
}

export function engineOfDeployment(d: Deployment, probes: Readonly<Record<string, ProbeState>>): Reading {
  const server = answered(d, probes)[0]?.server.engine;
  if (server !== undefined && server !== '') return { value: server, from: 'info' };
  const chart = chartName(d.chart);
  if (chart.startsWith('valkey')) return { value: 'Valkey', from: 'chart' };
  if (chart.startsWith('redis')) return { value: 'Redis', from: 'chart' };
  return { value: UNKNOWN };
}

export const MODES = ['standalone', 'replication', 'sentinel', 'cluster'] as const;

export function modeOf(d: Deployment, probes: Readonly<Record<string, ProbeState>>): Reading {
  if (answered(d, probes).some((a) => a.server.mode === 'cluster')) return { value: 'cluster', from: 'info' };
  if (chartName(d.chart).endsWith('-cluster')) return { value: 'cluster', from: 'chart' };
  if (d.members.some((m) => m.sentinel === true)) return { value: 'sentinel', from: 'members' };
  const redis = d.members.filter((m) => m.sentinel !== true);
  const roles = redis.map((m) => roleOf(m, probes[m.key]).role);
  if (roles.includes('replica') || answered(d, probes).some((a) => (a.server.replicas ?? 0) > 0)) {
    return { value: 'replication', from: roles.includes('replica') ? 'members' : 'info' };
  }
  const servers = redis.filter((m) => m.role !== 'headless');
  // One Service in front of one pod is a standalone Redis. One Service in front
  // of several, with nothing saying which is which, may be replicas or Cluster
  // nodes: unknown until probed.
  if (servers.length === 1 && servers[0]?.pods.length === 1) return { value: 'standalone', from: 'members' };
  if (servers.length === 0 && redis.length === 1 && redis[0]?.pods.length === 1) return { value: 'standalone', from: 'members' };
  return { value: UNKNOWN };
}

/** How many pods a member stands for: its pods, or one when they were not read. */
const size = (m: Member) => Math.max(1, m.pods.length);

/** The secondary line's shape: "1 primary · 2 replicas", or "6 nodes" for a Cluster. */
export function shapeOf(d: Deployment, probes: Readonly<Record<string, ProbeState>>, mode: string): string {
  const redis = d.members.filter((m) => m.sentinel !== true);
  if (redis.every((m) => !m.reachable)) return 'no pods';
  const servers = redis.filter((m) => roleOf(m, probes[m.key]).role !== 'headless');
  const counted = servers.length === 0 ? redis : servers;
  // Under Sentinel with nothing labeling the roles (bitnami's sentinel
  // chart: one Service in front of every node), any node may be the primary
  // tomorrow and the Service reaches one at random: nodes, not roles (such
  // an install once read "2 primaries").
  const unlabelled = counted.every((m) => m.role === undefined);
  if (mode === 'cluster' || (mode === 'sentinel' && unlabelled)) return plural(counted.reduce((n, m) => n + size(m), 0), 'node');
  let primaries = 0;
  let replicas = 0;
  let other = 0;
  for (const m of counted) {
    const said = roleOf(m, probes[m.key]);
    // The server's own answer is about the one pod the tunnel reached, never
    // about the others behind the same Service.
    const sure = said.from === 'info' ? 1 : size(m);
    if (said.role === 'primary') primaries += sure;
    else if (said.role === 'replica') replicas += sure;
    else other += sure;
    other += size(m) - sure;
  }
  const parts = [
    primaries === 0 ? '' : plural(primaries, 'primary', 'primaries'),
    replicas === 0 ? '' : plural(replicas, 'replica'),
    other === 0 ? '' : plural(other, 'pod'),
  ].filter((p) => p !== '');
  return parts.join(' · ');
}

/** The tile's status: the state of the member it opens. */
export function statusOf(d: Deployment, probes: Readonly<Record<string, ProbeState>>): StateTile {
  const main = mainMember(d);
  if (main === undefined) return 'notserving';
  return stateOf(main, probes[main.key]);
}

export const STATUS_WORD: Readonly<Record<StateTile, string>> = {
  reachable: 'reachable',
  unreachable: 'unreachable',
  password: 'password',
  unhandled: 'not handled',
  notserving: 'not serving',
  unprobed: 'not probed',
};

/** The hover sentence per status: the short word on the pill, the reason here. */
export const STATUS_HINT: Readonly<Record<StateTile, string>> = {
  reachable: 'It answered a probe.',
  unreachable: 'The probe did not reach it.',
  password: 'It wants a password this console could not use.',
  unhandled: 'A Cluster node or a Sentinel: this viewer reads single nodes.',
  notserving: 'No pod behind it, or what is behind it could not be read.',
  unprobed: 'Nothing has been asked of it yet. Probe opens one tunnel.',
};

export const STATUS_TONE: Readonly<Record<StateTile, TileTone | undefined>> = {
  reachable: 'ok',
  unreachable: 'bad',
  password: 'warn',
  unhandled: 'warn',
  notserving: 'off',
  unprobed: undefined,
};

export const PILL_TONE: Readonly<Record<StateTile, string | undefined>> = {
  reachable: 'ok',
  unreachable: 'bad',
  password: 'warn',
  unhandled: 'warn',
  notserving: undefined,
  unprobed: undefined,
};

export const SAID_WORDS: Readonly<Record<Said, string>> = {
  info: 'what the server said when probed',
  chart: 'the chart its Helm labels name',
  members: 'its Services and their roles',
};

// --- the filters: one value per axis, several axes at once ---------------------

export const AXES = ['engine', 'mode', 'state'] as const;
export type Axis = (typeof AXES)[number];
export type Filters = Partial<Record<Axis, string>>;

export const FILTER_PARAMETER = 'show';

/** `?show=engine.Valkey&show=state.reachable`: one per axis, the last wins. */
export function parseFilters(raw: readonly string[]): Filters {
  const out: Filters = {};
  for (const item of raw) {
    const [axis, ...rest] = item.split('.');
    const value = rest.join('.');
    if (value === '' || !(AXES as readonly string[]).includes(axis ?? '')) continue;
    out[axis as Axis] = value;
  }
  return out;
}

export function filterParams(f: Filters): string[] {
  return AXES.flatMap((a) => (f[a] === undefined ? [] : [`${a}.${f[a]}`]));
}

/** What one deployment reads as, on each axis. */
export interface TileReading {
  engine: Reading;
  mode: Reading;
  state: StateTile;
}

export function readingOf(d: Deployment, probes: Readonly<Record<string, ProbeState>>): TileReading {
  return { engine: engineOfDeployment(d, probes), mode: modeOf(d, probes), state: statusOf(d, probes) };
}

export function value(r: TileReading, axis: Axis): string {
  return axis === 'state' ? r.state : r[axis].value;
}

export function passes(r: TileReading, f: Filters): boolean {
  return AXES.every((a) => f[a] === undefined || value(r, a) === f[a]);
}
