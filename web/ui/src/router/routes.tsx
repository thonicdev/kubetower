import type { ReactNode } from 'react';
import { Navigate, useLocation, type RouteObject } from 'react-router';

import { SIGN_IN_PATH } from '../auth/next';
import { SignIn } from '../auth/SignIn';
import { CanonicalCluster } from '../clusters/CanonicalCluster';
import { ClusterList } from '../clusters/ClusterList';
import { OneClusterLanding } from '../clusters/OneClusterLanding';
import type { Extension } from '../extension';
import { Shell } from '../shell/Shell';
import { Pending } from '../state/StateNotice';
import { EXTENSION, FORWARDS, HELM, loadPage, type PageKey } from './pages';
import { RouteError } from './RouteError';

/**
 * The first segments under `/c/:cluster` that are pages and not kinds.
 *
 * Two readers, and that is why it is exported from the route table rather than
 * spelled where it is used. The table below mounts `helm` from it; the menu's
 * `deferredGroupHoldsPath` (`shell/menuMemory.ts`) takes it to answer "this
 * kind route belongs to no loaded group, so it must be a custom resource page"
 * - and must not give that answer for `/c/prod/helm`: Helm is not a kind, no
 * group claims it, and arriving there would open the deferred heading and fire
 * the CustomResourceDefinition listing the deferral exists to avoid (tens of
 * megabytes on a large cluster). A list kept in the menu would drift the day a
 * page was added here; one list, read by both, cannot.
 *
 * `x` is the extension namespace: the SPA's `/c/{cluster}/x/{key}` page is
 * reserved, while the extension's own routes are served at
 * `/x/{cluster}/{key}/`, and `menuMemory.ts` says an extra's `/x/…`
 * page is not a kind. Reserved by name here: an extension's pages mount under
 * it, and so does the page that says so when this build carries none for the
 * key asked about - which is why the menu's reader needs it too.
 *
 * The two spellings themselves live in `./pages.ts`, which has a third reader:
 * the address-to-chunk table that decides which page a hovered link warms, and
 * which must say "not a kind, not the object page" for exactly these segments.
 * They are imported here rather than declared here because that table cannot
 * import this file back without a cycle; the export below is unchanged for the
 * menu.
 */
export const NON_KIND_SEGMENTS: readonly string[] = [HELM, FORWARDS, EXTENSION];

/**
 * The route tree.
 *
 * **One structural decision:** the Shell is
 * the element of a *pathless layout route* wrapping everything. React Router
 * keeps a matched layout route mounted while its children change, so the rail,
 * the menu, the dock and the drawer are not re-rendered by a navigation and the
 * dock's terminals survive it. See `../shell/Shell.tsx`.
 *
 * The layout route is above `/c/:cluster` rather than inside it, so changing
 * cluster is a navigation like any other and the dock survives that too.
 *
 * **Sign-in is the one route outside the Shell**, and that is not a layout
 * preference. The Shell's rail, menu and dock are the previous session's
 * furniture; drawing them around a form asking who is at the keyboard shows one
 * operator another's cluster names. It also means `SessionWatch` - which lives
 * in the Shell - is not mounted there, so the 401 of a wrong password cannot
 * route the sign-in view to itself.
 *
 * What this file fixes is the four things a page must not be free to
 * re-decide: where the Shell sits, where an extension's routes are mounted,
 * that sign-in is inside neither, and that Settings is inside the Shell and
 * outside the cluster scope.
 *
 * **`:kind` is the resource list, and its position in this array decides
 * nothing.** React Router v7 ranks matches by specificity rather than by
 * declaration order, so a static segment beats the dynamic `:kind` whatever
 * order the two are written in. That is worth saying out loud: under an
 * ordered chain, **reversing two lines would draw the list where the detail
 * belonged with no error anywhere**. Under a ranked router that hazard is
 * gone. What remains is the
 * ordinary one: a kind literally named like a page here would be shadowed by
 * it. A kind comes from discovery and can be anything, which is why
 * `api/openapi.yaml` puts them under a reserved `/r/` segment on the API side;
 * the client routes have no such segment, and that is a difference recorded
 * rather than hidden.
 *
 * **Every page is a `lazy` route and nothing else is.** The table of chunks
 * is `./pages.ts`, which says why and what stays static. Two things about the
 * shape here are measured rather than chosen:
 *
 * - **`lazy` is the function form, never `lazy: { Component }`.** With the
 *   object form React Router awaits the property's promise inside an
 *   empty `catch` in its default data strategy, so an import that rejects the
 *   way a browser rejects a missing chunk completes the navigation with no
 *   error, no Component and an empty outlet: a blank page, and `RouteError`
 *   never hears of it. With the function form the rejection is the route's
 *   error, the boundary draws it and `chunkReload.ts` reloads the tab. The
 *   recovery path exists only in this shape, and `routes.test.tsx` pins it.
 *
 * - **Each lazy route carries `hydrateFallbackElement`.** On a cold load the
 *   initial match is lazy, and the router withholds the *whole* tree until it
 *   resolves unless some match carries a fallback - with none, the root renders
 *   null and the console is a blank page for the length of a chunk fetch. With
 *   one on the lazy route itself, the Shell renders at once and only the page
 *   slot waits. Nothing is drawn for a later navigation: the router keeps the
 *   previous page on screen while the next chunk arrives, as it does for a
 *   loader.
 */
