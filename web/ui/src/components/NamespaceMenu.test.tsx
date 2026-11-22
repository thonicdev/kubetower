import { fireEvent, render, screen } from '@testing-library/react';
import { useState, type ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyboardStack } from '../keyboard/KeyboardStack';
import { NamespaceMenu, lastTerm, splitTyped, summary } from './NamespaceMenu';
import { toggled } from './useUrlList';

/**
 * The namespace selector.
 *
 * Each case below names the decision it pins. What is deliberately **not**
 * asserted here is the placement in the panel head, which is a fact about
 * `ResourceList` and is tested there.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

function mount(props: Partial<Parameters<typeof NamespaceMenu>[0]> = {}): {
  changes: (readonly string[])[];
} {
  const changes: (readonly string[])[] = [];
  render(
    <KeyboardStack>
      <NamespaceMenu
        available={['argocd', 'default', 'kube-system', 'shop-prod']}
        selected={[]}
        onChange={(next) => changes.push(next)}
        {...props}
      />
    </KeyboardStack>,
  );
  return { changes };
}

/**
 * The same menu, but holding its own selection.
 *
 * `mount` records what `onChange` was called with and re-renders with the
 * selection it started from, which is enough for every case above and is exactly
 * wrong for the keyboard: "Enter ticks it and the menu stays open" is a claim
 * about what the entry looks like *after* the parent has taken the change, and a
 * harness that never applies it would pass with the tick never arriving.
 */
function mountControlled(initial: readonly string[] = []): void {
  function Host(): ReactNode {
    const [selected, setSelected] = useState<readonly string[]>(initial);
    return (
      <KeyboardStack>
        <NamespaceMenu
          available={['argocd', 'default', 'kube-public', 'kube-system', 'shop-prod']}
          selected={selected}
          onChange={setSelected}
        />
      </KeyboardStack>
    );
  }
  render(<Host />);
}

function open(): void {
  const control = document.querySelector('[aria-expanded]');
  if (control === null) throw new Error('no control');
  fireEvent.click(control);
}

/**
 * One entry of the open menu, found inside the list and not by name alone.
 *
 * The summary on the control carries the selected names too, so `getByRole` over
 * the document finds two buttons saying "argocd" and fails on the ambiguity -
 * which is itself the summary doing its job.
 */
function labelOf(button: Element): string {
  // The tick shares the button's text and keeps its width when empty, so the
  // entry's own name is the last span rather than the whole `textContent`.
  const spans = button.querySelectorAll('span');
  return spans[spans.length - 1]?.textContent?.trim() ?? '';
}

function entry(label: string): HTMLElement {
  const found = [...document.querySelectorAll('ul button')].find(
    (button) => labelOf(button) === label,
  );
  if (found === undefined) throw new Error(`no entry ${label}`);
  return found as HTMLElement;
}

function entries(): string[] {
  return [...document.querySelectorAll('ul button')].map(labelOf);
}

describe('the summary on the control', () => {
  it('counts past two rather than naming every one', () => {
    /*
      A measured decision rather than brevity: with three selected, all three
      names run together in a control that grows with its content inside a flex
      row would push the filter box off the end of the panel.
    */
    expect(summary([])).toBe('All namespaces');
    expect(summary(['argocd'])).toBe('argocd');
    expect(summary(['argocd', 'shop-prod'])).toBe('argocd, shop-prod');
    expect(summary(['a', 'b', 'c'])).toBe('3 namespaces');
  });

  it('carries the whole selection in the title whatever the label says', () => {
    mount({ selected: ['a', 'b', 'c'] });
    const control = document.querySelector('[aria-expanded]');
    expect(control?.textContent).toBe('3 namespaces');
    expect(control?.getAttribute('title')).toBe('a, b, c');
  });
});

