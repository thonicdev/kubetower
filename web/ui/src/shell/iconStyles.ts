/**
 * KubeTower's own icon styles, as DiceBear style
 * definitions. A hundred and twenty subjects from the things a
 * cluster is made of and what KubeTower does with them - Kubernetes objects,
 * servers, the network, data, the platform, security, operations, delivery,
 * resources, actions, insight and the console itself - each a DiceBear style
 * definition built here, in code, and drawn by `@dicebear/core` like any
 * other style.
 *
 * Drawn for this console from geometric primitives (hexagons, racks,
 * cylinders, arrows), so they are this repository's own work under its own
 * license: no third-party artwork, no attribution to carry. **No trademark**:
 * nothing here is the Kubernetes ship's wheel or its heptagon, or any CNCF or
 * vendor mark - a pod is a generic hexagon.
 *
 * Every style draws on a 100x100 canvas with two colors: `ink`, the subject,
 * which DiceBear keeps in contrast with the background, and `accent`, its
 * details - LEDs, dots, lines - which the dialog's Ink control sets. The seed
 * varies the accent, a status badge (a dot, a corner, a bar, a ring, or none)
 * and the subject's scale, so two clusters drawn in one style still differ.
 */

interface ColourRef {
  type: 'color';
  name: 'ink' | 'accent' | 'background';
}
type Value = string | ColourRef;
export interface Element {
  type: 'element' | 'component';
  name: string;
  attributes?: Record<string, Value>;
  children?: Element[];
}

const INK: ColourRef = { type: 'color', name: 'ink' };
const ACC: ColourRef = { type: 'color', name: 'accent' };
/** The background color, for a cut-out through the subject. */
const BG: ColourRef = { type: 'color', name: 'background' };

const n = (value: number): string => String(Math.round(value * 100) / 100);
const el = (name: string, attributes: Record<string, Value>, children?: Element[]): Element =>
  children === undefined ? { type: 'element', name, attributes } : { type: 'element', name, attributes, children };

const rect = (x: number, y: number, w: number, h: number, rx = 0, fill: Value = INK): Element =>
  el('rect', { x: n(x), y: n(y), width: n(w), height: n(h), rx: n(rx), fill });
const circle = (cx: number, cy: number, r: number, fill: Value = INK): Element =>
  el('circle', { cx: n(cx), cy: n(cy), r: n(r), fill });
const ellipse = (cx: number, cy: number, rx: number, ry: number, fill: Value = INK): Element =>
  el('ellipse', { cx: n(cx), cy: n(cy), rx: n(rx), ry: n(ry), fill });
const path = (d: string, fill: Value = INK): Element => el('path', { d, fill });
const stroke = (d: string, colour: Value = INK, width = 6, extra: Record<string, Value> = {}): Element =>
  el('path', {
    d,
    fill: 'none',
    stroke: colour,
    'stroke-width': n(width),
    'stroke-linecap': 'round',
    'stroke-linejoin': 'round',
    ...extra,
  });
const ring = (cx: number, cy: number, r: number, colour: Value = INK, width = 6): Element =>
  el('circle', { cx: n(cx), cy: n(cy), r: n(r), fill: 'none', stroke: colour, 'stroke-width': n(width) });
const frame = (x: number, y: number, w: number, h: number, rx: number, colour: Value = INK, width = 6, extra: Record<string, Value> = {}): Element =>
  el('rect', { x: n(x), y: n(y), width: n(w), height: n(h), rx: n(rx), fill: 'none', stroke: colour, 'stroke-width': n(width), ...extra });

/** A pointy-topped hexagon, as a path. */
function hex(cx: number, cy: number, r: number): string {
  const points = [0, 1, 2, 3, 4, 5].map((at) => {
    const angle = (Math.PI / 3) * at - Math.PI / 2;
    return `${n(cx + r * Math.cos(angle))} ${n(cy + r * Math.sin(angle))}`;
  });
  return `M${points.join(' L')} Z`;
}
const gear = (cx: number, cy: number): Element[] => [
  ...[0, 45, 90, 135, 180, 225, 270, 315].map((angle) =>
    el('rect', { x: n(cx - 5), y: n(cy - 31), width: '10', height: '14', rx: '2', fill: INK, transform: `rotate(${String(angle)} ${n(cx)} ${n(cy)})` }),
  ),
  circle(cx, cy, 22),
  circle(cx, cy, 9, BG),
];

/** A five-pointed star, as a path. */
function star(cx: number, cy: number, outer: number, inner: number): string {
  const points = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9].map((at) => {
    const angle = (Math.PI / 5) * at - Math.PI / 2;
    const r = at % 2 === 0 ? outer : inner;
    return `${n(cx + r * Math.cos(angle))} ${n(cy + r * Math.sin(angle))}`;
  });
  return `M${points.join(' L')} Z`;
}

/** The cluster family's frame: a hexagon outline with three nodes on its
 *  corners, in the accent - the same on all twenty, so they read as one family
 *  - and the cluster's purpose drawn inside it. */
function cluster(...emblem: Element[]): Element[] {
  const corners = [0, 2, 4].map((at) => {
    const angle = (Math.PI / 3) * at - Math.PI / 2;
    return circle(50 + 38 * Math.cos(angle), 50 + 38 * Math.sin(angle), 6, ACC);
  });
  return [stroke(hex(50, 50, 38), INK, 5), ...corners, ...emblem];
}

/** The hundred and forty subjects, in thirteen groups (the clusters first). The last forty follow
 *  what KubeTower itself does: the kinds it lists, the actions it takes on an
 *  object, the views it draws, and its own desktop shell. */
