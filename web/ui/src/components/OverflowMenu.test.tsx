import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { resetKeyboardStack } from '../keyboard/stack';
import { OverflowMenu, OverflowMenuCheck, OverflowMenuItem } from './OverflowMenu';

/**
 * The shared `⋯`. What each caller puts in it is that caller's test;
 * this is the part every caller gets: the dot, the roles, and the keys a menu
 * promises.
 */

afterEach(() => {
  resetKeyboardStack();
});

const MORE = 'More options';

function show({ flagged = false, checked = false } = {}): {
  onCheck: ReturnType<typeof vi.fn>;
  onAct: ReturnType<typeof vi.fn>;
} {
  const onCheck = vi.fn();
  const onAct = vi.fn();
  render(
    <KeyboardStack>
      <OverflowMenu label={MORE} flagged={flagged} flaggedText="Wrap on">
        <OverflowMenuCheck checked={checked} label="Wrap" onChange={onCheck} />
        <OverflowMenuItem label="Download" onSelect={onAct} />
      </OverflowMenu>
    </KeyboardStack>,
  );
  return { onCheck, onAct };
}

function open(): HTMLElement {
  act(() => {
    fireEvent.click(screen.getByRole('button', { name: MORE }));
  });
  return screen.getByRole('menu', { name: MORE });
}

describe('the overflow menu', () => {
  it('draws no dot and no description when nothing behind it is on', () => {
    show();
    expect(screen.queryByTestId('overflow-flag')).toBeNull();
    expect(screen.getByRole('button', { name: MORE }).getAttribute('aria-describedby')).toBeNull();
  });

  it('draws the dot, and says what it means as a description and not as the name', () => {
    show({ flagged: true });
    expect(screen.getByTestId('overflow-flag')).toBeTruthy();
    const more = screen.getByRole('button', { name: MORE });
    const described = more.getAttribute('aria-describedby');
    expect(described).not.toBeNull();
    expect(document.getElementById(described ?? '')?.textContent).toBe('Wrap on');
  });

  it('holds its toggles as menuitemcheckbox with aria-checked, and its acts as menuitem', () => {
    show({ checked: true });
    expect(screen.queryByRole('menu')).toBeNull();
    open();
    const wrap = screen.getByRole('menuitemcheckbox', { name: 'Wrap' });
    expect(wrap.getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('menuitem', { name: 'Download' })).toBeTruthy();
    expect(screen.getByRole('button', { name: MORE }).getAttribute('aria-expanded')).toBe('true');
  });

  it('turns a toggle over, closes, and gives the focus back to the ⋯', () => {
    const { onCheck } = show({ checked: false });
    open();
    act(() => {
      fireEvent.click(screen.getByRole('menuitemcheckbox', { name: 'Wrap' }));
    });
    expect(onCheck).toHaveBeenCalledWith(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: MORE }));
  });

  it('runs an act and closes', () => {
    const { onAct } = show();
    open();
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Download' }));
    });
    expect(onAct).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('puts the focus on the first item, moves it with the arrows, and closes on Escape', () => {
    show();
    const menu = open();
    const wrap = screen.getByRole('menuitemcheckbox', { name: 'Wrap' });
    const download = screen.getByRole('menuitem', { name: 'Download' });
    expect(document.activeElement).toBe(wrap);

    fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(download);
    fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(wrap);
    fireEvent.keyDown(menu, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(download);
    fireEvent.keyDown(menu, { key: 'Home' });
    expect(document.activeElement).toBe(wrap);
    fireEvent.keyDown(menu, { key: 'End' });
    expect(document.activeElement).toBe(download);

    // Escape is claimed from the one keyboard stack, at `KeyLayer.Menu`.
    act(() => {
      fireEvent.keyDown(window, { key: 'Escape' });
    });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: MORE }));
  });

  it('leaves on Tab from the ⋯, so the browser moves on from the trigger and not from the top', () => {
    show();
    const menu = open();
    const tab = fireEvent.keyDown(menu, { key: 'Tab' });
    // Not prevented: the browser's own move is what takes the focus on, and
    // it starts from wherever the focus is when the key is handled - the ⋯.
    expect(tab).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: MORE }));
  });

  it('closes on a click anywhere else, through the scrim', () => {
    show();
    open();
    const scrim = document.querySelector('button[aria-hidden="true"][tabindex="-1"]');
    expect(scrim).not.toBeNull();
    act(() => {
      fireEvent.click(scrim as HTMLElement);
    });
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('draws a face in place of the ⋯, its box in place of the square, and is the same menu', () => {
    // The header's account menu is this one under the signed-in name.
    render(
      <KeyboardStack>
        <OverflowMenu
          label="alice"
          face={<span data-testid="face">alice</span>}
          triggerClassName="rounded px-1.5 text-xs"
        >
          <OverflowMenuItem label="Settings" onSelect={() => undefined} />
        </OverflowMenu>
      </KeyboardStack>,
    );
    const trigger = screen.getByRole('button', { name: 'alice' });
    expect(trigger.textContent).toBe('alice');
    expect(trigger.contains(screen.getByTestId('face'))).toBe(true);
    expect(trigger.className).toContain('px-1.5');
    expect(trigger.className).not.toContain('border-line');
    act(() => {
      fireEvent.click(trigger);
    });
    const menu = screen.getByRole('menu', { name: 'alice' });
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Settings' }));
    expect(menu).toBeTruthy();
    act(() => {
      fireEvent.keyDown(window, { key: 'Escape' });
    });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('draws the glyph an item is given before its label, and none where it is given none', () => {
    // The account menu's Settings and Log out carry their
    // glyphs. The log pane's and a list row's items pass none and draw none.
    render(
      <KeyboardStack>
        <OverflowMenu label={MORE}>
          <OverflowMenuItem label="Settings" icon="settings" onSelect={() => undefined} />
          <OverflowMenuItem label="Download" onSelect={() => undefined} />
        </OverflowMenu>
      </KeyboardStack>,
    );
    open();
    const settings = screen.getByRole('menuitem', { name: 'Settings' });
    const glyph = settings.querySelector('svg');
    expect(glyph?.querySelector('use')?.getAttribute('href')).toBe('#i-settings');
    expect(glyph?.getAttribute('aria-hidden')).toBe('true');
    // Before the label, and the label is still the whole of the name.
    expect(settings.firstElementChild).toBe(glyph);
    expect(settings.textContent).toBe('Settings');
    // The glyph sits in the column the others keep empty, so it takes the
    // console's one icon size and the labels of a mixed menu do not jump.
    expect(glyph?.getAttribute('class')).toContain('size-[var(--icon)]');
    expect(screen.getByRole('menuitem', { name: 'Download' }).querySelector('svg')).toBeNull();
  });
});
