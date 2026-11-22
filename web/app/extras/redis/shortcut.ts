import type { ExtensionShortcutTarget, ExtensionTabProps } from '@kubetower/ui';

import { RedisProblem, getInventory, getService, type Deployment } from './data';
import { instanceHref } from './parts';
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
const notRedis = (error: unknown) => error instanceof RedisProblem && (error.code === 'not_redis' || error.code === 'gate');

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
    if (notRedis(error)) return null;
    throw error;
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
