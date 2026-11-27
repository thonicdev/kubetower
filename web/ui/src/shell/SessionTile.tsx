import { useMutation, useQueryClient } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { ApiError, logout, useSession, useSignsBackIn } from '../api';
import { leaveFor } from '../auth/leave';
import { SERVER_SIGN_IN_PATH } from '../auth/next';
import { Icon } from '../components/Icon';
import { OverflowMenu, OverflowMenuItem } from '../components/OverflowMenu';
import { useUpdateBadge } from '../pages/settings/UpdateControl';
import { useNow } from '../components/useNow';
import { focusRing } from '../focus';
import { useOpenSettings } from './settingsOrigin';

/**
 * Who is signed in, how long is left, and the way out.
 *
 * At the foot of the rail because the rail is the one region that is on screen
 * on every route including the ones that have no cluster - the shell has four
 * regions and adding a fifth for one line of text would be a header nobody
 * asked for. **Except where there is one cluster**, and so no rail: then the
 * same control is `SessionInline`, in the header, below.
 *
 * ## Not drawn where there is no way back in
 *
 * Log out leads to the sign-in page, and on the loopback desktop that page has
 * nothing to sign in with: the window arrives signed in from the application,
 * so the page can only say to open KubeTower again, and the user and Log out
 * are no use there. So both forms are drawn only where the server offers a way to sign
 * back in - a password or an identity provider (`useSignsBackIn`). The rule is
 * that fact and not `capabilities.mode`: a desktop started with
 * `KT_PASSWORD_HASH` (the dev loop, the smoke tests) offers a password, and
 * keeps its Log out. Nothing is drawn until the server has answered, and a
 * failed answer draws the control, because a console with no way out is the
 * worse mistake.
 *
 * **The absolute-expiry warning stays**. A desktop session
 * lasts 90 days by default (`DefaultSessionMax`), but `KT_SESSION_MAX` can make
 * it as short as anywhere else, and `Session.absoluteExpiresAt` is when it ends
 * whatever set it. So where the identity and Log out are not drawn, the warning
 * still is, alone, in the last ten minutes - the one line that is about the
 * session ending rather than about leaving it.
 *
 * ## Both bounds, because `api/openapi.yaml` carries both and says why
 *
 * "An idle bound it can push back by acting, and an absolute bound it cannot. A
 * console that shows neither signs the person out mid-sentence." Only the
 * absolute one is worth a warning: the idle bound is pushed back by the very act
 * of looking at the console, so counting it down would be an alarm that never
 * fires while anyone is watching it.
 *
 * ## One rendering, 56px
 *
 * The rail is 56px at every width, so there is one form: the identity is
 * the `account` glyph with the name on `title` and to a screen reader, and Log
 * out is its glyph with the word on `title` and in its accessible name.
 *
 * ## What the identity becomes at 56px: a glyph, not a monogram
 *
 * The candidates were nothing at all, a monogram like the cluster tiles, an
 * avatar, or `title` only. It is the `account` glyph, and the others are
 * rejected for reasons rather than taste:
 *
 * - **Nothing at all** fails the first condition: a `display: none` element has
 *   no hover box, so "reachable on hover" and the `title` go with it.
 * - **A monogram** is this rail's word for *a cluster*. Every square above this
 *   line is two letters in a bordered box, and one more at the foot would be
 *   read as another cluster before it was read as a person. The vocabulary is
 *   already spent.
 * - **An avatar** has no source. `Session` carries `identity`, `idleExpiresAt`
 *   and `absoluteExpiresAt` and nothing else, so an avatar means dialling a
 *   stranger's image server from a console that deliberately dials nothing.
 * - **`title` only** is the first case again, with an attribute nobody can
 *   reach.
 *
 * A glyph is the one answer whose size does not depend on the identity, which is
 * the case that matters: `admin` fits at 56px by luck, and an email
 * address fits at no width this column will ever have.
 *
 * ## Why the identity is an `sr-only` span
 *
 * The glyph says nothing to a screen reader, so the name is a visually hidden
 * span beside it. An `sr-only` span is safe *here* for the reason it was refused on a tile:
 * the word is kept off the cluster tile because `aria-label` on an anchor
 * **replaces** its contents, so a hidden word inside one is announced by
 * nothing. This is a `<p>` with no accessible name of its own - its contents are
 * all there is, and nothing swallows them.
 */
const WARN_WITHIN_MS = 10 * 60 * 1000;

/** The session, its end, and the way out - one reading, shared by both forms so
 *  that the header and the rail cannot disagree about when a session ends. */
