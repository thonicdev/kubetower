import type { UseQueryResult } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';

import type { ValueResult } from '../api';
import type { OverviewPage } from '../pages/overview/data';
import { cpuHistory, historyFailure, podCount, readOf, usage, type OverviewRead } from './fleet';

function page(capacity: OverviewPage['capacity'], rest: Partial<OverviewPage> = {}): OverviewRead {
  return {
    kind: 'ok',
    page: {
      cluster: 'c',
      version: undefined,
      nodesSummary: undefined,
      nodesTone: undefined,
      workloadsSummary: undefined,
      counts: undefined,
      capacity,
      top: undefined,
      events: undefined,
      eventsTotal: undefined,
      alerts: undefined,
      trouble: undefined,
      restarting: undefined,
      restartingTotal: undefined,
      phases: undefined,
      history: undefined,
      notShown: [],
      accessNote: undefined,
      ...rest,
    },
    stale: undefined,
  };
}

describe('the pods count', () => {
  it('reads pods from the counts panel', () => {
    const read = page(undefined, {
      counts: { state: 'ok', items: [{ kind: 'pods', label: 'Pods', count: 64, note: '2 not running' }] },
    });
    if (read.kind !== 'ok') throw new Error('unreachable');
    expect(podCount(read.page)).toEqual({ count: 64, note: '2 not running' });
  });

  it('says nothing where the panel failed, rather than zero', () => {
    const read = page(undefined, {
      counts: { state: 'unreachable', reason: { code: 'cluster_unreachable', message: 'no' } },
    });
    if (read.kind !== 'ok') throw new Error('unreachable');
    expect(podCount(read.page)).toBe('failed');
  });

  it('draws nothing, not a failure, where the panel answered with no pods entry', () => {
    const read = page(undefined, { counts: { state: 'ok', items: [{ kind: 'jobs', label: 'Jobs', count: 2 }] } });
    if (read.kind !== 'ok') throw new Error('unreachable');
    expect(podCount(read.page)).toBeUndefined();
  });
});

describe('usage', () => {
  function cell(label: string, used: [number | null, string], whole: [number, string]): OverviewPage['capacity'] {
    return {
      state: 'ok',
      items: [
        {
          label,
          requested: { value: 0, trustworthy: true },
          capacity: { value: whole[0], trustworthy: true, text: whole[1] },
          used: { value: used[0], trustworthy: used[0] !== null, text: used[1] },
        },
      ],
    };
  }
  const of = (capacity: OverviewPage['capacity'], label: string) => {
    const read = page(capacity);
    if (read.kind !== 'ok') throw new Error('unreachable');
    return usage(read.page, label);
  };

  // The figures below are spelt as the server spells them: `resources.CPUSize`
  // writes `%.1f cores`, `MemorySize` `16.0 GiB` (internal/resources/format.go),
  // and valued as the wire carries them: millicores and bytes.
  it('says CPU as used over allocatable in cores: 10/40, never 10/40.0', () => {
    expect(of(cell('Memory', [Math.round(97.8 * 2 ** 30), '97.8 GiB'], [Math.round(188.1 * 2 ** 30), '188.1 GiB']), 'Memory')).toMatchObject({
      used: '97.8',
      whole: '188.1',
    });
    expect(of(cell('Memory', [Math.round(150.4 * 2 ** 30), '150.4 GiB'], [Math.round(188.1 * 2 ** 30), '188.1 GiB']), 'Memory')).toMatchObject({
      used: '150.4',
      whole: '188.1',
    });
    expect(of(cell('CPU', [10_000, '10.0 cores'], [40_000, '40.0 cores']), 'CPU')).toEqual({
      used: '10',
      whole: '40',
      unit: 'cores',
      percent: 25,
    });
  });

  it('puts both halves in the unit of the whole, whatever the server wrote for the used one', () => {
    // 512 MiB of 16 GiB is 0.5/16 GiB, never 512/16.
    expect(of(cell('Memory', [512 * 2 ** 20, '512 MiB'], [16 * 2 ** 30, '16.0 GiB']), 'Memory')).toMatchObject({
      used: '0.5',
      whole: '16',
      unit: 'GiB',
    });
    // A small cluster's allocatable in millicores is said in cores.
    expect(of(cell('CPU', [250, '250m'], [500, '500m']), 'CPU')).toMatchObject({ used: '0.25', whole: '0.5', unit: 'cores' });
  });

  it('reads the exact values, not the rounded text, so it agrees with the server', () => {
    // 1930m prints `1.9 cores`, and rescaling from that text would read 0.98
    // where resources.Fraction reads 1 for the same cluster.
    expect(of(cell('CPU', [1000, '1.0 cores'], [1930, '1.9 cores']), 'CPU')).toMatchObject({
      used: '1',
      whole: '1.9',
      unit: 'cores',
    });
  });

  it('invents no figure: more than allocatable is said as measured, and a trickle is not zero', () => {
    expect(of(cell('CPU', [42_000, '42.0 cores'], [40_000, '40.0 cores']), 'CPU')).toMatchObject({
      used: '42',
      whole: '40',
      percent: 100,
    });
    expect(of(cell('CPU', [4, '4m'], [40_000, '40.0 cores']), 'CPU')).toMatchObject({ used: '<0.01', whole: '40' });
  });

  it('is nothing at all where usage was not measured, so no tile is drawn', () => {
    expect(of(cell('CPU', [null, 'metrics-server is not installed'], [4000, '4 cores']), 'CPU')).toBeUndefined();
    expect(of(cell('CPU', [0, '0'], [0, '0 cores']), 'CPU')).toBeUndefined();
    expect(of(undefined, 'CPU')).toBeUndefined();
  });
});

