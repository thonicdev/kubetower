import type { ReactNode } from 'react';

import { ApiError, EnvelopeError, NotJsonError } from '../api';
import type { Reason, ReasonOnlyState } from '../api';
import { btnSecondary } from '../components/buttons';

/**
 * The six states, drawn.
 *
 * There is one component for all of them on purpose. The defect this whole
 * product is organized around is a cluster that could not be asked looking like
 * a cluster with nothing in it, and the way that happens is not that somebody
 * decides to draw them the same - it is that five of the six have no drawing at
 * all, so every list falls through to the one that does. A single notice with a
 * word for each state means the cheapest thing to write is also the correct one.
 *
 * `reason.code` is what anything branches on; `reason.message` is the operator's
 * sentence and `api/openapi.yaml` says it "may be reworded in any release".
 * Nothing here reads the message except to print it.
 *
 * ## `pending` and `<Pending>` are two different facts and share a word
 *
 * The **state** `pending` is a fact about the server: it probes clusters on its
 * own loop, answers out of what it already holds, and has not yet got to this
 * one. The **component** `Pending` at the bottom of this file is a fact about
 * the browser: this request has not come back. A page can be in either without
 * the other, so they are drawn differently and worded differently - *"Not asked
 * yet"* against *"Reading clusters…"* - and neither is allowed to stand in for
 * the other.
 */

/** What the state is called, in the console's own words. Never "error". */
const TITLES: Readonly<Record<ReasonOnlyState | 'empty', string>> = {
  empty: 'Nothing here',
  pending: 'Not asked yet',
  unreadable: 'Could not be read',
  absent: 'Not installed',
  unreachable: 'Could not be asked',
  forbidden: 'Refused by the cluster',
};

/**
 * One sentence of standing explanation per state, shown when the server sent no
 * reason of its own. Deliberately says what the *console* knows rather than
 * guessing at a cause.
 *
 * **`pending` is the one that has to be read against `unreadable` to be worth
 * anything.** `unreadable` says a read happened and its answer could not be
 * made sense of - that is a fault, it is standing, and somebody has to act
 * before it changes. `pending` says no read has happened, so there is nothing
 * to fix and an answer is coming on its own. The sentence therefore has to make
 * *waiting* the next move, and it must not borrow a word from the failures: an
 * operator who reads "could not" goes looking for a cause that does not exist.
 */
const FALLBACKS: Readonly<Record<ReasonOnlyState | 'empty', string>> = {
  empty: '',
  pending:
    'The console asks each cluster on its own schedule and has not reached this one yet. Nothing has failed and there is nothing to fix - an answer arrives when the round gets here.',
  unreadable: 'It exists and could not be read.',
  absent: 'The thing that would serve this is not installed.',
  unreachable: 'The cluster could not be reached, so nothing about it is known.',
  forbidden: 'The API server refused this verb for these credentials.',
};

/**
 * Full class strings, looked up. **Never built by interpolation.**
 *
 * Tailwind v4 finds utilities by scanning source text, so `text-${tone}` is a
 * class that exists in the DOM and in no stylesheet - which fails silently and
 * looks exactly like a color that was never chosen.
 *
 * `pending` is the accent and not the muted gray the other two non-failures
 * wear: gray is the console's color for *this is as much as there is*, and
 * `pending` means the opposite. It is also the only tone here that is not on
 * the warn/bad axis, because it is not a fault.
 */
const TONE: Readonly<Record<ReasonOnlyState | 'empty', string>> = {
  empty: 'text-muted',
  pending: 'text-accent',
  absent: 'text-muted',
  unreadable: 'text-warn',
  forbidden: 'text-warn',
  unreachable: 'text-bad',
};

export interface StateNoticeProps {
  state: ReasonOnlyState | 'empty';
  reason?: Reason | undefined;
  /** Offered only where the reason says another attempt could differ. */
  onRetry?: (() => void) | undefined;
  /** The rail and the menu are 56 to 92 pixels wide. One line, no paragraph. */
  compact?: boolean;
  /**
   * The heading, where the state's own word would name the wrong subject.
   *
   * The state, its tone and `data-state` are unchanged - this is still the same
   * fact in the same vocabulary. It exists because `absent`'s word, *Not
   * installed*, is about a cluster everywhere else (an extra's page says it when
   * the thing it reads is missing from the cluster), and a page reporting that
   * *this console* lacks something must not wear the title an operator has
   * learned to read as "go and install it on the cluster". Not a license to
   * reword a state for taste: omit it and the state's own word is used.
   */
  title?: string | undefined;
}

