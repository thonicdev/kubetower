import { describe, expect, it } from 'vitest';

import {
  asList,
  asValue,
  everyFailedState,
  everyReasonOnlyState,
  isFailedState,
  type ReasonOnlyState,
} from './envelope';
import { EnvelopeError } from './errors';
import type { Reason } from './types';

const reason: Reason = { code: 'cluster_unreachable', message: 'the API server did not answer' };

const notProbed: Reason = {
  code: 'not_probed_yet',
  message: 'This cluster has not been probed yet.',
};

describe('asList', () => {
  it('narrows ok so that items exists', () => {
    const result = asList<string>({ state: 'ok', items: ['prod'] });
    expect(result.state).toBe('ok');
    // The discriminant does the work: on any other arm there is no `items` to
    // read, which is what makes `items ?? []` impossible to write.
    if (result.state !== 'ok') throw new Error('unreachable');
    expect(result.items).toEqual(['prod']);
  });

  it('narrows empty to an empty list that is still a list', () => {
    const result = asList<string>({ state: 'empty', items: [] });
    if (result.state !== 'empty') throw new Error('unreachable');
    expect(result.items).toHaveLength(0);
  });

  it.each(['unreadable', 'absent', 'unreachable', 'forbidden'] as const)(
    'narrows %s to a reason and no items',
    (state) => {
      const result = asList<string>({ state, reason });
      expect(result.state).toBe(state);
      expect('items' in result).toBe(false);
    },
  );

  // The one the whole design exists for. A console once reported "no pods" for a
  // cluster running 429 of them, and this is the shape it had.
  it('refuses ok with an empty list', () => {
    expect(() => asList<string>({ state: 'ok', items: [] })).toThrow(EnvelopeError);
  });

  it('refuses a failed state carrying an items array', () => {
    // `[]` on a failed arm is the one thing a renderer can map over, so it is
    // refused where a `null` - which carries nothing - is let through.
    expect(() => asList<string>({ state: 'unreachable', reason, items: [] })).toThrow(EnvelopeError);
    expect(() =>
      asList<string>({ state: 'unreachable', reason, items: null as unknown as string[] }),
    ).not.toThrow();
  });

  it('refuses a failed state with no reason', () => {
    expect(() => asList<string>({ state: 'forbidden' })).toThrow(EnvelopeError);
  });

  it('refuses a state it has never heard of', () => {
    expect(() => asList<string>({ state: 'partial' as never })).toThrow(EnvelopeError);
  });
});

describe('asValue', () => {
  it('narrows ok to data', () => {
    const result = asValue<{ groups: [] }>({ state: 'ok', data: { groups: [] } });
    if (result.state !== 'ok') throw new Error('unreachable');
    expect(result.data.groups).toEqual([]);
  });

  // "A single thing that is not there is `absent`", and two spellings of one
  // fact is how two clients end up disagreeing about which to check.
  it('refuses empty on a value', () => {
    expect(() => asValue<number>({ state: 'empty' })).toThrow(EnvelopeError);
  });

  it('refuses ok with no data', () => {
    expect(() => asValue<number>({ state: 'ok' })).toThrow(EnvelopeError);
  });

  it.each(['unreadable', 'absent', 'unreachable', 'forbidden'] as const)(
    'narrows %s to a reason and no data',
    (state) => {
      const result = asValue<number>({ state, reason });
      expect(result.state).toBe(state);
      expect('data' in result).toBe(false);
    },
  );
});

/**
 * The sixth state, and the two things about it that are not the same thing.
 *
 * `pending` obeys the **arm** rule of the four failures - a reason, no payload -
 * and is not one of them. Were it counted as a failure in the type and not in
 * the runtime list, `asList` would throw `EnvelopeError` on every `pending`
 * answer - *"the console's own API answered out of contract"* for a cluster
 * that is simply next in the queue.
 */
describe('pending', () => {
  it('is an arm and not a violation, on both envelope shapes', () => {
    const list = asList<string>({ state: 'pending', reason: notProbed });
    expect(list.state).toBe('pending');
    expect('items' in list).toBe(false);

    const value = asValue<number>({ state: 'pending', reason: notProbed });
    expect(value.state).toBe('pending');
    expect('data' in value).toBe(false);
  });

  it('still has to carry a reason, and still may not carry a payload', () => {
    expect(() => asList<string>({ state: 'pending' })).toThrow(EnvelopeError);
    expect(() => asList<string>({ state: 'pending', reason: notProbed, items: [] })).toThrow(
      EnvelopeError,
    );
    expect(() => asValue<number>({ state: 'pending', reason: notProbed, data: 1 })).toThrow(
      EnvelopeError,
    );
  });

  it('is not a failure, so nothing keyed on failure has an arm for it', () => {
    // `components/Remedy.tsx` is a `Record<FailedState, string>` - what the
    // operator does next. For a cluster nobody has asked yet the answer is
    // nothing, and an arm there would be the console inventing a next step for
    // a question it has not put.
    expect(isFailedState('pending')).toBe(false);
    expect(everyFailedState).not.toContain('pending');
    expect(everyReasonOnlyState).toContain('pending');
  });

  /*
    The guard against the drift that caused all of the above: the runtime list
    and the type are asserted against each other rather than trusted to stay in
    step. `EVERY` is typed, so a seventh state fails to compile here before any
    test runs - and the comparison below catches the other direction, a member
    added to the type and forgotten in the list.
  */
  it('and the list of reason-only states is the type, not a memory of it', () => {
    const EVERY: Readonly<Record<ReasonOnlyState, true>> = {
      pending: true,
      unreadable: true,
      absent: true,
      unreachable: true,
      forbidden: true,
    };
    expect([...everyReasonOnlyState].sort()).toEqual(Object.keys(EVERY).sort());
  });
});

describe('an envelope violation is a failure, never a state', () => {
  it('is an EnvelopeError with the request treated as having succeeded', () => {
    try {
      asList<string>({ state: 'ok', items: [] });
      throw new Error('unreachable');
    } catch (error) {
      expect(error).toBeInstanceOf(EnvelopeError);
      // 200, because the request did succeed. It is a bug in this product and
      // not a fact about a cluster - an operator told `unreadable` here would go
      // and look at a cluster that is working.
      expect((error as EnvelopeError).status).toBe(200);
      expect((error as EnvelopeError).isUnauthenticated).toBe(false);
    }
  });
});
