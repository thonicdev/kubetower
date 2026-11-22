import { describe, expect, it } from 'vitest';

import type { Direction, PeerView, RuleView } from './data';
import { arrowsOf, filterFlows, flowsOf, peerName, peerText, portsLabel, unionPorts } from './flows';

const pods = (label: string, namespaces: string[], n = 2): PeerView => ({ kind: 'pods', label, pods: n, namespaces });
const cidr = (label: string): PeerView => ({ kind: 'cidr', label, pods: 0, namespaces: [] });

function direction(rules: RuleView[]): Direction {
  return { isolated: true, policies: [{ namespace: 'shop', name: 'p' }], rules };
}
function rule(i: number, peers: PeerView[], ports: string[], kind: RuleView['kind'] = 'allow'): RuleView {
  return { namespace: 'shop', name: `p${String(i)}`, kind, rule: i, text: '', peers, ports };
}

describe('portsLabel', () => {
  it('writes the protocol before one or two ports', () => {
    expect(portsLabel(['80/TCP'])).toBe('TCP 80');
    expect(portsLabel(['80/TCP', '443/TCP'])).toBe('TCP 80, 443');
  });
  it('drops the protocol and counts the rest past two ports', () => {
    expect(portsLabel(['80/TCP', '443/TCP', '8080/TCP', '8443/TCP'])).toBe('80, 443 +2');
    expect(portsLabel(['80/TCP', '443/TCP', '8080/TCP'])).toBe('80, 443 +1');
  });
  it('keeps each protocol where they differ, and a named port as it is', () => {
    expect(portsLabel(['53/UDP', '53/TCP'])).toBe('53/UDP, 53/TCP');
    expect(portsLabel(['http'])).toBe('http');
  });
  it('says every port for a rule with no port filter', () => {
    expect(portsLabel([])).toBe('every port');
    expect(portsLabel(['every port'])).toBe('every port');
    expect(portsLabel(['80/TCP', 'every port'])).toBe('every port');
  });
  it('unions lists, an unfiltered one making the union unfiltered', () => {
    expect(unionPorts([['80/TCP'], ['80/TCP', '443/TCP']])).toEqual(['80/TCP', '443/TCP']);
    expect(unionPorts([['80/TCP'], []])).toEqual(['every port']);
  });
});

describe('peerText', () => {
  it('says the selector and the namespace, the pod count apart', () => {
    expect(peerText(pods('pods app=frontend in namespace shop', ['shop'], 3), 'x')).toEqual({ selector: 'app=frontend', ns: 'shop', sub: '3 pods' });
  });
  it('takes this namespace from the pod, and a namespace selector from the label', () => {
    expect(peerText(pods('pods app=web in this namespace', [], 0), 'shop')).toEqual({ selector: 'app=web', ns: 'shop', sub: 'no pod now' });
    expect(peerText(pods('every pod in namespaces team=data', [], 1), 'shop')).toEqual({ selector: 'all pods', ns: 'team=data', sub: '1 pod' });
  });
  it('counts namespaces when there are several, and leaves any other peer its label', () => {
    expect(peerText(pods('pods app=web in namespaces tier=a', ['a', 'b', 'c']), 'shop').ns).toBe('3 namespaces');
    expect(peerText(cidr('10.0.0.0/8'), 'shop')).toEqual({ selector: '10.0.0.0/8', ns: '', sub: '' });
    expect(peerName({ selector: 'app.kubernetes.io/name=checkout-api-gateway-internal', ns: 'team-platform-engineering-production', sub: '' }, 30).length).toBeLessThanOrEqual(30);
  });
});

describe('arrowsOf', () => {
  const side = (n: number, perNs = n) =>
    flowsOf(
      direction(Array.from({ length: n }, (_, i) => rule(i + 1, [pods(`pods app=s${String(i)} in namespace ns-${String(i % perNs)}`, [`ns-${String(i % perNs)}`])], ['80/TCP']))),
      'ingress',
      'shop',
    );
  it('draws one arrow a peer up to five', () => {
    const arrows = arrowsOf(side(5));
    expect(arrows).toHaveLength(5);
    expect(arrows.every((a) => !a.aggregate)).toBe(true);
  });
  it('draws one arrow a namespace from six peers', () => {
    const arrows = arrowsOf(side(6, 3));
    expect(arrows).toHaveLength(3);
    expect(arrows.every((a) => a.aggregate)).toBe(true);
    expect(arrows[0]?.name).toMatch(/^ns-\d · 4 pods$/);
  });
  it('keeps the four largest namespaces and a +N past five', () => {
    const arrows = arrowsOf(side(12, 12));
    expect(arrows).toHaveLength(5);
    expect(arrows[4]?.more).toBe(true);
    expect(arrows[4]?.name).toBe('+8');
    expect(arrows[4]?.flowIds).toHaveLength(8);
    expect(new Set(arrows.flatMap((a) => a.flowIds)).size).toBe(12);
  });
  it('draws only what is allowed, and collapses the ports of an aggregate', () => {
    const flows = flowsOf(
      direction([
        rule(1, [pods('pods a=1 in namespace x', ['x'])], ['80/TCP', '443/TCP']),
        rule(2, [pods('pods a=2 in namespace x', ['x'])], ['8080/TCP', '8443/TCP', '9090/TCP']),
        rule(3, [pods('pods a=3 in namespace x', ['x'])], ['80/TCP'], 'deny'),
        ...[4, 5, 6, 7].map((i) => rule(i, [pods(`pods a=${String(i)} in namespace x`, ['x'])], ['80/TCP'])),
      ]),
      'ingress',
      'shop',
    );
    const arrows = arrowsOf(flows);
    expect(arrows).toHaveLength(1);
    expect(portsLabel(arrows[0]?.ports ?? [])).toBe('80, 443 +3');
    expect(arrows[0]?.flowIds).toHaveLength(6);
  });
});

describe('flowsOf and filterFlows', () => {
  it('lists deny first, then allow, then not evaluated, by namespace then peer', () => {
    const flows = flowsOf(
      direction([
        rule(1, [pods('pods a=1 in namespace z', ['z'])], ['80/TCP']),
        rule(2, [pods('pods a=1 in namespace b', ['b'])], ['80/TCP']),
        rule(3, [pods('pods a=1 in namespace y', ['y'])], ['80/TCP'], 'unevaluated'),
        rule(4, [pods('pods a=1 in namespace q', ['q'])], ['80/TCP'], 'deny'),
      ]),
      'egress',
      'shop',
    );
    expect(flows.map((f) => `${f.kind}:${f.text.ns}`)).toEqual(['deny:q', 'allow:b', 'allow:z', 'unevaluated:y']);
  });
  it('matches the peer, the port and the policy', () => {
    const flows = flowsOf(direction([rule(1, [pods('pods app=web in namespace shop', ['shop'])], ['443/TCP']), rule(2, [cidr('10.0.0.0/8')], [])]), 'egress', 'shop');
    expect(filterFlows(flows, 'web')).toHaveLength(1);
    expect(filterFlows(flows, '443')).toHaveLength(1);
    expect(filterFlows(flows, 'P2')).toHaveLength(1);
    expect(filterFlows(flows, 'every port')).toHaveLength(1);
    expect(filterFlows(flows, '')).toHaveLength(2);
  });
});