const SUBJECTS: Readonly<Record<string, { label: string; group: InfraGroup; draw: () => Element[] }>> = {
  // --- Kubernetes objects ---------------------------------------------------
  'kt-pod': { label: 'Pod', group: 'kubernetes', draw: () => [path(hex(50, 50, 30)), circle(50, 50, 10, ACC)] },
  'kt-deployment': {
    label: 'Deployment',
    group: 'kubernetes',
    draw: () => [path(hex(35, 37, 15)), path(hex(65, 37, 15)), path(hex(50, 63, 15)), circle(50, 63, 5, ACC)],
  },
  'kt-replica-set': {
    label: 'Replica set',
    group: 'kubernetes',
    draw: () => [stroke(hex(26, 50, 12), INK, 5), path(hex(50, 50, 13), ACC), stroke(hex(74, 50, 12), INK, 5)],
  },
  'kt-stateful-set': {
    label: 'Stateful set',
    group: 'kubernetes',
    draw: () => [
      ...[26, 44, 62].flatMap((y) => [rect(22, y, 56, 13, 4), circle(31, y + 6.5, 3.5, ACC)]),
      rect(42, 31, 26, 3, 1.5, BG),
      rect(42, 49, 20, 3, 1.5, BG),
      rect(42, 67, 14, 3, 1.5, BG),
    ],
  },
  'kt-daemon-set': {
    label: 'Daemon set',
    group: 'kubernetes',
    draw: () =>
      [
        [20, 20],
        [52, 20],
        [20, 52],
        [52, 52],
      ].flatMap(([x = 0, y = 0]) => [frame(x, y, 28, 28, 6, INK, 5), path(hex(x + 14, y + 14, 7), ACC)]),
  },
  'kt-service': {
    label: 'Service',
    group: 'kubernetes',
    draw: () => [
      stroke('M50 50 L26 28 M50 50 L74 28 M50 50 L50 78', INK, 5),
      circle(26, 28, 9),
      circle(74, 28, 9),
      circle(50, 78, 9),
      circle(50, 50, 11, ACC),
    ],
  },
  'kt-ingress': {
    label: 'Ingress',
    group: 'kubernetes',
    draw: () => [frame(40, 26, 40, 48, 8, INK, 6), stroke('M14 50 H50', ACC, 7), path('M44 38 L60 50 L44 62 Z', ACC)],
  },
  'kt-namespace': {
    label: 'Namespace',
    group: 'kubernetes',
    draw: () => [frame(18, 18, 64, 64, 12, INK, 5, { 'stroke-dasharray': '9 6' }), path(hex(50, 50, 16), ACC)],
  },
  'kt-config-map': {
    label: 'Config map',
    group: 'kubernetes',
    draw: () => [
      path('M28 16 H60 L74 30 V84 H28 Z'),
      path('M60 16 V30 H74 Z', ACC),
      rect(36, 42, 30, 5, 2.5, BG),
      rect(36, 54, 30, 5, 2.5, BG),
      rect(36, 66, 18, 5, 2.5, ACC),
    ],
  },
  'kt-secret': {
    label: 'Secret',
    group: 'kubernetes',
    draw: () => [ring(34, 50, 14, INK, 7), stroke('M48 50 H82 M72 50 V61 M81 50 V58', INK, 7), circle(34, 50, 5, ACC)],
  },
  'kt-job': { label: 'Job', group: 'kubernetes', draw: () => [...gear(50, 50), circle(50, 50, 4, ACC)] },
  'kt-cron-job': {
    label: 'Cron job',
    group: 'kubernetes',
    draw: () => [ring(50, 52, 28, INK, 7), stroke('M50 34 V52 L63 60', ACC, 6), rect(42, 14, 16, 7, 3)],
  },

  // --- Servers & hardware --------------------------------------------------
  'kt-server': {
    label: 'Server',
    group: 'servers',
    draw: () => [22, 42, 62].flatMap((y) => [rect(20, y, 60, 16, 4), circle(30, y + 8, 3, ACC), rect(42, y + 6, 30, 4, 2, BG)]),
  },
  'kt-rack': {
    label: 'Rack',
    group: 'servers',
    draw: () => [
      frame(28, 12, 44, 76, 6, INK, 6),
      ...[22, 36, 50, 64].flatMap((y) => [rect(36, y, 28, 9, 2), circle(59, y + 4.5, 2.2, ACC)]),
    ],
  },
  'kt-node': {
    label: 'Node',
    group: 'servers',
    draw: () => [
      rect(18, 28, 64, 40, 6),
      ...[28, 40, 52].map((x) => rect(x, 40, 8, 8, 2, BG)),
      circle(70, 44, 4, ACC),
      rect(26, 68, 8, 7, 2),
      rect(66, 68, 8, 7, 2),
    ],
  },
  'kt-cpu': {
    label: 'CPU',
    group: 'servers',
    draw: () => [
      ...[36, 48, 60].flatMap((at) => [rect(at, 16, 4, 12, 2), rect(at, 72, 4, 12, 2), rect(16, at, 12, 4, 2), rect(72, at, 12, 4, 2)]),
      rect(26, 26, 48, 48, 8),
      rect(38, 38, 24, 24, 4, ACC),
    ],
  },
  'kt-memory': {
    label: 'Memory',
    group: 'servers',
    draw: () => [
      rect(14, 34, 72, 28, 3),
      ...[20, 34, 48, 62].map((x) => rect(x + 1, 40, 11, 14, 1.5, BG)),
      rect(18, 62, 64, 5, 1, ACC),
      rect(47, 60, 6, 9, 0, BG),
    ],
  },
  'kt-terminal': {
    label: 'Terminal',
    group: 'servers',
    draw: () => [
      rect(16, 22, 68, 56, 9),
      circle(27, 32, 3, ACC),
      circle(36, 32, 3, ACC),
      circle(45, 32, 3, ACC),
      stroke('M30 48 L40 56 L30 64', BG, 5),
      rect(46, 61, 20, 5, 2.5, ACC),
    ],
  },
  'kt-container': {
    label: 'Container',
    group: 'servers',
    draw: () => [
      rect(14, 30, 72, 42, 3),
      ...[22, 32, 42, 52, 62].map((x) => rect(x, 37, 4, 28, 2, BG)),
      rect(71, 40, 8, 22, 2, ACC),
    ],
  },
  'kt-virtual-machine': {
    label: 'Virtual machine',
    group: 'servers',
    draw: () => [rect(16, 20, 68, 46, 7), rect(23, 27, 54, 32, 3, BG), path(hex(50, 43, 11), ACC), rect(44, 66, 12, 8), rect(32, 74, 36, 6, 3)],
  },
  'kt-gpu': {
    label: 'GPU',
    group: 'servers',
    draw: () => [
      rect(12, 30, 76, 36, 5),
      ring(34, 48, 11, BG, 4),
      circle(34, 48, 3.5, ACC),
      ring(64, 48, 11, BG, 4),
      circle(64, 48, 3.5, ACC),
      rect(20, 66, 44, 6, 1, ACC),
    ],
  },
  'kt-power': {
    label: 'Power',
    group: 'servers',
    draw: () => [circle(50, 50, 32), path('M55 22 L35 54 H50 L45 78 L66 44 H51 Z', ACC)],
  },

  // --- Network & cloud ----------------------------------------------------
  'kt-cloud': {
    label: 'Cloud',
    group: 'network',
    draw: () => [path('M30 72 H70 A15 15 0 0 0 72 42 A21 21 0 0 0 33 37 A17.5 17.5 0 0 0 30 72 Z'), rect(40, 56, 22, 5, 2.5, ACC)],
  },
  'kt-load-balancer': {
    label: 'Load balancer',
    group: 'network',
    draw: () => [
      stroke('M50 30 L24 70 M50 30 L50 70 M50 30 L76 70', INK, 5),
      circle(50, 26, 11, ACC),
      circle(24, 74, 8),
      circle(50, 74, 8),
      circle(76, 74, 8),
    ],
  },
  'kt-router': {
    label: 'Router',
    group: 'network',
    draw: () => [
      stroke('M34 52 L28 24 M66 52 L72 24', INK, 5),
      rect(18, 50, 64, 24, 7),
      ...[30, 40, 50].map((x) => circle(x, 62, 3, ACC)),
      stroke('M42 34 Q50 26 58 34', ACC, 4),
    ],
  },
  'kt-mesh': {
    label: 'Mesh',
    group: 'network',
    draw: () => [
      stroke('M26 28 L74 28 L50 74 Z M26 28 L50 48 L74 28 M50 48 L50 74', INK, 4),
      circle(26, 28, 9),
      circle(74, 28, 9),
      circle(50, 74, 9),
      circle(50, 48, 7, ACC),
    ],
  },
  'kt-globe': {
    label: 'Globe',
    group: 'network',
    draw: () => [ring(50, 50, 30, INK, 6), stroke('M20 50 H80 M50 20 C34 34 34 66 50 80 C66 66 66 34 50 20', INK, 5), circle(50, 50, 5, ACC)],
  },
  'kt-firewall': {
    label: 'Firewall',
    group: 'network',
    draw: () => [
      rect(16, 26, 32, 14, 2),
      rect(52, 26, 32, 14, 2),
      rect(16, 44, 14, 14, 2),
      rect(34, 44, 32, 14, 2, ACC),
      rect(70, 44, 14, 14, 2),
      rect(16, 62, 32, 14, 2),
      rect(52, 62, 32, 14, 2),
    ],
  },
  'kt-gateway': {
    label: 'Gateway',
    group: 'network',
    draw: () => [path('M22 80 V46 A28 28 0 0 1 78 46 V80 H63 V48 A13 13 0 0 0 37 48 V80 Z'), stroke('M50 86 V62', ACC, 6), path('M42 68 L50 58 L58 68 Z', ACC)],
  },
  'kt-dns': {
    label: 'DNS',
    group: 'network',
    draw: () => [rect(45, 18, 10, 66, 3), path('M24 24 H64 L73 33 L64 42 H24 Z', ACC), path('M76 48 H36 L27 57 L36 66 H76 Z')],
  },
  'kt-shield': {
    label: 'Shield',
    group: 'network',
    draw: () => [path('M50 14 L80 27 V50 C80 67 67 79 50 86 C33 79 20 67 20 50 V27 Z'), stroke('M37 50 L47 60 L64 41', ACC, 7)],
  },

  // --- Data & storage -----------------------------------------------------
  'kt-database': {
    label: 'Database',
    group: 'data',
    draw: () => [
      path('M24 28 V72 A26 10 0 0 0 76 72 V28 Z'),
      ellipse(50, 28, 26, 10, ACC),
      stroke('M24 44 A26 10 0 0 0 76 44 M24 58 A26 10 0 0 0 76 58', BG, 4),
    ],
  },
  'kt-disk': {
    label: 'Disk',
    group: 'data',
    draw: () => [rect(20, 22, 60, 56, 7), ring(46, 48, 15, BG, 5), circle(46, 48, 4, ACC), circle(70, 70, 3, ACC)],
  },
  'kt-volume': {
    label: 'Volume',
    group: 'data',
    draw: () => [ellipse(50, 68, 30, 10), ellipse(50, 50, 30, 10, ACC), ellipse(50, 32, 30, 10)],
  },
  'kt-bucket': {
    label: 'Bucket',
    group: 'data',
    draw: () => [stroke('M27 34 C27 12 73 12 73 34', INK, 4), path('M24 34 L31 80 H69 L76 34 Z'), ellipse(50, 34, 26, 7, ACC)],
  },
  'kt-queue': {
    label: 'Queue',
    group: 'data',
    draw: () => [rect(16, 28, 44, 11, 5.5), rect(16, 45, 52, 11, 5.5), rect(16, 62, 34, 11, 5.5), path('M72 34 L86 50 L72 66 Z', ACC)],
  },
  'kt-cache': {
    label: 'Cache',
    group: 'data',
    draw: () => [ring(50, 54, 26, INK, 7), rect(43, 16, 14, 8, 3), stroke('M50 54 L61 41', ACC, 6), circle(50, 54, 4, ACC)],
  },
  'kt-backup': {
    label: 'Backup',
    group: 'data',
    draw: () => [stroke('M76 52 A26 26 0 1 1 64 28', INK, 7), path('M56 16 L74 26 L58 38 Z'), circle(50, 52, 9, ACC)],
  },
  'kt-logs': {
    label: 'Logs',
    group: 'data',
    draw: () => [
      rect(24, 16, 52, 68, 7),
      rect(32, 28, 36, 5, 2.5, ACC),
      ...[
        [40, 30],
        [52, 24],
        [64, 32],
      ].map(([y = 0, w = 0]) => rect(32, y, w, 5, 2.5, BG)),
    ],
  },
  'kt-metrics': {
    label: 'Metrics',
    group: 'data',
    draw: () => [rect(20, 56, 13, 24, 2), rect(39, 42, 13, 38, 2), rect(58, 28, 13, 52, 2), stroke('M18 40 L36 28 L54 34 L82 14', ACC, 5)],
  },

  // --- Platform: the cluster's own machinery ------------------------------
  'kt-cluster': {
    label: 'Cluster',
    group: 'platform',
    draw: () => [
      ...[0, 1, 2, 3, 4, 5].map((at) => {
        const angle = (Math.PI / 3) * at - Math.PI / 2;
        return path(hex(50 + 26 * Math.cos(angle), 50 + 26 * Math.sin(angle), 10));
      }),
      path(hex(50, 50, 11), ACC),
    ],
  },
  'kt-control-plane': {
    label: 'Control plane',
    group: 'platform',
    draw: () => [rect(37, 20, 26, 14, 5, ACC), rect(27, 42, 46, 14, 5), rect(17, 64, 66, 14, 5)],
  },
  'kt-api': {
    label: 'API',
    group: 'platform',
    draw: () => [
      stroke('M38 20 C28 20 30 34 30 42 C30 48 26 50 21 50 C26 50 30 52 30 58 C30 66 28 80 38 80', INK, 7),
      stroke('M62 20 C72 20 70 34 70 42 C70 48 74 50 79 50 C74 50 70 52 70 58 C70 66 72 80 62 80', INK, 7),
      circle(50, 50, 7, ACC),
    ],
  },
  'kt-scheduler': {
    label: 'Scheduler',
    group: 'platform',
    draw: () => [
      rect(18, 24, 64, 56, 8),
      stroke('M35 16 V30 M65 16 V30', INK, 6),
      ...[
        [28, 44],
        [44, 44],
        [60, 44],
        [28, 60],
        [44, 60],
      ].map(([x = 0, y = 0]) => rect(x, y, 12, 10, 2, BG)),
      rect(60, 60, 12, 10, 2, ACC),
    ],
  },
  'kt-key-value': {
    label: 'Key value',
    group: 'platform',
    draw: () => [
      rect(12, 34, 30, 32, 6),
      rect(58, 34, 30, 32, 6, ACC),
      stroke('M42 50 H58', INK, 6),
      ring(27, 50, 6, BG, 4),
      rect(66, 44, 14, 4, 2, BG),
      rect(66, 53, 10, 4, 2, BG),
    ],
  },
  'kt-agent': {
    label: 'Agent',
    group: 'platform',
    draw: () => [stroke('M50 38 V22', INK, 5), circle(50, 19, 7, ACC), rect(26, 38, 48, 42, 8), rect(35, 52, 30, 5, 2.5, BG), rect(35, 63, 18, 5, 2.5, ACC)],
  },
  'kt-autoscaler': {
    label: 'Autoscaler',
    group: 'platform',
    draw: () => [
      rect(34, 34, 32, 32, 6),
      path('M50 10 L61 25 H39 Z', ACC),
      path('M50 90 L61 75 H39 Z', ACC),
      path('M10 50 L25 39 V61 Z', ACC),
      path('M90 50 L75 39 V61 Z', ACC),
    ],
  },
  'kt-node-pool': {
    label: 'Node pool',
    group: 'platform',
    draw: () =>
      [18, 41, 64].flatMap((x, column) =>
        [28, 52].map((y, row) => rect(x, y, 18, 18, 4, column === 1 && row === 0 ? ACC : INK)),
      ),
  },
  'kt-webhook': {
    label: 'Webhook',
    group: 'platform',
    draw: () => [stroke('M52 22 V56 A15 15 0 1 1 30 60', INK, 7), path('M22 52 L29 70 L39 55 Z'), circle(52, 18, 7, ACC)],
  },
  'kt-extension': {
    label: 'Extension',
    group: 'platform',
    draw: () => [path('M28 30 H43 A8 8 0 1 1 57 30 H72 V44 A8 8 0 1 1 72 58 V72 H28 Z'), circle(42, 56, 6, ACC)],
  },

  // --- Security & access --------------------------------------------------
  'kt-lock': {
    label: 'Lock',
    group: 'security',
    draw: () => [stroke('M35 46 V36 A15 15 0 0 1 65 36 V46', INK, 7), rect(24, 46, 52, 38, 8), circle(50, 60, 6, ACC), rect(47.5, 62, 5, 12, 2.5, ACC)],
  },
  'kt-certificate': {
    label: 'Certificate',
    group: 'security',
    draw: () => [
      rect(16, 20, 68, 48, 5),
      rect(26, 31, 44, 5, 2.5, BG),
      rect(26, 42, 30, 5, 2.5, BG),
      path('M60 72 L55 90 L64 85 Z', ACC),
      path('M72 72 L77 90 L68 85 Z', ACC),
      circle(66, 66, 13, ACC),
    ],
  },
  'kt-id-card': {
    label: 'ID card',
    group: 'security',
    draw: () => [
      rect(12, 26, 76, 50, 7),
      rect(21, 37, 22, 28, 3, ACC),
      rect(50, 39, 30, 5, 2.5, BG),
      rect(50, 50, 22, 5, 2.5, BG),
      rect(50, 61, 26, 5, 2.5, BG),
    ],
  },
  'kt-role': { label: 'Role', group: 'security', draw: () => [path(star(50, 53, 34, 15)), circle(50, 54, 8, ACC)] },
  'kt-policy': {
    label: 'Policy',
    group: 'security',
    draw: () => [
      rect(22, 14, 56, 72, 7),
      ...[30, 47, 64].flatMap((y) => [stroke(`M31 ${String(y + 3)} L36 ${String(y + 8)} L45 ${String(y - 1)}`, ACC, 4.5), rect(51, y + 1, 18, 5, 2.5, BG)]),
    ],
  },
  'kt-audit': {
    label: 'Audit',
    group: 'security',
    draw: () => [
      rect(16, 14, 46, 58, 6),
      rect(24, 26, 30, 5, 2.5, BG),
      rect(24, 37, 22, 5, 2.5, BG),
      circle(62, 62, 15, BG),
      ring(62, 62, 14, ACC, 6),
      stroke('M72 72 L86 86', ACC, 8),
    ],
  },
  'kt-vault': {
    label: 'Vault',
    group: 'security',
    draw: () => [rect(16, 16, 68, 68, 9), ring(52, 50, 17, BG, 5), circle(52, 50, 6, ACC), stroke('M52 50 L62 40', ACC, 4), rect(18, 28, 6, 12, 2, ACC), rect(18, 60, 6, 12, 2, ACC)],
  },
  'kt-token': {
    label: 'Token',
    group: 'security',
    draw: () => [circle(50, 50, 32), ring(50, 50, 24, BG, 3), circle(50, 44, 7, ACC), path('M46 47 H54 L58 66 H42 Z', ACC)],
  },
  'kt-network-policy': {
    label: 'Network policy',
    group: 'security',
    draw: () => [rect(45, 14, 10, 72, 4), stroke('M12 50 H34', ACC, 7), path('M30 39 L43 50 L30 61 Z', ACC), stroke('M64 40 L82 58 M82 40 L64 58', INK, 7)],
  },
  'kt-scan': {
    label: 'Scan',
    group: 'security',
    draw: () => [ring(50, 50, 32, INK, 5), ring(50, 50, 18, INK, 4), path('M50 50 L50 18 A32 32 0 0 1 78 34 Z', ACC), circle(62, 40, 4)],
  },

  // --- Operations & observability ------------------------------------------
  'kt-alert': {
    label: 'Alert',
    group: 'operations',
    draw: () => [path('M50 14 C33 14 29 30 29 46 V60 L20 72 H80 L71 60 V46 C71 30 67 14 50 14 Z'), circle(50, 80, 8, ACC)],
  },
  'kt-heartbeat': {
    label: 'Heartbeat',
    group: 'operations',
    draw: () => [rect(12, 22, 76, 56, 10), stroke('M20 52 H35 L42 36 L52 68 L59 46 L64 52 H80', ACC, 6)],
  },
  'kt-gauge': {
    label: 'Gauge',
    group: 'operations',
    draw: () => [stroke('M20 66 A32 32 0 1 1 80 66', INK, 9), stroke('M50 64 L66 40', ACC, 6), circle(50, 64, 7)],
  },
  'kt-quota': {
    label: 'Quota',
    group: 'operations',
    draw: () => [circle(50, 50, 32), path('M50 50 L50 18 A32 32 0 0 1 82 50 Z', ACC)],
  },
  'kt-trace': {
    label: 'Trace',
    group: 'operations',
    draw: () => [
      stroke('M22 34 V50 H32 M36 54 V68 H46', INK, 3.5),
      rect(14, 22, 44, 12, 6),
      rect(30, 44, 44, 12, 6, ACC),
      rect(44, 64, 42, 12, 6),
    ],
  },
  'kt-dashboard': {
    label: 'Dashboard',
    group: 'operations',
    draw: () => [
      rect(14, 16, 42, 32, 6),
      rect(60, 16, 26, 32, 6, ACC),
      rect(14, 52, 26, 32, 6),
      rect(44, 52, 42, 32, 6),
      rect(52, 68, 6, 10, 1, BG),
      rect(62, 61, 6, 17, 1, BG),
      rect(72, 65, 6, 13, 1, BG),
    ],
  },
  'kt-rollout': {
    label: 'Rollout',
    group: 'operations',
    draw: () => [rect(14, 64, 20, 18, 3), rect(40, 50, 20, 32, 3), rect(66, 36, 20, 46, 3), stroke('M16 46 L40 30 L56 34 L76 18', ACC, 6), path('M68 14 L86 12 L80 30 Z', ACC)],
  },
  'kt-rollback': {
    label: 'Rollback',
    group: 'operations',
    draw: () => [stroke('M26 54 A25 25 0 1 0 38 30', INK, 8), path('M46 16 L26 24 L42 40 Z'), rect(44, 46, 16, 16, 4, ACC)],
  },
  'kt-pipeline': {
    label: 'Pipeline',
    group: 'operations',
    draw: () => [stroke('M30 50 H40 M60 50 H70', INK, 5), rect(10, 37, 22, 26, 5), rect(39, 37, 22, 26, 5, ACC), rect(68, 37, 22, 26, 5)],
  },
  'kt-hotspot': {
    label: 'Hotspot',
    group: 'operations',
    draw: () => [
      path('M50 12 C58 30 74 38 72 58 C70 78 58 88 50 88 C42 88 30 78 28 58 C27 46 36 40 40 28 C44 40 48 42 50 12 Z'),
      path('M50 50 C57 58 61 65 59 73 C57 81 43 81 41 73 C39 65 45 60 50 50 Z', ACC),
    ],
  },

  // --- Delivery: from code to a running image -------------------------------
  'kt-branch': {
    label: 'Branch',
    group: 'delivery',
    draw: () => [stroke('M32 30 V70 M68 42 C68 60 32 54 32 70', INK, 6), circle(32, 24, 9), circle(32, 76, 9), circle(68, 36, 9, ACC)],
  },
  'kt-commit': {
    label: 'Commit',
    group: 'delivery',
    draw: () => [stroke('M12 50 H34 M66 50 H88', INK, 7), ring(50, 50, 15, INK, 7), circle(50, 50, 6, ACC)],
  },
  'kt-merge': {
    label: 'Merge',
    group: 'delivery',
    draw: () => [stroke('M32 30 V70 M68 30 C68 52 32 48 32 66', INK, 6), circle(32, 24, 9), circle(68, 24, 9, ACC), circle(32, 76, 9)],
  },
  'kt-diff': {
    label: 'Diff',
    group: 'delivery',
    draw: () => [
      rect(20, 14, 60, 72, 7),
      rect(29, 30, 18, 6, 3, ACC),
      rect(35, 24, 6, 18, 3, ACC),
      rect(29, 54, 18, 6, 3, BG),
      rect(53, 30, 18, 6, 3, BG),
      rect(53, 54, 18, 6, 3, BG),
      rect(29, 70, 42, 5, 2.5, BG),
    ],
  },
  'kt-package': {
    label: 'Package',
    group: 'delivery',
    draw: () => [path('M50 12 L84 29 V71 L50 88 L16 71 V29 Z'), stroke('M16 29 L50 46 L84 29 M50 46 V88', BG, 3), stroke('M33 21 L67 38 V50', ACC, 7)],
  },
  'kt-registry': {
    label: 'Registry',
    group: 'delivery',
    draw: () => [
      stroke('M14 34 H86 M14 60 H86 M14 86 H86', INK, 4),
      ...[
        [22, 16],
        [40, 16, true],
        [58, 16],
        [31, 42],
        [49, 42],
        [22, 68, true],
        [40, 68],
        [58, 68],
      ].map(([x, y, accent]) => rect(Number(x), Number(y), 15, 15, 2, accent === true ? ACC : INK)),
    ],
  },
  'kt-build': {
    label: 'Build',
    group: 'delivery',
    draw: () => [stroke('M28 72 L56 44', INK, 11), ring(64, 36, 13, INK, 9), circle(71, 29, 7, BG), circle(28, 72, 5, ACC)],
  },
  'kt-test': {
    label: 'Test',
    group: 'delivery',
    draw: () => [
      path('M41 14 H59 V38 L77 76 C79 83 74 88 68 88 H32 C26 88 21 83 23 76 L41 38 Z'),
      path('M33 62 H67 L74 78 C75 82 72 84 69 84 H31 C28 84 25 82 26 78 Z', ACC),
      circle(46, 72, 3, BG),
      circle(57, 76, 2.5, BG),
    ],
  },
  'kt-code': {
    label: 'Code',
    group: 'delivery',
    draw: () => [stroke('M37 30 L18 50 L37 70 M63 30 L82 50 L63 70', INK, 8), stroke('M56 22 L44 78', ACC, 6)],
  },
  'kt-rocket': {
    label: 'Rocket',
    group: 'delivery',
    draw: () => [
      path('M38 52 L25 72 L39 67 Z'),
      path('M62 52 L75 72 L61 67 Z'),
      path('M42 68 H58 L50 88 Z', ACC),
      path('M50 10 C65 23 67 45 62 66 H38 C33 45 35 23 50 10 Z'),
      circle(50, 38, 7, ACC),
    ],
  },

  // --- Resources: the kinds KubeTower lists, beyond the first twelve ----------
  'kt-endpoint': {
    label: 'Endpoint',
    group: 'resources',
    draw: () => [ring(50, 50, 31, INK, 6), ring(50, 50, 17, INK, 6), circle(50, 50, 7, ACC)],
  },
  'kt-claim': {
    label: 'Claim',
    group: 'resources',
    draw: () => [rect(14, 24, 50, 52, 7), ring(39, 50, 12, BG, 4), circle(39, 50, 3.5, ACC), path('M58 54 L74 38 L88 52 L72 68 Z', ACC), circle(75, 49, 3, BG)],
  },
  'kt-storage-class': {
    label: 'Storage class',
    group: 'resources',
    draw: () => [
      path('M12 34 V68 A17 6 0 0 0 46 68 V34 Z'),
      ellipse(29, 34, 17, 6, ACC),
      rect(56, 60, 8, 14, 2),
      rect(68, 48, 8, 26, 2),
      rect(80, 36, 8, 38, 2, ACC),
    ],
  },
  'kt-lease': {
    label: 'Lease',
    group: 'resources',
    draw: () => [path('M28 14 H72 V23 L55 50 L72 77 V86 H28 V77 L45 50 L28 23 Z'), path('M38 77 L50 63 L62 77 Z', ACC), path('M39 27 H61 L50 40 Z', ACC)],
  },
  'kt-account': {
    label: 'Account',
    group: 'resources',
    draw: () => [rect(22, 16, 56, 70, 8), rect(41, 10, 18, 12, 4), path(hex(50, 42, 12), ACC), rect(33, 62, 34, 5, 2.5, BG), rect(38, 72, 24, 5, 2.5, BG)],
  },
  'kt-binding': {
    label: 'Binding',
    group: 'resources',
    draw: () => [ring(37, 50, 17, INK, 8), ring(63, 50, 17, ACC, 8)],
  },
  'kt-custom-resource': {
    label: 'Custom resource',
    group: 'resources',
    draw: () => [stroke(hex(50, 50, 32), INK, 7), rect(46, 34, 8, 32, 4, ACC), rect(34, 46, 32, 8, 4, ACC)],
  },
  'kt-event': {
    label: 'Event',
    group: 'resources',
    draw: () => [path('M16 18 H84 V64 H48 L32 80 V64 H16 Z'), path('M55 24 L39 46 H50 L45 60 L63 36 H52 Z', ACC)],
  },
  'kt-limit-range': {
    label: 'Limit range',
    group: 'resources',
    draw: () => [stroke('M14 28 V72 M86 28 V72', INK, 6), rect(14, 46, 72, 8, 4), circle(36, 50, 10, ACC), circle(64, 50, 10)],
  },
  'kt-disruption-budget': {
    label: 'Disruption budget',
    group: 'resources',
    draw: () => [path(hex(26, 38, 11)), path(hex(50, 38, 11)), stroke(hex(74, 38, 10), ACC, 4), rect(14, 62, 72, 12, 6), rect(14, 62, 48, 12, 6, ACC)],
  },

  // --- Actions: what the console does to an object --------------------------
  'kt-port-forward': {
    label: 'Port forward',
    group: 'actions',
    draw: () => [rect(8, 43, 7, 5, 1), rect(8, 53, 7, 5, 1), rect(14, 38, 26, 24, 5), stroke('M44 50 H72', ACC, 7), path('M68 37 L86 50 L68 63 Z', ACC)],
  },
  'kt-node-shell': {
    label: 'Node shell',
    group: 'actions',
    draw: () => [rect(14, 18, 72, 64, 8), rect(22, 26, 56, 4, 2, BG), stroke('M28 44 L39 53 L28 62', ACC, 6), rect(44, 59, 22, 5, 2.5, BG)],
  },
  'kt-edit': {
    label: 'Edit',
    group: 'actions',
    draw: () => [
      rect(14, 14, 50, 66, 7),
      rect(22, 28, 34, 5, 2.5, BG),
      rect(22, 40, 26, 5, 2.5, BG),
      rect(22, 52, 30, 5, 2.5, BG),
      path('M54 84 L58 66 L80 44 L90 54 L68 76 Z', ACC),
    ],
  },
  'kt-plan': {
    label: 'Plan',
    group: 'actions',
    draw: () => [rect(14, 18, 72, 64, 7), stroke('M14 40 H86 M14 60 H86 M38 18 V82 M62 18 V82', BG, 2), stroke('M24 72 L50 50 L76 28', ACC, 6), circle(76, 28, 6, ACC)],
  },
  'kt-scale': {
    label: 'Scale',
    group: 'actions',
    draw: () => [rect(14, 52, 26, 26, 5), rect(44, 52, 26, 26, 5), rect(29, 22, 26, 26, 5), rect(64, 28, 20, 6, 3, ACC), rect(71, 21, 6, 20, 3, ACC)],
  },
  'kt-restart': {
    label: 'Restart',
    group: 'actions',
    draw: () => [
      stroke('M26 46 A25 25 0 0 1 72 38', INK, 8),
      path('M78 24 L79 46 L59 41 Z'),
      stroke('M74 54 A25 25 0 0 1 28 62', ACC, 8),
      path('M22 76 L21 54 L41 59 Z', ACC),
    ],
  },
  'kt-cordon': {
    label: 'Cordon',
    group: 'actions',
    draw: () => [rect(20, 16, 60, 42, 7), rect(30, 28, 40, 5, 2.5, BG), rect(8, 64, 84, 14, 3, ACC), ...[14, 34, 54, 74].map((x) => path(`M${String(x)} 64 H${String(x + 10)} L${String(x + 4)} 78 H${String(x - 6)} Z`, BG))],
  },
  'kt-drain': {
    label: 'Drain',
    group: 'actions',
    draw: () => [rect(18, 14, 64, 38, 7), stroke('M32 58 V76 M50 58 V76 M68 58 V76', ACC, 5), ...[32, 50, 68].map((x) => path(`M${String(x - 7)} 72 L${String(x)} 86 L${String(x + 7)} 72 Z`, ACC))],
  },
  'kt-evict': {
    label: 'Evict',
    group: 'actions',
    draw: () => [frame(12, 22, 44, 56, 8, INK, 6), path(hex(34, 50, 12)), stroke('M48 50 H78', ACC, 7), path('M74 39 L88 50 L74 61 Z', ACC)],
  },
  'kt-pause': {
    label: 'Pause',
    group: 'actions',
    draw: () => [circle(50, 50, 33), rect(37, 33, 9, 34, 3, ACC), rect(54, 33, 9, 34, 3, ACC)],
  },

  // --- Insight: how the console shows what is happening ----------------------
  'kt-watch': {
    label: 'Watch',
    group: 'insight',
    draw: () => [path('M10 50 C26 26 74 26 90 50 C74 74 26 74 10 50 Z'), circle(50, 50, 15, BG), circle(50, 50, 8, ACC)],
  },
  'kt-health': {
    label: 'Health',
    group: 'insight',
    draw: () => [path('M50 84 C20 64 13 45 21 32 C29 19 46 21 50 34 C54 21 71 19 79 32 C87 45 80 64 50 84 Z'), rect(46, 38, 8, 26, 2, ACC), rect(37, 47, 26, 8, 2, ACC)],
  },
  'kt-changes': {
    label: 'Changes',
    group: 'insight',
    draw: () => [
      rect(10, 22, 32, 44, 5),
      rect(58, 34, 32, 44, 5),
      rect(16, 32, 20, 4, 2, BG),
      rect(64, 44, 20, 4, 2, BG),
      stroke('M42 50 H60', ACC, 6),
      path('M56 42 L66 50 L56 58 Z', ACC),
    ],
  },
  'kt-recorder': {
    label: 'Recorder',
    group: 'insight',
    draw: () => [circle(50, 50, 33), ring(50, 50, 23, BG, 3), circle(50, 50, 13, ACC)],
  },
  'kt-archive': {
    label: 'Archive',
    group: 'insight',
    draw: () => [rect(14, 22, 72, 15, 4), rect(20, 40, 60, 42, 5), stroke('M50 47 V68', ACC, 6), path('M41 62 L50 73 L59 62 Z', ACC)],
  },
  'kt-timeline': {
    label: 'Timeline',
    group: 'insight',
    draw: () => [stroke('M10 50 H90', INK, 5), circle(26, 50, 9), circle(50, 50, 9, ACC), circle(74, 50, 9), rect(17, 24, 18, 9, 3), rect(41, 67, 18, 9, 3, ACC), rect(65, 24, 18, 9, 3)],
  },
  'kt-search': {
    label: 'Search',
    group: 'insight',
    draw: () => [stroke('M58 58 L82 82', INK, 11), ring(42, 42, 22, INK, 9), circle(42, 42, 8, ACC)],
  },
  'kt-filter': {
    label: 'Filter',
    group: 'insight',
    draw: () => [path('M12 18 H88 L58 52 V78 L42 88 V52 Z'), rect(42, 52, 16, 7, 2, ACC)],
  },
  'kt-usage': {
    label: 'Usage',
    group: 'insight',
    draw: () => [rect(12, 16, 76, 68, 8), rect(24, 52, 9, 22, 2, ACC), rect(38, 40, 9, 34, 2, BG), rect(52, 46, 9, 28, 2, BG), rect(66, 30, 9, 44, 2, ACC)],
  },
  'kt-status': {
    label: 'Status',
    group: 'insight',
    draw: () => [rect(33, 10, 34, 80, 11), circle(50, 27, 9, BG), circle(50, 50, 9, ACC), circle(50, 73, 9, BG)],
  },

  // --- Console: the desktop shell itself -----------------------------------
  'kt-context': {
    label: 'Context',
    group: 'console',
    draw: () => [stroke('M18 36 H72', INK, 7), path('M66 24 L84 36 L66 48 Z'), stroke('M82 64 H28', ACC, 7), path('M34 52 L16 64 L34 76 Z', ACC)],
  },
  'kt-kubeconfig': {
    label: 'Kubeconfig',
    group: 'console',
    draw: () => [
      rect(18, 12, 52, 66, 7),
      rect(26, 26, 34, 5, 2.5, BG),
      rect(26, 38, 24, 5, 2.5, BG),
      circle(66, 68, 13, BG),
      ring(66, 68, 9, ACC, 5),
      stroke('M73 75 L86 88 M82 84 L86 80', ACC, 5),
    ],
  },
  'kt-hotbar': {
    label: 'Hotbar',
    group: 'console',
    draw: () => [rect(8, 36, 84, 28, 7), ...[15, 33, 51, 69].map((x, at) => rect(x, 42, 15, 16, 3, at === 1 ? ACC : BG)), circle(40, 74, 3.5, ACC)],
  },
  'kt-sign-in': {
    label: 'Sign in',
    group: 'console',
    draw: () => [rect(50, 14, 36, 72, 5), circle(58, 52, 3.5, BG), stroke('M10 50 H44', ACC, 7), path('M38 38 L52 50 L38 62 Z', ACC)],
  },
  'kt-read-only': {
    label: 'Read only',
    group: 'console',
    draw: () => [path('M10 50 C26 26 74 26 90 50 C74 74 26 74 10 50 Z'), circle(50, 50, 10, BG), stroke('M20 82 L80 18', BG, 12), stroke('M20 82 L80 18', ACC, 7)],
  },
  'kt-desktop': {
    label: 'Desktop',
    group: 'console',
    draw: () => [rect(18, 20, 64, 44, 6), rect(24, 26, 52, 32, 3, BG), circle(50, 42, 7, ACC), path('M8 68 H92 L85 80 H15 Z')],
  },
  'kt-update': {
    label: 'Update',
    group: 'console',
    draw: () => [circle(50, 50, 33), stroke('M50 28 V60', ACC, 8), path('M37 52 L50 68 L63 52 Z', ACC)],
  },
  'kt-release': {
    label: 'Release',
    group: 'console',
    draw: () => [path('M14 24 H54 L68 38 L54 52 H14 Z'), path('M30 48 H70 L84 62 L70 76 H30 Z', ACC), circle(24, 38, 4, BG)],
  },
  'kt-extra': {
    label: 'Extra',
    group: 'console',
    draw: () => [rect(22, 22, 56, 50, 11), rect(36, 36, 8, 18, 4, ACC), rect(56, 36, 8, 18, 4, ACC), stroke('M50 72 V90', INK, 7)],
  },
  'kt-settings': {
    label: 'Settings',
    group: 'console',
    draw: () => [stroke('M16 28 H84 M16 50 H84 M16 72 H84', INK, 6), circle(34, 28, 9, ACC), circle(64, 50, 9), circle(42, 72, 9, ACC)],
  },

  // --- Clusters: one family, a frame and the cluster's purpose inside ---------
  'kt-production-cluster': { label: 'Production cluster', group: 'clusters', draw: () => cluster(path(star(50, 52, 18, 7.5))) },
  'kt-staging-cluster': {
    label: 'Staging cluster',
    group: 'clusters',
    draw: () => cluster(rect(33, 58, 10, 9, 1.5), rect(45, 48, 10, 19, 1.5), rect(57, 38, 10, 29, 1.5)),
  },
  'kt-development-cluster': {
    label: 'Development cluster',
    group: 'clusters',
    draw: () => cluster(stroke('M42 39 L31 50 L42 61 M58 39 L69 50 L58 61', INK, 5.5)),
  },
  'kt-test-cluster': { label: 'Test cluster', group: 'clusters', draw: () => cluster(stroke('M34 51 L46 62 L66 38', INK, 7)) },
  'kt-sandbox-cluster': {
    label: 'Sandbox cluster',
    group: 'clusters',
    draw: () => cluster(frame(32, 35, 36, 30, 4, INK, 4, { 'stroke-dasharray': '6 4' }), path(hex(50, 50, 7))),
  },
  'kt-preview-cluster': { label: 'Preview cluster', group: 'clusters', draw: () => cluster(path('M42 35 L67 50 L42 65 Z')) },
  'kt-tooling-cluster': {
    label: 'Tooling cluster',
    group: 'clusters',
    draw: () => cluster(stroke('M36 65 L53 48', INK, 8), ring(58, 43, 8, INK, 6), circle(63, 38, 4.5, BG)),
  },
  'kt-ci-cluster': {
    label: 'CI cluster',
    group: 'clusters',
    draw: () => cluster(stroke('M50 50 C43 40 30 40 30 50 C30 60 43 60 50 50 C57 40 70 40 70 50 C70 60 57 60 50 50', INK, 5)),
  },
  'kt-monitoring-cluster': {
    label: 'Monitoring cluster',
    group: 'clusters',
    draw: () => cluster(stroke('M27 52 H37 L43 39 L52 63 L58 47 H73', INK, 5)),
  },
  'kt-logging-cluster': {
    label: 'Logging cluster',
    group: 'clusters',
    draw: () => cluster(rect(33, 37, 34, 5.5, 2.75), rect(33, 47.5, 24, 5.5, 2.75), rect(33, 58, 29, 5.5, 2.75)),
  },
  'kt-data-cluster': {
    label: 'Data cluster',
    group: 'clusters',
    draw: () => cluster(path('M35 40 V60 A15 5.5 0 0 0 65 60 V40 Z'), ellipse(50, 40, 15, 5.5, ACC)),
  },
  'kt-ml-cluster': {
    label: 'ML cluster',
    group: 'clusters',
    draw: () =>
      cluster(
        stroke('M35 40 L50 50 L35 60 M65 40 L50 50 L65 60 M35 40 L65 40 M35 60 L65 60', INK, 3),
        circle(35, 40, 5),
        circle(35, 60, 5),
        circle(65, 40, 5),
        circle(65, 60, 5),
        circle(50, 50, 6),
      ),
  },
  'kt-edge-cluster': {
    label: 'Edge cluster',
    group: 'clusters',
    draw: () => cluster(circle(39, 61, 6), stroke('M46 50 A14 14 0 0 1 52 66 M48 38 A26 26 0 0 1 64 64', INK, 5)),
  },
  'kt-multi-region-cluster': {
    label: 'Multi region cluster',
    group: 'clusters',
    draw: () => cluster(ring(50, 50, 17, INK, 4.5), stroke('M33 50 H67 M50 33 C41 41 41 59 50 67 C59 59 59 41 50 33', INK, 3.5)),
  },
  'kt-dr-cluster': {
    label: 'DR cluster',
    group: 'clusters',
    draw: () =>
      cluster(
        ring(50, 50, 13, INK, 9),
        rect(47, 32, 6, 9, 1.5, BG),
        rect(47, 59, 6, 9, 1.5, BG),
        rect(32, 47, 9, 6, 1.5, BG),
        rect(59, 47, 9, 6, 1.5, BG),
      ),
  },
  'kt-secure-cluster': {
    label: 'Secure cluster',
    group: 'clusters',
    draw: () => cluster(path('M50 31 L67 38 V50 C67 60 59 67 50 71 C41 67 33 60 33 50 V38 Z')),
  },
  'kt-hub-cluster': {
    label: 'Hub cluster',
    group: 'clusters',
    draw: () =>
      cluster(
        stroke('M50 50 L35 36 M50 50 L65 36 M50 50 L35 64 M50 50 L65 64', INK, 3.5),
        circle(35, 36, 5),
        circle(65, 36, 5),
        circle(35, 64, 5),
        circle(65, 64, 5),
        path(hex(50, 50, 9)),
      ),
  },
  'kt-batch-cluster': {
    label: 'Batch cluster',
    group: 'clusters',
    draw: () => cluster(path('M50 35 L69 44 L50 53 L31 44 Z'), stroke('M31 52 L50 61 L69 52 M31 60 L50 69 L69 60', INK, 4)),
  },
  'kt-local-cluster': {
    label: 'Local cluster',
    group: 'clusters',
    draw: () => cluster(rect(34, 37, 32, 21, 3), path('M28 61 H72 L68 67 H32 Z'), path(hex(50, 47.5, 5.5), BG)),
  },
  'kt-ephemeral-cluster': {
    label: 'Ephemeral cluster',
    group: 'clusters',
    draw: () => cluster(path('M38 33 H62 V37 L53 50 L62 63 V67 H38 V63 L47 50 L38 37 Z')),
  },
};

