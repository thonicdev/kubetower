import { useEffect, useSyncExternalStore } from 'react';

import type { ClusterColour } from '../api';
import type * as RenderModule from './iconRender';
import { PALETTE } from './clusterState';

/**
 * A cluster's icon.
 *
 * The tile is drawn from a KubeTower style rendered by DiceBear's engine. What is
 * stored is the *recipe* - the style, the seed and a few options - in
 * `Preferences.rail.nameplates[<context>].icon`, as one JSON object the server
 * holds and does not read. The picture is generated here, in the browser, every
 * time it is drawn.
 *
 * ## Colors are palette keys, never CSS values
 *
 * The same rule as `Cluster.colour`: the recipe names a swatch of the sixteen
 * (`ClusterColour`), and the paint is looked up from the theme when the icon is
 * drawn. So no color value from the wire reaches the SVG, and a recipe does
 * not drift from the theme.
 *
 * ## Every cluster is an icon
 *
 * A cluster with no recipe, or one this build cannot read
 * - another style, a later `v`, a hand-edited file - is drawn from
 * `defaultIcon`, whose background is the cluster's color. The initials are
 * kept only in the tile's accessible name. Where the console keeps preferences,
 * a cluster with no recipe is soon given one drawn at random (`randomIcon`,
 * stored by `firstIcon.ts`), so the default icon is mostly
 * what a tile is drawn as until that write lands, and after *Reset icon*.
 *
 * ## The renderer is a chunk of its own
 *
 * DiceBear's engine and KubeTower's style definitions are `iconRender.ts`,
 * imported dynamically.
 * Every tile is an icon, so it is fetched on every load - but after the main
 * bundle rather than inside it; until it arrives a tile is a neutral square.
 */

/**
 * The styles offered, in fifteen groups - **every one KubeTower's own**
 * (`iconStyles.ts`), and nothing from an icon library: the icons are
 * home-made only, so `@dicebear/core` is used as the engine that draws a
 * style definition and no third-party style is a dependency.
 *
 * Twenty clusters by purpose (production, staging, tooling…); a hundred and
 * twenty subjects from what a cluster is made of and what KubeTower does with
 * it - Kubernetes objects, servers, the network, data, the platform, security,
 * operations, delivery, and the console's own vocabulary: the kinds it lists,
 * the actions it takes, the views it draws, its desktop shell; forty abstract
 * compositions the seed varies; and forty-one patterns - stripes, checks,
 * tilings, rays, circuit and rack lines. Drawn for this console, so no
 * third-party artwork and no attribution. None is a trademark: no ship's
 * wheel, no heptagon, no vendor mark, no characters.
 */
