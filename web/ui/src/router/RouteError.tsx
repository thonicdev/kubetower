import { useEffect, type ReactNode } from 'react';
import { useRouteError } from 'react-router';

import { chunkReloadSpent, isChunkLoadFailure, reloadForNewBuild } from './chunkReload';

/**
 * The `errorElement` of the Shell's layout route, so a thrown route renders
 * inside the Shell rather than replacing it - the dock keeps its terminals while
 * a page is broken.
 *
 * It distinguishes one case and only one: a chunk that is missing because the
 * build moved under an open tab. Everything else is rendered rather than
 * swallowed, because an empty answer and a failed one must be told apart.
 *
 * That one case has two sentences. A missing chunk reloads the tab once; the
 * tab after that reload, if the chunk is *still* missing, is not a stale tab
 * but a build on the server that lacks a file, and `reloadForNewBuild` refuses.
 * Drawing "Reloading…" over a refused reload is a page that looks busy and is
 * not, for as long as the operator cares to wait. So whether a reload is coming
 * is read before rendering, and the sentence follows it.
 *
 * ## What it says when what was thrown is not an `Error`
 *
 * `String(error)` on an object is the string `[object Object]`. React Router's
 * own `ErrorResponse` - `{status, statusText, data}` - is exactly the shape a
 * router throws when **no route matched**, and `String()` would throw away the
 * one sentence that names the defect.
 *
 * So the message is derived rather than coerced: an `Error` gives its message, a
 * router response gives its status and whatever it carries, and anything else is
 * serialized. A boundary that cannot say what happened is a boundary that turns
 * every defect into the same defect.
 */
export function RouteError(): ReactNode {
  const error = useRouteError();
  const stale = isChunkLoadFailure(error);
  // Read at render, not set from the effect: the effect performs the reload
  // and this is the same flag it consults, so the two cannot disagree.
  const reloading = stale && !chunkReloadSpent();

  useEffect(() => {
    if (stale) reloadForNewBuild();
  }, [stale]);

  return (
    <div role="alert" className="p-4">
      <p className="text-bad">
        {reloading
          ? 'This page was updated. Reloading…'
          : stale
            ? 'This page was updated, and one reload did not bring it back. The server is serving a build that lacks this file.'
            : 'This page failed to load.'}
      </p>
      <p className="text-muted text-xs">{describe(error)}</p>
    </div>
  );
}

/**
 * The most useful sentence available about whatever was thrown.
 *
 * Ordered by how much it says, not by how likely it is: a router response
 * carries a status that names the case, an `Error` carries a message somebody
 * wrote, and an object carries at least its own fields. `[object Object]` is the
 * one answer this function may never give.
 */
function describe(error: unknown): string {
  if (isRouteResponse(error)) {
    const carried = typeof error.data === 'string' && error.data !== '' ? `: ${error.data}` : '';
    // 404 here means *no route matched this address*, which is a client-side
    // fact and not a server's answer - the distinction that cost an afternoon.
    const what = error.status === 404 ? 'no page at this address' : error.statusText;
    return `${error.status} ${what}${carried}`;
  }
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  if (error === null || error === undefined) return 'nothing was thrown, which is itself a defect';
  try {
    return JSON.stringify(error);
  } catch {
    return Object.prototype.toString.call(error);
  }
}

/** React Router's `ErrorResponse`, structurally. Imported as a type it would be
 *  an import of an internal shape; checked here it is three fields. */
function isRouteResponse(error: unknown): error is { status: number; statusText: string; data?: unknown } {
  return (
    typeof error === 'object' &&
    error !== null &&
    'status' in error &&
    typeof (error as { status: unknown }).status === 'number' &&
    'statusText' in error
  );
}