export type InfraGroup =
  | 'clusters'
  | 'kubernetes'
  | 'servers'
  | 'network'
  | 'data'
  | 'platform'
  | 'security'
  | 'operations'
  | 'delivery'
  | 'resources'
  | 'actions'
  | 'insight'
  | 'console';

/** A status badge in a corner, or none, chosen by the seed. */
const BADGES: Record<string, { elements: Element[]; weight?: number }> = {
  none: { elements: [], weight: 3 },
  dot: { elements: [circle(84, 16, 8, ACC)] },
  corner: { elements: [path('M100 0 V30 L70 0 Z', ACC)] },
  bar: { elements: [rect(0, 92, 100, 8, 0, ACC)] },
  ring: { elements: [ring(50, 50, 46, ACC, 3)] },
};

const hexes = (values: string[]): string[] => values.map((value) => `#${value}`);

/** One subject as a DiceBear style definition. */
function definition(id: string): unknown {
  const subject = SUBJECTS[id];
  if (subject === undefined) throw new Error(`no infra style ${id}`);
  return {
    meta: {
      license: { name: 'AGPL-3.0-only', url: 'https://www.gnu.org/licenses/agpl-3.0.html' },
      creator: { name: 'KubeTower' },
      source: { name: `KubeTower infra · ${subject.label}` },
    },
    attributes: { fill: 'none', 'shape-rendering': 'auto' },
    canvas: {
      width: 100,
      height: 100,
      elements: [
        { type: 'component', name: 'subject' },
        { type: 'component', name: 'badge' },
      ],
    },
    components: {
      subject: {
        width: 100,
        height: 100,
        scale: { min: 0.92, max: 1.04 },
        variants: { default: { elements: subject.draw() } },
      },
      badge: { width: 100, height: 100, variants: BADGES },
    },
    colors: {
      background: { values: hexes(['326ce5', '1f2937', '0f766e', '6d28d9', 'b45309', 'be123c', '15803d', '0e7490']) },
      ink: { values: hexes(['ffffff', '10131a']), contrastTo: 'background' },
      accent: {
        values: hexes(['3fb950', '58a6ff', 'f2cc60', 'ff7b72', 'd2a8ff', '56d4dd', 'ffa657']),
        notEqualTo: ['background', 'ink'],
      },
    },
  };
}