export const ICON_CATEGORIES = [
  { id: 'clusters', label: 'Clusters', styles: [
      'kt-production-cluster',
      'kt-staging-cluster',
      'kt-development-cluster',
      'kt-test-cluster',
      'kt-sandbox-cluster',
      'kt-preview-cluster',
      'kt-tooling-cluster',
      'kt-ci-cluster',
      'kt-monitoring-cluster',
      'kt-logging-cluster',
      'kt-data-cluster',
      'kt-ml-cluster',
      'kt-edge-cluster',
      'kt-multi-region-cluster',
      'kt-dr-cluster',
      'kt-secure-cluster',
      'kt-hub-cluster',
      'kt-batch-cluster',
      'kt-local-cluster',
      'kt-ephemeral-cluster',
    ] },
  { id: 'kubernetes', label: 'Kubernetes', styles: [
      'kt-pod',
      'kt-deployment',
      'kt-replica-set',
      'kt-stateful-set',
      'kt-daemon-set',
      'kt-service',
      'kt-ingress',
      'kt-namespace',
      'kt-config-map',
      'kt-secret',
      'kt-job',
      'kt-cron-job',
    ] },
  { id: 'servers', label: 'Servers', styles: [
      'kt-server',
      'kt-rack',
      'kt-node',
      'kt-cpu',
      'kt-memory',
      'kt-terminal',
      'kt-container',
      'kt-virtual-machine',
      'kt-gpu',
      'kt-power',
    ] },
  { id: 'network', label: 'Network & cloud', styles: [
      'kt-cloud',
      'kt-load-balancer',
      'kt-router',
      'kt-mesh',
      'kt-globe',
      'kt-firewall',
      'kt-gateway',
      'kt-dns',
      'kt-shield',
    ] },
  { id: 'data', label: 'Data & storage', styles: [
      'kt-database',
      'kt-disk',
      'kt-volume',
      'kt-bucket',
      'kt-queue',
      'kt-cache',
      'kt-backup',
      'kt-logs',
      'kt-metrics',
    ] },
  { id: 'platform', label: 'Platform', styles: [
      'kt-cluster',
      'kt-control-plane',
      'kt-api',
      'kt-scheduler',
      'kt-key-value',
      'kt-agent',
      'kt-autoscaler',
      'kt-node-pool',
      'kt-webhook',
      'kt-extension',
    ] },
  { id: 'security', label: 'Security', styles: [
      'kt-lock',
      'kt-certificate',
      'kt-id-card',
      'kt-role',
      'kt-policy',
      'kt-audit',
      'kt-vault',
      'kt-token',
      'kt-network-policy',
      'kt-scan',
    ] },
  { id: 'operations', label: 'Operations', styles: [
      'kt-alert',
      'kt-heartbeat',
      'kt-gauge',
      'kt-quota',
      'kt-trace',
      'kt-dashboard',
      'kt-rollout',
      'kt-rollback',
      'kt-pipeline',
      'kt-hotspot',
    ] },
  { id: 'delivery', label: 'Delivery', styles: [
      'kt-branch',
      'kt-commit',
      'kt-merge',
      'kt-diff',
      'kt-package',
      'kt-registry',
      'kt-build',
      'kt-test',
      'kt-code',
      'kt-rocket',
    ] },
  { id: 'resources', label: 'Resources', styles: [
      'kt-endpoint',
      'kt-claim',
      'kt-storage-class',
      'kt-lease',
      'kt-account',
      'kt-binding',
      'kt-custom-resource',
      'kt-event',
      'kt-limit-range',
      'kt-disruption-budget',
    ] },
  { id: 'actions', label: 'Actions', styles: [
      'kt-port-forward',
      'kt-node-shell',
      'kt-edit',
      'kt-plan',
      'kt-scale',
      'kt-restart',
      'kt-cordon',
      'kt-drain',
      'kt-evict',
      'kt-pause',
    ] },
  { id: 'insight', label: 'Insight', styles: [
      'kt-watch',
      'kt-health',
      'kt-changes',
      'kt-recorder',
      'kt-archive',
      'kt-timeline',
      'kt-search',
      'kt-filter',
      'kt-usage',
      'kt-status',
    ] },
  { id: 'console', label: 'Console', styles: [
      'kt-context',
      'kt-kubeconfig',
      'kt-hotbar',
      'kt-sign-in',
      'kt-read-only',
      'kt-desktop',
      'kt-update',
      'kt-release',
      'kt-extra',
      'kt-settings',
    ] },
  { id: 'abstract', label: 'Abstract', styles: [
      'kt-circles-solo',
      'kt-circles-pair',
      'kt-circles-quad',
      'kt-circles-stack',
      'kt-circles-diagonal',
      'kt-squares-solo',
      'kt-squares-pair',
      'kt-squares-quad',
      'kt-squares-stack',
      'kt-squares-diagonal',
      'kt-triangles-solo',
      'kt-triangles-pair',
      'kt-triangles-quad',
      'kt-triangles-stack',
      'kt-triangles-diagonal',
      'kt-hexagons-solo',
      'kt-hexagons-pair',
      'kt-hexagons-quad',
      'kt-hexagons-stack',
      'kt-hexagons-diagonal',
      'kt-diamonds-solo',
      'kt-diamonds-pair',
      'kt-diamonds-quad',
      'kt-diamonds-stack',
      'kt-diamonds-diagonal',
      'kt-rings-solo',
      'kt-rings-pair',
      'kt-rings-quad',
      'kt-rings-stack',
      'kt-rings-diagonal',
      'kt-bars-solo',
      'kt-bars-pair',
      'kt-bars-quad',
      'kt-bars-stack',
      'kt-bars-diagonal',
      'kt-dots-solo',
      'kt-dots-pair',
      'kt-dots-quad',
      'kt-dots-stack',
      'kt-dots-diagonal',
    ] },
  {
    id: 'patterns',
    label: 'Patterns',
    styles: [
      'kt-bold-stripes',
      'kt-diagonal-stripes',
      'kt-pinstripes',
      'kt-bands',
      'kt-checks',
      'kt-gingham',
      'kt-tartan',
      'kt-polka',
      'kt-fine-dots',
      'kt-confetti',
      'kt-zigzag',
      'kt-chevrons',
      'kt-wave-rows',
      'kt-ripples',
      'kt-target',
      'kt-sunburst',
      'kt-pinwheel',
      'kt-quadrants',
      'kt-lattice',
      'kt-harlequin',
      'kt-triangle-tiles',
      'kt-hex-tiles',
      'kt-honeycomb',
      'kt-scales',
      'kt-bricks',
      'kt-herringbone',
      'kt-basketweave',
      'kt-crosses',
      'kt-x-marks',
      'kt-circuit',
      'kt-network-lattice',
      'kt-bits',
      'kt-rack-lines',
      'kt-scanlines',
      'kt-crosshatch',
      'kt-truchet-arcs',
      'kt-maze',
      'kt-squiggles',
      'kt-halftone',
      'kt-split-diagonal',
      'kt-nested-squares',
    ],
  },
] as const;

