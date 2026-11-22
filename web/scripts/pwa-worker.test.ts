import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it, vi } from 'vitest';

/**
 * The console's service worker, `internal/web/static/pwa/sw.js`, run as a
 * browser runs it: the file evaluated against a `self` that records its
 * listeners, then handed fetch events.
 *
 * The behavior under test is what the worker does NOT do. A worker that
 * re-issues a navigation with `fetch(event.request)` makes Firefox send it as
 * `Sec-Fetch-Mode: same-origin`, `Sec-Fetch-Dest: empty` - the shape of a
 * script's fetch, which the server's shell answers with its asset 404 (`not in
 * the front-end bundle` on a refresh). A navigation must reach the network as the
 * browser's own request, so the worker must not call `respondWith` for one.
 */
// `import.meta.dirname` rather than a `URL`: under jsdom `import.meta.url` is
// not a `file:` URL.
const source = readFileSync(
  resolve(import.meta.dirname, '../../internal/web/static/pwa/sw.js'),
  'utf8',
);

type Listener = (event: unknown) => void;

function loadWorker(): Map<string, Listener> {
  const listeners = new Map<string, Listener>();
  const self = {
    addEventListener: (type: string, listener: Listener) => listeners.set(type, listener),
    skipWaiting: vi.fn(),
    clients: { claim: vi.fn() },
  };
  new Function('self', 'fetch', 'caches', source)(self, vi.fn(), { keys: vi.fn() });
  return listeners;
}

function fetchEvent(mode: string): { request: { mode: string; url: string }; respondWith: ReturnType<typeof vi.fn> } {
  return { request: { mode, url: 'http://127.0.0.1:8824/c/in-cluster/pods' }, respondWith: vi.fn() };
}

describe('the PWA service worker', () => {
  it('leaves a navigation to the browser, so a reload keeps its navigation headers', () => {
    const onFetch = loadWorker().get('fetch');
    expect(onFetch).toBeDefined();
    const event = fetchEvent('navigate');
    onFetch?.(event);
    expect(event.respondWith).not.toHaveBeenCalled();
  });

  it('still answers everything else, so the page stays controlled and installable', () => {
    const onFetch = loadWorker().get('fetch');
    for (const mode of ['cors', 'no-cors', 'same-origin']) {
      const event = fetchEvent(mode);
      onFetch?.(event);
      expect(event.respondWith, mode).toHaveBeenCalledTimes(1);
    }
  });
});