/**
 * The list panel's rule for a boxed notice. `#panel` has
 * no padding, so its table meets the toolbar and both edges; a notice drawn in
 * the table's place - `StateNotice`'s box, `Failure`'s - would then touch them
 * too and read as a bug. Its own margin, keyed on `data-box`, keeps a 12px
 * inset, and touches nothing else in the panel.
 */
export const PANEL_BOX = '[&>[data-box]]:m-3';

/**
 * An object that is not there any more, on a cluster that serves its kind:
 * `absent` with `no_such_object`. Drawn as gone and never as *Not installed*,
 * whose next move - install something - is wrong for an object that was
 * deleted.
 */
export function isGoneObject(state: ReasonOnlyState | 'empty', reason: Reason | undefined): boolean {
  return state === 'absent' && reason?.code === 'no_such_object';
}

const GONE_TITLE = 'No longer exists';
const GONE_FALLBACK = 'This object is not on the cluster any more.';

export function StateNotice({ state, reason, onRetry, compact = false, title: heading }: StateNoticeProps): ReactNode {
  const gone = isGoneObject(state, reason);
  const title = heading ?? (gone ? GONE_TITLE : TITLES[state]);
  const tone = TONE[state];
  const detail = reason?.message ?? (gone ? GONE_FALLBACK : FALLBACKS[state]);

  if (compact) {
    return (
      <p
        role="status"
        data-state={state}
        className={`px-2 py-1 text-xs ${tone}`}
        title={detail === '' ? undefined : detail}
      >
        {title}
      </p>
    );
  }

  // `retryable` drives a button and never a loop - `api/openapi.yaml` says so in
  // those words, and a console that retries `forbidden` on its own writes the
  // same denial into the audit log every few seconds.
  //
  // And never on `pending`, whatever `retryable` says. The contract is explicit
  // that "a failure invites a retry button, `pending` invites waiting": asking
  // again cannot make the server's own probe loop arrive sooner, so the button
  // would be a control that does nothing - which is the affordance this codebase
  // refuses everywhere else it comes up (`lockedNote`, the arrange dialog's
  // disabled arrows). What is offered instead is the fact that a round is due.
  const retry = state !== 'pending' && reason?.retryable === true ? onRetry : undefined;

  return (
    <div
      role="status"
      data-state={state}
      data-box
      className="rounded-lg border border-line bg-panel-2 p-3 text-sm"
    >
      <p className={`font-medium ${tone}`}>{title}</p>
      {detail === '' ? null : <p className="mt-1 text-muted">{detail}</p>}
      {reason?.scope !== undefined ? <ScopeLine scope={reason.scope} /> : null}
      {reason?.detail !== undefined ? (
        <p className="mt-1 font-mono text-xs break-words text-muted">{reason.detail}</p>
      ) : null}

      {/*
        The console's standing line for `pending`, drawn whether or not the
        server sent a sentence of its own - the same shape `components/Remedy`
        has for the four failures, and here for the same reason. Without it, a
        `pending` carrying its own `reason.message` renders as a paragraph with
        no next move in it, which is how an operator reads "not asked yet" as
        "gave up". The two facts an operator needs are that nothing is broken
        and that they do not have to do anything.
      */}
      {state === 'pending' ? (
        <p data-pending-advice="wait" className="mt-2 text-xs text-muted">
          Nothing has failed here. The console is working through its clusters and will reach this
          one; this page keeps itself up to date, so there is nothing to press.
        </p>
      ) : null}

      {/* Bordered, like the console's other small controls. */}
      {retry !== undefined ? (
        <button type="button" onClick={retry} className={`mt-2 ${btnSecondary}`}>
          {/* "Not installed" over "Try again" read as a failure to retry
              (#133 item 7). Nothing failed: whether it is installed is read
              again, the console's Refresh. */}
          {state === 'absent' ? 'Refresh' : 'Try again'}
        </button>
      ) : null}
    </div>
  );
}

/**
 * What was asked. `api/openapi.yaml`: without it, "unreachable" does not say
 * unreachable for what - and that is the difference between a notice an operator
 * can paste into a support conversation and one they cannot.
 */
