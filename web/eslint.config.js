import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';

/**
 * The front end's structural guards, as lint rules.
 *
 * Two of them are not style. They are the reason the network module and the XSS
 * surface are one place each rather than forty, and a rule adopted after forty
 * components exist is a rewrite of forty.
 *
 * **`eslint-plugin-react` is deliberately absent.** Its latest release declares
 * `eslint: "^3 || … || ^9.7"` and this is ESLint 10, so installing it means an
 * unmet peer and a rule set nobody can be sure is running. The only rule wanted
 * from it is `react/no-danger`, and that is a one-selector
 * `no-restricted-syntax` below. `eslint-plugin-react-hooks` supports ESLint 10
 * and stays.
 */
/**
 * **There is no web→native channel**, and the page is fenced off from the other
 * end of one.
 *
 * The native window reaches the
 * page through `evaluateJavaScript` and nothing else, native→web only; nothing
 * in the page speaks to the window. A page reaches a window through a global the
 * engine puts there: `webkit.messageHandlers.<name>.postMessage` in WKWebView,
 * `chrome.webview.postMessage` in WebView2. So both are refused here, and
 * `internal/launch`'s `TestNoWebToNativeChannel` refuses the native half -
 * either guard alone leaves a door that can be built from the side it does not
 * read.
 *
 * **Three spellings, so both rules.** `no-restricted-globals` sees the bare
 * `webkit` and nothing else; `window.webkit` and `globalThis.chrome` walk past
 * it (see the storage fences below), so `no-restricted-properties` names them on
 * `window`, `globalThis` and `self`. And since TypeScript knows neither global,
 * the spelling anybody would actually write is a cast -
 * `(window as unknown as Bridge).webkit` - whose object is not the identifier
 * `window` at all. That one is caught one step further in, on any object:
 * `messageHandlers` is WebKit's and `webview` is WebView2's, and nothing in this
 * tree uses either name. `webkit` and `chrome` cannot be refused on any object
 * the same way: `chrome` is already the name of a prop
 * (`ObjectDetail`'s drawer or page), and the rule reads a destructured
 * parameter as an access.
 *
 * **No exemption, and that is why it is spread into every block below.** A
 * later block's `no-restricted-globals` replaces this one's whole list rather
 * than adding to it, so the network module, the storage files and the tests -
 * each of which turns a fence off for its own reason - would each have turned
 * this one off with it. None of them has a reason to reach the window.
 */
const NO_CHANNEL =
  'There is no web→native channel. The window reaches the page through ' +
  'evaluateJavaScript, native→web only, and nothing in the page speaks to the window.';
const channelGlobals = ['webkit', 'chrome'].map((name) => ({ name, message: NO_CHANNEL }));
const channelProperties = [
  ...['window', 'globalThis', 'self'].flatMap((object) =>
    ['webkit', 'chrome'].map((property) => ({ object, property, message: NO_CHANNEL })),
  ),
  ...['messageHandlers', 'webview'].map((property) => ({ property, message: NO_CHANNEL })),
];

/**
 * **One network module, `ui/src/api/`, and no web storage** in shipped code -
 * the fence the blocks below put in place, named once because three of them
 * use it: the TypeScript block, the JavaScript block, and the storage
 * exemption, which keeps the network half.
 *
 * `no-restricted-globals` is the right rule rather than `no-restricted-imports`
 * because these are globals - nothing has to be imported to reach them.
 */
const NETWORK = 'Use request() from ui/src/api: one network module.';
const SOCKET = 'Open sockets from ui/src/api, and hold them with useLiveHandle.';
const STREAM = 'Open streams from ui/src/api, and hold them with useLiveHandle.';
const STORAGE = 'Nothing of the session is readable by the browser.';
const STORAGE_GLOBAL =
  STORAGE +
  ' If this is a preference rather than a credential, add the file to the storage exemption in this config and say why.';

const networkGlobals = [
  { name: 'fetch', message: NETWORK },
  { name: 'XMLHttpRequest', message: NETWORK },
  { name: 'WebSocket', message: SOCKET },
  { name: 'EventSource', message: STREAM },
];
// Storage, as the bare global, which is how every call in this tree is actually
// written. See the storage exemption below for the files exempted.
const storageGlobals = ['localStorage', 'sessionStorage'].map((name) => ({ name, message: STORAGE_GLOBAL }));