export const ICON_STYLES = ICON_CATEGORIES.flatMap((category) => category.styles);
export type IconStyle = (typeof ICON_CATEGORIES)[number]['styles'][number];

/**
 * The eight swatches the right-click menu offers: of the sixteen, the eight
 * furthest apart from each other - an exhaustive search for the set with the
 * largest smallest CIELAB distance between any two (ΔE76 37.6, against 16.9
 * for the eight deep hues). In hue order.
 */
export const QUICK_COLOURS: readonly ClusterColour[] = [
  'red',
  'amber',
  'lime-pale',
  'green',
  'teal',
  'blue-pale',
  'violet',
  'pink-pale',
];

const ACRONYMS = new Set(['api', 'ci', 'cpu', 'dns', 'dr', 'gpu', 'id', 'ml']);

/** `kt-replica-set` is "Replica set", `kt-cpu` "CPU", `kt-hexagons-quad` "Hexagons quad". */
export function iconStyleLabel(style: IconStyle): string {
  const words = style
    .replace(/^kt-/, '')
    .split('-')
    .map((word) => (ACRONYMS.has(word) ? word.toUpperCase() : word));
  const text = words.join(' ');
  return `${text.charAt(0).toUpperCase()}${text.slice(1)}`;
}

export type IconGradient = 'linear' | 'radial';

/** What is stored. Every optional member absent means "the style's default". */
export interface IconRecipe {
  v: 1;
  style: IconStyle;
  seed: string;
  /** A swatch of the sixteen, or absent for the style's own backgrounds. */
  background?: ClusterColour;
  /** Absent is a solid background. A gradient runs from the swatch to its
   *  other half - `teal` to `teal-pale` - so it needs no second choice. */
  gradient?: IconGradient;
  /** The style's `accent` color, a swatch. Every style has one: the subject's
   *  own ink keeps its contrast with the background by itself, and the accent
   *  is what the Ink control sets. Absent is the style's own accent. */
  ink?: ClusterColour;
  /** Degrees, 0-359. */
  rotate?: number;
  /** Percent, 50-150. */
  scale?: number;
  flip?: boolean;
}

/** What the server keeps (`nameplates.MaxIcon`), and what the seed field stops at. */
export const MAX_ICON_BYTES = 1024;
export const MAX_SEED = 64;

const isStyle = (value: unknown): value is IconStyle =>
  typeof value === 'string' && (ICON_STYLES as readonly string[]).includes(value);