function ScopeLine({ scope }: { scope: NonNullable<Reason['scope']> }): ReactNode {
  const parts = [
    scope.cluster !== undefined ? `cluster ${scope.cluster}` : null,
    scope.namespace !== undefined ? `namespace ${scope.namespace}` : null,
    scope.resource !== undefined ? scope.resource : null,
    scope.name !== undefined ? scope.name : null,
    scope.verb !== undefined ? `verb ${scope.verb}` : null,
  ].filter((part): part is string => part !== null);

  if (parts.length === 0) return null;
  return <p className="mt-1 text-xs text-muted">{parts.join(' · ')}</p>;
}

/**
 * An object an extra's route answered 404 for with one of `codes`: gone, not
 * failed. A route says so with a problem body carrying its own code, which is
 * what this reads - never the sentence.
 */
export function isGoneProblem(error: unknown, codes: readonly string[]): boolean {
  if (!(error instanceof ApiError) || error.status !== 404) return false;
  const code = (error.body as { code?: unknown } | undefined)?.code;
  return typeof code === 'string' && codes.includes(code);
}

/**
 * One short line for an object that is not there any more ("Pod no longer
 * exists"): no status, no problem code. The route's sentence rides in `title`
 * for anyone who hovers. It is `absent`, never the `Failure` that a request
 * that could not be answered gets (R5 §5).
 */
export function GoneNotice({ what, detail }: { what: string; detail?: string | undefined }): ReactNode {
  return (
    <p role="status" data-state="absent" data-gone="" className="p-3 text-sm text-muted" title={detail === '' ? undefined : detail}>
      {what} no longer exists
    </p>
  );
}

/**
 * A request that produced no answer at all: a problem object, a body that was
 * not JSON, an envelope that broke its own contract, or no status at all.
 *
 * **These are not states and are never drawn as one.** A `NotJsonError` is a
 * proxy or a misconfigured route, an `EnvelopeError` is a bug in this product,
 * and telling an operator `unreadable` for either sends them to look at a
 * cluster that is working.
 */
export function Failure({ error, onRetry }: { error: unknown; onRetry?: (() => void) | undefined }): ReactNode {
  const api = error instanceof ApiError ? error : undefined;

  const title =
    api === undefined
      ? 'Something failed'
      : api instanceof EnvelopeError
        ? "The console's own API answered out of contract"
        : api instanceof NotJsonError
          ? 'The answer was not JSON'
          : api.status === 0
            ? 'The console could not be reached'
            : `The request failed (${String(api.status)})`;

  return (
    <div role="alert" data-box className="rounded-lg border border-line bg-panel-2 p-3 text-sm">
      <p className="font-medium text-bad">{title}</p>
      <p className="mt-1 text-muted">{error instanceof Error ? error.message : String(error)}</p>
      {/* The server's own plain-text sentence, where it sent one - the extension
          gate's refusal is the case this exists for. Text, never markup: React
          escapes it, and `NotJsonError.excerpt` keeps plain-text bodies only. */}
      {api instanceof NotJsonError && api.excerpt !== undefined ? (
        <p data-excerpt className="mt-1 whitespace-pre-wrap break-words">
          {api.excerpt}
        </p>
      ) : null}
      {api?.code !== undefined ? <p className="mt-1 text-xs text-muted">code: {api.code}</p> : null}
      {onRetry !== undefined ? (
        <button type="button" onClick={onRetry} className={`mt-2 ${btnSecondary}`}>
          Try again
        </button>
      ) : null}
    </div>
  );
}

/**
 * Nothing has come back yet. Distinct from every state above, and from two of
 * them in particular:
 *
 * - from **`empty`**, because a list still loading has not said there is nothing
 *   in it;
 * - from **`pending`**, which is the closer call and the newer one. This says
 *   *the browser is waiting for an answer*; `pending` says *the answer arrived
 *   and it is that the server has not asked yet*. A request that is still in
 *   flight has nothing to draw; a `pending` envelope is a considered 200 with a
 *   reason in it, and it is drawn by `StateNotice` like any other state.
 *
 * `aria-busy` belongs here and not there, for the same reason: this is the one
 * of the two where something really is outstanding in this page.
 */
export function Pending({ what, compact = false }: { what: string; compact?: boolean }): ReactNode {
  return (
    <p
      role="status"
      aria-busy="true"
      className={compact ? 'px-2 py-1 text-xs text-muted' : 'p-3 text-sm text-muted'}
    >
      Reading {what}…
    </p>
  );
}
