import { waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { resetKeyboardStack } from '../keyboard/stack';
import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';

/**
 * The machine's notices and each cluster's remedy, on the home's cards: each
 * on the card of the cluster it concerns.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};

const SSO_REMEDY = { who: 'You', what: 'Sign in again on the host: `aws sso login`.', actionable: true };

/** Two AWS contexts on one lapsed SSO session, one that answers, one AWS
 *  context down for another reason and one OIDC context whose own sign-in
 *  lapsed. */
const CLUSTERS = [
  { name: 'ok-one', label: 'ok-one', initials: 'O', colour: 'amber', state: 'ok', order: 0, bar: 1, aws: true },
  {
    name: 'eks-a',
    label: 'eks-a',
    initials: 'A',
    colour: 'teal-pale',
    state: 'unreadable',
    order: 1,
    bar: 1,
    aws: true,
    reason: { code: 'credentials_expired', message: 'Error when retrieving token from sso: Token has expired' },
    remedy: SSO_REMEDY,
  },
  {
    name: 'eks-b',
    label: 'eks-b',
    initials: 'B',
    colour: 'red',
    state: 'unreadable',
    order: 2,
    bar: 1,
    aws: true,
    reason: { code: 'credentials_expired', message: 'Error when retrieving token from sso: Token has expired' },
    remedy: SSO_REMEDY,
  },
  {
    name: 'eks-iam',
    label: 'eks-iam',
    initials: 'I',
    colour: 'amber',
    state: 'unreadable',
    order: 3,
    bar: 1,
    aws: true,
    reason: { code: 'read_denied', message: 'AccessDenied: not authorized to perform sts:AssumeRole' },
  },
  {
    name: 'oidc',
    label: 'oidc',
    initials: 'OI',
    colour: 'teal-pale',
    state: 'unreadable',
    order: 4,
    bar: 1,
    reason: { code: 'credentials_expired', message: 'oidc: token is expired' },
  },
];

const SSO_NOTICE = {
  key: 'sso',
  title: 'This AWS SSO session has expired',
  detail: '2026-10-29 08:00',
  command: 'aws sso login --sso-session corp',
  severity: 'bad',
  measured: true,
};

const CACHE_NOTICE = {
  key: 'cache/host',
  title: "The operator's own credential cache",
  detail: '1 entry of 14 in /home/alice/.aws/cli/cache will not read',
  command: 'rm /home/alice/.aws/cli/cache/abc123.json',
  paths: ['abc123.json'],
  severity: 'bad',
  measured: true,
};

function health(notices: unknown[]): StubRoutes[string] {
  return { body: { state: 'ok', data: { checks: [], notices } } };
}

function serve(extra: StubRoutes = {}, clusters: unknown[] = CLUSTERS): void {
  stubFetch({
    '/api/session': session,
    '/api/clusters': { body: { state: 'ok', items: clusters } },
    '/api/c/ok-one/overview': { body: { state: 'unreadable', reason: { code: 'unclassified', message: 'not here' } } },
    ...extra,
  });
}

function card(name: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`[data-cluster-card="${name}"]`);
  if (found === null) throw new Error(`no card for ${name}`);
  return found;
}

async function cardsDrawn(): Promise<void> {
  await waitFor(() => {
    expect(document.querySelectorAll('[data-cluster-card]')).toHaveLength(5);
  });
}

