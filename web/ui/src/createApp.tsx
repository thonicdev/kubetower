import { QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router';

import type { Extension } from './extension';
import { KeyboardStack } from './keyboard/KeyboardStack';
import { createQueryClient } from './query/client';
import { clearChunkReloadGuardOnceRendered } from './router/chunkReload';
import { buildRoutes } from './router/routes';
import { CONSOLE_BASE } from './router/base';
import { ShellProvider } from './shell/ShellContext';

/**
 * Mount the console into `el`.
 *
 * This is the whole public entry point. The core SPA calls it with the extras
 * it ships (`app/main.tsx`), and a binary built elsewhere calls it with its own
 * list. That list is explicit in each `main` rather than assembled by
 * self-registration - one place says what ships, and there is no hidden global
 * registry to read.
 *
 * ## The provider order is load-bearing
 *
 * `KeyboardStack` is outside `RouterProvider`, so the application's one keyboard
 * listener exists on every route and is not reinstalled by navigation.
 * `QueryClientProvider` is outside both, so a query started on one page is not
 * canceled by leaving it. `ShellProvider` is inside neither the router nor the
 * Shell - the Shell reads it, and the Shell is a route element.
 *
 * ## StrictMode is on, deliberately
 *
 * It double-invokes effects in development, which is what turns one terminal
 * into two PTYs (`./live/handles.ts`). Leaving it on is what makes that visible
 * on the first run instead of on the first production remount. It compiles away
 * in a production build.
 */
export function createApp(el: HTMLElement, extensions: Extension[] = []): void {
  // `basename` and not a bare call: the server mounts this shell under a prefix
  // (`internal/web/spa.go`), and a router rooted at `/` matches nothing there.
  // `./router/base.ts` carries the string and the reason, and a Go test asserts
  // the two sides agree.
  const router = createBrowserRouter(buildRoutes(extensions), { basename: CONSOLE_BASE });
  const queryClient = createQueryClient();

  // Once the router has settled with nothing thrown, the next failed chunk is a
  // new upgrade and deserves its own single reload. Not "once `render()`
  // returns": with lazy routes that is before the first chunk has been asked
  // for, and `router/chunkReload.ts` records the loop that produced.
  clearChunkReloadGuardOnceRendered(router);

  createRoot(el).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <KeyboardStack>
          <ShellProvider extensions={extensions}>
            <RouterProvider router={router} />
          </ShellProvider>
        </KeyboardStack>
      </QueryClientProvider>
    </StrictMode>,
  );
}
