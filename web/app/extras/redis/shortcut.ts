import type { ExtensionShortcutTarget, ExtensionTabObject, ExtensionTabProps } from '@kubetower/ui';

import { RedisProblem, getService, sharedInventory, type Deployment } from './data';
import { externalAddress, instanceHref, inventoryHref, serviceHref } from './parts';
import { UNKNOWN, engineOfDeployment, mainDoor, modeOf, shapeOf } from './tiles';

/**
 * The Redis viewer's tile on an object's Details (asked for by the operator,
 * as a tile in the right-hand panel): on a Service or a Pod
 * that belongs to a Redis or Valkey deployment, a tile naming the deployment
 * that opens its page - the page the inventory's tile opens.
 *
 * From discovery alone: **nothing is dialled to draw it**, so what it says is
 * what the inventory says before a probe - the engine and mode only where the
 * chart or the members say them, never a Service's name.
 */
export function deploymentShortcut(cluster: string, d: Deployment): ExtensionShortcutTarget | null {
  const door = mainDoor(d);
  if (door === undefined) return null;
  const none = {};
  const mode = modeOf(d, none);
  const badges = [engineOfDeployment(d, none).value, mode.value].filter((word) => word !== UNKNOWN);
  return {
    title: d.name,
    badges,
    detail: [door.namespace, shapeOf(d, none, mode.value)].filter((part) => part !== '').join(' · '),
    to: instanceHref(cluster, door),
  };
}

/** A route answering "not a Redis": no tile, and no error either. */
const notRedis = (error: unknown) =>
  error instanceof RedisProblem && (error.code === 'not_redis' || error.code === 'gate' || error.code === 'unknown_cluster');

/**
 * The tile for an object the routes could not check: a listing refused or
 * failed. "Could not look" must not read as "not a Redis", so it is drawn,
 * with the reason on hover, and leads where the reason is said in full.
 */
function notChecked(title: string, error: unknown, to: string): ExtensionShortcutTarget {
  const hint = error instanceof Error && error.message !== '' ? error.message : 'The Redis routes did not answer.';
  return { title, status: { text: 'not checked', tone: 'warn', hint }, to };
}

/*
 * The label rule, word for word the server's (`extras/redis/evidence.go`,
 * labelSays, isTool, and engineToken in external.go): the engine as a token of
 * its own, and no tool beside the server. `extras/redis/testdata/label-rule.json`
 * is the table both are tested on.
 */
const ENGINES = ['redis', 'valkey'];
const TOOLS = ['exporter', 'commander', 'insight', 'sentinel', 'operator', 'metrics', 'admin', 'browser', 'proxy', 'backup',
  'dump', 'shake', 'tools', 'dashboard', 'redisson'];
const TOOL_TOKENS = new Set(['ui', 'cli', 'client', 'tool', 'gui', 'web', 'stat', 'stats', 'webui', 'webapp']);
const GLUED = new Set(['master', 'main', 'primary', 'replica', 'cache']);

function engineToken(value: string): boolean {
  return value.split(/[.\-_]/).some((token) =>
    ENGINES.some((engine) => token.startsWith(engine) && (/^[0-9]*$/.test(token.slice(engine.length)) || GLUED.has(token.slice(engine.length)))),
  );
}

function isTool(value: string): boolean {
  if (TOOLS.some((word) => value.includes(word))) return true;
  return value
    .split(/[-_.]/)
    .filter((token) => token !== '')
    .some((token) => {
      if (TOOL_TOKENS.has(token)) return true;
      let rest = token;
      for (const engine of ENGINES) rest = rest.split(engine).join('');
      return rest !== token && TOOL_TOKENS.has(rest);
    });
}

/** Whether a label value names a Redis or a Valkey server (the server's labelSays). */
export function labelSays(value: string): boolean {
  const lower = value.toLowerCase();
  return engineToken(lower) && !isTool(lower);
}

/** Whether a pod's labels say it is a Redis or a Valkey server. */
export function labelledRedis(object: ExtensionTabObject): boolean {
  return ['app.kubernetes.io/name', 'app'].some((key) => labelSays(object.labels[key] ?? ''));
}

/** A Service: the deployment its Redis doors belong to. */
export async function serviceShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    const answer = await getService(cluster, namespace, name, signal);
    // Outside the cluster: the tile says so and opens the Service's tab,
    // without a Probe.
    if (answer.external !== undefined) {
      return { title: name, badges: ['external'], detail: externalAddress(answer.external), to: `${serviceHref(cluster, namespace, name)}?tab=x.redis.backend` };
    }
    // Only through one of its own Redis doors: a Service in front of
    // Sentinel ports alone holds no keys, as for a Pod.
    const own = answer.deployments.find((d) => d.members.some((m) => m.sentinel !== true && m.doors.some((door) => door.service === name)));
    return own === undefined ? null : deploymentShortcut(cluster, own);
  } catch (error) {
    if (notRedis(error) || signal.aborted) return null;
    // The Service's Redis tab says what could not be read.
    return notChecked(name, error, `${serviceHref(cluster, namespace, name)}?tab=x.redis.backend`);
  }
}

/**
 * A Pod: the deployment one of whose Redis members it is behind, from the
 * namespace's inventory - one listing of its Services and EndpointSlices, read
 * for the person, dialling nothing, and shared with podNotChecked
 * (sharedInventory): the pod's two tiles come from one read and cannot
 * disagree. A pod behind a Sentinel port only is not a Redis to open.
 */
export async function podShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    const inventory = await sharedInventory(cluster, namespace, signal);
    for (const group of inventory.namespaces) {
      for (const d of group.deployments) {
        if (d.members.some((m) => m.sentinel !== true && m.pods.includes(name))) return deploymentShortcut(cluster, d);
      }
    }
    return null;
  } catch (error) {
    if (notRedis(error)) return null;
    throw error;
  }
}

/**
 * A Pod whose labels say Redis or Valkey, when its namespace could not be
 * read: the tile says it was not checked, and opens the namespace's
 * inventory, which says why. Nothing when the namespace answered: podShortcut
 * draws that one. Any other pod gets nothing on a failure, so a namespace the
 * person cannot list does not put a tile on every pod.
 */
export async function podNotChecked({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    await sharedInventory(cluster, namespace, signal);
    return null;
  } catch (error) {
    if (notRedis(error) || signal.aborted) return null;
    return notChecked(name, error, inventoryHref(cluster, namespace));
  }
}
