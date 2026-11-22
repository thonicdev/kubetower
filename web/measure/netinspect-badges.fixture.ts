import type { Direction, PeerView, PodAnswer, RuleView } from '../app/extras/netinspect/data';

/**
 * Pods of one namespace (`cases`) whose rules stress the badges: long names,
 * many peers, every kind of peer, nothing allowed, open, not evaluated.
 * Each case is a pod named after it, so a scene asks for one.
 */

const LONG_NS = 'team-platform-engineering-production-eu-west-1';
const LONG_SEL = 'app.kubernetes.io/name=checkout-api-gateway-internal-v2';

const pods = (label: string, namespaces: string[], n = 3): PeerView => ({ kind: 'pods', label, pods: n, namespaces });
const cidr = (label: string): PeerView => ({ kind: 'cidr', label, pods: 0, namespaces: [] });
const entity = (label: string): PeerView => ({ kind: 'entity', label, pods: 0, namespaces: [] });
const anyone: PeerView = { kind: 'anyone', label: 'anyone', pods: 0, namespaces: [] };
const policy = { namespace: 'cases', name: 'p' };

function rules(peers: PeerView[][], kind: RuleView['kind'] = 'allow'): RuleView[] {
  return peers.map((p, i) => ({ namespace: 'cases', name: `policy-${String(i + 1)}`, kind, rule: i + 1, text: '', peers: p, ports: ['443/TCP'] }));
}
const isolated = (rs: RuleView[]): Direction => ({ isolated: true, policies: [policy], rules: rs });
const open: Direction = { isolated: false, policies: [], rules: [] };

interface Case {
  in: Direction;
  out: Direction;
  hostNetwork?: boolean;
  unevaluated?: { kind: string; group: string; count: string }[];
}

const many = (n: number): PeerView[][] =>
  Array.from({ length: n }, (_, i) => [pods(`app=service-${String(i + 1)}`, [`ns-${String(i + 1)}`])]);


const rule = (i: number, kind: RuleView['kind'], peers: PeerView[], ports: string[]): RuleView => ({ namespace: 'cases', name: `policy-${String(1 + (i % 7))}`, kind, rule: i + 1, text: '', peers, ports });
const NAMESPACES = ['shop', 'payments', 'data', 'monitoring', 'ingress-nginx', 'auth', 'search', 'billing', 'ml-training', 'logging', 'batch-jobs', 'kube-system'];
const PORT_POOL = ['80/TCP', '443/TCP', '8080/TCP', '8443/TCP', '5432/TCP', '6379/TCP', '9090/TCP', '9100/TCP', '53/UDP', '53/TCP', '3306/TCP', '27017/TCP', '9200/TCP', '9300/TCP', '4317/TCP', '2181/TCP', '9092/TCP', '15021/TCP', '10250/TCP', 'metrics'];
/** About 30 rules over 40 peers, 12 namespaces and 20 ports, allow, deny and not evaluated mixed. */
function flows(seed: number): RuleView[] {
  const out: RuleView[] = [];
  for (let i = 0; i < 30; i++) {
    const ns = NAMESPACES[(i + seed) % NAMESPACES.length] ?? 'shop';
    const kind: RuleView['kind'] = i % 9 === 4 ? 'deny' : i % 11 === 7 ? 'unevaluated' : 'allow';
    const peers: PeerView[] =
      i % 10 === 3
        ? [cidr(`10.${String(i)}.0.0/16`)]
        : i % 13 === 5
          ? [entity('world')]
          : [pods(`pods app=svc-${String(i)} in namespace ${ns}`, [ns], 1 + (i % 6)), ...(i % 3 === 0 ? [pods(`pods tier=web in namespace ${ns}`, [ns], 2)] : [])];
    const ports = i % 8 === 2 ? ['every port'] : Array.from({ length: 1 + ((i * 3) % 4) }, (_, k) => PORT_POOL[(i + k * 5) % PORT_POOL.length] ?? '80/TCP');
    out.push(rule(i, kind, kind === 'unevaluated' ? [{ kind: 'unevaluated', label: 'CiliumNetworkPolicy l7-rules', pods: 0, namespaces: [] }] : peers, ports));
  }
  return out;
}