const isColour = (value: unknown): value is ClusterColour =>
  typeof value === 'string' && (PALETTE as readonly string[]).includes(value);
const inRange = (value: unknown, min: number, max: number): value is number =>
  typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max;

/** The stored string, read. `undefined` for none, and for anything not
 *  understood - which the tile draws as its color. */
export function parseIcon(raw: string | undefined): IconRecipe | undefined {
  if (raw === undefined || raw === '') return undefined;
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (typeof data !== 'object' || data === null || Array.isArray(data)) return undefined;
  const r = data as Record<string, unknown>;
  if (r.v !== 1 || !isStyle(r.style) || typeof r.seed !== 'string' || r.seed.length > MAX_SEED) return undefined;
  const recipe: IconRecipe = { v: 1, style: r.style, seed: r.seed };
  if (isColour(r.background)) recipe.background = r.background;
  if (r.gradient === 'linear' || r.gradient === 'radial') recipe.gradient = r.gradient;
  if (isColour(r.ink)) recipe.ink = r.ink;
  if (inRange(r.rotate, 0, 359)) recipe.rotate = r.rotate;
  if (inRange(r.scale, 50, 150)) recipe.scale = r.scale;
  if (r.flip === true) recipe.flip = true;
  return recipe;
}

/** The recipe as stored: defaults left out, so equal recipes are equal strings. */
export function serialiseIcon(recipe: IconRecipe): string {
  const out: IconRecipe = { v: 1, style: recipe.style, seed: recipe.seed };
  if (recipe.background !== undefined) out.background = recipe.background;
  if (recipe.background !== undefined && recipe.gradient !== undefined) out.gradient = recipe.gradient;
  if (recipe.ink !== undefined) out.ink = recipe.ink;
  if (recipe.rotate !== undefined && recipe.rotate !== 0) out.rotate = recipe.rotate;
  if (recipe.scale !== undefined && recipe.scale !== 100) out.scale = recipe.scale;
  if (recipe.flip === true) out.flip = true;
  return JSON.stringify(out);
}

/**
 * The icon a cluster has when none was chosen: `kt-hexagons-quad`, seeded with
 * the context name, on the cluster's color - the automatic one, or the swatch
 * stored before icons existed. So every cluster is an icon from
 * the first start, two clusters look different straight away, and a color
 * chosen earlier still decides the default.
 *
 * `kt-hexagons-quad` because it is KubeTower's own (nothing comes from an icon
 * library), the seed varies all four of its slots - shape, quarter
 * turn and color - so neighbors differ, and its hexagons are the console's
 * own motif.
 */
export const DEFAULT_ICON_STYLE: IconStyle = 'kt-hexagons-quad';

export function defaultIcon(context: string, colour: ClusterColour | undefined): IconRecipe {
  const recipe: IconRecipe = { v: 1, style: DEFAULT_ICON_STYLE, seed: context.slice(0, MAX_SEED) };
  if (isColour(colour)) recipe.background = colour;
  return recipe;
}

/**
 * What *Reset icon* stores in the nameplate's `icon` field: a recipe object
 * with no style, which `parseIcon` does not read, so the tile draws its default
 * icon. It is what tells the first-icon draw a cluster was reset on purpose,
 * across reloads and browsers. Choosing an icon replaces it; clearing the
 * field (`icon: ''`) removes it, and the cluster is drawn a random icon again.
 */
export const RESET_ICON = '{"v":1,"reset":true}';

/** Whether a stored icon is the reset marker. */
export const isResetIcon = (stored: string | undefined): boolean => stored === RESET_ICON;

/** What a cluster is drawn as: its stored recipe, or its default icon when it
 *  has none or one this build cannot read. */
export function iconOf(
  context: string,
  colour: ClusterColour | undefined,
  stored: string | undefined,
): IconRecipe {
  return parseIcon(stored) ?? defaultIcon(context, colour);
}

/**
 * The write a new color makes: the cluster's color, and - when an icon is
 * stored - that icon with the color as its background, in the same write. The
 * color is the default icon's background, so a stored icon follows it too;
 * the menu and Arrange both pick colors through this, so they cannot disagree.
 */