// --- Abstract: eight motifs in five layouts -----------------------------------

/** Each motif's shapes on a 100x100 component canvas, filled with `colour`;
 *  the seed picks one per slot, its rotation and its color. */
const MOTIFS: Readonly<Record<string, (colour: ColourRef) => Record<string, Element[]>>> = {
  circles: (c) => ({
    full: [circle(50, 50, 46, c)],
    ring: [ring(50, 50, 38, c, 16)],
    half: [path('M4 50 A46 46 0 0 1 96 50 Z', c)],
    quarter: [path('M4 96 V4 A92 92 0 0 1 96 96 Z', c)],
  }),
  squares: (c) => ({
    full: [rect(6, 6, 88, 88, 0, c)],
    round: [rect(6, 6, 88, 88, 22, c)],
    outline: [frame(14, 14, 72, 72, 4, c, 16)],
    split: [rect(6, 6, 40, 88, 4, c), rect(54, 6, 40, 88, 4, c)],
  }),
  triangles: (c) => ({
    up: [path('M50 6 L94 90 H6 Z', c)],
    corner: [path('M6 6 H94 L6 94 Z', c)],
    outline: [stroke('M50 16 L86 84 H14 Z', c, 14)],
    twin: [path('M6 94 L50 6 L50 94 Z', c), path('M60 94 L94 30 V94 Z', c)],
  }),
  hexagons: (c) => ({
    full: [path(hex(50, 50, 46), c)],
    outline: [stroke(hex(50, 50, 38), c, 14)],
    trio: [path(hex(30, 32, 22), c), path(hex(70, 32, 22), c), path(hex(50, 70, 22), c)],
  }),
  diamonds: (c) => ({
    full: [path('M50 4 L96 50 L50 96 L4 50 Z', c)],
    outline: [stroke('M50 14 L86 50 L50 86 L14 50 Z', c, 14)],
    kite: [path('M50 4 L86 40 L50 96 L14 40 Z', c)],
    pair: [path('M28 10 L50 50 L28 90 L6 50 Z', c), path('M72 10 L94 50 L72 90 L50 50 Z', c)],
  }),
  rings: (c) => ({
    thin: [ring(50, 50, 42, c, 8)],
    thick: [ring(50, 50, 34, c, 24)],
    double: [ring(50, 50, 42, c, 7), ring(50, 50, 22, c, 7)],
    arc: [stroke('M14 50 A36 36 0 0 1 86 50', c, 16)],
  }),
  bars: (c) => ({
    one: [rect(6, 36, 88, 28, 14, c)],
    two: [rect(6, 18, 88, 24, 12, c), rect(6, 58, 88, 24, 12, c)],
    three: [rect(6, 8, 88, 18, 9, c), rect(6, 41, 88, 18, 9, c), rect(6, 74, 88, 18, 9, c)],
    steps: [rect(6, 62, 26, 32, 4, c), rect(37, 38, 26, 56, 4, c), rect(68, 14, 26, 80, 4, c)],
  }),
  dots: (c) => ({
    grid: [20, 50, 80].flatMap((x) => [20, 50, 80].map((y) => circle(x, y, 11, c))),
    one: [circle(50, 50, 30, c)],
    row: [circle(18, 50, 14, c), circle(50, 50, 14, c), circle(82, 50, 14, c)],
    corners: [circle(22, 22, 16, c), circle(78, 78, 16, c)],
  }),
};

