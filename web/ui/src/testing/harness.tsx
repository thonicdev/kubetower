import { QueryClientProvider } from '@tanstack/react-query';
import { render, type RenderResult } from '@testing-library/react';
import type { ReactNode } from 'react';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { vi } from 'vitest';

import type { Extension } from '../extension';
import { KeyboardStack } from '../keyboard/KeyboardStack';
import { createQueryClient } from '../query/client';
import { buildRoutes } from '../router/routes';
import { ShellProvider } from '../shell/ShellContext';

/**
 * Mounting the real application in a test, and answering it with a real
 * `Response`.
 *
 * **Test scaffolding only.** It imports `vitest`, so nothing outside a test
 * imports it.
 *
 * ## Why the stub builds a `Response` rather than an object that looks like one
 *
 * `api/request.ts` reads `headers.get('Content-Type')`, `status`, `ok` and
 * `json()`, and the one behavior under test in several of these files is what
 * it does with a content type. A hand-rolled `{ ok: true, json: () => ... }`
 * passes every test while proving nothing about header parsing, which is exactly
 * where the `application/problem+json` defect lived.
 */

export interface StubReply {
  status?: number;
  contentType?: string;
  /** An object is serialized; a string is sent verbatim, which is how an HTML
   *  login page or a proxy interstitial is expressed. */
  body?: unknown;
}

export type StubRoutes = Readonly<Record<string, StubReply>>;

function toResponse(reply: StubReply): Response {
  const status = reply.status ?? 200;
  if (status === 204 || reply.body === undefined) {
    return new Response(null, { status });
  }
  const body = typeof reply.body === 'string' ? reply.body : JSON.stringify(reply.body);
  return new Response(body, {
    status,
    headers: { 'Content-Type': reply.contentType ?? 'application/json' },
  });
}

/**
 * Answer exactly the paths named, and answer everything else 404.
 *
 * The default is deliberately **not** 401: a catch-all 401 would send every test
 * through the session-lapse redirect and make the one test that is about that
 * redirect prove nothing.
 */
export function stubFetch(routes: StubRoutes): void {
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
    const key = path.split('?')[0] ?? path;
    const reply = routes[key];
    if (reply === undefined) {
      return Promise.resolve(
        toResponse({
          status: 404,
          contentType: 'application/problem+json',
          body: { status: 404, title: 'Not Found', code: 'not_found', detail: `no stub for ${key}` },
        }),
      );
    }
    return Promise.resolve(toResponse(reply));
  });
}

export interface MountedApp {
  router: ReturnType<typeof createMemoryRouter>;
  view: RenderResult;
}

/** The whole application, at one address, with the providers `createApp` wires
 *  in the same order - `KeyboardStack` outside the router, the query client
 *  outside both. */
export function mountApp(initial: string, extensions: Extension[] = []): MountedApp {
  const router = createMemoryRouter(buildRoutes(extensions), { initialEntries: [initial] });
  const view = render(
    <QueryClientProvider client={createQueryClient()}>
      <KeyboardStack>
        <ShellProvider extensions={extensions}>
          <RouterProvider router={router} />
        </ShellProvider>
      </KeyboardStack>
    </QueryClientProvider>,
  );
  return { router, view };
}

/** One component, with a query client and nothing else. */
export function renderWithClient(ui: ReactNode): RenderResult {
  return render(<QueryClientProvider client={createQueryClient()}>{ui}</QueryClientProvider>);
}

/** One component, with a query client, under a data router - a memory one, at
 *  `/` - for a component that needs what only a data router offers
 *  (`useBlocker`). The router comes back too, so a test can navigate away. */
export function renderInRouter(ui: ReactNode): RenderResult & { router: ReturnType<typeof createMemoryRouter> } {
  const router = createMemoryRouter([{ path: '*', element: ui }], { initialEntries: ['/'] });
  return { ...renderWithClient(<RouterProvider router={router} />), router };
}