export const CASES: Readonly<Record<string, Case>> = {
  'flows-few': {
    in: isolated([rule(0, 'allow', [pods('pods app=frontend in namespace shop', ['shop'], 3)], ['80/TCP', '443/TCP']), rule(1, 'allow', [cidr('10.0.0.0/8')], ['every port']), rule(2, 'deny', [pods('pods app=batch in namespace jobs', ['jobs'], 2)], ['8080/TCP'])]),
    out: isolated([rule(0, 'allow', [pods('pods app=ledger in namespace payments', ['payments'], 2)], ['5432/TCP']), rule(1, 'allow', [pods('every pod in namespace kube-system', ['kube-system'], 9)], ['53/UDP', '53/TCP']), rule(2, 'allow', [entity('world')], ['443/TCP', '80/TCP', '8443/TCP', '8080/TCP', '9090/TCP'])]),
  },
  'flows-many': { in: isolated(flows(0)), out: isolated(flows(5)) },
  'open-in': { in: open, out: isolated(rules([[pods('pods app=ledger in namespace payments', ['payments'], 2)]])) },
  'open-both': { in: open, out: open },
  'isolated-nothing': { in: isolated([]), out: isolated([]) },
  'by-label': { in: isolated(rules([[pods('pods app=web', ['shop'])]])), out: isolated(rules([[pods('pods app=ledger', ['payments'], 2)], [pods('pods tier=db', ['data'], 1)]])) },
  'by-namespace': { in: isolated(rules([[pods('all pods in namespace monitoring', ['monitoring'], 4)]])), out: isolated(rules([[pods('all pods in 3 namespaces', ['a', 'b', 'c'], 9)]])) },
  'by-cidr': { in: isolated(rules([[cidr('10.0.0.0/8')], [cidr('192.168.0.0/16 except 192.168.1.0/24')]])), out: isolated(rules([[cidr('172.16.0.0/12 except 172.16.4.0/24, 172.16.8.0/24')], [cidr('2001:db8:1234:5678::/64')]])) },
  'all-pods': { in: isolated(rules([[anyone]])), out: isolated(rules([[anyone]])) },
  'long-names': {
    in: isolated(rules([[pods(`pods ${LONG_SEL} in namespace ${LONG_NS}`, [LONG_NS])]])),
    out: isolated(rules([[pods(`pods ${LONG_SEL} in namespace ${LONG_NS}`, [LONG_NS])], [pods(`pods ${LONG_SEL}-canary in ${LONG_NS}`, [LONG_NS, 'other'])]])),
  },
  'many-peers': { in: isolated(rules(many(12))), out: isolated(rules(many(14))) },
  'entities': {
    in: isolated(rules([[entity('world')], [entity('host')], [entity('kube-apiserver')]])),
    out: isolated(rules([[entity('world')], [entity('host')], [entity('toFQDNs api.payments-provider.example.com')], [entity('toFQDNs *.cdn.very-long-content-delivery-network.example.org')]])),
  },
  'not-evaluated': {
    in: isolated(rules([[{ kind: 'unevaluated', label: 'CiliumClusterwideNetworkPolicy cluster-wide-egress-baseline', pods: 0, namespaces: [] }]], 'unevaluated')),
    out: isolated(rules([[{ kind: 'unevaluated', label: 'GlobalNetworkPolicy', pods: 0, namespaces: [] }]], 'unevaluated')),
    unevaluated: ['CiliumClusterwideNetworkPolicy', 'GlobalNetworkPolicy', 'CiliumNetworkPolicy', 'AdminNetworkPolicy', 'BaselineAdminNetworkPolicy'].map((kind) => ({ kind, group: 'x', count: 'at least 1' })),
  },
  'host-network': { in: open, out: open, hostNetwork: true },
};

function answer(name: string, c: Case): PodAnswer {
  return {
    mode: 'rules',
    complete: true,
    notes: [],
    cni: { name: 'Cilium', enforces: 'yes', detail: '' },
    unevaluated: c.unevaluated ?? [],
    pod: { namespace: 'cases', name, workload: name, ip: '10.0.0.9', in: c.in.isolated, out: c.out.isolated, hostNetwork: c.hostNetwork ?? false, policies: 3 },
    ingress: c.in,
    egress: c.out,
  };
}

export const BADGE_ANSWERS: Readonly<Record<string, unknown>> = Object.fromEntries(
  Object.entries(CASES).map(([name, c]) => [`/x/production/netinspect/pods/cases/${name}`, answer(name, c)]),
);
