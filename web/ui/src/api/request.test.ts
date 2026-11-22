import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError, NotJsonError } from './errors';
import { isJsonMediaType, request } from './request';

afterEach(() => {
  vi.unstubAllGlobals();
});

function answer(status: number, contentType: string | null, body: string | null): void {
  vi.stubGlobal('fetch', () =>
    Promise.resolve(
      new Response(body, {
        status,
        ...(contentType === null ? {} : { headers: { 'Content-Type': contentType } }),
      }),
    ),
  );
}

describe('what counts as JSON', () => {
  // The whole point. `api/openapi.yaml` answers every 4xx and 5xx with
  // `application/problem+json`, and a substring test for `application/json` is
  // false for it - so a 401 arrived as "the body was not JSON" and the sign-in
  // flow could not recognize the one status it exists to route on.
  it.each([
    ['application/json', true],
    ['application/json; charset=utf-8', true],
    ['application/problem+json', true],
    ['application/problem+json; charset=utf-8', true],
    ['application/vnd.kubetower.v2+json', true],
    ['text/html', false],
    ['text/html; charset=utf-8', false],
    ['application/jsonp', false],
    ['', false],
  ])('%s -> %s', (contentType, expected) => {
    expect(isJsonMediaType(contentType)).toBe(expected);
  });
});

describe('request', () => {
  it('decodes a problem object rather than calling it not-JSON', async () => {
    answer(
      401,
      'application/problem+json',
      JSON.stringify({ status: 401, title: 'Unauthorized', code: 'session_expired', detail: 'the session lapsed' }),
    );

    const error = await request('/api/session').catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).not.toBeInstanceOf(NotJsonError);
    const api = error as ApiError;
    expect(api.status).toBe(401);
    expect(api.isUnauthenticated).toBe(true);
    // `code` is protocol and `detail` is prose. Anything that branches reads the
    // first; only the display reads the second.
    expect(api.code).toBe('session_expired');
    expect(api.message).toBe('the session lapsed');
  });

  it('reports an HTML login page as a failure and never as a session', async () => {
    // A guard that answers a lapsed session with a page renders as nothing at
    // all through a fetch. It must stay loud.
    answer(401, 'text/html; charset=utf-8', '<!doctype html><title>Sign in</title>');

    const error = await request('/api/clusters').catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(NotJsonError);
    expect((error as NotJsonError).contentType).toContain('text/html');
    // Markup is not a sentence: the page's source is never kept to be shown.
    expect((error as NotJsonError).excerpt).toBeUndefined();
  });

  it("keeps a plain-text refusal's sentence, so the page can say why it was refused", async () => {
    // The extension gate answers with `http.Error`: text/plain, one sentence
    // saying the page is off and how to turn it on. Dropped, an extra that may
    // fetch only through `request` could show nothing but "expected JSON".
    const sentence = 'Alfa Center is not turned on for this cluster.';
    answer(404, 'text/plain; charset=utf-8', `${sentence}\n`);

    const error = await request('/x/prod/alfa/state').catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(NotJsonError);
    expect((error as NotJsonError).status).toBe(404);
    expect((error as NotJsonError).excerpt).toBe(sentence);
  });

  it('bounds the excerpt, so a large text body is not held in every failed query', async () => {
    answer(502, 'text/plain', 'y'.repeat(10_000));

    const error = (await request('/api/clusters').catch((cause: unknown) => cause)) as NotJsonError;

    expect(error.excerpt).toBe(`${'y'.repeat(512)}…`);
  });

  it('returns nothing for a 204 and does not try to decode it', async () => {
    answer(204, null, null);
    await expect(request<void>('/api/auth/logout', { method: 'POST' })).resolves.toBeUndefined();
  });

  it('gives a request that never reached a status its own status 0', async () => {
    vi.stubGlobal('fetch', () => Promise.reject(new TypeError('Failed to fetch')));

    const error = await request('/api/clusters').catch((cause: unknown) => cause);

    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).status).toBe(0);
  });

  it('marks a mutation with the header the Origin check is paired with', async () => {
    const seen: RequestInit[] = [];
    vi.stubGlobal('fetch', (_input: unknown, init: RequestInit) => {
      seen.push(init);
      return Promise.resolve(new Response(null, { status: 204 }));
    });

    await request('/api/preferences/rail/hide', { method: 'POST', body: { cluster: 'prod' } });
    await request('/api/clusters');

    const mutation = new Headers(seen[0]?.headers);
    const read = new Headers(seen[1]?.headers);
    expect(mutation.get('X-KubeTower-Request')).toBe('1');
    expect(read.get('X-KubeTower-Request')).toBeNull();
  });
});