describe('readOf', () => {
  const held = page(undefined);
  if (held.kind !== 'ok') throw new Error('unreachable');
  const query = (shape: Partial<UseQueryResult<ValueResult<OverviewPage>>>) =>
    shape as unknown as UseQueryResult<ValueResult<OverviewPage>>;

  it('keeps the figures of a refetch that failed over them, and says it is stale', () => {
    const read = readOf(query({ data: { state: 'ok', data: held.page }, isError: true, error: new Error('socket hang up') }));
    expect(read).toMatchObject({ kind: 'ok', stale: 'socket hang up' });
  });

  it('is fresh while the last poll answered', () => {
    expect(readOf(query({ data: { state: 'ok', data: held.page }, isError: false }))).toMatchObject({
      kind: 'ok',
      stale: undefined,
    });
  });

  it('tells a read that never answered from one still in flight, and gives no empty reason', () => {
    expect(readOf(query({ data: undefined, isError: true, error: new Error('') }))).toEqual({ kind: 'failed', why: undefined });
    expect(readOf(query({ data: undefined, isError: false }))).toEqual({ kind: 'waiting' });
  });
});

describe('the CPU line', () => {
  const series = (unit: string | undefined, values: (number | null)[]) => ({
    key: 'cpu-used',
    label: 'CPU used',
    ...(unit === undefined ? {} : { unit }),
    data: { startedAt: '2026-10-21T10:00:00Z', stepSeconds: 60, values, trustworthy: true },
  });

  it("converts the server's millicores to cores, and takes a line that says cores at its word", () => {
    const millis = page(undefined, { history: { state: 'ok', data: { series: [series(undefined, [1500, null])] } } });
    const cores = page(undefined, { history: { state: 'ok', data: { series: [series('cores', [1.5])] } } });
    if (millis.kind !== 'ok' || cores.kind !== 'ok') throw new Error('unreachable');
    expect(cpuHistory(millis.page)?.series[0]).toMatchObject({ unit: 'cores', data: { values: [1.5, null] } });
    expect(cpuHistory(cores.page)?.series[0]?.data.values).toEqual([1.5]);
  });

  it('tells a history that failed from a cluster with no series source', () => {
    const failed = page(undefined, {
      history: { state: 'unreadable', reason: { code: 'timeout', message: 'Prometheus timed out' } },
    });
    const none = page(undefined, {
      history: { state: 'absent', reason: { code: 'not_installed', message: 'no Prometheus' } },
    });
    if (failed.kind !== 'ok' || none.kind !== 'ok') throw new Error('unreachable');
    expect(historyFailure(failed.page)).toBe('Prometheus timed out');
    expect(historyFailure(none.page)).toBeUndefined();
  });
});
