import { afterEach, describe, expect, it, vi } from 'vitest';

import { KeyLayer } from './layers';
import { claimKey, claimedLayers, installKeyboardStack, resetKeyboardStack } from './stack';

function escape(): KeyboardEvent {
  return new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
}

afterEach(() => {
  resetKeyboardStack();
});

describe('the keyboard priority stack', () => {
  it('gives the key to the highest layer, whatever order claimants registered in', () => {
    installKeyboardStack();
    const seen: string[] = [];

    // Registered bottom-up, which is the order components mount in: the drawer
    // is in the Shell, the dialog is opened later by a page inside it, and the
    // confirmation last of all - and the confirmation is also the highest, so
    // this says nothing about registration order on its own. `escapeReaches`
    // below runs the reverse order for that.
    claimKey(KeyLayer.Drawer, () => {
      seen.push('drawer');
      return true;
    });
    claimKey(KeyLayer.Dialog, () => {
      seen.push('dialog');
      return true;
    });
    claimKey(KeyLayer.Confirm, () => {
      seen.push('confirm');
      return true;
    });

    window.dispatchEvent(escape());

    expect(seen).toEqual(['confirm']);
  });

  it('falls through a claimant that declines the key', () => {
    installKeyboardStack();
    const seen: string[] = [];

    claimKey(KeyLayer.Drawer, () => {
      seen.push('drawer');
      return true;
    });
    // A dialog that only handles Enter still lets Escape past it.
    claimKey(KeyLayer.Dialog, (event) => {
      seen.push('dialog');
      return event.key === 'Enter';
    });

    window.dispatchEvent(escape());

    expect(seen).toEqual(['dialog', 'drawer']);
  });

  it('gives the key to whatever opened last within one layer', () => {
    installKeyboardStack();
    const seen: string[] = [];

    // The covered-dialog case: open Save, then click a verb icon. The second
    // dialog is the visible one, and no z-index is read to work that out.
    claimKey(KeyLayer.Dialog, () => {
      seen.push('first');
      return true;
    });
    claimKey(KeyLayer.Dialog, () => {
      seen.push('second');
      return true;
    });

    window.dispatchEvent(escape());

    expect(seen).toEqual(['second']);
  });

  it('lets a released claimant fall back to the one underneath', () => {
    installKeyboardStack();
    const seen: string[] = [];

    claimKey(KeyLayer.Drawer, () => {
      seen.push('drawer');
      return true;
    });
    const release = claimKey(KeyLayer.Dialog, () => {
      seen.push('dialog');
      return true;
    });

    window.dispatchEvent(escape());
    release();
    window.dispatchEvent(escape());

    expect(seen).toEqual(['dialog', 'drawer']);
  });

  it('survives a claimant that unregisters another one while handling the key', () => {
    installKeyboardStack();
    const seen: string[] = [];

    const releaseLower = claimKey(KeyLayer.Drawer, () => {
      seen.push('drawer');
      return true;
    });
    claimKey(KeyLayer.Dialog, () => {
      seen.push('dialog');
      releaseLower();
      return false;
    });
    claimKey(KeyLayer.Menu, () => {
      seen.push('menu');
      return true;
    });

    window.dispatchEvent(escape());

    // The menu is still asked even though an entry was spliced out of the
    // registry mid-walk: the walk is over a snapshot.
    expect(seen).toEqual(['dialog', 'menu']);
  });

  it('stops a consumed key from reaching the document', () => {
    installKeyboardStack();
    const onDocument = vi.fn();
    document.addEventListener('keydown', onDocument);

    claimKey(KeyLayer.Dialog, () => true);
    const event = escape();
    window.dispatchEvent(event);

    expect(onDocument).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(true);
    document.removeEventListener('keydown', onDocument);
  });

  it('installs exactly one listener across StrictMode mount, unmount, remount', () => {
    const seen: string[] = [];
    const release1 = installKeyboardStack();
    release1();
    const release2 = installKeyboardStack();

    claimKey(KeyLayer.Dialog, () => {
      seen.push('dialog');
      return true;
    });
    window.dispatchEvent(escape());

    expect(seen).toEqual(['dialog']);

    // Once released by its last holder, nothing is listening.
    release2();
    window.dispatchEvent(escape());
    expect(seen).toEqual(['dialog']);
  });

  it('keeps its registry ordered highest-first', () => {
    claimKey(KeyLayer.Menu, () => false);
    claimKey(KeyLayer.Overlay, () => false);
    claimKey(KeyLayer.Drawer, () => false);
    claimKey(KeyLayer.Confirm, () => false);

    expect(claimedLayers()).toEqual([
      KeyLayer.Overlay,
      KeyLayer.Confirm,
      KeyLayer.Menu,
      KeyLayer.Drawer,
    ]);
  });
});

/**
 * Registers a dialog and a confirmation in the order given and returns whoever
 * Escape reached.
 *
 * Both orders are asserted because the answer must rest on the layers alone.
 * The manifest editor renders its confirmation as a sibling *after* the editor
 * - it has to, paint order is what puts it on top - so the tree order alone
 * would hold the keyboard up. A test that registered them in one order would
 * still pass on the day the layers disagreed with their own comment.
 */
function escapeReaches(order: readonly ('dialog' | 'confirm')[]): string[] {
  const seen: string[] = [];
  // Released on the way out rather than reset: `resetKeyboardStack` empties the
  // registry and zeroes the refcount without removing the listener, so calling
  // it between the two orders would install a second one on the same window.
  const releases = order.map((who) =>
    claimKey(who === 'dialog' ? KeyLayer.Dialog : KeyLayer.Confirm, () => {
      seen.push(who);
      return true;
    }),
  );
  window.dispatchEvent(escape());
  for (const release of releases) release();
  return seen;
}

describe('a confirmation raised from inside a dialog', () => {
  it('takes Escape for itself, whichever of the two was registered first', () => {
    installKeyboardStack();

    expect(escapeReaches(['dialog', 'confirm'])).toEqual(['confirm']);
    expect(escapeReaches(['confirm', 'dialog'])).toEqual(['confirm']);
  });

  it('is above the dialog in the table, and the table is strictly ascending', () => {
    const declared = [
      KeyLayer.Page,
      KeyLayer.Drawer,
      KeyLayer.Menu,
      KeyLayer.Dialog,
      KeyLayer.Confirm,
      KeyLayer.Overlay,
    ];

    // Written out in the order `layers.ts` documents, then checked to be the
    // order the numbers actually give. A comment that says one thing and a
    // number that says another is what this test exists to catch.
    expect(declared).toEqual([...declared].sort((a, b) => a - b));
  });
});
