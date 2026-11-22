import { describe, expect, it } from 'vitest';

import type { Reason } from './data';
import { leadReason, orderedReasons } from './reasons';

const r = (category: Reason['category'], text: string, nodes = 1): Reason => ({ category, text, nodes });
const CP = r('taints', 'node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }');

describe('the reason a row leads with', () => {
  it('puts the control-plane taint after the reason that blocks the pod', () => {
    const mem = r('resources', 'Insufficient memory', 3);
    expect(leadReason([CP, mem])).toBe(mem);
    expect(orderedReasons([CP, mem])).toEqual([mem, CP]);
  });

  it('orders resources, selector, affinity, spread, volumes, then the rest', () => {
    const list = [CP, r('volumes', 'v'), r('spread', 's'), r('other', 'o'), r('podAffinity', 'p'), r('nodeAffinity', 'n'), r('resources', 'r')];
    expect(orderedReasons(list).map((x) => x.category)).toEqual(['resources', 'nodeAffinity', 'podAffinity', 'spread', 'volumes', 'other', 'taints']);
  });

  it('keeps a taint that is the only reason, and another taint in place', () => {
    expect(leadReason([CP])).toBe(CP);
    const mine = r('taints', 'node(s) had untolerated taint {gpu: true}');
    expect(orderedReasons([mine, CP])).toEqual([mine, CP]);
    expect(leadReason(null)).toBeUndefined();
  });
});
