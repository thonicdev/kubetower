import { describe, expect, it } from 'vitest';

import type { Cause, Change, ObjectGroup } from './data';
import {
  BINS,
  NODE_GAP_MS,
  binOf,
  binSpan,
  causeKey,
  causeLit,
  changeLit,
  correlations,
  figure,
  groupNodes,
  heldOf,
  laneLit,
  lanesOf,
  maxOf,
  nodeTracks,
  normalCount,
  shownTypes,
  spread,
  workloadsOf,
  type Axis,
} from './model';

/** The timeline's pure logic (#86): node groups, cells, the Normal toggle, workloads, correlations. */

const NOW = Date.parse('2026-11-15T12:00:00Z');
const MIN = 60_000;
const at = (minutesAgo: number): string => new Date(NOW - minutesAgo * MIN).toISOString();
const AXIS: Axis = { start: NOW - 40 * MIN, end: NOW };

const node = (name: string, minutesAgo: number, kind: Change['kind'] = 'node-added'): Change => ({ id: `${kind}//${name}/0`, kind, objectKind: 'Node', name, time: at(minutesAgo), referenced: true });

function group(name: string, over: Partial<ObjectGroup> = {}): ObjectGroup {
  return { kind: 'Pod', namespace: 'kube-system', name, count: 10, firstSeen: at(20), lastSeen: at(0), message: 'm', ...over };
}

function cause(reason: string, type: string, groups: ObjectGroup[], over: Partial<Cause> = {}): Cause {
  return { reason, message: `${reason} m`, type, count: groups.reduce((n, g) => n + g.count, 0), objects: groups.length, kinds: ['Pod'], firstSeen: at(20), lastSeen: at(0), examples: [], groups, ...over };
}

describe('groupNodes', () => {
  it('collapses node changes each within the gap of the one before, and keeps a lone node alone', () => {
    const changes = [node('n1', 38), node('n2', 35), node('n3', 31), node('n4', 27), node('far', 10), node('n2', 6, 'node-notready')];
    const { groups, others } = groupNodes(changes);
    expect(others).toEqual([]);
    expect(groups.map((g) => [g.kind, g.changes.map((c) => c.name)])).toEqual([
      ['node-added', ['n1', 'n2', 'n3', 'n4']],
      ['node-added', ['far']],
      ['node-notready', ['n2']],
    ]);
  });

  it('measures the gap from the previous node, not the first, and splits just past it', () => {
    const t = (ms: number): Change => ({ ...node(`n${String(ms)}`, 0), time: new Date(NOW + ms).toISOString() });
    const { groups } = groupNodes([t(0), t(NODE_GAP_MS), t(2 * NODE_GAP_MS), t(3 * NODE_GAP_MS + 1)]);
    expect(groups.map((g) => g.changes.length)).toEqual([3, 1]);
  });

  it('puts groups that share a cell on tracks of their own, one track when none do', () => {
    const { groups } = groupNodes([node('a', 30), node('b', 30, 'node-notready'), node('c', 10)]);
    const { track, tracks } = nodeTracks(groups, AXIS);
    expect(tracks).toBe(2);
    expect(groups.map((g) => track.get(g.id))).toEqual([0, 1, 0]);
    expect(nodeTracks(groupNodes([node('a', 30), node('c', 10)]).groups, AXIS).tracks).toBe(1);
  });

  it('never mixes added and not-ready nodes, and leaves the other changes as they are', () => {
    const rollout: Change = { id: 'rollout/shop/api/7', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'api', time: at(30), referenced: true };
    const { groups, others } = groupNodes([node('a', 30), node('b', 29, 'node-notready'), node('c', 28), rollout]);
    expect(groups.map((g) => `${g.kind}:${String(g.changes.length)}`)).toEqual(['node-added:2', 'node-notready:1']);
    expect(others).toEqual([rollout]);
  });
});