/** Where each slot sits, as a translate and a scale on the 100x100 canvas. */
const LAYOUTS: Readonly<Record<string, readonly (readonly [number, number, number])[]>> = {
  solo: [[14, 14, 0.72]],
  pair: [
    [6, 6, 0.58],
    [46, 46, 0.48],
  ],
  quad: [
    [8, 8, 0.4],
    [52, 8, 0.4],
    [8, 52, 0.4],
    [52, 52, 0.4],
  ],
  stack: [
    [6, 6, 0.88],
    [22, 22, 0.56],
    [37, 37, 0.26],
  ],
  diagonal: [
    [4, 4, 0.42],
    [29, 29, 0.42],
    [54, 54, 0.42],
  ],
};

/** The slots' colors: the first is the accent, so the dialog's Ink control
 *  colors it; the others differ from it and from the background. */
const SLOT_COLOURS = ['accent', 'c2', 'c3', 'c4'] as const;

function abstractDefinition(motif: string, layout: string): unknown {
  const shapes = MOTIFS[motif];
  const slots = LAYOUTS[layout];
  if (shapes === undefined || slots === undefined) throw new Error(`no abstract style ${motif}-${layout}`);
  const components: Record<string, unknown> = {};
  const elements: Element[] = [];
  slots.forEach(([x, y, scale], at) => {
    const name = `slot${String(at + 1)}`;
    const colour = { type: 'color', name: SLOT_COLOURS[at] ?? 'accent' } as unknown as ColourRef;
    components[name] = {
      width: 100,
      height: 100,
      rotate: { min: 0, max: 270, step: 90 },
      variants: Object.fromEntries(Object.entries(shapes(colour)).map(([key, value]) => [key, { elements: value }])),
    };
    elements.push(el('g', { transform: `translate(${n(x)} ${n(y)}) scale(${n(scale)})` }, [{ type: 'component', name }]));
  });
  const palette = hexes(['3fb950', '58a6ff', 'f2cc60', 'ff7b72', 'd2a8ff', '56d4dd', 'ffa657', 'ffffff']);
  return {
    meta: {
      license: { name: 'AGPL-3.0-only', url: 'https://www.gnu.org/licenses/agpl-3.0.html' },
      creator: { name: 'KubeTower' },
      source: { name: `KubeTower abstract · ${motif} ${layout}` },
    },
    attributes: { fill: 'none', 'shape-rendering': 'auto' },
    canvas: { width: 100, height: 100, elements },
    components,
    colors: {
      background: { values: hexes(['326ce5', '1f2937', '0f766e', '6d28d9', 'b45309', 'be123c', '15803d', '0e7490']) },
      accent: { values: palette, notEqualTo: ['background'] },
      c2: { values: palette, notEqualTo: ['background', 'accent'] },
      c3: { values: palette, notEqualTo: ['background', 'accent', 'c2'] },
      c4: { values: palette, notEqualTo: ['background', 'accent', 'c2', 'c3'] },
    },
  };
}