describe('picking', () => {
  it('flips one namespace against the selection it already has', () => {
    /*
      `toggleNamespace` in `internal/web/namespaces.go`: every entry is "the
      selection I have now, with this one flipped". A click is a statement about
      one namespace, resolved against what the address bar already says — an
      index or a whole new list "would be a claim about the page the browser last
      drew".
    */
    expect(toggled([], 'argocd')).toEqual(['argocd']);
    expect(toggled(['argocd'], 'default')).toEqual(['argocd', 'default']);
    expect(toggled(['argocd', 'default'], 'argocd')).toEqual(['default']);
  });

  it('adds rather than replaces, and stays open while a selection is built', () => {
    const { changes } = mount({ selected: ['argocd'] });
    open();
    fireEvent.click(entry('default'));

    expect(changes).toEqual([['argocd', 'default']]);
    // Still open: picking two namespaces is one gesture, not two.
    expect(entries()).toContain('kube-system');
  });

  it('says plainly what the box under it does', () => {
    mount();
    open();
    const box = document.querySelector('input[aria-describedby]');
    const hint = document.getElementById(box?.getAttribute('aria-describedby') ?? '');
    expect(hint?.textContent).toBe(
      'Press Enter to list the namespaces typed here. Separate several with commas.',
    );
  });

  it('announces the entries as toggles and not as the current page', () => {
    // `aria-pressed` and not `aria-current`: several can be on at once, and a
    // reader announcing three entries as "current page" describes a page that
    // does not exist.
    mount({ selected: ['argocd'] });
    open();
    expect(entry('argocd').getAttribute('aria-pressed')).toBe('true');
    expect(entry('default').getAttribute('aria-pressed')).toBe('false');
    expect(document.querySelector('[aria-current]')).toBeNull();
  });

  it('keeps a way back to every namespace', () => {
    const { changes } = mount({ selected: ['argocd', 'default'] });
    open();
    fireEvent.click(entry('All namespaces'));
    // Empty, and empty is "every namespace the caller may read" — deliberately
    // not `default`, which would show a fraction of a cluster with no sign of it.
    expect(changes).toEqual([[]]);
  });
});

describe('the typed box', () => {
  it('searches the fragment after the last comma and not the whole value', () => {
    /*
      The box holds the whole selection and the operator types the
      next name onto the end of it, so matching the whole value against each row
      would hide every row the moment anything was selected — a control that
      breaks as soon as it is used once.
    */
    expect(lastTerm('argocd,kube-sys')).toBe('kube-sys');
    expect(lastTerm('argocd,')).toBe('');
    expect(lastTerm('  KUBE ')).toBe('kube');
  });

  it('opens holding the selection, ready for the next name', () => {
    // The trailing comma is deliberate: without it the next thing typed is glued
    // onto the last name. `parseNamespaces` drops the empty part it creates.
    mount({ selected: ['argocd', 'default'] });
    open();
    expect(screen.getByRole('textbox').getAttribute('value')).toBe('argocd,default,');
  });

  it('narrows the entries as it is typed, and never hides the way out', () => {
    mount();
    open();
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'kube' } });

    expect(entries()).toContain('kube-system');
    expect(entries()).not.toContain('argocd');
    // `data-ns` is on the toggles and on nothing else, for this:
    // a search that hides the way out is a menu you have to reload to escape.
    expect(entries()).toContain('All namespaces');
  });

  it('says when nothing matched, and offers to ask the cluster anyway', () => {
    /*
      Empty and failed are different answers, inside a menu too: a list that
      has silently gone empty under a search reads as a cluster with no
      namespaces. Enter is offered because the name may still be real — the
      cluster's list may be stale, or the caller may not be allowed to enumerate.
    */
    mount();
    open();
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'nosuch' } });

    const said = document.querySelector('[data-note="no-match"]');
    expect(said?.textContent).toContain('nosuch');
    expect(said?.textContent).toContain('Press Enter');
  });

  it('says nothing about matching while the box is empty', () => {
    // An empty box matches everything, and a "nothing matches" line under a full
    // list is a control arguing with itself.
    mount();
    open();
    expect(document.querySelector('[data-note="no-match"]')).toBeNull();
  });

  it('shows what the box says on Enter, whatever the list below thinks', () => {
    const { changes } = mount();
    open();
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'nosuch' } });
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Enter' });

    expect(changes).toEqual([['nosuch']]);
  });

  it('drops the empty part a trailing comma leaves', () => {
    // Exactly what `parseNamespaces` does, so the box and the server agree about
    // what `argocd,` means.
    expect(splitTyped('argocd,')).toEqual(['argocd']);
    expect(splitTyped(' argocd , default ,')).toEqual(['argocd', 'default']);
    expect(splitTyped('')).toEqual([]);
    // Deduplicated and sorted, so two spellings of one selection are one page.
    expect(splitTyped('b,a,b')).toEqual(['a', 'b']);
  });
});

describe('what the menu says about its own list', () => {
  it('draws a note as a sentence and never as a toggle', () => {
    /*
      "Could not be asked", "answered with none", "these were
      read earlier" are facts about the list rather than entries in it. It has no
      tick and is not pressed, and a reader announcing it as an unpressed toggle
      describes a control that does not exist.
    */
    mount({ note: 'Kept from four minutes ago; this cluster has stopped answering.' });
    open();
    const said = document.querySelector('[data-note="namespaces"]');
    expect(said?.tagName).toBe('P');
    expect(said?.getAttribute('aria-pressed')).toBeNull();
    expect(said?.textContent).toContain('four minutes ago');
  });
});

