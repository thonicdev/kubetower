import { objectPath, type ExtensionShortcutTarget, type ExtensionTabProps } from '@kubetower/ui';

import { KEY, getPod, riskWord, type Container } from './data';

/**
 * The JVM Inspector's tile on a pod's Details: drawn as soon as the pod's spec
 * says a container is probably Java (the same spec route as the page and the
 * tab's probe - **nothing runs in the container to draw it**). It names the
 * Java runtime where the image says it, grades the OOMKill risk, and draws the
 * estimated maximum heap against the memory limit with the 75 % line. A click
 * opens the pod on its JVM tab.
 */

/** The tab the tile opens: `x.<extension>.<tab>`, as the panel spells it. */
export const TAB = 'jvm';

/** Known Java distributions, by a word of the image's repository. */
const DISTRIBUTIONS: readonly (readonly [RegExp, string])[] = [
  [/temurin/, 'Temurin'],
  [/corretto/, 'Corretto'],
  [/semeru/, 'Semeru'],
  [/zulu/, 'Zulu'],
  [/liberica/, 'Liberica'],
  [/graalvm/, 'GraalVM'],
  [/sapmachine/, 'SapMachine'],
  [/microsoft\/openjdk|mcr\.microsoft\.com\/openjdk/, 'Microsoft OpenJDK'],
  [/(^|\/)openjdk(:|$)|ubi\d*\/openjdk/, 'OpenJDK'],
];

/**
 * The runtime an image names, "Temurin 21", or `undefined` where the image
 * does not say: a guess from the image's name and tag, never from the JVM.
 */
export function runtimeOf(image: string): string | undefined {
  const lower = image.toLowerCase();
  const slash = lower.lastIndexOf('/');
  const colon = lower.indexOf(':', slash + 1);
  const repo = colon < 0 ? lower : lower.slice(0, colon);
  const tag = colon < 0 ? '' : lower.slice(colon + 1).split('@')[0] ?? '';
  const distribution = DISTRIBUTIONS.find(([pattern]) => pattern.test(repo))?.[1];
  // `lts-jdk17`, `2.3-jdk17`: the number after jdk, jre or java. A leading
  // number (`21-jdk`, `8u402-b06-jre`) is the Java version only on a runtime's
  // own image; on any other it is the application's.
  const major =
    /(?:jdk|jre|java)-?(\d{1,2})(?!\d)/.exec(tag)?.[1] ??
    (distribution === undefined ? undefined : /^(\d{1,2})(?:[.u_-]|$)/.exec(tag)?.[1]);
  if (distribution === undefined) return major === undefined ? undefined : `Java ${major}`;
  return major === undefined ? distribution : `${distribution} ${major}`;
}

const RANK: Readonly<Record<string, number>> = { error: 4, warning: 3, nolimit: 2, unknown: 1, ok: 0 };

/** The container the tile speaks for: the Java one with the worst risk. */
function worst(containers: readonly Container[]): Container | undefined {
  let pick: Container | undefined;
  for (const c of containers) {
    if (!c.java) continue;
    if (pick === undefined || (RANK[c.risk?.level ?? 'unknown'] ?? 0) > (RANK[pick.risk?.level ?? 'unknown'] ?? 0)) pick = c;
  }
  return pick;
}

/** The tile for one pod's answer, or `null` where nothing in it is Java. */
export function tileFor(
  object: { cluster: string; namespace: string; name: string },
  containers: readonly Container[],
): ExtensionShortcutTarget | null {
  const c = worst(containers);
  if (c === undefined) return null;
  const others = containers.filter((each) => each.java).length - 1;
  const runtime = runtimeOf(c.image);
  const out: ExtensionShortcutTarget = {
    title: runtime === undefined ? `Java in ${c.name}` : `${runtime} in ${c.name}`,
    to: `${objectPath({ cluster: object.cluster, kind: 'pods', namespace: object.namespace, name: object.name })}?tab=x.${KEY}.${TAB}`,
  };
  if (others > 0) out.detail = `+${String(others)} Java ${others === 1 ? 'container' : 'containers'}`;
  const risk = c.risk;
  if (risk !== null) {
    if (risk.level === 'ok' || risk.level === 'warning' || risk.level === 'error') {
      out.status = { text: riskWord(risk), tone: risk.level === 'ok' ? 'ok' : risk.level === 'warning' ? 'warn' : 'bad', hint: risk.sentence };
    } else {
      out.badges = [riskWord(risk)];
    }
  }
  const heap = c.heap?.bytes ?? null;
  if (c.limit !== null && heap !== null && c.limit.bytes > 0) {
    const percent = Math.round((heap / c.limit.bytes) * 1000) / 10;
    const tone = risk?.level === 'error' ? 'bad' : risk?.level === 'warning' ? 'warn' : 'ok';
    out.meter = {
      label: 'estimated maximum heap against the memory limit',
      segments: [{ width: percent, tone, label: `maximum heap: ${c.heap?.text ?? ''} (${c.heap?.source ?? ''})` }],
      threshold: { at: 75, label: '75 % of the limit: a heuristic, not a JDK rule' },
      text: `heap ${c.heap?.text ?? ''} of ${c.limit.text}`,
    };
  }
  return out;
}

/** The shortcut's `resolve`: the pod's spec, through the extra's own route. */
export async function podTile({ cluster, namespace, name }: ExtensionTabProps, signal: AbortSignal): Promise<ExtensionShortcutTarget | null> {
  if (namespace === undefined || namespace === '') return null;
  const pod = await getPod(cluster, namespace, name, signal);
  return pod.java ? tileFor({ cluster, namespace, name }, pod.containers) : null;
}
