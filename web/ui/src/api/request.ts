import { ApiError, NOT_JSON_EXCERPT, NotJsonError } from './errors';

/**
 * The one place in the SPA that calls `fetch`.
 *
 * Brief §3.5 requires exactly that, and `eslint.config.js` enforces it:
 * `no-restricted-globals` forbids `fetch` and `WebSocket` everywhere outside
 * this directory. One module is what makes the session cookie, the mutation
 * header and the error mapping properties of the application rather than of
 * whichever component was written most recently.
 */

/** Mutations carry a header no cross-site form can set, which is what the
 *  server's Origin check is paired with. The name is arbitrary;
 *  that it is *custom* is the point. It is `X-KubeTower-Request: 1` in
 *  `api/openapi.yaml` as `parameters/RequestMarker`, required on every
 *  mutation, so the value is fixed there rather than chosen here. */
const MUTATION_HEADER = 'X-KubeTower-Request';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * Every JSON media type, including the suffixed ones.
 *
 * **This is not a tidy-up.** `api/openapi.yaml` answers every 4xx and 5xx with
 * `application/problem+json` (RFC 9457) and every 2xx with `application/json`.
 * A substring test for `application/json` is false for `application/problem+json`
 * - the two share no such run of characters - so it turned every problem
 * response, *including the 401 the whole login flow routes on*, into a
 * `NotJsonError` whose message said the body was not JSON. The one status the
 * SPA must be able to recognize was the one it could not.
 *
 * RFC 6839 §3.1 is the rule: a `+json` structured suffix means the body is
 * JSON. Matched on the essence only, because a charset parameter is legal and
 * says nothing about the shape.
 */
const JSON_MEDIA_TYPE = /^application\/(?:[a-z0-9][a-z0-9!#$&^_.-]*\+)?json$/i;

export function isJsonMediaType(contentType: string): boolean {
  const essence = contentType.split(';', 1)[0]?.trim() ?? '';
  return JSON_MEDIA_TYPE.test(essence);
}

export interface RequestOptions {
  method?: string;
  /** Sent as JSON. Omit for a request with no body. */
  body?: unknown;
  signal?: AbortSignal;
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = (options.method ?? 'GET').toUpperCase();

  const headers = new Headers({ Accept: 'application/json' });
  if (options.body !== undefined) headers.set('Content-Type', 'application/json');
  if (!SAFE_METHODS.has(method)) headers.set(MUTATION_HEADER, '1');

  const init: RequestInit = {
    method,
    headers,
    // The session is an HttpOnly cookie and the SPA is served by the same
    // process as the API. No token is ever readable here, and nothing is ever
    // put in localStorage or sessionStorage.
    credentials: 'same-origin',
    redirect: 'error',
  };
  if (options.body !== undefined) init.body = JSON.stringify(options.body);
  if (options.signal !== undefined) init.signal = options.signal;

  let response: Response;
  try {
    response = await fetch(path, init);
  } catch (cause) {
    // No status at all: offline, DNS, a refused connection, an abort. Status 0
    // so a caller can tell it from anything the server said.
    throw new ApiError(0, cause instanceof Error ? cause.message : 'network error');
  }

  const contentType = response.headers.get('Content-Type') ?? '';

  if (response.status === 204 || response.headers.get('Content-Length') === '0') {
    if (!response.ok) throw new ApiError(response.status, response.statusText);
    return undefined as T;
  }

  if (!isJsonMediaType(contentType)) {
    throw new NotJsonError(response.status, contentType, await plainExcerpt(response, contentType));
  }

  const body: unknown = await response.json();
  if (!response.ok) throw new ApiError(response.status, messageOf(body) ?? response.statusText, body);
  return body as T;
}

/**
 * The start of a plain-text body, bounded, or nothing.
 *
 * `NotJsonError.excerpt` says why it is plain text only. A body that cannot be
 * read - aborted, or a stream that fails half-way - is `undefined` rather than
 * a second error: the failure being reported is that the body was not JSON,
 * and a read error thrown here would replace it with a less useful one.
 */
async function plainExcerpt(response: Response, contentType: string): Promise<string | undefined> {
  const essence = contentType.split(';', 1)[0]?.trim().toLowerCase() ?? '';
  if (essence !== 'text/plain') return undefined;
  let text: string;
  try {
    text = await response.text();
  } catch {
    return undefined;
  }
  const trimmed = text.trim();
  if (trimmed === '') return undefined;
  return trimmed.length > NOT_JSON_EXCERPT ? `${trimmed.slice(0, NOT_JSON_EXCERPT)}…` : trimmed;
}

/**
 * The sentence to show an operator.
 *
 * A problem object (RFC 9457) carries `detail` for the specific occurrence and
 * `title` for the kind of thing it is, and `api/openapi.yaml` requires `title`
 * and `code` on every one. `detail` first because it is the one written about
 * this request; `message` is accepted too so that a body which is not a problem
 * object still contributes something rather than nothing.
 */
function messageOf(body: unknown): string | undefined {
  if (typeof body !== 'object' || body === null) return undefined;
  const record = body as Record<string, unknown>;
  for (const key of ['detail', 'message', 'title']) {
    const value = record[key];
    if (typeof value === 'string' && value.length > 0) return value;
  }
  return undefined;
}

/** A file the server answered: its bytes and the name it gave it. */
export interface Downloaded {
  blob: Blob;
  filename: string;
}

/**
 * Asks for a file: a mutation (it carries the mutation header) whose 2xx answer
 * is the file, and whose failure is a problem object like any other. The name
 * is the one `Content-Disposition` gives, `fallback` when it gives none.
 */
export async function download(path: string, fallback: string, options: { method?: string; signal?: AbortSignal } = {}): Promise<Downloaded> {
  const method = (options.method ?? 'POST').toUpperCase();
  const headers = new Headers({ Accept: 'application/octet-stream, application/problem+json' });
  if (!SAFE_METHODS.has(method)) headers.set(MUTATION_HEADER, '1');
  const init: RequestInit = { method, headers, credentials: 'same-origin', redirect: 'error' };
  if (options.signal !== undefined) init.signal = options.signal;

  let response: Response;
  try {
    response = await fetch(path, init);
  } catch (cause) {
    throw new ApiError(0, cause instanceof Error ? cause.message : 'network error');
  }
  const contentType = response.headers.get('Content-Type') ?? '';
  if (!response.ok || isJsonMediaType(contentType)) {
    if (!isJsonMediaType(contentType)) {
      throw new NotJsonError(response.status, contentType, await plainExcerpt(response, contentType));
    }
    const body: unknown = await response.json();
    throw new ApiError(response.status, messageOf(body) ?? response.statusText, body);
  }
  let blob: Blob;
  try {
    blob = await response.blob();
  } catch (cause) {
    throw new ApiError(0, cause instanceof Error ? cause.message : 'the download broke off');
  }
  return { blob, filename: filenameOf(response.headers.get('Content-Disposition')) ?? fallback };
}

/** The `filename` of a `Content-Disposition`, kept to a plain file name. */
function filenameOf(disposition: string | null): string | undefined {
  const match = /filename="([^"/\\]+)"/.exec(disposition ?? '');
  return match?.[1];
}
