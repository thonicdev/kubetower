import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { NotJsonError } from '../api';
import { Failure, PANEL_BOX, Pending, StateNotice } from './StateNotice';

afterEach(cleanup);

describe('Failure', () => {
  it("shows a plain-text body's sentence, where the server sent one", () => {
    // The extension gate's refusal is a sentence for the operator; the failure
    // draws it under its own title rather than only "expected JSON".
    const sentence = 'Alfa Center is not turned on for this cluster.';
    render(<Failure error={new NotJsonError(404, 'text/plain; charset=utf-8', sentence)} />);

    expect(screen.getByText('The answer was not JSON')).toBeDefined();
    expect(screen.getByText(sentence).hasAttribute('data-excerpt')).toBe(true);
  });

  it('draws the excerpt as text, never as markup', () => {
    const excerpt = '<b>bold</b><img src=x onerror=alert(1)>';
    const { container } = render(<Failure error={new NotJsonError(500, 'text/plain', excerpt)} />);

    expect(container.querySelector('[data-excerpt]')?.textContent).toBe(excerpt);
    expect(container.querySelector('b, img')).toBeNull();
  });

  it('draws nothing extra when there is no excerpt', () => {
    const { container } = render(<Failure error={new NotJsonError(401, 'text/html')} />);

    expect(container.querySelector('[data-excerpt]')).toBeNull();
  });
});

describe('StateNotice', () => {
  it('draws an empty state as its title alone, with no sentence about it', () => {
    const { container } = render(<StateNotice state="empty" />);

    expect(container.textContent).toBe('Nothing here');
    expect(container.textContent).not.toContain('succeeded');
    expect(container.querySelectorAll('p')).toHaveLength(1);
  });

  it('asks again about something not installed, rather than offering to retry a failure', () => {
    // "Not installed" over "Try again" (#133 item 7): nothing failed.
    const reason = { code: 'unclassified' as const, message: 'No such kind here.', retryable: true };
    const { rerender } = render(<StateNotice state="absent" reason={reason} onRetry={() => {}} />);
    expect(screen.getByRole('button').textContent).toBe('Refresh');

    rerender(<StateNotice state="unreachable" reason={reason} onRetry={() => {}} />);
    expect(screen.getByRole('button').textContent).toBe('Try again');
  });

  it("uses the state's own word unless a title names a different subject, and keeps the state", () => {
    const { container, rerender } = render(<StateNotice state="absent" />);
    expect(container.textContent).toContain('Not installed');

    rerender(<StateNotice state="absent" title="No such page in this console" />);
    expect(container.textContent).toContain('No such page in this console');
    expect(container.textContent).not.toContain('Not installed');
    expect(container.querySelector('[data-state]')?.getAttribute('data-state')).toBe('absent');
  });
});

// The list panel has no padding, so a boxed notice drawn in
// the table's place takes its margin from `PANEL_BOX`, keyed on `data-box`. The
// pending line keeps its own padding and is not boxed.
describe('a notice in the list panel', () => {
  it('marks the two boxes, and only them, for the panel to set apart', () => {
    const { container } = render(
      <div id="panel" className={PANEL_BOX}>
        <Failure error={new Error('boom')} />
        <StateNotice state="unreachable" reason={undefined} />
        <StateNotice state="unreachable" reason={undefined} compact />
        <Pending what="the pods" />
      </div>,
    );
    const boxed = [...container.querySelectorAll('#panel > [data-box]')].map((box) => box.getAttribute('role'));
    expect(boxed).toStrictEqual(['alert', 'status']);
    expect(PANEL_BOX).toBe('[&>[data-box]]:m-3');
  });
});

// An object not there any more, on a cluster that serves its kind, is gone -
// never "Not installed", whose next move is to install something.
describe('an object that is gone', () => {
  it('reads as gone, and a kind nothing serves still reads as not installed', () => {
    const { container, unmount } = render(
      <StateNotice state="absent" reason={{ code: 'no_such_object', message: 'configmaps "web" not found' }} />,
    );
    expect(container.textContent).toContain('No longer exists');
    expect(container.textContent).not.toContain('Not installed');
    unmount();
    render(<StateNotice state="absent" reason={{ code: 'no_such_kind', message: 'no kind called widgets' }} />);
    expect(document.body.textContent).toContain('Not installed');
  });
});