const ABSTRACT: Readonly<Record<string, () => unknown>> = Object.fromEntries(
  Object.keys(MOTIFS).flatMap((motif) =>
    Object.keys(LAYOUTS).map((layout) => [`kt-${motif}-${layout}`, () => abstractDefinition(motif, layout)]),
  ),
);

// --- Patterns: forty-one repeating textures -------------------------------

/** A texture on a 100x100 tile in two colors, `a` and `b`, over the
 *  background. The seed turns the tile by quarter turns and swaps the two. */
type Texture = (a: ColourRef, b: ColourRef) => Element[];

const range = (count: number): number[] => Array.from({ length: count }, (_, at) => at);
const grid = (count: number, step: number, draw: (x: number, y: number, column: number, row: number) => Element[]): Element[] =>
  range(count).flatMap((row) => range(count).flatMap((column) => draw(column * step, row * step, column, row)));
const wave = (y: number, amplitude: number): string =>
  `M-10 ${n(y)} ` + range(6).map((at) => `q 10 ${n(at % 2 === 0 ? -amplitude : amplitude)} 20 0`).join(' ');

const TEXTURES: Readonly<Record<string, Texture>> = {
  'bold-stripes': (a) => range(5).map((at) => rect(at * 20, 0, 10, 100, 0, a)),
  'diagonal-stripes': (a) => range(10).map((at) => stroke(`M${String(at * 20 - 100)} 100 L${String(at * 20)} 0`, a, 7)),
  pinstripes: (a) => range(10).map((at) => rect(at * 10 + 4, 0, 2, 100, 0, a)),
  bands: (a, b) => [rect(0, 0, 100, 33, 0, a), rect(0, 67, 100, 33, 0, b)],
  checks: (a) => grid(4, 25, (x, y, column, row) => ((column + row) % 2 === 0 ? [rect(x, y, 25, 25, 0, a)] : [])),
  gingham: (a) => [
    ...range(5).map((at) => el('rect', { x: n(at * 20), y: '0', width: '10', height: '100', fill: a, opacity: '.5' })),
    ...range(5).map((at) => el('rect', { x: '0', y: n(at * 20), width: '100', height: '10', fill: a, opacity: '.5' })),
  ],
  tartan: (a, b) => [
    rect(0, 30, 100, 24, 0, a),
    rect(30, 0, 24, 100, 0, a),
    rect(0, 72, 100, 6, 0, b),
    rect(72, 0, 6, 100, 0, b),
    el('rect', { x: '30', y: '30', width: '24', height: '24', fill: b, opacity: '.4' }),
  ],
  polka: (a) => grid(4, 25, (x, y, _column, row) => [circle(x + (row % 2 === 0 ? 8 : 20), y + 12.5, 6, a)]),
  'fine-dots': (a) => grid(8, 12.5, (x, y) => [circle(x + 6.25, y + 6.25, 2, a)]),
  confetti: (a, b) =>
    [
      [12, 14, 0],
      [44, 8, 30],
      [78, 20, 60],
      [24, 46, 15],
      [60, 40, 75],
      [88, 58, 45],
      [10, 78, 80],
      [40, 70, 20],
      [70, 86, 50],
    ].map(([x = 0, y = 0, turn = 0], at) =>
      el('rect', { x: n(x), y: n(y), width: '10', height: '5', rx: '1.5', fill: at % 2 === 0 ? a : b, transform: `rotate(${String(turn)} ${n(x + 5)} ${n(y + 2.5)})` }),
    ),
  zigzag: (a) => range(5).map((at) => stroke(`M-5 ${String(at * 22 + 14)} ` + range(6).map((step) => `l 10 ${step % 2 === 0 ? -10 : 10} l 10 ${step % 2 === 0 ? 10 : -10}`).join(' '), a, 4)),
  chevrons: (a) => range(4).map((at) => path(`M0 ${String(at * 28)} L50 ${String(at * 28 + 20)} L100 ${String(at * 28)} V${String(at * 28 + 12)} L50 ${String(at * 28 + 32)} L0 ${String(at * 28 + 12)} Z`, a)),
  'wave-rows': (a) => range(5).map((at) => stroke(wave(at * 22 + 10, 8), a, 4)),
  ripples: (a) => range(6).map((at) => ring(0, 100, at * 18 + 12, a, 5)),
  target: (a, b) => range(5).map((at) => circle(50, 50, 50 - at * 10, at % 2 === 0 ? a : b)),
  sunburst: (a) =>
    range(12).map((at) => {
      const from = (Math.PI / 6) * at;
      const to = from + Math.PI / 12;
      return path(`M50 50 L${n(50 + 80 * Math.cos(from))} ${n(50 + 80 * Math.sin(from))} L${n(50 + 80 * Math.cos(to))} ${n(50 + 80 * Math.sin(to))} Z`, a);
    }),
  pinwheel: (a, b) => [path('M50 50 L50 0 H100 Z', a), path('M50 50 L100 50 V100 Z', b), path('M50 50 L50 100 H0 Z', a), path('M50 50 L0 50 V0 Z', b)],
  quadrants: (a, b) => [rect(0, 0, 50, 50, 0, a), rect(50, 50, 50, 50, 0, a), circle(75, 25, 18, b), circle(25, 75, 18, b)],
  lattice: (a) => [
    ...range(6).map((at) => stroke(`M${String(at * 20 - 50)} 0 L${String(at * 20 + 50)} 100`, a, 3)),
    ...range(6).map((at) => stroke(`M${String(at * 20 + 50)} 0 L${String(at * 20 - 50)} 100`, a, 3)),
  ],
  harlequin: (a) => grid(3, 34, (x, y, column, row) => ((column + row) % 2 === 0 ? [path(`M${n(x + 17)} ${n(y)} L${n(x + 34)} ${n(y + 17)} L${n(x + 17)} ${n(y + 34)} L${n(x)} ${n(y + 17)} Z`, a)] : [])),
  'triangle-tiles': (a, b) => grid(4, 25, (x, y, column, row) => [path(`M${n(x)} ${n(y + 25)} L${n(x + 12.5)} ${n(y)} L${n(x + 25)} ${n(y + 25)} Z`, (column + row) % 2 === 0 ? a : b)]),
  'hex-tiles': (a, b) => grid(4, 26, (x, y, column, row) => [path(hex(x + (row % 2 === 0 ? 10 : 23), y + 10, 11.5), (column + row) % 3 === 0 ? b : a)]),
  honeycomb: (a) => grid(4, 26, (x, y, _column, row) => [stroke(hex(x + (row % 2 === 0 ? 10 : 23), y + 10, 12), a, 3)]),
  scales: (a, b) => grid(5, 20, (x, y, column, row) => [path(`M${n(x)} ${n(y + 20)} A10 10 0 0 1 ${n(x + 20)} ${n(y + 20)} Z`, (column + row) % 2 === 0 ? a : b)]),
  bricks: (a) => range(6).flatMap((row) => range(4).map((column) => rect(column * 34 - (row % 2 === 0 ? 0 : 17), row * 17 + 2, 30, 13, 2, a))),
  herringbone: (a, b) =>
    grid(4, 25, (x, y, column) => [
      el('rect', { x: n(x + 4), y: n(y + 9), width: '20', height: '7', rx: '1', fill: column % 2 === 0 ? a : b, transform: `rotate(${column % 2 === 0 ? 45 : -45} ${n(x + 12.5)} ${n(y + 12.5)})` }),
    ]),
  basketweave: (a, b) =>
    grid(4, 25, (x, y, column, row) =>
      (column + row) % 2 === 0
        ? [rect(x + 2, y + 3, 21, 8, 2, a), rect(x + 2, y + 14, 21, 8, 2, a)]
        : [rect(x + 3, y + 2, 8, 21, 2, b), rect(x + 14, y + 2, 8, 21, 2, b)],
    ),
  crosses: (a) => grid(4, 25, (x, y) => [rect(x + 10, y + 4, 5, 17, 1, a), rect(x + 4, y + 10, 17, 5, 1, a)]),
  'x-marks': (a) => grid(4, 25, (x, y) => [stroke(`M${n(x + 6)} ${n(y + 6)} L${n(x + 19)} ${n(y + 19)} M${n(x + 19)} ${n(y + 6)} L${n(x + 6)} ${n(y + 19)}`, a, 3.5)]),
  circuit: (a, b) => [
    stroke('M0 20 H30 L45 35 H100 M0 60 H20 L35 75 H70 L85 60 H100 M60 0 V20 L70 30 V45 M30 100 V85', a, 3),
    ...[
      [30, 20],
      [45, 35],
      [70, 45],
      [35, 75],
      [70, 75],
      [30, 85],
      [60, 20],
    ].map(([x = 0, y = 0]) => circle(x, y, 4, b)),
  ],
  'network-lattice': (a, b) => [
    ...grid(3, 40, (x, y) => [stroke(`M${n(x + 10)} ${n(y + 10)} L${n(x + 50)} ${n(y + 10)} M${n(x + 10)} ${n(y + 10)} L${n(x + 10)} ${n(y + 50)} M${n(x + 10)} ${n(y + 10)} L${n(x + 50)} ${n(y + 50)}`, a, 2)]),
    ...grid(3, 40, (x, y, column, row) => [circle(x + 10, y + 10, 5, (column + row) % 2 === 0 ? b : a)]),
  ],
  bits: (a, b) => grid(6, 16.6, (x, y, column, row) => ((column * 7 + row * 3) % 5 < 3 ? [rect(x + 2, y + 2, 12, 12, 2, (column + row) % 3 === 0 ? b : a)] : [])),
  'rack-lines': (a, b) => range(6).flatMap((at) => [rect(6, at * 16 + 4, 88, 10, 2, a), circle(14, at * 16 + 9, 2.5, b), circle(22, at * 16 + 9, 2.5, b)]),
  scanlines: (a) => range(16).map((at) => rect(0, at * 6.25 + 2, 100, 2, 0, a)),
  crosshatch: (a) => [
    ...range(10).map((at) => stroke(`M${String(at * 20 - 100)} 100 L${String(at * 20)} 0`, a, 2)),
    ...range(10).map((at) => stroke(`M${String(at * 20 - 100)} 0 L${String(at * 20)} 100`, a, 2)),
  ],
  'truchet-arcs': (a) =>
    grid(4, 25, (x, y, column, row) =>
      (column * 3 + row) % 2 === 0
        ? [stroke(`M${n(x + 12.5)} ${n(y)} A12.5 12.5 0 0 1 ${n(x)} ${n(y + 12.5)} M${n(x + 25)} ${n(y + 12.5)} A12.5 12.5 0 0 0 ${n(x + 12.5)} ${n(y + 25)}`, a, 4)]
        : [stroke(`M${n(x + 12.5)} ${n(y)} A12.5 12.5 0 0 0 ${n(x + 25)} ${n(y + 12.5)} M${n(x)} ${n(y + 12.5)} A12.5 12.5 0 0 1 ${n(x + 12.5)} ${n(y + 25)}`, a, 4)],
    ),
  maze: (a) => grid(5, 20, (x, y, column, row) => [stroke((column * 5 + row * 3) % 3 === 0 ? `M${n(x)} ${n(y)} L${n(x + 20)} ${n(y + 20)}` : `M${n(x + 20)} ${n(y)} L${n(x)} ${n(y + 20)}`, a, 4)]),
  squiggles: (a, b) => range(5).map((at) => el('path', { d: wave(at * 22 + 10, 6), fill: 'none', stroke: at % 2 === 0 ? a : b, 'stroke-width': '5', 'stroke-linecap': 'round', transform: 'rotate(90 50 50)' })),
  halftone: (a) => grid(7, 14.3, (x, y, column, row) => [circle(x + 7, y + 7, 1 + ((column + row) / 12) * 6, a)]),
  'split-diagonal': (a, b) => [path('M0 0 H100 L0 100 Z', a), circle(72, 72, 14, b)],
  'nested-squares': (a, b) => range(5).map((at) => rect(at * 10, at * 10, 100 - at * 20, 100 - at * 20, 4, at % 2 === 0 ? a : b)),
};

