import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import type { FailedState } from '../api';
import { Remedy, remedies } from './Remedy';

const STATES: FailedState[] = ['unreadable', 'absent', 'unreachable', 'forbidden'];

/** The remedy, drawn as an extra would: no router and no query client. It
 *  needs neither - it links nowhere and reads nothing. */
function show(state: FailedState, reason?: Parameters<typeof Remedy>[0]['reason']): void {
  render(<Remedy state={state} reason={reason} />);
}

describe('the remedy', () => {
  it('says something different for every failed state', () => {
    // Asserted on the table rather than on a rendering of it, because the table
    // is where a fifth state would be added and where two of them would come to
    // say the same thing. Four sentences, four next steps.
    const sentences = new Set(STATES.map((state) => remedies[state]));
    expect(sentences.size).toBe(STATES.length);
  });

  it('sends the operator to a different place for each', () => {
    // The distinction that matters is not the wording. It is: wait
    // (unreachable), ask for a role (forbidden), install something (absent),
    // look at this console rather than the cluster (unreadable).
    expect(remedies.unreachable).toMatch(/try again/i);
    expect(remedies.forbidden).toMatch(/role/i);
    expect(remedies.absent).toMatch(/install/i);
    expect(remedies.unreadable).toMatch(/this console/);
  });

  it('spells out what to ask for, from the scope the server sent', () => {
    show('forbidden', {
      code: 'verb_denied',
      message: 'refused',
      scope: { verb: 'list', resource: 'pods', namespace: 'shop-prod' },
    });
    expect(screen.getByText('Ask for: list on pods in namespace shop-prod.')).toBeTruthy();
  });

  it('says cluster-wide when the scope names no namespace', () => {
    show('forbidden', {
      code: 'verb_denied',
      message: 'refused',
      scope: { verb: 'list', resource: 'nodes' },
    });
    expect(screen.getByText('Ask for: list on nodes, cluster-wide.')).toBeTruthy();
  });

  it('invents nothing when the server sent no scope', () => {
    // A denial an operator can paste into a request for access is worth more
    // than one they have to translate - but a guessed one is worth less than
    // nothing, because it names a permission that may not be the one refused.
    show('forbidden', { code: 'verb_denied', message: 'refused' });
    expect(screen.queryByText(/^Ask for:/)).toBeNull();
  });

  it('names no page for unreadable, and links nowhere in any state', () => {
    // `/health` leads to the cluster list, so the sentence says where the fault is - this
    // console - and sends nobody to a page that is not there.
    for (const state of STATES) {
      const { unmount } = render(<Remedy state={state} />);
      expect(screen.queryByRole('link')).toBeNull();
      expect(document.body.textContent).not.toMatch(/Cluster page/);
      unmount();
    }
  });

  it('says an object is gone, and not that its kind needs installing', () => {
    render(<Remedy state="absent" reason={{ code: 'no_such_object', message: 'configmaps "web" not found' }} />);
    expect(document.body.textContent).toContain('deleted');
    expect(document.body.textContent).not.toContain(remedies.absent);
  });

  it('draws with no query client and no router', () => {
    show('unreadable');
    expect(screen.getByText(remedies.unreadable)).toBeTruthy();
  });
});
