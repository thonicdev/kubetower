import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { DetailAction } from '../../api';
import { KeyboardStack } from '../../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../../keyboard/stack';
import { renderWithClient } from '../../testing/harness';
import type { ObjectIdentity } from './address';
import { useObjectAct } from './useObjectAct';

/**
 * The hook's own refusal, on its own. The row menu
 * draws its verbs disabled while a confirmation is open, and that is tested
 * with the menu; this pins the guard *inside* `start`, which is what holds for
 * any caller that does not disable anything - a second `start` while a plan is
 * open asks nothing, and the dialog keeps naming the first object.
 */

afterEach(() => {
  vi.unstubAllGlobals();
  resetKeyboardStack();
});

const DELETE: DetailAction = { verb: 'delete', label: 'Delete', permitted: true, destructive: true, irreversible: true };
const A: ObjectIdentity = { cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'api-a' };
const B: ObjectIdentity = { cluster: 'prod', kind: 'pods', namespace: 'shop', name: 'api-b' };

function Harness(): ReactNode {
  const act = useObjectAct();
  return (
    <>
      <button type="button" onClick={() => act.start(A, DELETE)}>
        start a
      </button>
      <button type="button" onClick={() => act.start(B, DELETE)}>
        start b
      </button>
      <span data-busy={String(act.busy)} />
      {act.dialog}
    </>
  );
}

describe('useObjectAct', () => {
  it('starts nothing while a plan is open, whatever the caller draws', async () => {
    const posted: unknown[] = [];
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') posted.push(JSON.parse(String(init.body)));
      const body = {
        token: `plan-${String(posted.length)}`,
        expiresAt: '2999-01-01T00:00:00Z',
        sentence: 'Delete the pod api-a in shop.',
        facts: {
          verb: 'delete',
          button: 'Delete it',
          count: 1,
          destructive: true,
          targets: [{ kind: 'pods', namespace: 'shop', name: 'api-a' }],
          confirmation: { kind: 'name', expected: 'api-a', label: 'Type api-a to confirm' },
        },
      };
      void input;
      return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    });
    renderWithClient(
      <KeyboardStack>
        <Harness />
      </KeyboardStack>,
    );

    act(() => {
      fireEvent.click(screen.getByRole('button', { name: 'start a' }));
    });
    await waitFor(() => {
      expect(document.body.textContent).toContain('Delete api-a?');
    });
    expect(document.querySelector('[data-busy]')?.getAttribute('data-busy')).toBe('true');

    act(() => {
      fireEvent.click(screen.getByRole('button', { name: 'start b' }));
    });
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(posted).toHaveLength(1);
    expect(document.body.textContent).toContain('Delete api-a?');
    expect(document.body.textContent).not.toContain('Delete api-b?');
  });
});