describe('the machine’s notices on the home', () => {
  it('puts the expired SSO session, with its command, on each card that failed on it and no other', async () => {
    serve({ '/api/health': health([SSO_NOTICE]) });
    mountApp('/');
    await cardsDrawn();

    await waitFor(() => {
      expect(card('eks-a').querySelector('[data-notice="sso"]')).not.toBeNull();
    });
    for (const name of ['eks-a', 'eks-b']) {
      const notice = card(name).querySelector('[data-notice="sso"]');
      expect(notice?.textContent).toContain('This AWS SSO session has expired');
      expect(notice?.querySelector('[data-command]')?.textContent).toBe('aws sso login --sso-session corp');
    }
    // Not on the cluster that answers, nor on an AWS cluster down for another
    // reason, nor on a non-AWS context whose own sign-in lapsed - and not drawn
    // a second time above.
    for (const name of ['ok-one', 'eks-iam', 'oidc']) {
      expect(card(name).querySelector('[data-notice]')).toBeNull();
    }
    expect(document.querySelector('[data-local-notices]')).toBeNull();
  });

  it('draws the corrupt credential cache once above the cards, with its paths and the command', async () => {
    // It names no context: the server says what is broken, not which cluster
    // it explains, so no card claims it - and it is not dropped either.
    serve({ '/api/health': health([CACHE_NOTICE]) });
    mountApp('/');
    await cardsDrawn();

    const strip = await waitFor(() => {
      const found = document.querySelector('[data-local-notices]');
      if (found === null) throw new Error('no notices above the cards');
      return found;
    });
    const notice = strip.querySelector('[data-notice="cache/host"]');
    expect(notice?.textContent).toContain('1 entry of 14 in /home/alice/.aws/cli/cache will not read');
    expect(notice?.querySelector('li')?.textContent).toBe('abc123.json');
    expect(notice?.querySelector('[data-command]')?.textContent).toBe('rm /home/alice/.aws/cli/cache/abc123.json');
    for (const name of ['ok-one', 'eks-a', 'eks-b', 'eks-iam', 'oidc']) {
      expect(card(name).querySelector('[data-notice]')).toBeNull();
    }
  });

  it('draws an SSO notice no card failed on above the cards, without inventing a command', async () => {
    const unmeasured = {
      key: 'sso',
      title: 'The AWS SSO cache could not be read',
      detail: 'open /home/alice/.aws/sso/cache: permission denied',
      paths: ['/home/alice/.aws/sso/cache'],
      severity: 'warn',
      measured: false,
    };
    serve({ '/api/health': health([unmeasured]) }, [CLUSTERS[0]]);
    mountApp('/');
    await waitFor(() => {
      expect(document.querySelector('[data-local-notices] [data-notice="sso"]')).not.toBeNull();
    });
    const notice = document.querySelector('[data-local-notices] [data-notice="sso"]');
    expect(notice?.getAttribute('data-measured')).toBe('false');
    expect(notice?.textContent).toContain('/home/alice/.aws/sso/cache');
    expect(notice?.querySelector('[data-command]')).toBeNull();
  });

  it('draws each failed cluster’s remedy line on its card', async () => {
    serve({ '/api/health': health([]) });
    mountApp('/');
    await cardsDrawn();

    for (const name of ['eks-a', 'eks-b']) {
      expect(card(name).querySelector('[data-cluster-remedy]')?.textContent).toBe(
        'You: Sign in again on the host: `aws sso login`.',
      );
    }
    expect(card('ok-one').querySelector('[data-cluster-remedy]')).toBeNull();
  });

  it('draws nothing when the machine has no notice', async () => {
    const asked: string[] = [];
    serve({ '/api/health': health([]) });
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      asked.push(typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url);
      return stubbed(input, init);
    });
    mountApp('/');
    await cardsDrawn();
    await waitFor(() => {
      expect(asked).toContain('/api/health');
    });

    expect(document.querySelector('[data-notice]')).toBeNull();
    expect(document.querySelector('[data-local-notices]')).toBeNull();
    expect(document.querySelector('[data-local-notices-unread]')).toBeNull();
  });

  it('says once that the local checks were not read, and nothing about them on any card', async () => {
    // A read that failed is not a machine with nothing wrong, and not a
    // reason to blame the machine on a card either.
    serve({
      '/api/health': {
        body: { state: 'unreadable', reason: { code: 'unclassified', message: 'the probe is not running' } },
      },
    });
    mountApp('/');
    await cardsDrawn();

    const line = await waitFor(() => {
      const found = document.querySelector('[data-local-notices-unread]');
      if (found === null) throw new Error('no line for the unread checks');
      return found;
    });
    expect(line.getAttribute('title')).toBe('the probe is not running');
    expect(document.querySelector('[data-notice]')).toBeNull();
    // Each cluster's own remedy is the cluster list's, and stays.
    expect(card('eks-a').querySelector('[data-cluster-remedy]')).not.toBeNull();
  });
});
