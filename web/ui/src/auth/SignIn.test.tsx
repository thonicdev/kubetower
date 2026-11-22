import { cleanup, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch } from '../testing/harness';
import { resetLeaving, watchLeaving } from './leave';

/**
 * There is one sign-in page, the server's. The console never draws a form of
 * its own: a lapsed session and a route written for one both leave for it,
 * carrying where to come back to.
 */

let left: string[] = [];

beforeEach(() => {
  left = [];
  watchLeaving((href) => left.push(href));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  resetKeyboardStack();
  resetLeaving();
});

const problem401 = {
  status: 401,
  contentType: 'application/problem+json',
  body: { status: 401, title: 'Unauthorized', code: 'session_expired', detail: 'no session' },
} as const;

describe('a 401 anywhere leaves for the sign-in page', () => {
  it('leaves the cluster page and remembers where it was', async () => {
    // Everything answers 401, which is what an expired cookie looks like on the
    // first paint of a tab left open overnight.
    stubFetch({
      '/api/clusters': problem401,
      '/api/session': problem401,
      '/api/c/prod/nav': problem401,
      '/api/c/prod/extensions': problem401,
    });

    mountApp('/c/prod');

    // In the URL rather than in router state, so that reloading the sign-in
    // page - the most likely thing to happen there - does not lose it.
    await waitFor(() => {
      expect(left).toContain('/auth/login?next=%2Fc%2Fprod');
    });
  });
});

describe('the console draws no sign-in form of its own', () => {
  it('hands its sign-in route over to the server page, with its query', async () => {
    stubFetch({});
    mountApp('/login?next=%2Fc%2Fprod&sso=refused');

    await waitFor(() => {
      expect(left).toEqual(['/auth/login?next=%2Fc%2Fprod&sso=refused']);
    });
    expect(document.querySelector('form')).toBeNull();
    expect(document.querySelector('input[type="password"]')).toBeNull();
    expect(document.body.textContent).not.toContain('Sign in to continue.');
  });

  it('draws none of the previous session furniture on the way', async () => {
    stubFetch({});
    mountApp('/login');

    await waitFor(() => {
      expect(left).toEqual(['/auth/login']);
    });
    // The rail carries cluster names; drawn around a sign-in, it shows one
    // operator another's clusters.
    expect(document.getElementById('rail')).toBeNull();
    expect(document.getElementById('dock')).toBeNull();
  });
});