describe('the way out of the box', () => {
  /*
    *A control you type into needs a way out of the box that is not
    Tab-and-count.*
  */

  it('walks from the box into the matches, ticks one with Enter, and comes back up', () => {
    mountControlled();
    open();
    const box = screen.getByRole('textbox');
    fireEvent.change(box, { target: { value: 'kub' } });
    expect(entries()).toEqual(['All namespaces', 'kube-public', 'kube-system']);

    // Down from the box lands on the first *match* and never on "All
    // namespaces": `data-ns` is on the namespace toggles and on nothing else,
    // and that attribute is what the walk reads.
    fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(entry('kube-public'));

    /*
      Enter on a focused `<button>` is the browser activating it, and nothing in
      the component claims that key — a handler that did would fire beside the
      native click and toggle the namespace twice, which reads as the key doing
      nothing. jsdom does not synthesize the activation, so this pins the half
      that is ours (the key is not intercepted) and drives the half that is the
      browser's below.
    */
    fireEvent.keyDown(entry('kube-public'), { key: 'Enter' });
    expect(entry('kube-public').getAttribute('aria-pressed')).toBe('false');

    fireEvent.click(entry('kube-public'));
    expect(entry('kube-public').getAttribute('aria-pressed')).toBe('true');
    // Open, and still on the entry that was just ticked: picking two namespaces
    // is one gesture, so neither the menu nor the focus is thrown away.
    expect(entries()).toContain('kube-system');
    expect(document.activeElement).toBe(entry('kube-public'));

    // Up from the first match returns to the box — "a dead key at the top of a
    // list is how an operator concludes the keyboard is not supported here" —
    // and the box still holds the term, ready for the next name.
    fireEvent.keyDown(entry('kube-public'), { key: 'ArrowUp' });
    expect(document.activeElement).toBe(box);
    expect(box.getAttribute('value')).toBe('kub');
  });

  it('never lands on a row the search has filtered out, and stops rather than wrapping', () => {
    mountControlled();
    open();
    const box = screen.getByRole('textbox');
    fireEvent.change(box, { target: { value: 'kub' } });
    expect(entries()).not.toContain('argocd');

    const walked: string[] = [];
    let from: HTMLElement = box;
    for (let step = 0; step < 5; step++) {
      fireEvent.keyDown(from, { key: 'ArrowDown' });
      from = document.activeElement as HTMLElement;
      walked.push(labelOf(from));
    }

    /*
      Two matches and then a stop. Two things at once:

      - `argocd`, `default` and `shop-prod` are never reached: they are not
        rendered at all.
      - Past the last match `list[at + 1]` is undefined and the handler returns
        *without* preventing the default, so the menu scrolls instead of the
        focus jumping silently back to the top. It stops; it does not wrap.
    */
    expect(walked).toEqual([
      'kube-public',
      'kube-system',
      'kube-system',
      'kube-system',
      'kube-system',
    ]);

    // And the key is left unclaimed there rather than swallowed, which is the
    // difference between stopping and wrapping-with-nothing-to-wrap-to: a
    // handler that prevented the default on the last row would take the menu's
    // scroll away and give nothing back. `fireEvent` returns true when the
    // default survived.
    expect(fireEvent.keyDown(entry('kube-system'), { key: 'ArrowDown' })).toBe(true);
    expect(document.activeElement).toBe(entry('kube-system'));
  });

  it('leaves the caret alone on ArrowUp in the box, and lets the way out back in', () => {
    mountControlled();
    open();
    const box = screen.getByRole('textbox') as HTMLInputElement;
    fireEvent.change(box, { target: { value: 'kub' } });
    box.focus();

    // Up in the box keeps the box and prevents the default, which is the one
    // line of this that looks like a no-op and is not: ArrowUp in a text field
    // otherwise throws the caret to position 0, and the caret at the end is what
    // lets the next name continue the selection. `fireEvent` returns false when
    // the default was prevented.
    expect(fireEvent.keyDown(box, { key: 'ArrowUp' })).toBe(false);
    expect(document.activeElement).toBe(box);

    // "All namespaces" carries no `data-ns`, so the walk never visits it — but
    // walking *out* of it works, as for any off-list row: down to the first
    // match, up to the box.
    entry('All namespaces').focus();
    fireEvent.keyDown(entry('All namespaces'), { key: 'ArrowDown' });
    expect(document.activeElement).toBe(entry('kube-public'));

    entry('All namespaces').focus();
    fireEvent.keyDown(entry('All namespaces'), { key: 'ArrowUp' });
    expect(document.activeElement).toBe(box);
  });

  it('does nothing at all when the search has left no match', () => {
    // Not a dead key by accident: with no row to move to there is nothing to
    // move to, and the handler returns before preventing anything so the menu
    // still scrolls. The "nothing matched" line is what speaks here, not a key.
    mountControlled();
    open();
    const box = screen.getByRole('textbox');
    fireEvent.change(box, { target: { value: 'nosuch' } });

    expect(fireEvent.keyDown(box, { key: 'ArrowDown' })).toBe(true);
    expect(document.activeElement).toBe(box);
  });
});
