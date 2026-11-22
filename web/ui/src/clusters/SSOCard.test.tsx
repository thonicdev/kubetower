import { waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { formatInstant } from '../components/instant';
import { resetKeyboardStack } from '../keyboard/stack';
import { sessionKeys } from './SSOCard';
import { mountApp, stubFetch, type StubRoutes } from '../testing/harness';

/**
 * The AWS SSO session's own card on the home: signed in
 * and until when, expired with the command, a dashed ghost where there is no
 * session, "not read" where nobody could tell - and nothing at all under the
 * server profile.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const session = {
  body: { identity: 'alice', idleExpiresAt: '2999-01-01T00:00:00Z', absoluteExpiresAt: '2999-01-01T00:00:00Z' },
};

const CLUSTERS = [
  { name: 'eks-a', label: 'eks-a', initials: 'A', colour: 'amber', state: 'unreachable', order: 0, bar: 1, aws: true },
];

const UNTIL = '2026-10-30T18:00:00Z';
const SINCE = '2026-10-29T08:00:00Z';

function health(sso: unknown): StubRoutes[string] {
  return { body: { state: 'ok', data: { checks: [], notices: [], ...(sso === undefined ? {} : { sso }) } } };
}

function serve(routes: StubRoutes, mode = 'desktop'): void {
  stubFetch({
    '/api/session': session,
    '/api/capabilities': { body: { mode, localShell: false } },
    '/api/clusters': { body: { state: 'ok', items: CLUSTERS } },
    ...routes,
  });
}

async function ssoCards(): Promise<HTMLElement[]> {
  return waitFor(() => {
    const found = [...document.querySelectorAll<HTMLElement>('[data-sso-card]')];
    if (found.length === 0) throw new Error('no SSO card drawn');
    return found;
  });
}

describe('the AWS SSO card on the home', () => {
  it('draws a valid session with its name and until when, and no command', async () => {
    serve({
      '/api/health': health({ measured: true, sessions: [{ name: 'corp', state: 'valid', expiresAt: UNTIL }] }),
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-card')).toBe('corp');
    expect(card?.getAttribute('data-sso-state')).toBe('valid');
    expect(card?.textContent).toContain('corp');
    expect(card?.textContent).toContain('signed in');
    expect(card?.querySelector('[data-sso-expiry]')?.textContent).toBe(`until ${formatInstant(UNTIL)}`);
    expect(card?.querySelector('time')?.getAttribute('dateTime')).toBe(UNTIL);
    expect(card?.querySelector('[data-command]')).toBeNull();
    // It sits in the cluster grid, after the cluster's card.
    expect(card?.closest('ul')?.querySelector('[data-cluster-card="eks-a"]')).not.toBeNull();
  });

  it('draws an expired session as expired, with the command that signs in again', async () => {
    serve({
      '/api/health': health({
        measured: true,
        sessions: [
          { name: 'corp', state: 'valid', expiresAt: UNTIL },
          { name: 'lab', state: 'expired', expiresAt: SINCE, command: 'aws sso login --sso-session lab' },
        ],
      }),
    });
    mountApp('/');

    await waitFor(() => {
      expect(document.querySelectorAll('[data-sso-card]')).toHaveLength(2);
    });
    const lab = document.querySelector('[data-sso-card="lab"]');
    expect(lab?.getAttribute('data-sso-state')).toBe('expired');
    expect(lab?.textContent).toContain('expired');
    expect(lab?.querySelector('[data-sso-expiry]')?.textContent).toBe(`since ${formatInstant(SINCE)}`);
    expect(lab?.querySelector('[data-command]')?.textContent).toBe('aws sso login --sso-session lab');
    expect(document.querySelector('[data-sso-state="none"]')).toBeNull();
  });

  it('draws a dashed ghost card when the cache holds no session', async () => {
    serve({ '/api/health': health({ measured: true, sessions: [] }) });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('none');
    expect(card?.className).toContain('border-dashed');
    expect(card?.textContent).toContain('No AWS SSO session is configured on this machine.');
    expect(card?.querySelector('[data-command]')?.textContent).toBe('aws configure sso');
    expect(document.querySelectorAll('[data-sso-card]')).toHaveLength(1);
  });

  it('says in a container that the cache is the one the container sees, not the machine', async () => {
    serve({ '/api/health': health({ measured: true, sessions: [] }) }, 'container');
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('none');
    const where = card?.querySelector('[data-sso-where]')?.textContent;
    expect(where).toBe('No AWS SSO session is configured in the AWS CLI cache this container sees.');
    expect(card?.textContent).not.toContain('this machine');
    expect(card?.querySelector('[data-command]')?.textContent).toBe('aws configure sso');
  });

  it('says the cache was not read rather than drawing the ghost', async () => {
    serve({ '/api/health': health({ measured: false, problem: 'permission denied', sessions: [] }) });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    expect(card?.className).not.toContain('border-dashed');
    expect(card?.textContent).toContain('not read');
    expect(card?.textContent).toContain('permission denied');
    expect(card?.textContent).not.toContain('No AWS SSO session is configured');
  });

  it('says not read when the health answer’s state is not ok', async () => {
    serve({
      '/api/health': {
        body: { state: 'unreadable', reason: { code: 'unclassified', message: 'the probe is not running' } },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    expect(card?.textContent).toContain('the probe is not running');
    expect(document.querySelector('[data-sso-state="none"]')).toBeNull();
  });

  it('draws no card and no ghost under the server profile', async () => {
    // Sessions in the answer on purpose: it is the profile that keeps the card
    // off, not the server's silence. The cache notice proves the health read
    // was drawn, and the guardrail line that the capabilities were.
    const cache = { key: 'cache/container', title: 'cache', severity: 'warn', measured: false };
    stubFetch({
      '/api/session': session,
      '/api/capabilities': { body: { mode: 'shared', localShell: false, readOnly: { fence: 'on' } } },
      // Two clusters: the server profile opens straight onto a lone one.
      '/api/clusters': { body: { state: 'ok', items: [...CLUSTERS, { ...CLUSTERS[0], name: 'eks-b', label: 'eks-b' }] } },
      '/api/health': {
        body: {
          state: 'ok',
          data: {
            checks: [],
            notices: [cache],
            sso: { measured: true, sessions: [{ name: 'corp', state: 'valid', expiresAt: UNTIL }] },
          },
        },
      },
    });
    mountApp('/');

    await waitFor(() => {
      expect(document.querySelector('[data-cluster-card="eks-a"]')).not.toBeNull();
      expect(document.querySelector('[data-local-notices]')).not.toBeNull();
      expect(document.querySelector('[data-readonly-fence]')).not.toBeNull();
    });
    expect(document.querySelector('[data-sso-card]')).toBeNull();
  });

  it('says not read when the health request itself fails', async () => {
    // A 4xx problem, which the query client does not retry: an error, where the
    // test above is an answer whose state is not `ok`.
    serve({
      '/api/health': {
        status: 404,
        contentType: 'application/problem+json',
        body: { status: 404, title: 'Not Found', code: 'not_found', detail: 'no health here' },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    expect(document.querySelector('[data-sso-state="none"]')).toBeNull();
  });

  it('keeps two unnamed sessions apart, each under a label', async () => {
    serve({
      '/api/health': health({
        measured: true,
        sessions: [
          { name: '', state: 'valid', expiresAt: UNTIL },
          { name: '', state: 'expired', expiresAt: SINCE, command: 'aws sso login' },
        ],
      }),
    });
    mountApp('/');

    await waitFor(() => {
      expect(document.querySelectorAll('[data-sso-card]')).toHaveLength(2);
    });
    const headings = [...document.querySelectorAll('[data-sso-card] h2')].map((h) => h.textContent);
    expect(headings).toEqual(['Unnamed session', 'Unnamed session']);
    expect(document.querySelectorAll('[data-sso-state="valid"]')).toHaveLength(1);
    expect(document.querySelectorAll('[data-sso-state="expired"]')).toHaveLength(1);
  });

  it('does not draw the card until the cluster list answers, so it never moves', async () => {
    let answer: (response: Response) => void = () => undefined;
    const held = new Promise<Response>((resolve) => {
      answer = resolve;
    });
    serve({
      '/api/health': health({ measured: true, sessions: [{ name: 'corp', state: 'valid', expiresAt: UNTIL }] }),
    });
    const stubbed = globalThis.fetch;
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url;
      return path.split('?')[0] === '/api/clusters' ? held : stubbed(input, init);
    });
    mountApp('/');

    // The health read has answered: the capabilities line is drawn from the
    // same round of reads, and the card is still not there.
    await waitFor(() => {
      expect(document.querySelector('h1')?.textContent).toBe('Clusters');
    });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(document.querySelector('[data-sso-card]')).toBeNull();
    expect(document.querySelector('[data-sso-grid]')).toBeNull();

    answer(
      new Response(JSON.stringify({ state: 'ok', items: CLUSTERS }), {
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    const [card] = await ssoCards();
    expect(card?.closest('[data-sso-grid]')).toBeNull();
    expect(card?.closest('ul')?.querySelector('[data-cluster-card="eks-a"]')).not.toBeNull();
  });

  it('draws the card when the kubeconfig names no cluster', async () => {
    serve({
      '/api/clusters': { body: { state: 'empty', items: [] } },
      '/api/health': health({ measured: true, sessions: [{ name: 'corp', state: 'valid', expiresAt: UNTIL }] }),
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-card')).toBe('corp');
    expect(card?.closest('[data-sso-grid]')).not.toBeNull();
    expect(document.querySelector('[data-cluster-card]')).toBeNull();
  });

  it('draws the card when the cluster list could not be read', async () => {
    serve({
      '/api/clusters': {
        body: { state: 'unreadable', reason: { code: 'unclassified', message: 'the kubeconfig does not parse' } },
      },
      '/api/health': health({ measured: true, sessions: [] }),
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('none');
    expect(card?.closest('[data-sso-grid]')).not.toBeNull();
  });
});

/**
 * One fact, one surface: where the SSO card is drawn it owns the session's
 * state, and the expired-session notice stays only on the AWS cluster
 * cards that failed on it.
 */
describe('the SSO card and the machine notices say each fact once', () => {
  const EXPIRED = { name: 'corp', state: 'expired', expiresAt: SINCE, command: 'aws sso login --sso-session corp' };
  const EXPIRED_NOTICE = {
    key: 'sso',
    title: 'This AWS SSO session has expired',
    detail: '2026-10-29 08:00',
    command: 'aws sso login --sso-session corp',
    severity: 'bad',
    measured: true,
  };

  it('does not repeat an unreadable cache above the cards', async () => {
    const unmeasured = {
      key: 'sso',
      title: 'The AWS SSO cache could not be read',
      detail: 'permission denied',
      severity: 'warn',
      measured: false,
    };
    serve({
      '/api/health': {
        body: {
          state: 'ok',
          data: { checks: [], notices: [unmeasured], sso: { measured: false, problem: 'permission denied', sessions: [] } },
        },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    expect(document.querySelector('[data-notice="sso"]')).toBeNull();
  });

  it('does not repeat an expired session above the cards when no AWS card failed on it', async () => {
    serve({
      '/api/health': {
        body: { state: 'ok', data: { checks: [], notices: [EXPIRED_NOTICE], sso: { measured: true, sessions: [EXPIRED] } } },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.querySelector('[data-command]')?.textContent).toBe('aws sso login --sso-session corp');
    expect(document.querySelector('[data-notice="sso"]')).toBeNull();
    expect(document.querySelectorAll('[data-command]')).toHaveLength(1);
  });

  it('keeps the notice on an AWS cluster card that failed on the session', async () => {
    const failing = {
      ...CLUSTERS[0],
      state: 'unreadable',
      reason: { code: 'credentials_expired', message: 'Token has expired' },
    };
    serve({
      '/api/clusters': { body: { state: 'ok', items: [failing] } },
      '/api/health': {
        body: { state: 'ok', data: { checks: [], notices: [EXPIRED_NOTICE], sso: { measured: true, sessions: [EXPIRED] } } },
      },
    });
    mountApp('/');

    await ssoCards();
    await waitFor(() => {
      expect(document.querySelector('[data-cluster-card="eks-a"] [data-notice="sso"]')).not.toBeNull();
    });
    expect(document.querySelector('[data-local-notices]')).toBeNull();
  });

  it('names the unreadable cache’s directory on the card that says it was not read', async () => {
    const unmeasured = {
      key: 'sso',
      title: 'The AWS SSO cache could not be read',
      detail: 'permission denied',
      paths: ['/home/alice/.aws/sso/cache'],
      severity: 'warn',
      measured: false,
    };
    serve({
      '/api/health': {
        body: {
          state: 'ok',
          data: { checks: [], notices: [unmeasured], sso: { measured: false, problem: 'permission denied', sessions: [] } },
        },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    await waitFor(() => {
      expect(card?.querySelector('[data-sso-paths]')?.textContent).toBe('/home/alice/.aws/sso/cache');
    });
    expect(document.querySelector('[data-notice="sso"]')).toBeNull();
  });

  it('gives a renewable session the clusters contradict its command when no AWS card claims it', async () => {
    // Failing, but not on `credentials_expired`: no cluster card claims the
    // notice, and the card is then the only place left to say it.
    const failing = { ...CLUSTERS[0], reason: { code: 'timeout', message: 'no answer' } };
    const renewable = { name: 'corp', state: 'renewable', expiresAt: SINCE };
    const lapsed = {
      key: 'sso',
      title: 'This SSO session is renewable and the clusters disagree',
      detail: '2026-10-29 08:00',
      command: 'aws sso login --sso-session corp',
      severity: 'bad',
      measured: true,
    };
    serve({
      '/api/clusters': { body: { state: 'ok', items: [failing] } },
      '/api/health': {
        body: { state: 'ok', data: { checks: [], notices: [lapsed], sso: { measured: true, sessions: [renewable] } } },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('renewable');
    await waitFor(() => {
      expect(card?.querySelector('[data-command]')?.textContent).toBe('aws sso login --sso-session corp');
    });
    expect(card?.textContent).not.toContain('renews it on its next call');
    expect(document.querySelector('[data-notice="sso"]')).toBeNull();
    expect(document.querySelectorAll('[data-command]')).toHaveLength(1);
  });

  it('names only the credential caches in the line for a failed read, the card saying the rest', async () => {
    serve({
      '/api/health': {
        body: { state: 'unreadable', reason: { code: 'unclassified', message: 'the probe is not running' } },
      },
    });
    mountApp('/');

    const [card] = await ssoCards();
    expect(card?.getAttribute('data-sso-state')).toBe('unread');
    const line = document.querySelector('[data-local-notices-unread]');
    expect(line?.textContent).toContain('local checks (credential caches) not read');
    expect(line?.textContent).not.toContain('SSO');
  });
});

describe('a session card’s key', () => {
  it('stays the same when the sessions are reordered', () => {
    const corp = { name: 'corp', state: 'valid' as const, expiresAt: UNTIL };
    const lab = { name: 'lab', state: 'expired' as const, expiresAt: SINCE };
    const unnamed = { name: '', state: 'renewable' as const, expiresAt: SINCE };
    const before = sessionKeys([corp, lab, unnamed]);
    const after = sessionKeys([unnamed, lab, corp]);
    expect(after).toEqual([before[2], before[1], before[0]]);
    expect(new Set(sessionKeys([unnamed, unnamed])).size).toBe(2);
  });
});
