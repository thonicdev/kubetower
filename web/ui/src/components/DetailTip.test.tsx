import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState, type ReactNode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { KeyLayer } from '../keyboard/layers';
import { resetKeyboardStack } from '../keyboard/stack';
import { useEscape } from '../keyboard/useKeyLayer';
import { DetailTip, placeTip } from './DetailTip';

afterEach(() => {
  cleanup();
  resetKeyboardStack();
});

/** A drawer as the shell's is: it takes Escape at its own layer. */
function Drawer({ children }: { children: ReactNode }): ReactNode {
  const [open, setOpen] = useState(true);
  useEscape(KeyLayer.Drawer, () => setOpen(false), open);
  return open ? <div data-testid="drawer">{children}</div> : null;
}

function inDrawer(): HTMLElement {
  render(
    <KeyboardStack>
      <Drawer>
        <DetailTip text="sha256:0123456789abcdef" label="Version of app" />
      </Drawer>
    </KeyboardStack>,
  );
  return screen.getByRole('button', { name: 'Version of app' });
}

function press(on: HTMLElement): void {
  act(() => {
    on.focus();
    fireEvent.click(on);
  });
}

const panel = (): HTMLElement | null => document.querySelector('[data-detail-tip-text]');

describe('DetailTip', () => {
  it('closes itself on Escape, and leaves the drawer it sits in open', () => {
    const why = inDrawer();
    press(why);
    expect(panel()).not.toBeNull();
    act(() => {
      fireEvent.keyDown(why, { key: 'Escape' });
    });
    expect(panel()).toBeNull();
    expect(screen.queryByTestId('drawer')).not.toBeNull();
    expect(document.activeElement).toBe(why);
    // Closed, the next Escape is the drawer's again.
    act(() => {
      fireEvent.keyDown(why, { key: 'Escape' });
    });
    expect(screen.queryByTestId('drawer')).toBeNull();
  });

  it('closes when the focus leaves', () => {
    const why = inDrawer();
    press(why);
    expect(panel()).not.toBeNull();
    act(() => {
      fireEvent.blur(why);
    });
    expect(panel()).toBeNull();
  });

  it('shows its text once, in the panel the trigger is described by', () => {
    const why = inDrawer();
    expect(why.hasAttribute('title')).toBe(false);
    expect(why.hasAttribute('aria-describedby')).toBe(false);
    press(why);
    const id = why.getAttribute('aria-describedby');
    expect(id).not.toBeNull();
    expect(document.getElementById(id ?? '')?.textContent).toBe('sha256:0123456789abcdef');
    expect(document.querySelectorAll('[title]')).toHaveLength(0);
  });

  it('stays inside a 390px window, wherever its trigger is', () => {
    const phone = { width: 390, height: 844 };
    const tip = { width: 256, height: 40 };
    // A trigger near the right edge: the panel moves left, not past the edge.
    const right = placeTip({ left: 300, top: 100, bottom: 124 }, tip, phone);
    expect(right.left).toBe(390 - 256 - 8);
    expect(right.top).toBe(128);
    // Near the left edge, and a trigger scrolled out on the left.
    expect(placeTip({ left: 2, top: 100, bottom: 124 }, tip, phone).left).toBe(8);
    expect(placeTip({ left: -40, top: 100, bottom: 124 }, tip, phone).left).toBe(8);
    // Near the bottom: above the trigger.
    expect(placeTip({ left: 20, top: 820, bottom: 840 }, tip, phone).top).toBe(820 - 4 - 40);
  });
});
