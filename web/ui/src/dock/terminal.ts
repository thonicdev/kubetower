/**
 * The terminal emulator, as one chunk that arrives when a terminal is opened.
 *
 * This module exists to be imported dynamically - `await import('./terminal')`
 * in `TerminalPane.tsx`, inside the live handle's `open`, which is the one place
 * a terminal is constructed - and nowhere statically. `@xterm/xterm` is a large
 * share of the bundle, and in the entry chunk the browser would download and
 * parse it before it could draw the sign-in form, on the one page reached with
 * a cold cache. A sign-in view has no use for a terminal emulator.
 *
 * Three things ride together, deliberately, so that one `import()` is one
 * request and one failure:
 *
 * - the emulator and the fit addon, which the pane cannot construct without;
 * - xterm's own stylesheet, **imported from the package rather than vendored.**
 *   A copy beside the code is a copy that can drift from the version of the
 *   JavaScript it styles, and catching that would need a hash table kept by
 *   hand. Importing it from the dependency makes drift impossible instead of
 *   detectable: the CSS and the JS come out of one package at one version,
 *   pinned in one lockfile entry, and bumped by one change. It passes
 *   `scripts/check-styles.mjs` unchanged - the walk there skips
 *   `node_modules`, so no stylesheet is added to this repository at all.
 *
 * Vite attaches a CSS import inside a dynamically imported module to that
 * module's chunk, so importing it here rather than in `web/app/main.tsx` takes
 * the stylesheet off the sign-in view along with the code it styles.
 * When the chunk is stale - a tab left open across an upgrade - the CSS is
 * what fails first, with Vite's own wording, and `router/chunkReload.ts`
 * recognizes it.
 *
 * ## The renderer: xterm's default, the DOM one, chosen on purpose
 *
 * xterm 6 paints with DOM elements unless a renderer addon is loaded, and the
 * WebGL and canvas renderers are separate packages - `@xterm/addon-webgl`,
 * `@xterm/addon-canvas` - neither of which is a dependency here. The DOM
 * renderer is kept on a measurement of the two against each other: xterm 6.0.0
 * against
 * `@xterm/addon-webgl` 0.19.0, Chromium 153 headed, an i7-14700KF with an RTX
 * 4070 SUPER at 144 Hz, the dock at its default 300 px.
 *
 * - **The default is already smooth, unthrottled, on the measuring machine.**
 *   144 fps with no dropped frame in every headed scenario run there: one
 *   terminal or four, a flood or a chatty log, a short or a tall dock. The
 *   figure that ruled WebGL out: on a 2,000 lines/s log the DOM renderer takes
 *   **10.0 % main-thread time against WebGL's 3.4 %, both at 144 fps with 0
 *   dropped frames** - a saving nobody can see. On a chatty log it becomes frames only
 *   on a CPU about 4x slower in a tall dock (82 fps against 144). **Under a
 *   flood it shows in the default dock too**: with 4x CPU throttling, the 300 px
 *   dock ran a flood at 113 fps with 22 % of frames dropped under DOM, against
 *   143 fps and 0.8 % under WebGL.
 * - **The dock holds several terminals but draws one.** Only the active pane is
 *   displayed, and xterm stops rendering an element that is not visible: a
 *   hidden pane streaming 2,000 lines/s rendered 0 times in 5 s under either
 *   renderer. More terminals cost memory and parsing, not frames.
 * - **Each of WebGL's failure modes would need code to hold it shut:**
 *   - a lost context leaves the pane a white rectangle for good unless the
 *     addon is disposed, and white for 3 s even then;
 *   - Chrome keeps 16 live WebGL contexts per page and `MAX_TABS` is 16
 *     (`tabs.ts`), so the margin is zero: a 17th context, from anywhere on the
 *     page, turns the oldest terminal white;
 *   - on software GL - a VM, a remote desktop, a blocklisted driver - a log
 *     tail took 9 to 15 cores and fell to 20 fps in a tall dock;
 *   - it rounds the cell to whole device pixels, so the same text is 4 %
 *     narrower at 100 % display scaling and 9 % at 150 %;
 *   - +31 kB gzip (+114 kB raw) on this chunk, or a chunk of its own.
 *
 * So no renderer addon is imported, declared or locked, and `terminal.test.ts`
 * fails on any of the three. Revisit if someone on a slower laptop sees the dock
 * stutter on a chatty stream; the 4x-throttled tall dock is the case to
 * reproduce first, and each failure mode above needs a guard before WebGL is
 * adopted.
 */
import '@xterm/xterm/css/xterm.css';

export { FitAddon } from '@xterm/addon-fit';
export { Terminal } from '@xterm/xterm';
