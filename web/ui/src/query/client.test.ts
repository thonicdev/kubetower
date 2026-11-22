import { afterEach, describe, expect, it, vi } from 'vitest';

import { ANONYMOUS } from '../api';
import { ApiError, NotJsonError } from '../api/errors';
import { onSessionLapsed, resetSessionLapse } from '../auth/lapse';
import { createQueryClient } from './client';

afterEach(() => {
  resetSessionLapse();
});

/**
 * One place notices a lapsed session, and one thing is exempt from it.
 *
 * Both halves are load-bearing and neither is visible from a component: a 401
 * that is missed leaves a blank panel where a session ended, and a 401 that is
 * *not* missed on the sign-in form routes the sign-in view to itself.
 */
describe('a 401 reaches the router exactly once, from one place', () => {
  it('reports a lapse from a query', async () => {
    const seen = vi.fn();
    onSessionLapsed(seen);
    const client = createQueryClient();

    await client
      .fetchQuery({
        queryKey: ['anything'],
        queryFn: () => Promise.reject(new ApiError(401, 'no session')),
        retry: false,
      })
      .catch(() => undefined);

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('reports a lapse from a mutation', async () => {
    const seen = vi.fn();
    onSessionLapsed(seen);
    const client = createQueryClient();

    const mutation = client.getMutationCache().build(client, {
      mutationFn: () => Promise.reject(new ApiError(401, 'no session')),
    });
    await mutation.execute(undefined).catch(() => undefined);

    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('says nothing for a mutation that declared itself anonymous', async () => {
    const seen = vi.fn();
    onSessionLapsed(seen);
    const client = createQueryClient();

    const mutation = client.getMutationCache().build(client, {
      meta: ANONYMOUS,
      mutationFn: () => Promise.reject(new ApiError(401, 'wrong password')),
    });
    await mutation.execute(undefined).catch(() => undefined);

    // A wrong password is an ordinary 401 and not a lapsed session. Without this
    // the sign-in view redirects to the sign-in view, forever.
    expect(seen).not.toHaveBeenCalled();
  });

  it('says nothing for a 401 whose body was HTML', async () => {
    const seen = vi.fn();
    onSessionLapsed(seen);
    const client = createQueryClient();

    await client
      .fetchQuery({
        queryKey: ['html'],
        queryFn: () => Promise.reject(new NotJsonError(401, 'text/html')),
        retry: false,
      })
      .catch(() => undefined);

    // A guard answering with a login page is the configuration fault the
    // contract forbids. Turning it into the sign-in flow it is impersonating
    // would hide it; it has to surface as a failure an operator can see.
    expect(seen).not.toHaveBeenCalled();
  });

  it('says nothing for anything that is not a 401', async () => {
    const seen = vi.fn();
    onSessionLapsed(seen);
    const client = createQueryClient();

    await client
      .fetchQuery({
        queryKey: ['forbidden'],
        queryFn: () => Promise.reject(new ApiError(403, 'read-only')),
        retry: false,
      })
      .catch(() => undefined);

    expect(seen).not.toHaveBeenCalled();
  });
});