export function buildRoutes(extensions: readonly Extension[]): RouteObject[] {
  return [
    {
      // Derived from the constant in `../auth/next.ts`, because the redirect
      // that sends an operator here is built from the same one. Two spellings of
      // one address is a sign-in loop that only appears once a session lapses.
      path: SIGN_IN_PATH.replace(/^\//, ''),
      element: <SignIn />,
      errorElement: <RouteError />,
    },
    {
      element: <Shell />,
      errorElement: <RouteError />,
      children: [
        {
          // The page about which clusters there are, under a pathless layout
          // that sends an in-cluster console to its one cluster instead: it has
          // no choice to offer. Every other console draws it.
          // `../clusters/OneClusterLanding.tsx` says when, and why a failed or
          // empty list opens nothing.
          element: <OneClusterLanding />,
          children: [{ index: true, element: <ClusterList /> }],
        },
        // **`/health` has no page of its own.** The address is kept and leads
        // to the console's home, so a bookmark or an old link lands somewhere
        // true rather than on the router's error page. Replaced, so Back does not
        // return to it; on the in-cluster console `/` then sends on to its one
        // cluster.
        { path: 'health', element: <Navigate to="/" replace /> },
        // **Settings is outside the cluster scope**: it is about this console
        // rather than about any cluster, and a page reached through
        // `/c/:cluster` would be unreachable on a machine whose kubeconfig is
        // the thing being fixed.
        //
        // Each tab is a **real address**. A `useState` tab strip loses reload
        // and loses a bookmark, and this is the page somebody is told to open by
        // somebody else. `/settings` redirects to General rather than being a
        // second spelling of it - two addresses for one place puts two entries
        // in the history for one navigation.
        { path: 'settings', element: <SettingsIndex /> },
        { path: 'settings/:tab', ...page('settings') },
        {
          path: 'c/:cluster',
          // One spelling of the cluster in the address: an
          // old one is replaced with the canonical one. Where the address could
          // still move, the page waits for the cluster list rather than drawing
          // and being redrawn; a canonical address draws at once.
          element: <CanonicalCluster />,
          children: [
            { index: true, ...page('overview') },
            // **Helm is core**, so it is an ordinary child
            // of the cluster scope and not an extension route. Both segments are
            // static, so they outrank `:kind` whatever order this array is in -
            // React Router v7 ranks by specificity - but a *kind* literally named
            // `helm` would be shadowed by this, which is the ordinary hazard the
            // comment above records and not a new one.
            { path: HELM, ...page('helmList') },
            { path: `${HELM}/:namespace/:name`, ...page('helmRelease') },
            // **The open port-forwards**: process state,
            // not a kind, so a page of its own beside Helm and with Helm's
            // hazard - a kind literally named `port-forwards` is shadowed here.
            { path: FORWARDS, ...page('forwards') },
            // One kind, listed. `/c/:cluster/pods`, `/c/:cluster/nodes`, …
            { path: ':kind', ...page('resourceList') },
            // **One object, at an address of its own.** Two routes, and which
            // one matches is a rule rather than a convenience:
            // `/c/{cluster}/{kind}/{name}` is a cluster-scoped object and
            // `/c/{cluster}/{kind}/{namespace}/{name}` a namespaced one. One
            // trailing segment is a name, never a namespace; reading them all
            // as the second would report a node's name as a namespace, which
            // passes every validator on its way to being stated as fact.
            //
            // A single `:kind/*` route taking the rest as a path would match
            // both and hand the page a string to split, which is the same guess
            // written one layer down. Two declarations, and the router does the
            // arithmetic.
            { path: ':kind/:name', ...page('object') },
            { path: ':kind/:namespace/:name', ...page('object') },
            // **The reserved `x` segment, answered when no extension claims it.**
            // An extension's own routes (`x/<key>…`, spread below) carry a
            // static key and outrank these, so they are reached only for a key
            // this build does not carry, or for an address a registered extra
            // does not serve. Without them the address would fall through to the
            // object routes and be drawn as a kind the cluster does not serve,
            // blaming the cluster for the console's own build.
            //
            // **`x/:key/*` and not `x/*`**, and that is ranking arithmetic, not
            // style. React Router scores a splat route with a penalty
            // (`computeScore`), so `/c/:cluster/x/*`
            // (27) would lose to `/c/:cluster/:kind/:namespace/:name` (29) for
            // any three-segment address: `/c/p/x/argocd/typo` would be an
            // object of kind `x`. The
            // dynamic `:key` lifts it to 31, above every object route and still
            // below a static key's 39. The bare `x` is its own route for the
            // same reason - `:kind` would take it.
            //
            // **The hazard this takes on is the `helm` one, stated rather than
            // hidden.** A cluster kind the server resolves from `x` - a CRD
            // whose plural, singular or short name is `x` - is shadowed at
            // `/c/p/x`, `/c/p/x/<name>` and `/c/p/x/<ns>/<name>`: those draw
            // this page, not the kind. Only a typed address can reach it: every
            // link the console builds spells a custom kind as `plural.group`,
            // which has a dot and is never `x`.
            { path: EXTENSION, ...page('noExtension') },
            { path: `${EXTENSION}/:key/*`, ...page('noExtension') },
            // Extensions are mounted here and nowhere else, so their paths are
            // relative to /c/:cluster exactly as the TS contract says, and so
            // that an extension cannot mount itself outside the cluster scope.
            // Inside the scope, `extensionRoutes` holds each one to its own
            // `x/<key>`: a static path outranks every dynamic one here, so a
            // route `pods` would replace the core's page.
            ...extensionRoutes(extensions),
          ],
        },
      ],
    },
  ];
}

/**
 * An extension's routes, refused unless every one lands under `x/<key>`.
 *
 * They are spread into `/c/:cluster` beside the core's own pages, and React
 * Router ranks a static segment above a dynamic one, so an extension route
 * `pods`, `helm` or `settings` would not sit beside the core page at that
 * address - it would replace it, with nothing erroring. A route with no path -
 * a pathless layout, or an index route - is refused for the same reason: its
 * children, or the route itself, would answer at the cluster's own addresses.
 * An index route that carries an `x/<key>` path matches as that path and is
 * accepted like any other. The Go half places an
 * extension's API under its key by construction (`mountextensions.go`); this is
 * the TypeScript half's check, at `createApp`, before any route exists.
 *
 * A check rather than mounting the routes as children of `x/<key>`, which
 * would hold it by construction but change what `path` means in the contract -
 * a breaking change for every extension.
 *
 * **The key is held to an allow-list, not a list of banned characters.** React
 * Router reads `:`, `*` and a trailing `?` in a segment, and a key `argocd?`
 * would make `x/argocd?` an optional segment that answers at the bare
 * `/c/:cluster/x` too; a list of what to refuse is complete only until the
 * router learns another sign. `KEY_PATTERN` is the API's `extensionPattern`
 * (`internal/api/router.go`, from `api/openapi.yaml`), the pattern
 * `PUT /api/c/{cluster}/extensions/{key}` is held to;
 * `TestTheConsoleHoldsExtensionKeysToTheAPIPattern` in that package fails when
 * the two copies part. The Go registry refuses, at mount, a key that fails
 * `extras.KeyPattern`.
 *
 * **Two extensions with one key are refused** rather than both mounted: their
 * routes would rank equally and React Router would keep one of them, silently.
 */
function extensionRoutes(extensions: readonly Extension[]): RouteObject[] {
  const seen = new Set<string>();
  return extensions.flatMap((extension) => {
    if (seen.has(extension.key)) {
      throw new Error(`Two extensions share the key "${extension.key}"; each key names one extension's pages.`);
    }
    seen.add(extension.key);
    return routesOf(extension);
  });
}

/** Lowercase letters, digits and inner hyphens, 2 to 40 characters - the
 *  API's `extensionPattern`, so that every key is one literal path segment.
 *  Read as text by a Go test: keep it one regular-expression literal. */
const KEY_PATTERN = /^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$/;

function routesOf(extension: Extension): RouteObject[] {
  const { key } = extension;
  if (!KEY_PATTERN.test(key)) {
    throw new Error(
      `The extension key ${JSON.stringify(key)} cannot name a route of its own: a key is 2 to 40 lowercase letters, digits or inner hyphens, one literal segment.`,
    );
  }
  const own = `${EXTENSION}/${key}`;
  for (const route of extension.routes) {
    const path = route.path;
    if (path === undefined || (path !== own && !path.startsWith(`${own}/`))) {
      throw new Error(
        `The extension "${key}" declares a route at ${path === undefined ? 'no path' : JSON.stringify(path)}; ` +
          `an extension's routes must be "${own}" or begin with "${own}/", so that none replaces a console page.`,
      );
    }
  }
  return extension.routes;
}

/**
 * The two properties every page route carries. See the header for why it is
 * the function form and why the fallback is on the route itself.
 *
 * The fallback is the same `Pending` a page draws while its own query is in
 * flight, so the slot reads "Reading this page…" and then "Reading pods…" -
 * one shape for one kind of wait, rather than a spinner of its own for the
 * chunk.
 */
function page(key: PageKey): Pick<RouteObject, 'lazy' | 'hydrateFallbackElement'> {
  return {
    lazy: async () => ({ Component: await loadPage(key) }),
    hydrateFallbackElement: <Pending what="this page" />,
  };
}

/** `/settings` leads to General, keeping the `?cluster=` it was opened with
 *  and the way back it carries (`shell/settingsOrigin.ts`). */
function SettingsIndex(): ReactNode {
  const { search, state } = useLocation();
  return <Navigate to={{ pathname: '/settings/general', search }} state={state} replace />;
}
