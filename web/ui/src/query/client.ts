import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query';

import { ApiError, NotJsonError } from '../api';
import { reportSessionLapsed } from '../auth/lapse';
import { watchCredentials } from '../clusters/credentialWatch';

/**
 * The application's TanStack Query client.
 *
 * Created once per `createApp` rather than at module scope: a module-scope
 * client is shared by every app on the page, which is wrong in a test file and
 * wrong in an extension's own development harness.
 */
export function createQueryClient(): QueryClient {
  const client: QueryClient = new QueryClient({
    // One place notices a lapsed session, for every read and every write in the
    // application. A component that handled its own 401 would be a component
    // that has to remember to, and the one that forgets renders a blank panel
    // for a session that ended - which is the failure `api/openapi.yaml` makes
    // the server answer JSON to prevent.
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (isLapse(error) && query.meta?.['anonymous'] !== true) reportSessionLapsed();
      },
      // And one place notices a cluster whose session lapsed after its last
      // probe: every read of it answers `credentials_*`, and the first one asks
      // the probe so the page can say it once (`clusters/credentialWatch.ts`).
      onSuccess: (data) => {
        watchCredentials(client, data);
      },
    }),
    mutationCache: new MutationCache({
      onError: (error, _variables, _context, mutation) => {
        if (isLapse(error) && mutation.meta?.['anonymous'] !== true) reportSessionLapsed();
      },
    }),

    defaultOptions: {
      queries: {
        // Retrying an answer the server already gave is how a 403 becomes four
        // 403s in the audit log. Only retry what could plausibly differ
        // next time: no status at all, or a 5xx.
        retry: (failureCount, error) => {
          if (failureCount >= 2) return false;
          if (!(error instanceof ApiError)) return false;
          return error.status === 0 || error.status >= 500;
        },
        // A refetch interval is a per-query decision because the
        // interval is part of what the page promises; the default is none.
        refetchOnWindowFocus: true,
        staleTime: 5_000,
      },
      mutations: {
        retry: false,
      },
    },
  });
  return client;
}

/**
 * A 401, and only a 401.
 *
 * Not `NotJsonError`, even at 401: a 401 whose body is HTML is a guard answering
 * with a login page, which is the configuration fault the contract forbids. It
 * must surface as a failure an operator can see rather than be quietly turned
 * into the sign-in flow it is impersonating.
 */
function isLapse(error: unknown): boolean {
  if (error instanceof NotJsonError) return false;
  return error instanceof ApiError && error.isUnauthenticated;
}
