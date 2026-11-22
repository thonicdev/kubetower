import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { createRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { CopyButton, copyOutcomes } from './CopyButton';

interface ExecCapable {
  execCommand?: (command: string) => boolean;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  Reflect.deleteProperty(document, 'execCommand');
  Reflect.deleteProperty(navigator, 'clipboard');
});

/** `navigator.clipboard` is absent in jsdom and read-only where it exists, so it
 *  is defined rather than assigned. `undefined` is the shape of a page that is
 *  not a secure context, which is the case step 2 exists for. */
function withClipboard(writeText: ((value: string) => Promise<void>) | undefined): void {
  Object.defineProperty(navigator, 'clipboard', {
    value: writeText === undefined ? undefined : { writeText },
    configurable: true,
  });
}

function withExec(result: boolean | (() => boolean)): void {
  (document as Document & ExecCapable).execCommand = vi.fn(() =>
    typeof result === 'function' ? result() : result,
  );
}

function button(): HTMLButtonElement {
  const element = document.querySelector('button');
  if (element === null) throw new Error('no button');
  return element;
}

describe('step 1: the Clipboard API', () => {
  it('writes the whole value, not what is drawn, and says so', async () => {
    vi.stubGlobal('isSecureContext', true);
    const writeText = vi.fn(() => Promise.resolve());
    withClipboard(writeText);

    render(<CopyButton value="a.example,b.example,c.example" label="Copy the addresses" />);
    fireEvent.click(button());

    await waitFor(() => {
      expect(button().dataset['copyState']).toBe('copied');
    });
    // The capped column shows three of five hosts; the button copies all five.
    expect(writeText).toHaveBeenCalledWith('a.example,b.example,c.example');
  });

  it('returns to its resting state, so the row beside it is not confusing', () => {
    vi.useFakeTimers();
    vi.stubGlobal('isSecureContext', false);
    withExec(true);

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());
    expect(button().dataset['copyState']).toBe('copied');

    act(() => {
      vi.advanceTimersByTime(1400);
    });
    expect(button().dataset['copyState']).toBe('idle');
  });
});

describe('step 2: the console reached over plain http on a LAN address', () => {
  it('never asks for the Clipboard API outside a secure context', () => {
    // 127.0.0.1 IS a secure context by the specification; 10.0.0.4 is not, and
    // that is how this console is most often reached. Reading `.writeText` off
    // an undefined `navigator.clipboard` would throw before anything could be
    // reported, which is why the absence is checked rather than assumed.
    vi.stubGlobal('isSecureContext', false);
    withClipboard(undefined);
    withExec(true);

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());

    expect(button().dataset['copyState']).toBe('copied');
    expect(document.execCommand).toHaveBeenCalledWith('copy');
  });

  it('falls through to it when the Clipboard API refuses', async () => {
    vi.stubGlobal('isSecureContext', true);
    withClipboard(() => Promise.reject(new Error('permissions policy')));
    withExec(true);

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());

    // A rejected promise is a refusal by the browser or by a permissions policy,
    // not a bug here. Reporting a failure the operator can do nothing about is
    // the thing to avoid.
    await waitFor(() => {
      expect(button().dataset['copyState']).toBe('copied');
    });
  });

  it('leaves no textarea behind when the copy throws', () => {
    vi.stubGlobal('isSecureContext', false);
    withExec(() => {
      throw new Error('not allowed');
    });

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());

    // One left in the document is a focusable, selectable element a keyboard can
    // reach and nothing accounts for.
    expect(document.querySelectorAll('textarea')).toHaveLength(0);
  });
});

describe('step 3: both refused, and the value is still obtainable', () => {
  it('selects the shown text in place and says to press Ctrl+C', () => {
    vi.stubGlobal('isSecureContext', false);
    withExec(false);

    const shown = createRef<HTMLSpanElement>();
    render(
      <p>
        <span ref={shown}>10.0.0.1</span>
        <CopyButton value="10.0.0.1" label="Copy" selectRef={shown} />
      </p>,
    );
    fireEvent.click(button());

    expect(button().dataset['copyState']).toBe('selected');
    expect(button().getAttribute('aria-label')).toBe(copyOutcomes.selected);
    // A selection the operator can press Ctrl+C on is obtainable. A button that
    // quietly did nothing is the defect the whole component exists to avoid.
    expect(window.getSelection()?.toString()).toBe('10.0.0.1');
  });

  it('says it plainly when there is nothing it can select either', () => {
    vi.stubGlobal('isSecureContext', false);
    withExec(false);

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());

    expect(button().dataset['copyState']).toBe('refused');
    expect(button().getAttribute('aria-label')).toBe(copyOutcomes.refused);
  });

  it('reports through a live region rather than only through the label', () => {
    vi.stubGlobal('isSecureContext', false);
    withExec(false);

    render(<CopyButton value="10.0.0.1" label="Copy" />);
    fireEvent.click(button());

    // `aria-label` is read when the control is next visited, which may be never.
    const said = document.querySelector('[role="status"]');
    expect(said?.textContent).toBe(copyOutcomes.refused);
  });

  it('says three different things', () => {
    // Three outcomes collapsing into one message is a silent loss in eleven
    // characters, and the assertion is on the table rather than on a rendering
    // of it.
    const said = Object.values(copyOutcomes);
    expect(new Set(said).size).toBe(said.length);
  });
});

describe('the click does not reach the row', () => {
  it('stops propagation, so the drawer does not open under the copy', () => {
    vi.stubGlobal('isSecureContext', false);
    withExec(true);
    const row = vi.fn();

    render(
      <tr onClick={row}>
        <td>
          <CopyButton value="10.0.0.1" label="Copy" />
        </td>
      </tr>,
      { container: document.body.appendChild(document.createElement('table')) },
    );
    fireEvent.click(button());

    // And it is also what keeps the selection made by step 3 from being cleared
    // by something else acting on the same click.
    expect(row).not.toHaveBeenCalled();
  });
});
