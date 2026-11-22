import { cleanup, configure } from '@testing-library/react';
import { afterEach } from 'vitest';

import { resetLogStreamSlots } from '../api/logstream';
import { resetEnvFolds } from '../pages/object/envFolds';
import { resetExtraColumnMemory } from '../pages/resources/extraColumnMemory';
import { resetColumnMemory } from '../pages/resources/columnMemory';
import { resetDrawerWidth } from '../shell/drawerWidth';
import { resetFirstIcons } from '../shell/firstIcon';
import { resetKindsMemory } from '../shell/kindsMemory';
import { resetMenuMode, resetOpenGroups } from '../shell/menuMemory';
import { resetNamespaceMemory } from '../shell/namespaceMemory';
import { resetNavMemory } from '../shell/navMemory';
import { resetViewMemory } from '../shell/viewMemory';

// Three seconds for every `findBy*` and `waitFor`, not the library's one.
//
// Every page is a lazy chunk, which adds a tick between a navigation and the
// page's first render, and under the load of the full suite a one-second
// ceiling measures the machine, not the console. Three seconds is still short
// enough that a hung wait fails a run. The tests themselves are untouched: a
// timeout that lives in one file is one that can be revisited with one edit.
configure({ asyncUtilTimeout: 3000 });

/**
 * **What this console remembers in the browser, and how a test forgets it.**
 *
 * One entry per module that writes `localStorage`, naming the key prefix it
 * owns and the seam that empties it. Both halves are needed: every one of these
 * modules caches its value in a module-level variable, so
 * `localStorage.clear()` alone leaves the *next* test reading a snapshot that
 * is no longer in storage. Two of the three reset helpers here
 * (`resetOpenGroups`, `resetNavMemory`) clear only the cache and never touch
 * the key - so the key is removed here, from the prefix the entry declares.
 *
 * `kubetower.dock.*` has no reset helper because `DockContext` keeps no
 * module-level cache: it reads storage in the provider, so removing the key is
 * the whole of forgetting it.
 *
 * **`sessionStorage` is covered for the dock's key and the namespace memory
 * only.** The dock's tab list is removed below with the same prefix. The other
 * key there is `kubetower.chunk-reloaded`, from `router/chunkReload.ts` - and
 * `chunkReload.test.ts` already clears it in both a `beforeEach` and an
 * `afterEach`, so there is nothing to catch, and the sweep below names the
 * dock's prefix and nothing wider.
 *
 * The prefix is what makes the list checkable. Anything left in storage after
 * every entry has run is a key **no module here claims**, and that is the
 * failure below.
 */
const memory: readonly { readonly prefix: string; readonly forget?: () => void }[] = [
  { prefix: 'kubetower.view', forget: resetViewMemory },
  { prefix: 'kubetower.menu.open', forget: resetOpenGroups },
  { prefix: 'kubetower.menu.mode', forget: resetMenuMode },
  { prefix: 'kubetower.nav.', forget: resetNavMemory },
  { prefix: 'kubetower.dock.' },
  { prefix: 'kubetower.columns', forget: resetColumnMemory },
  { prefix: 'kubetower.extracolumns', forget: resetExtraColumnMemory },
  { prefix: 'kubetower.drawer.width', forget: resetDrawerWidth },
  { prefix: 'kubetower.kinds', forget: resetKindsMemory },
];

// **Every rendered tree is unmounted after its test, here rather than in each
// file.**
//
// `document.body.innerHTML = ''` takes the DOM away and leaves the React root
// mounted behind it. The tree then keeps its effects, its queries and its
// router, and anything still in flight lands on a jsdom that the file's last
// test has already finished with - an uncaught `ReferenceError: window is not
// defined` from react-dom's scheduler after teardown, attributed to whichever
// file happens to be running.
//
// `cleanup()` here is what unmounts. A test that needs a second render's
// queries unambiguous calls `cleanup()` too, and `no-restricted-syntax` in
// `web/eslint.config.js` refuses the `innerHTML` wipe. Written once here
// because a copy per suite is a place for it to go missing.
//
// One hook with the storage teardown below rather than two, and the order
// inside it is the point: vitest runs `afterEach` hooks in reverse order of
// registration, so two hooks here would clear storage *before* unmounting and
// hand an unmounting tree a clean store to write into again.


/**
 * **No test leaves anything of itself in `localStorage`** - and a key nobody
 * here owns fails the test that wrote it.
 *
 * Vitest isolates per file and `vite.config.ts` sets no `isolate: false`, so a
 * leaked key harms only the tests after it in the same file and nothing goes
 * red: a trap left armed. This hook makes the leak impossible rather than
 * absent, exactly as `cleanup()` above did for the
 * mounted tree - and which **fails loudly on the one case it cannot fix
 * itself**: a new module writing a key this list does not know about. That
 * test goes red on the run that introduces it, naming the key, rather than
 * arming a trap for later.
 */
afterEach(() => {
  cleanup();

  // The one memory kept in neither storage: which of a pod's long variable
  // lists were opened (`envFolds.ts`). It lives in a module-level set for the
  // life of the page, so it has no prefix for the list above to claim, and a
  // test that opened a list would otherwise hand the next one a list drawn
  // open. After `cleanup()`, so nothing mounted is told of the change.
  resetEnvFolds();

  // The page's log connections, held in a module-level
  // count for the life of the page. A stream a test left open - the stubbed
  // EventSources never end on their own - would otherwise hold a slot the next
  // test of the file counts against.
  resetLogStreamSlots();

  // Which clusters were given a random first icon on this page, a module-level
  // set for the same reason: a test that
  // drew one would otherwise leave the next test's cluster of that name undrawn.
  resetFirstIcons();

  for (const { prefix, forget } of memory) {
    forget?.();
    for (const key of Object.keys(localStorage)) {
      // A trailing dot is a family (`kubetower.nav.<cluster>.<group>`); anything
      // else is one key, matched exactly so `kubetower.view` never claims a
      // `kubetower.viewport` somebody adds later.
      if (prefix.endsWith('.') ? key.startsWith(prefix) : key === prefix) {
        localStorage.removeItem(key);
      }
    }
  }

  // The dock's tab list lives in `sessionStorage` - a reload keeps it and
  // another browser tab never sees it - and a list left there is a dock the
  // next test's provider restores. Same prefix, same rule.
  for (const key of Object.keys(sessionStorage)) {
    if (key.startsWith('kubetower.dock.')) sessionStorage.removeItem(key);
  }
  // The namespace carried from list to list (`namespaceMemory.ts`) is the
  // third key there, and it has a module-level
  // snapshot, so it is forgotten through its seam - which removes the key too.
  // Left behind, one test's `default` would ride on the next one's menu links.
  resetNamespaceMemory();

  const strays = Object.keys(localStorage)
    .filter((key) => key.startsWith('kubetower.'))
    .sort();
  if (strays.length === 0) return;

  // Cleared before the throw: one unregistered key should fail one test, not
  // every test after it in the file.
  localStorage.clear();
  throw new Error(
    `This test left ${strays.map((k) => `\`${k}\``).join(', ')} in localStorage, and no ` +
      `entry in \`memory\` (ui/src/testing/setup.ts) owns that prefix. Add one, naming the key ` +
      `and the module's own reset helper - not \`localStorage.clear()\`, which leaves the ` +
      `module's cached snapshot behind.`,
  );
});
