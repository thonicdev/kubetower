import { fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { KeyLayer } from '../keyboard/layers';
import { resetKeyboardStack } from '../keyboard/stack';
import { Dialog } from './Dialog';

afterEach(() => {
  resetKeyboardStack();
});

function veil(): HTMLElement {
  const box = document.querySelector('[role="dialog"]');
  const element = box?.parentElement;
  if (!(element instanceof HTMLElement)) throw new Error('no veil');
  return element;
}

function box(): HTMLElement {
  const element = document.querySelector('[role="dialog"]');
  if (!(element instanceof HTMLElement)) throw new Error('no dialog');
  return element;
}

describe('Escape, claimed from the one keyboard stack', () => {
  it('closes the dialog', () => {
    const onClose = vi.fn();
    render(
      <KeyboardStack>
        <Dialog title="Arrange" onClose={onClose}>
          nothing
        </Dialog>
      </KeyboardStack>,
    );

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('leaves a dialog underneath alone when something above it claims the key', () => {
    const under = vi.fn();
    const over = vi.fn();
    render(
      <KeyboardStack>
        <Dialog title="Arrange" onClose={under}>
          nothing
        </Dialog>
        <Dialog title="Really?" onClose={over} layer={KeyLayer.Overlay}>
          nothing
        </Dialog>
      </KeyboardStack>,
    );

    fireEvent.keyDown(window, { key: 'Escape' });

    // An order kept as a CSS z-index read by hand is how one keystroke ends up
    // doing the opposite of what it looks like. Here it is a number in
    // `keyboard/layers.ts`.
    expect(over).toHaveBeenCalledTimes(1);
    expect(under).not.toHaveBeenCalled();
  });

  it('ignores every other key, so typing still reaches the page', () => {
    const onClose = vi.fn();
    render(
      <KeyboardStack>
        <Dialog title="Arrange" onClose={onClose}>
          nothing
        </Dialog>
      </KeyboardStack>,
    );

    fireEvent.keyDown(window, { key: 'a' });
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe('a click on the veil, and not on the box', () => {
  it('closes when the press and the release are both on the veil', () => {
    const onClose = vi.fn();
    render(
      <Dialog title="Arrange" onClose={onClose}>
        nothing
      </Dialog>,
    );

    fireEvent.mouseDown(veil());
    fireEvent.click(veil());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('does not close on a click inside the dialog', () => {
    const onClose = vi.fn();
    render(
      <Dialog title="Arrange" onClose={onClose}>
        nothing
      </Dialog>,
    );

    fireEvent.mouseDown(box());
    fireEvent.click(box());
    // "A modal that closes when you miss a button by four pixels is a modal
    // people stop reading."
    expect(onClose).not.toHaveBeenCalled();
  });

  it('does not close when a drag began inside the dialog and ended outside it', () => {
    const onClose = vi.fn();
    render(
      <Dialog title="Arrange" onClose={onClose}>
        nothing
      </Dialog>,
    );

    // Selecting text in the dialog and releasing beyond its edge. The click
    // event's target is then the common ancestor, which IS the veil here - so
    // without the mousedown guard this is indistinguishable from a dismissal.
    fireEvent.mouseDown(box());
    fireEvent.click(veil());
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe('the keyboard is handed in and given back', () => {
  it('focuses the field the dialog names', () => {
    render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <button type="button">first</button>
        <input data-dialog-focus aria-label="type the name" />
      </Dialog>,
    );

    expect(document.activeElement?.getAttribute('aria-label')).toBe('type the name');
  });

  it('falls back to the first field, then to a button', () => {
    const { unmount } = render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <input aria-label="nameplate" />
      </Dialog>,
    );
    expect(document.activeElement?.getAttribute('aria-label')).toBe('nameplate');
    unmount();

    render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <p>nothing to type</p>
      </Dialog>,
    );
    // The Close control, which is both the commit and the way out.
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Close');
  });

  it('gives the keyboard back to whatever opened it', () => {
    const opener = document.createElement('button');
    opener.textContent = 'Arrange';
    document.body.appendChild(opener);
    opener.focus();

    const { unmount } = render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <input aria-label="nameplate" />
      </Dialog>,
    );
    expect(document.activeElement).not.toBe(opener);

    unmount();
    // "So dismissing a confirmation returns the operator to the icon they
    // clicked rather than to the top of the document."
    expect(document.activeElement).toBe(opener);
  });

  it('does not chase an opener that has left the document', () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();

    const { unmount } = render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <input aria-label="nameplate" />
      </Dialog>,
    );

    // The row it sat in was refreshed away while the dialog was up. Focusing a
    // detached node moves the keyboard to <body> without saying so.
    opener.remove();
    expect(() => {
      unmount();
    }).not.toThrow();
  });
});

describe('there is deliberately no focus trap', () => {
  it('leaves focusable elements outside the dialog reachable', () => {
    const outside = document.createElement('button');
    document.body.appendChild(outside);

    render(
      <Dialog title="Arrange" onClose={vi.fn()}>
        <input aria-label="nameplate" />
      </Dialog>,
    );

    outside.focus();
    // "A trap that survives a failed request is a browser tab the operator has
    // to close." Nothing here pulls focus back.
    expect(document.activeElement).toBe(outside);
  });
});
