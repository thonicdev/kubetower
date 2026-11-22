import { describe, expect, it } from 'vitest';

import type { Deployment, Member, ProbeState } from './data';
import { chartName, engineOfDeployment, mainMember, modeOf, parseFilters, filterParams, shapeOf } from './tiles';

const member = (key: string, pods: number, over: Partial<Member> = {}): Member => ({
  key,
  pods: Array.from({ length: pods }, (_, i) => `${key}-${i}`),
  behind: key,
  ready: pods,
  reachable: pods > 0,
  endpointless: pods === 0,
  workloads: [],
  doors: [{ namespace: 'ns', service: key, port: 6379, why: 'the port is named redis' }],
  ...over,
});
const deployment = (members: Member[], over: Partial<Deployment> = {}): Deployment => ({
  key: 'd',
  name: 'd',
  evidence: [],
  workloads: [],
  members,
  ...over,
});
const none: Record<string, ProbeState> = {};

describe('what a tile reads', () => {
  it('takes the chart without its version', () => {
    expect(chartName('redis-19.6.4')).toBe('redis');
    expect(chartName('redis-cluster-11.0.2')).toBe('redis-cluster');
    expect(chartName(undefined)).toBe('');
  });

  it('says a Cluster by its chart, and counts its nodes', () => {
    const d = deployment([member('c', 6), member('h', 6, { role: 'headless' })], { chart: 'valkey-cluster-1.0.0' });
    expect(modeOf(d, none)).toEqual({ value: 'cluster', from: 'chart' });
    expect(engineOfDeployment(d, none)).toEqual({ value: 'Valkey', from: 'chart' });
    expect(shapeOf(d, none, 'cluster')).toBe('6 nodes');
  });

  it('leaves the mode unknown where one Service fronts several pods nothing labels', () => {
    const d = deployment([member('x', 3)]);
    expect(modeOf(d, none)).toEqual({ value: 'unknown' });
    expect(engineOfDeployment(d, none)).toEqual({ value: 'unknown' });
    expect(shapeOf(d, none, 'unknown')).toBe('3 pods');
  });

  it('opens the primary, not a replica listed first', () => {
    const d = deployment([member('r', 2, { role: 'replica' }), member('p', 1, { role: 'primary' })]);
    expect(mainMember(d)?.key).toBe('p');
    expect(modeOf(d, none)).toEqual({ value: 'replication', from: 'members' });
    expect(shapeOf(d, none, 'replication')).toBe('1 primary · 2 replicas');
  });

  it('counts nodes under Sentinel, and lets a server speak only for its own pod', () => {
    const d = deployment([member('n', 2)], { chart: 'redis-28.3.9' });
    const probed: Record<string, ProbeState> = {
      n: { status: 'answered', answer: { server: { engine: 'Redis', version: '8', mode: 'standalone', role: 'primary', databases: 16 } } as never },
    };
    expect(shapeOf(d, probed, 'sentinel')).toBe('2 nodes');
    expect(shapeOf(d, probed, 'replication')).toBe('1 primary · 1 pod');
  });

  it('keeps one filter per axis in the address, the last one winning', () => {
    const f = parseFilters(['engine.Redis', 'state.reachable', 'engine.Valkey', 'nonsense', 'mode.']);
    expect(f).toEqual({ engine: 'Valkey', state: 'reachable' });
    expect(filterParams(f)).toEqual(['engine.Valkey', 'state.reachable']);
  });
});