describe('cells', () => {
  it('spreads a count evenly from first to last, and keeps the total', () => {
    const bins = Array.from({ length: BINS }, () => 0);
    // 40 cells over 40 min: one per minute.
    spread(bins, NOW - 10 * MIN, NOW - 5 * MIN, 50, AXIS);
    expect(bins.slice(30, 35).map((v) => Math.round(v * 1000) / 1000)).toEqual([10, 10, 10, 10, 10]);
    expect(bins[29]).toBe(0);
    expect(bins[35]).toBe(0);
    expect(bins.reduce((a, b) => a + b, 0)).toBeCloseTo(50);
  });

  it('keeps only the share inside the axis of an object that began long before it', () => {
    // 480 BackOff over 48 h, last one now, on a 1 h axis: 10 in the hour, 0.25 a cell.
    const hour: Axis = { start: NOW - 60 * MIN, end: NOW };
    const bins = Array.from({ length: BINS }, () => 0);
    spread(bins, NOW - 48 * 60 * MIN, NOW, 480, hour);
    for (const v of bins) expect(v).toBeCloseTo(0.25);
    expect(bins.reduce((a, b) => a + b, 0)).toBeCloseTo(10);
    // Wholly before the axis: nothing.
    const none = Array.from({ length: BINS }, () => 0);
    spread(none, NOW - 5 * 60 * MIN, NOW - 2 * 60 * MIN, 99, hour);
    expect(none.every((v) => v === 0)).toBe(true);
  });

  it('puts an Event dated past the axis’s end in the last cell, whole', () => {
    const point = Array.from({ length: BINS }, () => 0);
    spread(point, NOW + 2 * MIN, NOW + 2 * MIN, 1, AXIS);
    expect(point[BINS - 1]).toBe(1);
    expect(point.reduce((a, b) => a + b, 0)).toBe(1);
    const across = Array.from({ length: BINS }, () => 0);
    spread(across, NOW - 2 * MIN, NOW + 3 * MIN, 10, AXIS);
    expect(across.reduce((a, b) => a + b, 0)).toBeCloseTo(10);
    expect(across.slice(0, BINS - 2).every((v) => v === 0)).toBe(true);
  });

  it('takes the largest of a hundred thousand cells without spreading them as arguments', () => {
    const many = Array.from({ length: 200_000 }, (_, i) => i % 97);
    expect(maxOf(many, 1)).toBe(96);
    expect(maxOf([], 1)).toBe(1);
  });

  it('names the slice a cell covers, from its start to its end before now', () => {
    expect(binSpan(0, AXIS)).toBe('−40 min to −39 min');
    expect(binSpan(BINS - 1, AXIS)).toBe('−1 min to now');
    // Cells under a minute: to the second.
    const short: Axis = { start: NOW - 10 * MIN, end: NOW };
    expect(binSpan(1, short)).toBe('−9 min 45 s to −9 min 30 s');
    expect(binSpan(BINS - 1, short)).toBe('−15 s to now');
  });

  it('puts a single Event in one cell, and clamps what falls outside the axis', () => {
    expect(binOf(NOW - 39.5 * MIN, AXIS)).toBe(0);
    expect(binOf(NOW - 90 * MIN, AXIS)).toBe(0);
    expect(binOf(NOW, AXIS)).toBe(BINS - 1);
    const bins = Array.from({ length: BINS }, () => 0);
    spread(bins, NOW - 5 * MIN, NOW - 5 * MIN, 3, AXIS);
    expect(bins.filter((v) => v > 0)).toEqual([3]);
  });
});

describe('the Normal toggle', () => {
  const warn = cause('BackOff', 'Warning', [group('a')]);
  const pulled = cause('Pulled', 'Normal', [group('a', { count: 4 })]);

  it('hides Normal causes until asked for, and counts their Events', () => {
    expect(shownTypes([warn, pulled], { type: '' }, false)).toEqual([warn]);
    expect(shownTypes([warn, pulled], { type: '' }, true)).toEqual([warn, pulled]);
    expect(normalCount([warn, pulled])).toBe(4);
  });

  it('leaves the choice to the Type filter once it chose', () => {
    expect(shownTypes([pulled], { type: 'Normal' }, false)).toEqual([pulled]);
  });
});

describe('workloads', () => {
  it('gathers a workload’s pods into one lane per reason, and its lanes into one row', () => {
    const backoff = cause('BackOff', 'Warning', [
      group('cilium-a', { workloadKind: 'DaemonSet', workloadName: 'cilium', count: 30 }),
      group('cilium-b', { workloadKind: 'DaemonSet', workloadName: 'cilium', count: 20 }),
      group('api-1', { namespace: 'shop' }),
    ]);
    const unhealthy = cause('Unhealthy', 'Warning', [group('cilium-a', { workloadKind: 'DaemonSet', workloadName: 'cilium', count: 5 })]);
    const ready = cause('NodeReady', 'Normal', [group('n1', { kind: 'Node', namespace: '' }), group('n2', { kind: 'Node', namespace: '' })]);
    const lanes = lanesOf([backoff, unhealthy, ready], AXIS);
    expect(lanes.map((l) => `${l.workload}/${l.reason}:${String(l.count)}/${String(l.objects)}`)).toEqual(['cilium/BackOff:50/2', 'nodes/NodeReady:20/2', 'api-1/BackOff:10/1', 'cilium/Unhealthy:5/1']);
    const rows = workloadsOf(lanes);
    expect(rows.map((w) => `${w.key}:${String(w.count)}:${w.lanes.map((l) => l.reason).join('+')}:${w.type}`)).toEqual([
      'kube-system/cilium:55:BackOff+Unhealthy:Warning',
      '/nodes:20:NodeReady:Normal',
      'shop/api-1:10:BackOff:Warning',
    ]);
    expect(rows[0]?.bins.reduce((a, b) => a + b, 0)).toBeCloseTo(55);
  });
});

