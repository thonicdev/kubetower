/**
 * "The session is gone" reaches the router from here, and from nowhere else.
 *
 * ## Why this is a signal and not a redirect
 *
 * The 401 is noticed in `query/client.ts`, which is created before the router
 * exists and has no access to it. The redirect has to happen inside the router,
 * from a component. One module-scope emitter between them is the whole of it -
 * no context, because the emitter must outlive every component that subscribes,
 * for the same reason `keyboard/stack.ts` is module scope.
 *
 * ## Why it exists
 *
 * A lapsed session answered with a login **page** would render through a
 * `fetch` as nothing at all: no error, no unusual status in a log, and a
 * blank screen. `api/openapi.yaml` fixes the server half - "Always JSON, never a
 * login page… the front end routes to its own login screen on this status; that
 * is the whole contract". This is the front end half of that sentence.
 */
type Listener = () => void;

const listeners = new Set<Listener>();

/** Called by the query client when anything answers 401. */
export function reportSessionLapsed(): void {
  // A snapshot: a listener that navigates may unmount another listener, and
  // deleting from the live set under the iterator skips an entry.
  for (const listener of [...listeners]) listener();
}

export function onSessionLapsed(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Test seam. Leaves the module as a fresh import would. */
export function resetSessionLapse(): void {
  listeners.clear();
}