export function recolour(key: ClusterColour, stored: string | undefined): { colour: ClusterColour; icon?: string } {
  const recipe = parseIcon(stored);
  return recipe === undefined ? { colour: key } : { colour: key, icon: serialiseIcon({ ...recipe, background: key }) };
}

/** A number in [0, 1) from the browser's generator. */
const cryptoRandom = (): number => (crypto.getRandomValues(new Uint32Array(1))[0] ?? 0) / 2 ** 32;

/** One of `items`, by the dice. */
const pick = <T>(items: readonly T[], random: () => number): T | undefined =>
  items[Math.min(items.length - 1, Math.floor(random() * items.length))];

/** A fresh seed for the dice button: eight base-36 characters. */
export function randomSeed(random: () => number = cryptoRandom): string {
  return Array.from({ length: 8 }, () => Math.min(35, Math.floor(random() * 36)).toString(36)).join('');
}

/**
 * A cluster's first icon: a style of the offered ones, a
 * swatch of the sixteen as its background, and a fresh seed, all drawn by
 * `random`. A color chosen by hand stays the background - chance never
 * undoes a hand choice. Every other member is the style's default, so the
 * recipe is one the dialog could have saved.
 */
export function randomIcon(colour: ClusterColour | undefined, random: () => number = cryptoRandom): IconRecipe {
  const style = pick(ICON_STYLES, random) ?? DEFAULT_ICON_STYLE;
  const background = isColour(colour) ? colour : pick(PALETTE, random);
  const recipe: IconRecipe = { v: 1, style, seed: randomSeed(random) };
  if (background !== undefined) recipe.background = background;
  return recipe;
}

/** The swatch a gradient runs to: the other half of the same hue. */
export function pairOf(key: ClusterColour): ClusterColour {
  return (key.endsWith('-pale') ? key.slice(0, -'-pale'.length) : `${key}-pale`) as ClusterColour;
}

// --- The renderer and the styles, loaded once each and on demand ------------

type Renderer = typeof RenderModule;

let renderer: Renderer | undefined;
let loading: Promise<Renderer> | undefined;
/** Bumped whenever the renderer or a style arrives, so a drawing that was
 *  waiting for either is drawn again. */
let version = 0;
const listeners = new Set<() => void>();

let scheduled = false;

/**
 * Says that something arrived - once per burst. Opening the dialog asks for
 * every style at once, one hook per thumbnail; announcing each arrival on its
 * own re-rendered every subscriber once per style, and each re-render redrew
 * its icon. So arrivals within one turn of the event loop make one change.
 */
function changed(): void {
  if (scheduled) return;
  scheduled = true;
  setTimeout(() => {
    scheduled = false;
    version += 1;
    for (const listener of listeners) listener();
  }, 0);
}

function loadRenderer(): Promise<Renderer> {
  loading ??= import('./iconRender').then(
    (module) => {
      renderer = module;
      changed();
      return module;
    },
    (error: unknown) => {
      // A chunk that failed to load leaves the tile a neutral square. The next
      // draw asks again.
      loading = undefined;
      throw error;
    },
  );
  return loading;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/**
 * The renderer, once loaded, with the styles asked for loaded into it - each
 * style's definition is a chunk of its own, so the rail fetches only the styles
 * its tiles are drawn in, and the dialog the ones it shows. `version` changes
 * whenever something arrives; a memo of a drawing keys on it.
 */
export function useIconRenderer(styles: readonly IconStyle[]): { renderer: Renderer | undefined; version: number } {
  const current = useSyncExternalStore(
    subscribe,
    () => version,
    () => 0,
  );
  const key = [...new Set(styles)].sort().join(',');
  useEffect(() => {
    if (key === '') return;
    void loadRenderer()
      .then((module) => {
        // Only what is missing: a style already loaded has nothing to announce.
        const missing = (key.split(',') as IconStyle[]).filter((style) => !module.isStyleLoaded(style));
        return Promise.all(missing.map((style) => module.loadStyle(style).then(changed)));
      })
      .catch(() => undefined);
  }, [key]);
  return { renderer, version: current };
}
