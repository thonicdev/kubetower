import type { ConfirmationPlan, Problem } from './types';

/**
 * What went wrong, in a shape the UI can branch on.
 *
 * *An empty answer and a failed answer are different answers* is a rule about
 * rendering, and it can only be obeyed if the layer underneath keeps the
 * difference. A network module that turns every failure into `null` has already
 * lost it.
 */
export class ApiError extends Error {
  /** The HTTP status, or 0 when the request never reached a status. */
  readonly status: number;
  /** The decoded error body, when the server sent one. */
  readonly body: unknown;

  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }

  /** The session lapsed. The router routes on this rather than rendering it.
   *
   *  A guard that answers a lapsed session with a login *page* renders as
   *  nothing at all through a `fetch`. The API must answer
   *  401, and `NotJsonError` below is what catches it if one day it does not. */
  get isUnauthenticated(): boolean {
    return this.status === 401;
  }

  /** The caller is signed in and may not do this. Kubernetes' own 403 is mapped
   *  to this status with its message intact, never to a 500. */
  get isForbidden(): boolean {
    return this.status === 403;
  }

  /**
   * A write to the console's own preferences **took effect** and could not be
   * written down: in effect now, gone at the next start (`not_saved`).
   *
   * Not a refusal, and the difference is what a caller does next: a refusal
   * changed nothing, so a gesture of several writes stops; this changed the
   * server's arrangement, so the rest of the gesture is sent and the
   * arrangement is read again (`writeInOrder`).
   */
  get isNotSaved(): boolean {
    return this.code === 'not_saved';
  }

  /**
   * The problem object's `code`, when the body was one.
   *
   * `api/openapi.yaml` makes `code` required on every problem and says why:
   * it is stable and branchable, where `title` and `detail` are prose that may
   * be reworded in any release. Anything in this SPA that branches on a failure
   * branches on this and never on the sentence beside it.
   */
  get code(): Problem['code'] | undefined {
    if (typeof this.body !== 'object' || this.body === null) return undefined;
    const code = (this.body as { code?: unknown }).code;
    return typeof code === 'string' ? (code as Problem['code']) : undefined;
  }

  /**
   * The fresh plan a `plan_changed` 409 carries, when it carries one.
   *
   * The act is bound to a plan by a single-use token, and the server re-derives
   * the facts before acting; when they moved it spends the token and answers
   * with what would happen *now*. A dialog that keeps the old plan is then a
   * dialog whose button resends a spent token, and whose second refusal -
   * "unknown, already spent or expired" - is about the token rather than about
   * the object. This is what the dialog adopts instead.
   */
  get plan(): ConfirmationPlan | undefined {
    if (this.status !== 409 || typeof this.body !== 'object' || this.body === null) return undefined;
    const plan = (this.body as { plan?: unknown }).plan;
    if (typeof plan !== 'object' || plan === null) return undefined;
    return typeof (plan as { token?: unknown }).token === 'string' ? (plan as ConfirmationPlan) : undefined;
  }
}

/**
 * The server answered, with a status, and the body was not JSON.
 *
 * A distinct type on purpose. This is what an HTML login page, an HTML error
 * page or a proxy's interstitial looks like from inside `fetch`, and every one
 * of them is a configuration fault that must be visible rather than decoded
 * into an empty list.
 */
export class NotJsonError extends ApiError {
  readonly contentType: string;
  /**
   * The start of a `text/plain` body, at most `NOT_JSON_EXCERPT` characters,
   * trimmed; `undefined` for any other body, and for an empty one.
   *
   * It exists for one sender: a route whose answer is a sentence written for
   * the operator - the extension gate's refusal is `http.Error`, plain text,
   * saying why the page is off and how to turn it on. Dropping the body threw
   * that sentence away, and an extra, which may fetch only through `request`,
   * had nothing to show but "expected JSON". **Plain text only**, because an
   * HTML body is markup and not a sentence - an HTML sign-in page shown as its
   * source would be noise - and **a string only, never markup**: whoever draws
   * it draws it as text, and React escapes it.
   */
  readonly excerpt: string | undefined;

  constructor(status: number, contentType: string, excerpt?: string) {
    super(status, `expected JSON, received ${contentType || 'no content type'}`);
    this.name = 'NotJsonError';
    this.contentType = contentType;
    this.excerpt = excerpt;
  }
}

/** How much of a plain-text body a `NotJsonError` keeps. A sentence, not a
 *  page: a proxy that answers with a megabyte of text is not a reason to hold
 *  a megabyte in every failed query. */
export const NOT_JSON_EXCERPT = 512;

/**
 * The server answered 200 with JSON, and the JSON was not a legal envelope.
 *
 * `api/errors.md` writes the arm rules as a JSON Schema `if/then` so that a
 * conformance test fails on a violation "rather than a reviewer having to
 * notice". That test runs on the Go side against the schemas. This is the same
 * assertion on the client, and it exists because the one violation that matters
 * - `{"state":"ok","items":[]}` - is indistinguishable from a legal `empty` to
 * any code that does not check, which is the exact collapse the envelope was
 * designed to prevent.
 *
 * It is a failure and not a state. Rendering it as one of the five would put
 * the console's own contract bug into the vocabulary reserved for facts about a
 * cluster, and an operator would go looking at the cluster.
 */
export class EnvelopeError extends ApiError {
  /** Which arm rule was broken, for a report rather than for branching. */
  readonly violation: string;

  constructor(violation: string, body?: unknown) {
    // 200: the request succeeded and the body is wrong. Any other status never
    // carries an envelope at all - it carries a problem object.
    super(200, `the response did not match the envelope contract: ${violation}`, body);
    this.name = 'EnvelopeError';
    this.violation = violation;
  }
}