// **Every spelling, on every side.** `no-restricted-globals` matches a bare
// identifier and nothing else, so `window.fetch(…)`, `globalThis.fetch(…)` and
// `self.fetch(…)` would walk through it. `no-restricted-properties` with an
// `object` matches only that object's spelling, while every storage call in
// this tree is the bare global. Either rule alone covers the spelling nobody
// uses, so each fence is named in both, on the three names of the global
// object. `sendBeacon` is a property with no object: it hangs off `navigator`,
// which can be reached as `window.navigator`, `self.navigator` or a variable,
// and nothing in this tree has another method of that name.
const globalObjects = ['window', 'globalThis', 'self'];
const networkProperties = [
  ...globalObjects.flatMap((object) =>
    networkGlobals.map(({ name, message }) => ({ object, property: name, message })),
  ),
  { property: 'sendBeacon', message: NETWORK },
];
const storageProperties = globalObjects.flatMap((object) =>
  ['localStorage', 'sessionStorage'].map((property) => ({ object, property, message: STORAGE })),
);

const fence = {
  'no-restricted-globals': ['error', ...networkGlobals, ...storageGlobals, ...channelGlobals],
  'no-restricted-properties': ['error', ...storageProperties, ...networkProperties, ...channelProperties],
};

export default tseslint.config(
  { ignores: ['dist/**', 'ui/dist/**', 'node_modules/**'] },

  js.configs.recommended,
  ...tseslint.configs.recommended,

  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      globals: globals.browser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,

      // `dangerouslySetInnerHTML` is banned outright. There is no reviewed use
      // of it in a console that renders cluster-controlled strings, and
      // `yamlcolour.go` returns tokens precisely so that the API never has to
      // return HTML.
      'no-restricted-syntax': [
        'error',
        {
          selector: 'JSXAttribute[name.name="dangerouslySetInnerHTML"]',
          message: 'Banned: the API returns tokens, never HTML strings.',
        },
        {
          selector: 'Property[key.name="dangerouslySetInnerHTML"]',
          message: 'Banned: the API returns tokens, never HTML strings.',
        },
        // **A wiped body is not an unmounted tree.**
        // `document.body.innerHTML = ''` takes the DOM away and leaves the React
        // root, its router and its queries alive. The tree keeps answering -
        // through whatever `fetch` the *next* test stubs, pushing its own
        // addresses into that test's recording: a flake that looks like load.
        // `ui/src/testing/setup.ts` unmounts after every test, so a wipe used as
        // teardown has nothing to do; a wipe in the middle of a test is
        // `cleanup()`.
        //
        // The selector is any `innerHTML` assignment rather than
        // `document.body`'s, because `container.innerHTML = ''` is the same
        // defect wearing a different receiver - and because nothing in this tree
        // assigns `innerHTML` for any other reason.
        {
          selector: 'AssignmentExpression[left.type="MemberExpression"][left.property.name="innerHTML"]',
          message:
            'A wiped body is not an unmounted tree - the React root, its router and its ' +
            'queries stay alive and answer through the fetch stub the NEXT test installs. Use ' +
            'cleanup() from @testing-library/react. As teardown it is already done for you in ' +
            'ui/src/testing/setup.ts.',
        },
      ],

      // The network and storage fence (`fence`, above). Overridden for the
      // network module and the storage files below, each turning off only its
      // own half.
      ...fence,

      '@typescript-eslint/consistent-type-imports': ['error', { fixStyle: 'inline-type-imports' }],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
    },
  },

  {
    // **The same fence on a JavaScript file in shipped code.** None exists
    // today, and the TypeScript block above would not see one. Only where code
    // ships - `ui/src` and the extensions under `app` - never a bare
    // `**/*.js`, which would fence the Node scripts and the config files.
    files: ['ui/src/**/*.{js,jsx,mjs,cjs}', 'app/**/*.{js,jsx,mjs,cjs}'],
    languageOptions: {
      globals: globals.browser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    rules: fence,
  },

  {
    // The one place allowed to talk to the network - and not to the window.
    files: ['ui/src/api/**/*.ts'],
    rules: {
      'no-restricted-globals': ['error', ...channelGlobals],
      'no-restricted-properties': ['error', ...channelProperties],
    },
  },

  {
    // **Storage is banned for credentials, not for preferences**, and the rule
    // cannot express that distinction — so the exemption is a closed list of
    // files and each new entry is a decision rather than a drift.
    //
    // Each of these keeps something a person arranged, in their browser, and
    // none of them can reach the session: it is an HttpOnly cookie and the SPA
    // never sees it.
    //
    // One line per file, all of them:
    //
    //   dock/DockContext.tsx    the dock's height and its all-clusters scope, in
    //                           `localStorage`; and the tab list in
    //                           `sessionStorage` - each tab's address, a Claude
    //                           tab's conversation id, and which was in front
    //   shell/menuMemory.ts     which menu groups the person opened
    //   shell/viewMemory.ts     which kind a cluster was last looked at, and how
    //                           each kind was sorted: a kind name and a column
    //                           name; the namespace selection and the filter
    //                           live in the address, not here
    //   shell/navMemory.ts      what a deferred menu group last answered, per
    //                           cluster: kind names, paths and the server's note,
    //                           which `GET /api/c/{cluster}/nav/{group}` sends to
    //                           any signed-in browser anyway. Without it F5 pays
    //                           a full discovery read again for a heading opened
    //                           a minute ago
    //   router/chunkReload.ts   one boolean, so a reload happens once
    //   components/useUrlParameter.ts  removes a stale key from older builds
    //   pages/resources/columnMemory.ts  a person's column widths, order,
    //                           hidden columns and density, per browser, where
    //                           the console keeps none
    //                           (`capabilities.preferences` false)
    //   pages/resources/extraColumnMemory.ts  which of an extra's list columns
    //                           a person ticked in Settings > Extra, per browser
    //   shell/drawerWidth.ts    the width the detail drawer was dragged to: one
    //                           number, per browser on every profile
    //   shell/namespaceMemory.ts  the namespace chosen on a list, per cluster,
    //                           carried to the next list's link, in
    //                           `sessionStorage`, so it ends with the session
    //   shell/suggestionMemory.ts  which extension suggestions a person
    //                           answered "Not now" to, per cluster
    //   shell/kindsMemory.ts    whether the kinds column shows the kinds holding
    //                           nothing, and the favorite kinds per cluster,
    //                           where the server keeps none
    //   whatsnew/seenMemory.ts  the last release whose What's new was seen,
    //                           where the server keeps none
    //
    // Apart from the dock's tab list, each holds names or numbers the address
    // or the menu already carry. None of them holds the session.
    files: [
      'ui/src/dock/DockContext.tsx',
      'ui/src/shell/menuMemory.ts',
      'ui/src/shell/viewMemory.ts',
      'ui/src/shell/navMemory.ts',
      'ui/src/router/chunkReload.ts',
      'ui/src/components/useUrlParameter.ts',
      'ui/src/pages/resources/columnMemory.ts',
      'ui/src/pages/resources/extraColumnMemory.ts',
      'ui/src/shell/drawerWidth.ts',
      'ui/src/shell/namespaceMemory.ts',
      'ui/src/shell/suggestionMemory.ts',
      'ui/src/shell/kindsMemory.ts',
      'ui/src/whatsnew/seenMemory.ts',
    ],
    rules: {
      // Off here: the bare storage globals, and nothing else. The network half
      // stays, in both rules - `no-restricted-properties` is inherited whole
      // from the block above, so `window.localStorage` and `self.localStorage`
      // stay refused too: every exempted call is the bare global.
      'no-restricted-globals': ['error', ...networkGlobals, ...channelGlobals],
    },
  },

  {
    // The suites, and the setup file every one of them loads. `ui/src/testing/`
    // is here rather than only `*.test.*` because `setup.ts` holds the storage
    // teardown every suite shares: exempting that one file is narrower than
    // exempting every suite.
    files: ['**/*.test.{ts,tsx}', 'ui/src/testing/**/*.{ts,tsx}', 'measure/**/*.{ts,tsx,mjs}'],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    // A test stands in for the browser, so both fences are off here.
    //
    // `measure/` is here for the same reason and ships the same distance: the
    // layout harness stands in for the back end, and its console scene answers
    // `/api/*` from a fixture by holding `window.fetch` — the browser-side
    // spelling of what `testing/harness.tsx` does with `vi.stubGlobal`. Nothing
    // under `measure/` is built into the console. `.mjs` too: `shot-*.mjs` are
    // Node scripts that drive a browser, so they need both sets of globals.
    //
    // They exist to keep *shipped* code off web storage and off the network
    // outside `ui/src/api`. A test that asserts what `menuMemory` writes has to
    // read what it wrote, and a test that stubs the network has to reach
    // `globalThis.fetch` to stub it; every such call is setup or assertion.
    //
    // What this gives up: a test could put a credential in storage and nothing
    // would say so. It would ship nowhere, and the alternative — an inline
    // disable on every call — invites the next one without reading anything.
    //
    // The window's fence stays on (see `NO_CHANNEL`): no test stands in for a
    // channel that does not exist.
    rules: {
      'no-restricted-globals': ['error', ...channelGlobals],
      'no-restricted-properties': ['error', ...channelProperties],
    },
  },

  {
    files: ['*.config.{js,ts}', 'ui/*.config.ts', 'scripts/**/*.mjs'],
    languageOptions: { globals: globals.node },
  },
);