function useSessionControl() {
  const session = useSession();
  const queryClient = useQueryClient();
  const now = useNow();

  const signOut = useMutation({
    mutationFn: () => logout(),
    // Whatever happens, leave. `api/openapi.yaml` answers 204 "whether or not a
    // session was live" precisely so that this cannot get stuck, and a failed
    // sign-out that left the person on the console would be the worse of the
    // two failures.
    //
    // **Except when the store did not record it.** Then nothing was signed
    // out: the session is still live, and the server keeps this browser's
    // cookie so that the sign-out can be tried again. Leaving for the sign-in
    // page would draw a person as signed out who is not, so the tile stays
    // and says so, and the same button tries again.
    onSettled: (_data, error) => {
      if (unrecorded(error)) return;
      queryClient.clear();
      // The server's sign-in page, the one there is; it says this browser is
      // signed out, which the log-out recorded.
      leaveFor(SERVER_SIGN_IN_PATH);
    },
  });

  const data = session.data;
  const endsAt = data === undefined ? undefined : Date.parse(data.absoluteExpiresAt);
  const soon = endsAt !== undefined && Number.isFinite(endsAt) && endsAt - now < WARN_WITHIN_MS;
  return { data, endsAt, soon, now, signOut };
}

/**
 * The same three things for the header - where they go when there is no rail
 * to carry them.
 *
 * **The rail is not drawn when there is one cluster** (`Shell.tsx`), and the
 * rail was the only place the way out was. So this is not a nicety: without it,
 * a console with one cluster and a way to sign back in - every in-cluster
 * deployment, and a desktop with `KT_PASSWORD_HASH` and one kubeconfig context -
 * has no Log out at all. The loopback desktop draws none on purpose (above).
 * `Shell.test.tsx` holds both.
 *
 * ## The name is the menu, and Settings and Log out are in it
 *
 * The signed-in name is a button, and what it opens
 * holds Settings and Log out; `Topbar.tsx` draws no Settings link of its own
 * wherever this menu is drawn (`useAccountMenu`). It is `OverflowMenu` under a
 * different face, so its keys, its scrim, its Escape and the focus going back
 * to the name are the console's one menu and not a copy of it.
 *
 * Three things stay outside it, because a closed menu is not read:
 *
 * - **the session-ending warning**, left of the name, in the last ten minutes;
 * - **a sign-out the store did not record**: the item closes the
 *   menu as it acts, so the sentence that says nothing was signed out is drawn
 *   in the header, where the eye is when the menu shuts, and Log out in the
 *   menu tries again;
 * - **Arrange**, which `Topbar.tsx` keeps beside it.
 *
 * Each item carries its glyph before its label - the sheet's gear for
 * Settings, a door left open for Log out - and the way out is called *Log out*,
 * here and on the rail's tile.
 *
 * The name is still the account glyph and the name, cut at `max-w-40` with the
 * whole of it on `title`, and it is the button's accessible name too: what is
 * read and what is heard are one. Before the session has answered, the button
 * says *Account*, so Log out is never out of reach for want of a name.
 */
export function SessionInline(): ReactNode {
  const { data, endsAt, soon, now, signOut } = useSessionControl();
  const offered = useSignsBackIn();
  const openSettings = useOpenSettings().open;
  const badge = useUpdateBadge();
  const warning =
    soon && endsAt !== undefined ? (
      <span role="status" className="shrink-0 text-warn">
        Session ends in {minutes(endsAt - now)} min
      </span>
    ) : null;
  if (offered !== true) {
    return warning === null ? null : <div className="flex min-w-0 items-center text-2xs">{warning}</div>;
  }
  const name = data?.identity ?? 'Account';
  return (
    <div className="flex min-w-0 items-center gap-2 text-2xs text-muted">
      {warning}
      {/* The tile's sentence, here too. A sign-out the store did
          not record answers 503 only where there is a session store - the
          server profile with Valkey - and the server profile serves one cluster,
          so this, not the tile, is the Log out it is pressed on. One
          line in a 36px header, so it is cut at its width with the whole
          sentence on `title`; the alert reads all of it either way. */}
      {unrecorded(signOut.error) ? <NotSignedOut className="min-w-0 max-w-96 truncate" /> : null}
      <OverflowMenu
        label={name}
        // The update behind the menu says itself on hover too, as the dot does
        // to a screen reader: the account's line, then the update's.
        title={[data === undefined ? 'Account' : `Signed in as ${data.identity}`, ...(badge.shown ? [badge.title] : [])].join('\n')}
        // An update behind the menu is its dot, described to a screen reader;
        // the trigger's name stays the account. Settings inside carries the "1".
        {...(badge.shown ? { flagged: true, flaggedText: 'Update available' } : {})}
        // The header's other text controls' box - `text-xs`, `py-0.5` - so the
        // name sits on their line, and drawn pressed while its menu is open.
        triggerClassName="flex min-h-6 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs pointer-coarse:min-h-11 text-muted hover:bg-panel-2 hover:text-fg aria-expanded:bg-panel-2 aria-expanded:text-fg"
        face={
          <>
            <Icon name="account" />
            <span className="max-w-40 truncate">{name}</span>
            {/* Pointing down: there is more under the name. `Menu.tsx`'s
                chevron, turned rather than a second shape. */}
            <Icon name="chevron" className="size-3 rotate-90" />
          </>
        }
      >
        <OverflowMenuItem
          label="Settings"
          icon="settings"
          {...(badge.shown ? { badge: '1', title: badge.title } : {})}
          onSelect={() => openSettings('/settings')}
        />
        <OverflowMenuItem
          label="Log out"
          icon="logout"
          disabled={signOut.isPending}
          onSelect={() => {
            signOut.mutate();
          }}
        />
      </OverflowMenu>
    </div>
  );
}