function patternDefinition(name: string): unknown {
  const texture = TEXTURES[name];
  if (texture === undefined) throw new Error(`no pattern ${name}`);
  const A: ColourRef = ACC;
  const B = { type: 'color', name: 'c2' } as unknown as ColourRef;
  const palette = hexes(['3fb950', '58a6ff', 'f2cc60', 'ff7b72', 'd2a8ff', '56d4dd', 'ffa657', 'ffffff']);
  return {
    meta: {
      license: { name: 'AGPL-3.0-only', url: 'https://www.gnu.org/licenses/agpl-3.0.html' },
      creator: { name: 'KubeTower' },
      source: { name: `KubeTower pattern · ${name}` },
    },
    attributes: { fill: 'none', 'shape-rendering': 'auto' },
    canvas: { width: 100, height: 100, elements: [{ type: 'component', name: 'tile' }] },
    components: {
      tile: {
        width: 100,
        height: 100,
        rotate: { min: 0, max: 270, step: 90 },
        variants: { plain: { elements: texture(A, B) }, swapped: { elements: texture(B, A) } },
      },
    },
    colors: {
      background: { values: hexes(['326ce5', '1f2937', '0f766e', '6d28d9', 'b45309', 'be123c', '15803d', '0e7490']) },
      accent: { values: palette, notEqualTo: ['background'] },
      c2: { values: palette, notEqualTo: ['background', 'accent'] },
    },
  };
}

const PATTERNS: Readonly<Record<string, () => unknown>> = Object.fromEntries(
  Object.keys(TEXTURES).map((name) => [`kt-${name}`, () => patternDefinition(name)]),
);

export const STYLE_IDS = [...Object.keys(SUBJECTS), ...Object.keys(ABSTRACT), ...Object.keys(PATTERNS)];

/** Every style's definition, by id - what `iconRender` validates once each. */
export const STYLE_DEFINITIONS: Readonly<Record<string, unknown>> = Object.fromEntries([
  ...Object.keys(SUBJECTS).map((id) => [id, definition(id)]),
  ...Object.entries(ABSTRACT).map(([id, build]) => [id, build()]),
  ...Object.entries(PATTERNS).map(([id, build]) => [id, build()]),
]);
