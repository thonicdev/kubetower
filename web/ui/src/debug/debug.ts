import type { Capabilities, PodContainer, ShellOffer } from '../api';
import type { ShellRequest } from '../dock/DockContext';

/**
 * A debug container: a shell in an ephemeral container this console adds to a
 * running pod (`ws/debug`, #146). The rules every entry to it shares - the
 * drawer's Debug control, the Shell menu's section and a pod shell that found
 * no shell - in one place, so the three cannot disagree about which image is a
 * preset, which container a debug container may join, or when it is offered.
 *
 * **Adding one is a change that cannot be taken back.** An ephemeral container
 * stays in the pod's spec until the pod is deleted; nothing removes it. That is
 * why it is opened by a gesture and by nothing else - a reloaded debug tab comes
 * back dormant (`tabs.ts`, `restore`) - and why the form says so on a badge.
 */

/** The presets. Pinned tags, because `latest` would make two sessions on one
 *  pod two different toolboxes, and the pull is the slow part of the wait. */
export const DEBUG_IMAGES = [
  { label: 'busybox', image: 'busybox:1.36' },
  { label: 'netshoot', image: 'nicolaka/netshoot:v0.13' },
] as const;

/** The kept badge's tooltip: what stays in the pod, said where the badge is. */
export const KEPT_TITLE =
  'An ephemeral container cannot be removed: it stays in the pod until the pod is deleted';

/**
 * A free image reference the server can be handed: not empty, and no white
 * space anywhere. The server checks it again (`internal/terminal/debug.go`)
 * and refuses with 400; this is the half that keeps Start grayed instead.
 */
export function validImage(image: string): boolean {
  return image !== '' && !/\s/.test(image);
}

/** The repository and the tag of a reference, lower-cased, without a digest.
 *  The tag is what follows the last `:` **after the last `/`**, so a registry's
 *  port (`localhost:5000/app`) is not read as one. */
function splitReference(image: string): { path: string; tag: string } {
  const ref = image.trim().toLowerCase().split('@')[0] ?? '';
  const slash = ref.lastIndexOf('/');
  const colon = ref.lastIndexOf(':');
  return colon > slash ? { path: ref.slice(0, colon), tag: ref.slice(colon + 1) } : { path: ref, tag: '' };
}

/** The image's own short name - `netshoot` for `nicolaka/netshoot:v0.13` - for
 *  a tab label, where the registry and the tag are noise. */
export function shortImage(image: string): string {
  const { path } = splitReference(image);
  return path.slice(path.lastIndexOf('/') + 1) || image;
}

/**
 * Whether an image probably carries no shell, read from its reference alone,
 * with no call: distroless (`gcr.io/distroless/…`), Chainguard's images
 * (`cgr.dev/…`, `…/chainguard/…`) and `scratch`.
 *
 * **Their shell-carrying variants are left out**: distroless's `:debug` tags
 * (`:debug`, `:debug-nonroot`) and Chainguard's `-dev` ones (`:latest-dev`)
 * ship a busybox shell, and offering Debug on them would be offering the
 * remedy for a fault they do not have.
 *
 * A guess, in the cheap direction: a miss is a Shell that fails with
 * `no_shell`, which the console then remembers (`noShellMemory.ts`).
 */
export function probablyNoShell(image: string | undefined): boolean {
  if (image === undefined) return false;
  const { path, tag } = splitReference(image);
  if (path === '') return false;
  if (tag.startsWith('debug')) return false;
  if (tag === 'dev' || tag.endsWith('-dev') || tag.includes('-dev-')) return false;
  return (
    path.includes('distroless') ||
    path.startsWith('cgr.dev/') ||
    path.includes('/chainguard/') ||
    path === 'scratch' ||
    path.endsWith('/scratch')
  );
}

/**
 * The containers a debug container may join (`targetContainerName`): the pod's
 * own running app containers. Never an init container, which has finished, and
 * never another ephemeral container, which is not part of the workload.
 */
export function debugTargets(containers: readonly PodContainer[] | undefined): PodContainer[] {
  return (containers ?? []).filter(
    (one) => one.init !== true && one.ephemeral !== true && one.status?.state === 'running',
  );
}

/**
 * Whether Debug is drawn on a pod, and why it is grayed where it is.
 *
 * `DetailPage.debug` decides on both profiles, because the server answers it on
 * both: on the desktop it is offered where the channel is served (as `podExec`
 * offers Shell), refused with its sentence on a read-only cluster; under
 * `shared` it is reviewed for the person and the console on both questions the
 * socket asks. `podExec` is asked first because the session ends in an exec:
 * a build that cannot open one has nothing to open a debug shell with.
 *
 * - **not drawn until the page has answered**, as Shell under `shared`;
 * - **not drawn where `debug` is absent**: withheld, as a verb the person may
 *   not perform is;
 * - **drawn grayed with the server's sentence** where it is not permitted.
 *
 * The cosmetic half: the socket reviews again when it opens.
 */
export function debugOn(
  able: Capabilities | undefined,
  offer: ShellOffer | undefined,
  read: boolean,
): { drawn: boolean; refused: string | undefined } {
  if (able?.podExec !== true || !read || offer === undefined) return { drawn: false, refused: undefined };
  if (offer.permitted) return { drawn: true, refused: undefined };
  return {
    drawn: true,
    refused: offer.reason?.message ?? 'The console could not establish whether a debug container may be added here.',
  };
}

/** What the form hands back: the image, and the container whose processes the
 *  debug container joins, absent for none. */
export interface DebugChoice {
  readonly image: string;
  readonly target?: string | undefined;
}

/**
 * The dock tab a Start opens. `debug · <pod> · <image>`: the word first, so a
 * debug session and a shell in the same pod are not the same label, and the
 * image last, so `truncate.ts`'s middle cut keeps both ends.
 */
export function debugRequest(
  on: { cluster: string; namespace: string; pod: string },
  choice: DebugChoice,
): ShellRequest {
  return {
    kind: 'debug',
    cluster: on.cluster,
    label: `debug · ${on.pod} · ${shortImage(choice.image)}`,
    namespace: on.namespace,
    name: on.pod,
    image: choice.image,
    ...(choice.target === undefined || choice.target === '' ? {} : { target: choice.target }),
  };
}