/**
 * Whether `SessionInline`, where it is drawn, draws the account menu - and so
 * whether Settings is in that menu rather than a link of the header's own.
 * The one fact `SessionInline` decides on, read by `Topbar.tsx` too so
 * that the two cannot disagree: Settings is drawn exactly once. Unknown - the
 * methods not answered yet - is not the menu, so a cold load keeps the plain
 * link until the answer, and a failed answer is the menu, for the reason above.
 */
export function useAccountMenu(): boolean {
  return useSignsBackIn() === true;
}

export function SessionTile(): ReactNode {
  const { data, endsAt, soon, now, signOut } = useSessionControl();
  const offered = useSignsBackIn();
  if (offered !== true) {
    return soon && endsAt !== undefined ? (
      <div className="border-t border-line pt-2">
        <p role="status" className="px-1 text-center text-2xs text-warn">
          Session ends in {minutes(endsAt - now)} min
        </p>
      </div>
    ) : null;
  }

  return (
    <div className="border-t border-line pt-2">
      {data !== undefined ? (
        <p className="px-1 text-2xs text-muted" title={data.identity}>
          <Icon name="account" className="mx-auto block" />
          <span className="sr-only">{data.identity}</span>
        </p>
      ) : null}

      {/*
        Not shortened, deliberately. This is the one line with no short form, and
        the entry puts its wording out of scope - so it wraps inside a 56px
        column rather than being abbreviated into something an operator has to
        hover to understand. A warning that has to be asked for is not a warning.

        Centered because a two-words-per-line column ragged to the left reads as
        a broken sentence rather than as a paragraph.
      */}
      {soon && endsAt !== undefined ? (
        <p role="status" className="px-1 text-center text-2xs text-warn">
          Session ends in {minutes(endsAt - now)} min
        </p>
      ) : null}

      {/*
        The way out, and the only control in this block: its glyph, with the
        word on `title` and in the accessible name - the Arrange button's shape,
        a few lines above this one in `Rail.tsx`, for the reason it gives: a
        control whose label is clipped mid-word is worse than one drawn as its
        icon with the word on `title`.

        `logout`, the glyph the account menu's Log out carries, so the act is
        one shape wherever it is drawn. It was `external`
        until then, for want of a shape of its own - the arrow out of a box,
        which says *opens elsewhere*.
      */}
      {unrecorded(signOut.error) ? <NotSignedOut className="px-1" /> : null}

      <button
        type="button"
        onClick={() => signOut.mutate()}
        disabled={signOut.isPending}
        aria-label="Log out"
        title={signOut.isPending ? 'Logging out…' : 'Log out'}
        className={`mt-1 flex min-h-6 w-full items-center justify-center rounded-md border border-line px-1 py-1 text-2xs text-muted disabled:cursor-not-allowed disabled:opacity-50 pointer-coarse:min-h-11 ${focusRing}`}
      >
        <Icon name="logout" />
      </button>
    </div>
  );
}

// The act is called Log out wherever a person reads it;
// *signed in* stays, since Sign in keeps its name.
const NOT_SIGNED_OUT =
  'The console could not log this session out: its session store did not answer. You are still ' +
  'signed in; log out again in a moment.';

/**
 * The sentence for a sign-out the store did not record, drawn by both forms -
 * wherever Log out is drawn, so is this. One component, so the
 * header and the rail cannot say it in two ways.
 */
function NotSignedOut({ className }: { className: string }): ReactNode {
  return (
    <p role="alert" title={NOT_SIGNED_OUT} className={`text-2xs text-bad ${className}`}>
      {NOT_SIGNED_OUT}
    </p>
  );
}

/** A sign-out the session store did not record: 503 `session_store_unreachable`. */
function unrecorded(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'session_store_unreachable';
}

function minutes(ms: number): number {
  return Math.max(0, Math.ceil(ms / 60_000));
}
