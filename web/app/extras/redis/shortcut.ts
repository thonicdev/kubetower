import type { ExtensionShortcutTarget, ExtensionTabObject, ExtensionTabProps } from '@kubetower/ui';

import { RedisProblem, getInventory, getService, type Deployment } from './data';
import { instanceHref, inventoryHref } from './parts';
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

/** The words a Redis or Valkey pod's own labels carry. */
const ENGINE = /redis|valkey/;
const TOOL = /exporter|commander|insight|sentinel|operator|metrics|admin|browser|proxy/;

/** Whether a pod's labels say it is a Redis or a Valkey server. */
export function labelledRedis(object: ExtensionTabObject): boolean {
  return ['app.kubernetes.io/name', 'app'].some((key) => {
    const value = (object.labels[key] ?? '').toLowerCase();
    return ENGINE.test(value) && !TOOL.test(value);
  });
}

/** A Service: the deployment its Redis doors belong to. */
export async function serviceShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    const answer = await getService(cluster, namespace, name, signal);
    // Only through one of its own Redis doors: a Service in front of
    // Sentinel ports alone holds no keys, as for a Pod.
    const own = answer.deployments.find((d) => d.members.some((m) => m.sentinel !== true && m.doors.some((door) => door.service === name)));
    return own === undefined ? null : deploymentShortcut(cluster, own);
  } catch (error) {
    if (notRedis(error) || signal.aborted) return null;
    // The Service's Redis tab says what could not be read.
    const here = `/c/${encodeURIComponent(cluster)}/services/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
    return notChecked(name, error, `${here}?tab=x.redis.backend`);
  }
}

/**
 * A Pod: the deployment one of whose Redis members it is behind, from the
 * namespace's inventory - one listing of its Services and EndpointSlices, read
 * for the person, dialling nothing. A pod behind a Sentinel port only is not a
 * Redis to open.
 */
export async function podShortcut({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  try {
    const inventory = await getInventory(cluster, namespace, signal);
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
    await getInventory(cluster, namespace, signal);
    return null;
  } catch (error) {
    if (notRedis(error) || signal.aborted) return null;
    return notChecked(name, error, inventoryHref(cluster, namespace));
  }
}