describe('correlations', () => {
  const nodes = [node('n1', 38), node('n2', 35), node('n3', 31)];
  const rollout: Change = { id: 'rollout/shop/api/7', kind: 'rollout', objectKind: 'Deployment', namespace: 'shop', name: 'api', time: at(30), revision: 7, referenced: true };
  const backoff = cause('BackOff', 'Warning', [
    group('cilium-a', { count: 100, since: nodes[0]!.id }),
    group('cilium-b', { count: 60, since: nodes[2]!.id }),
    group('api-1', { namespace: 'shop', count: 7, since: rollout.id }),
    group('old', { count: 999 }),
  ]);
  const unhealthy = cause('Unhealthy', 'Warning', [group('cilium-a', { count: 12, since: nodes[1]!.id })]);
  const scheduled = cause('Scheduled', 'Normal', [group('cilium-c', { count: 1, since: nodes[0]!.id })]);

  it('gathers a node group’s changes into one line, Warnings only, most Events first', () => {
    const changes = [...nodes, rollout];
    const list = correlations([backoff, unhealthy, scheduled], changes, groupNodes(changes).groups);
    expect(list.map((c) => ({ label: c.label, reasons: c.reasons, objects: c.objects, changes: c.changes.length }))).toEqual([
      { label: '3 nodes added', reasons: [{ reason: 'BackOff', count: 160, objects: 2 }, { reason: 'Unhealthy', count: 12, objects: 1 }], objects: 2, changes: 3 },
      { label: 'rollout api rev. 7', reasons: [{ reason: 'BackOff', count: 7, objects: 1 }], objects: 1, changes: 1 },
    ]);
  });

  it('lights the lanes, the changes and the causes of a correlation, and nothing else', () => {
    const changes = [...nodes, rollout];
    const corr = correlations([backoff, unhealthy], changes, groupNodes(changes).groups)[0]!;
    const focus = { kind: 'correlation' as const, id: corr.id, ids: corr.changes };
    const lanes = lanesOf([backoff, unhealthy], AXIS);
    expect(lanes.filter((l) => laneLit(focus, l)).map((l) => `${l.workload}/${l.reason}`)).toEqual(['cilium-a/BackOff', 'cilium-b/BackOff', 'cilium-a/Unhealthy']);
    expect(changes.filter((c) => changeLit(focus, c.id, [])).map((c) => c.name)).toEqual(['n1', 'n2', 'n3']);
    expect(causeLit(focus, unhealthy)).toBe(true);
    expect(causeLit(focus, cause('FailedMount', 'Warning', [group('x', { since: rollout.id })]))).toBe(false);
    // A change and a cause light as they did.
    expect(changeLit({ kind: 'change', id: rollout.id }, rollout.id, [])).toBe(true);
    expect(changeLit({ kind: 'cause', id: causeKey(backoff) }, rollout.id, [{ ...backoff, since: rollout.id }])).toBe(true);
    expect(laneLit(null, lanes[0]!)).toBe(false);
  });
});

describe('the count in place of truncated', () => {
  it('says the Warnings the lists hold against those counted, only for a cut answer', () => {
    // The cause counts past the groups the server kept: the figure is the groups'.
    const causes = [cause('BackOff', 'Warning', [group('a', { count: 2012 })], { count: 2058 }), cause('Pulled', 'Normal', [group('a', { count: 238 })])];
    const tiles = { warnings: { count: 2058, state: 'read' as const } };
    expect(heldOf({ truncated: true, causes, tiles: tiles as never })).toEqual({ held: 2012, total: 2058 });
    expect(heldOf({ truncated: false, causes, tiles: tiles as never })).toBeUndefined();
    expect(heldOf({ truncated: true, causes, tiles: { warnings: { count: 0, state: 'denied' } } as never })).toBeUndefined();
    // A cut answer whose lists still hold every Warning counted says nothing.
    expect(heldOf({ truncated: true, causes, tiles: { warnings: { count: 2012, state: 'read' } } as never })).toBeUndefined();
    expect(heldOf({ truncated: true, causes, tiles: { warnings: { count: 340, state: 'read' } } as never })).toBeUndefined();
    expect(figure(2012)).toBe('2 012');
    expect(figure(999)).toBe('999');
  });
});
