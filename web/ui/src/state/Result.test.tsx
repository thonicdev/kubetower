import { useQuery } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import type { ListResult, ValueResult } from '../api';
import { renderWithClient } from '../testing/harness';
import { ListView, ValueView } from './Result';

// A fresh key per mount, so one test's answer is never another's cache hit.
// Read once per component through `useState`, because computing it in the render
// body gives every re-render a new key - which is a query that is created,
// starts, and is replaced before it can settle, so the view never leaves
// `Pending` and every assertion below would be waiting on nothing.
let seq = 0;

function ListHarness({
  result,
  onItems,
}: {
  result: ListResult<string>;
  onItems?: (items: readonly string[]) => void;
}) {
  const [key] = useState(() => (seq += 1));
  const query = useQuery({ queryKey: ['list', key], queryFn: () => Promise.resolve(result) });
  return (
    <ListView query={query} what="things">
      {(items) => {
        onItems?.(items);
        return (
          <ul data-testid="items">
            {items.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        );
      }}
    </ListView>
  );
}

function EmptyPropHarness({
  result,
  ...given
}: {
  result: ListResult<string>;
  empty?: ReactNode;
}) {
  const [key] = useState(() => (seq += 1));
  const query = useQuery({ queryKey: ['list', key], queryFn: () => Promise.resolve(result) });
  // `given` is spread rather than passed as `empty={empty}`, so that "the prop
  // was not supplied" is expressible at all - writing `empty={undefined}` and
  // omitting it are the same call, and the distinction under test is exactly
  // the one that gets flattened by writing the test the obvious way.
  return (
    <ListView query={query} what="things" {...given}>
      {(items) => <ul data-testid="items">{items.length}</ul>}
    </ListView>
  );
}

function ValueHarness({ result }: { result: ValueResult<string> }) {
  const [key] = useState(() => (seq += 1));
  const query = useQuery({ queryKey: ['value', key], queryFn: () => Promise.resolve(result) });
  return (
    <ValueView query={query} what="the thing">
      {(data) => <p data-testid="data">{data}</p>}
    </ValueView>
  );
}

const reason = { code: 'cluster_unreachable', message: 'the API server did not answer' } as const;

/** The notice a title belongs to. `findByRole('status')` would also match the
 *  pending line, which is the one thing these tests must not accept. */
async function noticeFor(title: string): Promise<HTMLElement> {
  const heading = await screen.findByText(title);
  const notice = heading.closest('[data-state]');
  if (notice === null) throw new Error(`"${title}" is not inside a state notice`);
  return notice as HTMLElement;
}

describe('a list that could not be read never looks like a list with nothing in it', () => {
  it.each([
    ['unreadable', 'Could not be read'],
    ['absent', 'Not installed'],
    ['unreachable', 'Could not be asked'],
    ['forbidden', 'Refused by the cluster'],
  ] as const)('%s draws its own notice and does not render the list', async (state, title) => {
    const onItems = vi.fn();
    renderWithClient(<ListHarness result={{ state, reason }} onItems={onItems} />);

    const notice = await noticeFor(title);
    expect(notice.getAttribute('data-state')).toBe(state);
    expect(notice.textContent).toContain(reason.message);

    // The load-bearing assertion. The render function is the only way items
    // reach the screen, and on these four arms it is never called - so there is
    // no list, empty or otherwise, for an operator to read as "nothing here".
    expect(onItems).not.toHaveBeenCalled();
    expect(screen.queryByTestId('items')).toBeNull();
  });

  it('draws empty as its own answer, distinct from all four', async () => {
    const onItems = vi.fn();
    renderWithClient(<ListHarness result={{ state: 'empty', items: [] }} onItems={onItems} />);

    const notice = await noticeFor('Nothing here');
    expect(notice.getAttribute('data-state')).toBe('empty');
    expect(onItems).not.toHaveBeenCalled();
  });

  it('renders the list when there is one', async () => {
    renderWithClient(<ListHarness result={{ state: 'ok', items: ['prod', 'staging'] }} />);
    const list = await screen.findByTestId('items');
    expect(list.textContent).toContain('prod');
    expect(list.textContent).toContain('staging');
  });
});

describe('retry is offered only where the server says it could differ', () => {
  it('offers nothing on a refusal', async () => {
    renderWithClient(
      <ListHarness result={{ state: 'forbidden', reason: { code: 'verb_denied', message: 'no' } }} />,
    );
    await noticeFor('Refused by the cluster');
    // `retryable` "drives a retry button, not a retry loop", and a console that
    // retries a denial on its own writes it into the audit log every few seconds.
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull();
  });

  it('offers a button where the reason is retryable', async () => {
    renderWithClient(
      <ListHarness result={{ state: 'unreachable', reason: { ...reason, retryable: true } }} />,
    );
    expect(await screen.findByRole('button', { name: 'Try again' })).toBeTruthy();
  });
});

describe('a value envelope', () => {
  it('renders the data on ok', async () => {
    renderWithClient(<ValueHarness result={{ state: 'ok', data: 'a menu' }} />);
    expect((await screen.findByTestId('data')).textContent).toBe('a menu');
  });

  it('draws absent rather than nothing', async () => {
    renderWithClient(
      <ValueHarness
        result={{ state: 'absent', reason: { code: 'not_installed', message: 'no metrics source' } }}
      />,
    );
    const notice = await noticeFor('Not installed');
    expect(notice.getAttribute('data-state')).toBe('absent');
    expect(screen.queryByTestId('data')).toBeNull();
  });
});

describe('a failed request is not a state', () => {
  it('is drawn as an alert and not as one of the five', async () => {
    function Broken() {
      const [key] = useState(() => (seq += 1));
      const query = useQuery<ListResult<string>>({
        queryKey: ['broken', key],
        queryFn: () => Promise.reject(new Error('nope')),
        retry: false,
      });
      return (
        <ListView query={query} what="things">
          {() => <p>never</p>}
        </ListView>
      );
    }

    renderWithClient(<Broken />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('nope');
    expect(alert.querySelector('[data-state]')).toBeNull();
  });
});

// `empty={null}` means "draw nothing", and `undefined` means "draw the
// default". Reading `ListViewProps.empty` with `??`, which falls through on
// `null` as well, would select the very notice the caller is suppressing - and
// the type would check, so only a test that distinguishes the two catches it.
describe('an empty answer nobody wanted drawn', () => {
  it('says "Nothing here" when no empty was given', async () => {
    renderWithClient(
      <EmptyPropHarness result={{ state: 'empty', items: [], reason: { code: 'empty_scope', message: 'the namespace holds none of these' } }} />,
    );
    expect(await screen.findByText(/nothing here/i)).toBeTruthy();
  });

  it('draws nothing at all when empty is null', async () => {
    renderWithClient(
      <EmptyPropHarness result={{ state: 'empty', items: [], reason: { code: 'empty_scope', message: 'the namespace holds none of these' } }} empty={null} />,
    );
    // Waited for rather than asserted immediately: the view passes through
    // `Pending` first, so an assertion taken straight away would pass against a
    // component still loading and prove nothing. The wait is on the pending
    // line going away, because on this arm there is nothing else to wait for -
    // drawing nothing is the whole assertion.
    await waitFor(() => {
      expect(screen.queryByText(/Reading things/i)).toBeNull();
    });
    expect(screen.queryByText(/nothing here/i)).toBeNull();
  });

  it('draws what it is given when empty is an element', async () => {
    renderWithClient(
      <EmptyPropHarness
        result={{ state: 'empty', items: [], reason: { code: 'empty_scope', message: 'the namespace holds none of these' } }}
        empty={<p>no extras are built in</p>}
      />,
    );
    expect(await screen.findByText('no extras are built in')).toBeTruthy();
    expect(screen.queryByText(/nothing here/i)).toBeNull();
  });

  // The half that would otherwise be silently disabled by the fix above: a
  // *failure* is not the empty arm and must keep its notice whatever `empty`
  // says. Passing `null` to hide "nothing here" must never hide "unreachable".
  it('still draws a failure when empty is null', async () => {
    renderWithClient(
      <EmptyPropHarness
        result={{ state: 'unreachable', reason: { code: 'timeout', message: 'no answer within 10s' } }}
        empty={null}
      />,
    );
    // The notice's own title, not the state's name: `StateNotice` writes
    // "Could not be asked" for `unreachable`, and a test matching the wire
    // value would pass against a component that renders the raw state.
    expect(await screen.findByText('Could not be asked')).toBeTruthy();
  });
});
