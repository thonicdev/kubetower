import { EnvelopeError } from './errors';
import type { Reason, State } from './types';

/**
 * The six states, turned into something a component cannot render wrongly.
 *
 * ## What the generated type gives us, and what it does not
 *
 * `openapi-typescript` renders `ListArms` faithfully: `items` is **optional**,
 * so `envelope.items.map(...)` does not compile. That is already most of the
 * value and it is why the spec is written the way it is - the collapse becomes
 * opt-in and visible at the call site.
 *
 * What it cannot give us is a *discriminated* union: the generated shape is one
 * object with an optional member, so `state === 'ok'` does not narrow `items`
 * to an array, and the shortest way past the compiler is `items ?? []` - which
 * is the collapse, written by the same developer who was warned, in one keypress.
 *
 * So the wire shape is narrowed here, exactly once, into a union whose `state`
 * *is* the discriminant. After `asList`, `items` exists on two arms and on no
 * other, and there is nothing to default.
 *
 * ## And the arms are checked rather than trusted
 *
 * `api/errors.md` states the rules as a JSON Schema `if/then` so the conformance
 * test catches a violation "rather than a reviewer having to notice". That test
 * is on the Go side. This is the same check on the client, and it earns its keep
 * on one case: `{"state":"ok","items":[]}` is a spec violation that no type can
 * catch and that renders, calmly, as a cluster with nothing in it. A console
 * once reported "no pods" for a cluster running 429 of them; that is the shape
 * it had.
 *
 * A violation raises `EnvelopeError` and therefore reaches the UI as a
 * *failure*, never as one of the six states - it is a fault in this product,
 * not a fact about a cluster, and an operator told `unreadable` would go and
 * look at the cluster.
 *
 * ## `pending` is carried here and is deliberately not a failure
 *
 * Two types rather than one, because the two questions they answer are
 * different and merging them is the defect:
 *
 * - **`ReasonOnlyState`** - this arm carries a `reason` and no payload. That is
 *   an arm rule and `pending` obeys it exactly like the four failures do, which
 *   is why `asList` and `asValue` check against this set.
 * - **`FailedState`** - a read happened and did not produce data. `pending` is
 *   **not** in it: nothing was tried, so there is nothing that failed and nobody
 *   has anything to fix. `components/Remedy.tsx` is keyed on this type, and a
 *   `pending` arm in that table would be the console inventing a remedy for a
 *   question it has not yet asked.
 *
 * A type spelled `Exclude<State, 'ok' | 'empty'>` would silently grow a member
 * when the contract does, while a runtime list beside it would not, and a
 * cluster nobody had probed yet would read as *"the console's own API answered
 * out of contract"*. So the two are written out separately and
 * `everyReasonOnlyState` is asserted in `envelope.test.ts`.
 */

/** The four states that are an answer to a read that happened and produced no
 *  data. `empty` is not among them - it is an answer with a list in it, and `ok`
 *  has one too - and neither is `pending`, where no read happened at all. */
export type FailedState = Exclude<State, 'ok' | 'empty' | 'pending'>;

/** Every arm that carries a `reason` and no payload: the four failures, and
 *  `pending`. What a client draws for them differs - a failure invites a retry,
 *  `pending` invites waiting - but what the envelope carries does not. */
export type ReasonOnlyState = FailedState | 'pending';

const FAILED: readonly FailedState[] = ['unreadable', 'absent', 'unreachable', 'forbidden'];

const REASON_ONLY: readonly ReasonOnlyState[] = [...FAILED, 'pending'];

export function isFailedState(state: State): state is FailedState {
  return (FAILED as readonly State[]).includes(state);
}

export function isReasonOnlyState(state: State): state is ReasonOnlyState {
  return (REASON_ONLY as readonly State[]).includes(state);
}

/** The runtime list, exported so a test can assert it against the generated
 *  `State` union rather than against this file's own memory of it. */
export { REASON_ONLY as everyReasonOnlyState, FAILED as everyFailedState };

/** A collection, narrowed. `items` exists on `ok` and `empty` and nowhere else. */
export type ListResult<T> =
  | { readonly state: 'ok'; readonly items: readonly T[]; readonly reason?: Reason }
  | { readonly state: 'empty'; readonly items: readonly []; readonly reason?: Reason }
  | { readonly state: ReasonOnlyState; readonly reason: Reason };

/** A single value, narrowed. There is no `empty` arm - `api/errors.md`: "a
 *  single thing that is not there is `absent`", and two spellings of one fact is
 *  how two clients end up disagreeing about which to check. */
export type ValueResult<T> =
  | { readonly state: 'ok'; readonly data: T; readonly reason?: Reason }
  | { readonly state: ReasonOnlyState; readonly reason: Reason };

/** The wire shape, as `openapi-typescript` renders every `*ListEnvelope`. */
export interface ListEnvelopeWire<T> {
  state: State;
  reason?: Reason;
  items?: T[];
}

/** The wire shape, as `openapi-typescript` renders every value envelope. */
export interface ValueEnvelopeWire<T> {
  state: State;
  reason?: Reason;
  data?: T;
}

export function asList<T>(wire: ListEnvelopeWire<T>): ListResult<T> {
  const { state, reason, items } = wire;

  if (state === 'ok') {
    if (!Array.isArray(items)) throw new EnvelopeError('state "ok" without an items array', wire);
    // The one that matters. Zero elements read successfully is `empty`, and
    // allowing both spellings puts the collapse straight back in.
    if (items.length === 0) throw new EnvelopeError('state "ok" with an empty items array', wire);
    return reason === undefined ? { state, items } : { state, items, reason };
  }

  if (state === 'empty') {
    if (!Array.isArray(items)) throw new EnvelopeError('state "empty" without an items array', wire);
    if (items.length > 0) throw new EnvelopeError('state "empty" with a non-empty items array', wire);
    return reason === undefined ? { state, items: [] } : { state, items: [], reason };
  }

  // `pending` passes through here with the four failures: the arm rule is the
  // same one. It must never fail this check: a cluster nobody had probed would
  // read as a contract violation.
  if (!isReasonOnlyState(state)) {
    throw new EnvelopeError(`unknown state ${JSON.stringify(state)}`, wire);
  }
  if (reason === undefined) throw new EnvelopeError(`state "${state}" without a reason`, wire);
  // The spec says no `items` member at all - "not `[]`, not `null`". A `null`
  // is let through because it carries nothing and is what a nil Go slice
  // marshals to; an *array* is not, because an array is the one thing a
  // renderer can map over, which is the failure being prevented.
  if (Array.isArray(items)) throw new EnvelopeError(`state "${state}" carrying items`, wire);
  return { state, reason };
}

export function asValue<T>(wire: ValueEnvelopeWire<T>): ValueResult<T> {
  const { state, reason, data } = wire;

  if (state === 'empty') throw new EnvelopeError('state "empty" on a value envelope', wire);

  if (state === 'ok') {
    if (data === undefined) throw new EnvelopeError('state "ok" without data', wire);
    return reason === undefined ? { state, data } : { state, data, reason };
  }

  if (!isReasonOnlyState(state)) {
    throw new EnvelopeError(`unknown state ${JSON.stringify(state)}`, wire);
  }
  if (reason === undefined) throw new EnvelopeError(`state "${state}" without a reason`, wire);
  if (data !== undefined && data !== null) throw new EnvelopeError(`state "${state}" carrying data`, wire);
  return { state, reason };
}
