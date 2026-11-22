import { useQueries } from '@tanstack/react-query';
import { Suspense, useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';

import {
  isFailedState,
  isReasonOnlyState,
  useNav,
  useResourceDetail,
  type DetailField,
  type DetailPage,
  type DetailSection,
  type EventSubject,
} from '../../api';
import { clusterPath } from '../../clusters/clusterPath';
import { backHere } from '../../components/back';
import { BackLink } from '../../components/BackLink';
import { CloseButton } from '../../components/CloseButton';
import { CopyButton } from '../../components/CopyButton';
import { Message } from '../../components/FoldedMessage';
import { PageHeader } from '../../components/PageHeader';
import { Icon } from '../../components/Icon';
import { Pill } from '../../components/Pill';
import { useUrlParameter } from '../../components/useUrlParameter';
import { externalHref, internalHref } from '../../externalHref';
import { focusRing, focusRingTight } from '../../focus';
import { toolbarSquare } from '../../components/toolbar';
import { Remedy } from '../../components/Remedy';
import { LogsButton } from '../../dock/LogsButton';
import { useChosenNamespaces, withNamespaces } from '../../shell/namespaceMemory';
import { trail, useDeclarePageFacts } from '../../shell/pageFacts';
import { useShellIfPresent } from '../../shell/ShellContext';
import { ValueView } from '../../state/Result';
import { isGoneObject, Pending, StateNotice } from '../../state/StateNotice';
import { answers, contributedActivity, contributedTabs, enabledExtensions, marksOf, type ContributedTab } from './extensionTabs';
import { kindListPath, objectPath, selectorPath, type ObjectIdentity } from './address';
import { Addresses, Certificates } from './Certificates';
import { Containers } from './Containers';
import { Data } from './Data';
import { Activity } from './Activity';
import { Manifest } from './Manifest';
import { Terminals } from './Terminals';
import { useObjectAct } from './useObjectAct';
import { Verbs } from './Verbs';
import { Metrics } from './Metrics';
import { Ports } from './Ports';
import { Relations } from './Relations';
import { Rules } from './Rules';
import { Shortcuts } from './Shortcuts';
import { formatInstant } from '../../components/instant';
import { TABLE } from '../../components/tableLook';

/**
 * One object, drawn.
 *
 * **The same component in both places.** An object has a route of its own -
 * `/c/:cluster/:kind/:ns/:name` - *and* the drawer is what a list opens beside
 * itself. Two components would be two renderings of one payload, which can
 * drift apart (a different view model, a flag set on one and not the other).
 * Here there is one payload and one renderer, so the question cannot be asked.
 *
 * What differs between the two is the frame, and only the frame: the drawer has a
 * close control and a 560px column, the page has neither. That is `chrome`.
 *
 * ## Why a panel at all, since the route exists
 *
 * The resource opens in a panel beside the table, the table keeps refreshing
 * behind it, and closing the panel returns you to exactly where you were; a full
 * page for each resource is the pattern that makes a user stop opening
 * resources. So clicking a row opens the panel and does **not** navigate; the
 * route is what survives a reload and what a link in a ticket points at.
 *
 * ## Every section carries its own state
 *
 * An empty answer and a failed one are drawn differently, per block.
 * `DetailSection` has `state` and `reason` because a drawer whose Data panel
 * could not be read must not draw as a drawer whose object has no data. The
 * whole-object failure is drawn by `ValueView` above; a section's own failure is
 * drawn inside the section, under its own heading, so that what *was* read stays
 * on screen beside what was not. The contract is stricter than a single error
 * paragraph for the object, which is why the section arm is written even though
 * `sectionsOf` sends `ok` on the one section it sends today. What Details says
 * about a Secret - type, key count, immutability - is in the summary's facts,
 * and the keys are the Data tab's alone.
 *
 * ## The order on the page is an argument
 *
 * Facts, then labels, then containers, then certificates, then addresses, then
 * relations. The ordering decision that matters is about what does *not* go
 * first: a severity band pushing "Status: Running" off the first screenful
 * answers a question nobody asked yet. What the object is and whether it is
 * running is why the panel was opened.
 *
 * ## A tab is drawn only where there is a panel behind it
 *
 * The tab strip and the verb row come from the payload (`DetailPage.tabs` and
 * `DetailPage.actions`). A tab that always opens the same refusal teaches the
 * user to stop reading the strip, and a button that looks like Delete and cannot
 * act is worse than no button, so neither is drawn without something behind it.
 */
export interface ObjectDetailProps {
  identity: ObjectIdentity;
  /**
   * The drawer's frame, or the page's.
   *
   * `drawer` draws the close control. `page` does not, because there is nothing
   * to close - the browser's own Back is what leaves a page, and a control that
   * duplicates it is a second answer to *where does this go*.
   */
  chrome: 'drawer' | 'page';
  onClose?: (() => void) | undefined;
}

/**
 * The panels this object can have, in the order the strip draws them.
 *
 * **A closed vocabulary, drawn from what the server declared.** Both halves are
 * load-bearing:
 *
 * - *Closed*, because a tab per thing the server happens to send is a strip that
 *   changes shape between two objects of the same kind, and a key nobody has
 *   written a panel for would draw a tab over an empty box. A key that is not
 *   here is ignored, silently and deliberately.
 * - *Declared*, because a list that is closed **and** fixed would drop the tabs
 *   the object endpoint announces, each with its endpoint. A fixed strip is a
 *   client deciding what an object has, which is the server's answer to give.
 *
 * Details is first, is always present, and is the empty value, so the ordinary
 * address carries no `tab` at all.
 *
 * **`activity` is not a key the server declares.** The server declares `events`
 * and `changes`, and both are drawn as one
 * Activity tab: present when either is declared, absent when neither is, with
 * the one source it has where the server declared only one. An address that
 * still says `tab=events` or `tab=changes` - an older link - opens Activity
 * (`ACTIVITY_SOURCES`).
 *
 * **`metrics` is not in here.** The charts are not a tab: they are drawn above
 * the fact table on the panel that opens, because
 * *what has this been doing* is the question the panel was opened with and a
 * table of facts cannot answer it. They are `DetailPage.metrics` - an address
 * rather than a tab - and the details panel below draws them.
 */
const TAB_ORDER = ['details', 'activity', 'manifest'] as const;

/** The declared keys Activity draws, which are also the old tabs' addresses. */
const ACTIVITY_SOURCES: readonly string[] = ['events', 'changes'];

type TabKey = (typeof TAB_ORDER)[number];

/** The label to draw where the server declared the tab without one, and for
 *  Details, which the server never declares because it is the object itself. */
const FALLBACK_LABEL: Record<TabKey, string> = {
  details: 'Details',
  activity: 'Activity',
  manifest: 'Manifest',
};

function isTabKey(value: string | undefined): value is TabKey {
  return TAB_ORDER.some((key) => key === value);
}

export function ObjectDetail({ identity, chrome, onClose }: ObjectDetailProps): ReactNode {
  const query = useResourceDetail(
    identity.cluster,
    identity.kind,
    identity.namespace,
    identity.name,
  );

  /*
    The chosen tab lives in the address, not in a `useState`.

    It is the same argument as for the object having a route at all: a reload,
    a shared link and a back button all have to land on what was being read. The
    empty string is Details, so the ordinary case adds nothing to the address.
  */
  const [addressedTab, setTab] = useUrlParameter('tab');
  // The two tabs Activity replaced still name it, so an older link lands on
  // what it was pointing at.
  const tab = addressedTab !== undefined && ACTIVITY_SOURCES.includes(addressedTab) ? 'activity' : addressedTab;
  const location = useLocation();
  /* The empty address is Details, so `''` and `'details'` are the same tab. A
     value nobody recognizes — an old link, a typo — is Details too rather than a
     strip with nothing selected and a blank panel underneath.

     Checked against the **vocabulary** and not against what this payload
     declared, so that a reload straight onto `?tab=metrics` opens the metrics
     panel while the object itself is still being read, instead of falling back
     to Details and then jumping when the strip fills in. */

  const answer = query.data;
  const failed = answer !== undefined && answer.state !== 'ok' ? answer : undefined;
  const page = answer !== undefined && answer.state === 'ok' ? answer.data : undefined;

  /* The strip: Details, then whatever the payload declared that this client has
     a panel for, in the vocabulary's order rather than the payload's. The
     server's label wins - it is the one that knows a CronJob's archive is called
     something else - and a declared key nobody here can draw is dropped. */
  const declared = new Map((page?.tabs ?? []).map((one) => [one.key, one.label]));

  /*
    The tab in the address, or Details.

    **Every object lands on Details**, and that is a constant rather than a
    computation. The keys of a ConfigMap or a Secret are *inside* this panel -
    between the facts and Related, `Body` below - so there is no kind with a
    different landing tab.

    Were the landing tab a payload-dependent value, it would be `undefined` for
    a whole round trip; the strip draws Details from the first frame, so a fast
    click in that window would clear the address and then watch the payload move
    the reader elsewhere; and the click handler would have to name the landing
    tab rather than clear unconditionally. The empty address means Details
    everywhere, so none of that can happen.

    An address that names a tab still wins: a reload and a pasted link land on
    what was being read.

    **A row click is not such an address.** The list drops `tab` from the
    address as it opens a row, so a new object lands here rather than on the
    tab the previous one was read on. This panel does not know that and does not need to: it reads the address,
    and the list is what decides whether the address still names a tab.
  */
  /*
    The tabs extensions add for this kind, drawn
    only while the extension is on for this cluster as the server's menu says.
    The menu is asked only when some extension offers a tab for this kind, so a
    panel no extension adds to makes no extra request.
  */
  const shell = useShellIfPresent();
  const offered = contributedTabs(shell?.extensions ?? [], identity.kind);
  const offeredActivity = contributedActivity(shell?.extensions ?? [], identity.kind);
  const nav = useNav(identity.cluster, offered.length > 0 || offeredActivity !== undefined);
  const on = enabledExtensions(nav.data);
  // An extension's Activity view is drawn only while it is on, and never over a refusal.
  const activityView =
    offeredActivity !== undefined && failed?.state !== 'forbidden' && on.has(offeredActivity.extension)
      ? offeredActivity.Component
      : undefined;
  const activityPending = offeredActivity !== undefined && failed?.state !== 'forbidden' && nav.isPending;
  // A tab with a condition on the object is asked about
  // the marks this payload carries, and is not drawn before it has arrived.
  const marks = marksOf(page);
  // An object the person may not read offers no extension tab: the server refuses them too.
  const refused = failed?.state === 'forbidden';
  const eligible = refused ? [] : offered.filter((one) => on.has(one.extension) && answers(one, marks));
  // A tab with a probe is asked once its condition holds, and drawn when the
  // extension answers yes. Keyed by the object, so
  // a second visit draws it at once.
  const probes = useQueries({
    queries: eligible.map((one) => ({
      queryKey: ['extension-tab-probe', one.id, identity.cluster, identity.kind, identity.namespace ?? '', identity.name] as const,
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        one.probe === undefined
          ? Promise.resolve(true)
          : one.probe({ cluster: identity.cluster, kind: identity.kind, namespace: identity.namespace, name: identity.name }, signal),
      enabled: one.probe !== undefined,
      retry: false,
      staleTime: 60_000,
    })),
  });
  const contributed = eligible.filter((one, i) => one.probe === undefined || probes[i]?.data === true);
  const probing = (one: ContributedTab): boolean => {
    const i = eligible.indexOf(one);
    return one.probe !== undefined && (i < 0 ? query.isPending : probes[i]?.isPending === true);
  };
  // A reload onto an extension's tab waits for the menu - and, for a tab with a
  // condition, for the object - rather than falling back to Details and
  // jumping when they answer.
  const addressed = offered.find((one) => one.id === tab);
  const waiting =
    addressed !== undefined &&
    (nav.isPending || (addressed.when !== undefined && query.isPending) || probing(addressed));
  const activeContributed: ContributedTab | undefined = contributed.find((one) => one.id === tab);

  const hasActivity = ACTIVITY_SOURCES.some((key) => declared.has(key));
  const active: string = isTabKey(tab) ? tab : activeContributed !== undefined || waiting ? (tab ?? 'details') : 'details';
  const strip: { key: string; label: string }[] = [
    ...TAB_ORDER.filter((key) => key === 'details' || (key === 'activity' ? hasActivity : declared.has(key))).map((key) => ({
      key,
      label: declared.get(key) ?? FALLBACK_LABEL[key],
    })),
    ...contributed.map((one) => ({ key: one.id, label: one.label })),
  ];


  // The heading is the identity the user clicked, not the payload's - so a
  // panel that could not be read still says which object it is about. The
  // payload's `title` is the kind as a person writes it ("StatefulSet") and wins
  // as soon as there is one, and is kept once the object is gone: a read that
  // answers "no longer exists" must not turn the heading into the URL's slug.
  const objectKey = JSON.stringify([identity.cluster, identity.kind, identity.namespace ?? '', identity.name]);
  const [spelled, setSpelled] = useState<{ key: string; title: string } | undefined>(undefined);
  if (page?.title !== undefined && (spelled?.key !== objectKey || spelled.title !== page.title)) {
    setSpelled({ key: objectKey, title: page.title });
  }
  const title = page?.title ?? (spelled?.key === objectKey ? spelled.title : identity.kind);
  const namespace = page?.namespace ?? identity.namespace;

  /*
    **An object this panel's own verb removed is left**: the drawer closes and
    the page goes back to its list.

    - A Delete the cluster accepted leaves as soon as its outcome is closed,
      without waiting for the object to go. A pod in its grace period, or
      anything a finalizer holds, is still there for a while, and a panel
      waiting on it showed "Running" and live verbs about an object the person
      had just deleted.
    - Any other verb of this panel's leaves once its outcome is closed and the
      read answers the object gone: a pod's Restart deletes it and something
      else makes another.

    **The outcome stays up until it is closed**, even where the object is gone
    meanwhile: "Deleted Pod x." is what the person reads, and the keyboard is
    on its Close. So the act is held here and its dialog drawn by the section,
    not by the verbs row, which goes when the read answers the object gone.

    Keyed, because the page's component is reused from one object to the
    next. An object gone for any other reason stays, saying it is gone.
  */
  const acted = useRef<string | undefined>(undefined);
  const listPath = useKindList(identity, namespace);
  const navigate = useNavigate();
  const leave = useCallback(() => {
    acted.current = undefined;
    if (chrome === 'drawer') onClose?.();
    else void navigate(listPath, { replace: true });
  }, [chrome, onClose, navigate, listPath]);
  const gone = failed !== undefined && isGoneObject(failed.state, failed.reason);
  useEffect(() => {
    if (gone && acted.current === objectKey) leave();
  }, [gone, objectKey, leave]);
  const act = useObjectAct(undefined, (outcome, verb) => {
    acted.current = objectKey;
    if ((verb === 'delete' && outcome.succeeded > 0) || gone) leave();
  });

  return (
    <section
      className="flex h-full min-h-0 flex-col"
      aria-label={`${title} ${identity.name}`}
      data-object-chrome={chrome}
    >
      {chrome === 'page' ? <DeclarePage identity={identity} title={title} namespace={namespace} /> : null}
      <PageHeader
        // The page's way back: the browser's Back is not there in the desktop window.
        back={chrome === 'page' ? <PageBack identity={identity} namespace={namespace} tab={active} /> : undefined}
        kind={title}
        name={identity.name}
        namespace={namespace}
        actions={
          <>
            {chrome === 'drawer' ? (
              // **The drawer's way to the object's own page.** On the tab being
              // read, so the page opens on what the drawer showed. A plain link, so
              // a middle-click opens it in a tab of its own. It carries the address
              // it was followed from - the list, with this object and tab in its
              // query - for the page's Back.
              <Link
                to={objectPath(identity) + (active === 'details' ? '' : `?tab=${encodeURIComponent(active)}`)}
                state={backHere(`${location.pathname}${location.search}`)}
                aria-label="Open as page"
                title="Open as page"
                className={`${toolbarSquare} text-muted hover:text-fg ${focusRing}`}
              >
                <Icon name="external" />
              </Link>
            ) : null}
            {chrome === 'drawer' && onClose !== undefined ? (
              <CloseButton onClick={onClose} escape />
            ) : null}
          </>
        }
      />

      {/* The action row: what can be *done* to this object, and the ways into a
          terminal on it, between the heading and the tabs. They answer one
          question - the verbs change the object, the dock controls watch it -
          while the tabs below answer a different one, which is what to read
          about it.

          ## The watching controls come first

          Watching is what a user does forty times an hour and changing is what
          they do carefully, a few times a day, so the near end of the row
          belongs to Logs, Shell and Claude, and the `flex-1` spacer pushes the
          destructive end **away** rather than towards.

          ## One arm, so the order never depends on the object

          Two rows - one for an object with verbs, one for an object without -
          could disagree about where the dock controls live: a spacer pushes them
          right on a Pod and has nothing to push them on a Namespace, so Shell
          would be on the right of one object and on the left of the next. So the
          row is drawn by one arm whose order does not depend on the object: the
          watching controls are unconditional and come first, and the verbs are
          what is conditional. `Verbs` answers `null` on an empty list. Only the
          spacer stays conditional, and that is `empty:hidden`'s doing rather
          than a second opinion about layout: a `flex-1` span is a child like any
          other, and a row holding one would draw an empty band on an object that
          offers nothing.

          ## One gap inside the watching set, and the spacer keeps the other

          Logs is a child of this row and Shell and Claude are children of
          `Terminals`' own `gap-1` group, so the row's gap between columns is that
          same `gap-1`, which is also `Verbs`' - one distance between neighbors
          in a set, whichever component draws them.

          **The verbs' separation:** the spacer plus 4px of gap and 8px of its own
          margin on each side makes 12px on a row that fits on one line. The gap
          between lines is `gap-y-3`.

          **What it costs on a row that wraps.** The spacer's outer size is 16px,
          so appending it to a line costs 4 + 16 = 20px. It therefore drops to a
          line of its own slightly sooner. That line is 0px tall, but it brings a
          second `gap-y-3` with it, so the row grows by a 12px empty band: on a
          Node at about 167-175px of row width, and on a Pod at about 135-143px.
          Taken knowingly: `ml-2` alone would keep a later break point, but would
          bring Claude and the first verb to 16px apart at the single-line
          threshold, and a tight gap there matters more than wrapping 8px sooner
          at phone width.

          Not a wrapper round Logs and `Terminals`: an empty wrapper is still a
          child, and it would defeat `empty:hidden` above. Not `contents` on the
          group either: that spaces the set at the row's 12px, wider than every
          other set in the console, and puts `role="group"` on a box that is
          not drawn. */}
      <div
        // Named so a test can read this row's children in order rather than
        // finding it by its position under the header; that is the whole
        // reason a presentational div carries an attribute at all.
        data-object-controls=""
        className="flex flex-wrap items-center gap-x-1 gap-y-3 border-b border-line px-3 py-1.5 empty:hidden"
      >
        {refused ? null : (
          <LogsButton
            cluster={identity.cluster}
            kind={identity.kind}
            namespace={identity.namespace}
            name={identity.name}
          />
        )}
        <Terminals
          identity={identity}
          containers={page?.containers}
          kindLabel={page?.title}
          shellOffer={page?.shell}
          subjectNote={
            failed === undefined
              ? 'Still reading this object. A session about it needs its Kind, which arrives with the payload.'
              : `This object could not be read (${failed.state}), so a session about it would have no subject.`
          }
        />
        {/* The verbs, at the far end, in the order the server sent them - the
            list is `DetailPage.actions` and `Verbs` says why nothing on this
            side may compute it. Their order *within* the group is deliberately
            left alone: every verb a Pod offers is destructive, so a
            destructive-last sort would reorder nothing there. */}
        {page?.actions === undefined || page.actions.length === 0 ? null : (
          <>
            <span
              // Named for the same reason the row above it is: a measurement
              // has to name what it is measuring. Without it the only way to
              // reach this span is structurally - the arm's one empty child -
              // and the day a second empty one appears the check goes on
              // passing against the wrong box rather than breaking.
              data-arm-spacer=""
              // `mx-2` is the 8px the row's gap gave up on each side of the
              // spacer (see above): 4 + 8 is 12.
              className="mx-2 flex-1"
            />
            <Verbs
              identity={identity}
              actions={page.actions}
              act={act}
            />
          </>
        )}
      </div>

      {/* The tab strip. A strip of one tab is chrome that teaches a user there is
          nothing else, but Details is always present and the strip is drawn
          from the first frame. */}
      <div role="tablist" aria-label="Panels" className="flex gap-1 border-b border-line px-2">
        {strip.map((one) => (
          <button
            key={one.key}
            type="button"
            role="tab"
            id={`object-tab-${one.key}`}
            aria-selected={active === one.key}
            aria-controls={`object-panel-${one.key}`}
            // **The empty address means Details, on every kind**, so Details clears
            // the parameter and every other tab names itself. That keeps the link
            // short for the ordinary case. There is no other landing tab for the
            // comparison to be wrong about.
            onClick={() => setTab(one.key === 'details' ? '' : one.key)}
            className={`-mb-px border-b-2 px-2 py-1 text-xs ${focusRingTight} ${
              active === one.key
                ? 'border-accent text-fg'
                : 'border-transparent text-muted hover:text-fg'
            }`}
          >
            {one.label}
          </button>
        ))}
      </div>

      <div
        id={`object-panel-${active}`}
        role="tabpanel"
        aria-labelledby={`object-tab-${active}`}
        // `relative`: the scroller is the containing block of what it scrolls.
        // Without it a copy button's `sr-only` live region low in the ports
        // could escape this panel and make the whole document scroll beside it
        // - `Shell.tsx` says why.
        className="relative min-h-0 flex-1 overflow-auto p-3"
      >
        {activeContributed !== undefined ? (
          <div data-extension-tab={activeContributed.id}>
            <Suspense fallback={<Pending what={activeContributed.label} />}>
              <activeContributed.Component
                cluster={identity.cluster}
                kind={identity.kind}
                namespace={identity.namespace}
                name={identity.name}
              />
            </Suspense>
          </div>
        ) : waiting ? (
          <Pending what="this tab" />
        ) : active === 'manifest' ? (
          <Manifest
            cluster={identity.cluster}
            kind={identity.kind}
            namespace={identity.namespace}
            name={identity.name}
          />
        ) : active === 'activity' && page === undefined ? (
          /* A reload onto `?tab=activity` (or an old `?tab=events`) while the
             object is still being read: which sources the panel has is the
             payload's to say, so it waits rather than drawing "Nothing here"
             over a question not yet asked, and an object that could not be
             read says so here rather than as an empty list. */
          <ValueView query={query} what={`${title} ${identity.name}`}>
            {() => null}
          </ValueView>
        ) : active === 'activity' ? (
          /* The events came with the object, so they are drawn from `page`; the
             changes are a read of their own, asked only where the server
             declared them. */
          <Activity
            identity={identity}
            events={declared.has('events') ? { rows: page?.events ?? [], note: page?.eventsNote } : undefined}
            changes={declared.has('changes')}
            view={activityView}
            waiting={activityPending}
            place={{ cluster: identity.cluster, namespace: identity.namespace, chrome }}
          />
        ) : (
          <ValueView query={query} what={`${title} ${identity.name}`}>
            {(detail) => (
              <div className="flex flex-col gap-4">
                {/* Charts first, on the kinds that have them: a table of facts
                    cannot answer *what has this been doing*, and that is the
                    question the panel was opened with.

                    It is a separate read and stays one - the facts are on screen
                    and readable before Prometheus has been asked anything. An
                    object with no measurements carries no address and draws
                    nothing at all. */}
                {detail.metrics === undefined || detail.metrics === '' ? null : (
                  <Metrics
                    cluster={identity.cluster}
                    kind={identity.kind}
                    namespace={identity.namespace}
                    name={identity.name}
                    /* A claim's storage chart is there or it is not: no notice
                       for a cluster without Prometheus, a claim nothing
                       mounts, or a failed read. */
                    silent={identity.kind.toLowerCase().startsWith('persistentvolumeclaim')}
                  />
                )}
                <Body
                  cluster={identity.cluster}
                  page={detail}
                  chrome={chrome}
                  extras={
                    /* The extras, one line each, between what the object says
                       about itself and Related: where to look next, after the
                       charts and the facts. Nothing is drawn until one answers. */
                    <Shortcuts
                      identity={identity}
                      marks={{ labels: detail.labels ?? {}, annotations: detail.annotations ?? {} }}
                      tabs={contributed}
                      onTab={setTab}
                    />
                  }
                />
              </div>
            )}
          </ValueView>
        )}

        {/* The same arrangement the list has, and for the same reason: `Remedy`
            answers *what does the user do next*, and `pending` has no next
            move - nothing was asked, so nothing failed. `isFailedState` is what
            keeps `pending` out. */}
        {active === 'details' && failed !== undefined && isFailedState(failed.state) ? (
          <Remedy state={failed.state} reason={failed.reason} />
        ) : null}
      </div>

      {/* The verbs' dialog, drawn here rather than in their row: the row goes
          when the object does, and the outcome stays until it is closed. */}
      {act.dialog}
    </section>
  );
}

function Body({
  cluster,
  page,
  chrome,
  extras,
}: {
  cluster: string;
  page: DetailPage;
  /** The extras' lines for this object, drawn just above Related. */
  extras?: ReactNode;
  /** Handed down to the relations and read by nothing else here. Which frame
   *  this is decides where a relation goes - the related kind's list from the
   *  drawer, the related object's page from a page - and `Relations.tsx` says
   *  why. */
  chrome: 'drawer' | 'page';
}): ReactNode {
  const labels = page.labels ?? {};
  const annotations = page.annotations ?? {};
  const relations = page.relations ?? [];
  const containers = page.containers ?? [];
  const ports = page.ports ?? [];
  const urls = page.urls ?? [];

  if (
    page.sections.length === 0 &&
    Object.keys(labels).length === 0 &&
    relations.length === 0 &&
    containers.length === 0 &&
    ports.length === 0 &&
    urls.length === 0 &&
    page.certificates === undefined &&
    // **An Event is the one object whose whole content is this member**, and
    // leaving it out of the check drew "Nothing here" over the sentence the
    // panel exists for. Every member added to the body has to be added here
    // too, or the emptiness test outlives what it was counting.
    page.event === undefined &&
    // The same warning applies: a Role whose page has no relations
    // and no events - which is most of them - would draw "Nothing here" over
    // the permissions it exists to grant. The member is present on a Role and a
    // ClusterRole whatever its state, including `empty`, so its presence is the
    // test rather than its length.
    page.rules === undefined &&
    // An object whose whole Details content is its annotations - a
    // ServiceAccount that Helm owns is the ordinary case - would otherwise
    // render "Nothing here" over the one thing it has to say.
    Object.keys(annotations).length === 0 &&
    (page.relationsNote ?? '') === '' &&
    // A ConfigMap nothing references has no relations, no note and - on the
    // majority of them - nothing else on the Details tab either, so the one
    // sentence it does have would be drawn under "Nothing here". The member's presence is the test rather than its
    // state: `ok` means there are rows, which the line above already counts.
    page.usedBy === undefined &&
    // The keys. A ConfigMap that nothing references has no relations and no note,
    // and most of them have no sections either: the object's whole content is
    // its data block, so leaving this line out would draw "Nothing here" over
    // the only thing on the page. The address is the test: it is the server's
    // statement that there is a block to draw, and whether that block turns out
    // to be empty is the panel's sentence to write rather than this one's.
    (page.data ?? '') === '' &&
    // Every member the body can draw has to be counted here, or the emptiness
    // test outlives what it was counting. This one is not a member - it is drawn
    // from the kind - but it is drawn, and a Node whose facts could not be read
    // would otherwise get "Nothing here" over the one thing this panel could
    // still offer.
    page.kind !== 'nodes'
  ) {
    // Not an error and not a blank panel. An object the console read and found
    // nothing to say about is a different sentence from every failure above and
    // has to be one.
    return (
      <div className="flex flex-col gap-4">
        <StateNotice state="empty" />
        {extras}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {/* **An Event first, above its own metadata**, because the message *is*
          the object: everything else on the panel describes that sentence. */}
      <EventSaid event={page.event} />
      {page.sections.map((section) => (
        <Section key={section.key} section={section} />
      ))}
      {/* Immediately under the facts, because the fact directly above it is
          what raises the question: a Node's summary ends in `Pods: 110
          allocatable of 110`, which is how many it *may* hold. The ones it
          *has* are one click away. */}
      <NodePods cluster={cluster} kind={page.kind} name={page.name} />
      {/* A chip leads to this kind's list in this namespace, selected on that
          label: `key=value` exactly, as `kubectl -l` reads it. A key with no
          value is `key=`, present and empty, and contains `=`, so the box reads
          it back as a selector (`filterText.ts`). */}
      <Labels labels={labels} listOf={(key, value) => selectorPath(cluster, page.kind, page.namespace, `${key}=${value}`)} />
      {/* Immediately after the labels - facts, then labels, then annotations -
          and never before them: a label is the set a selector matches on and is why the panel was
          opened; an annotation is reference material. */}
      <Annotations annotations={annotations} />
      <Containers
        cluster={cluster}
        kind={page.kind}
        namespace={page.namespace}
        name={page.name}
        containers={containers}
        note={page.envNote}
        chrome={chrome}
      />
      <Certificates set={page.certificates} />
      {/* Immediately after the facts and the sets the object is described by,
          and before the ports and the addresses: on a Role this is the panel
          the page was opened for, and on every other kind it is absent. */}
      <Rules set={page.rules} />
      {/* Above the addresses, because a port is what an address is made of and
          because this is where a forward is opened - the panel a user comes
          to the object for when the object is a Service. */}
      <Ports
        cluster={cluster}
        namespace={page.namespace}
        name={page.name}
        kind={page.kind}
        ports={ports}
        note={page.portsNote}
      />
      <Addresses urls={urls} note={page.urlsNote} />
      {/* **The keys, above Related and below everything the object says about
          itself.** A pod reads facts-then-Related; this puts the one section
          ConfigMaps and Secrets add in the middle of it rather than behind a tab.

          Gated on the address and not on the kind, which is the same rule the
          charts above follow and the reason `TAB_ORDER` was never a fixed strip:
          *which panels does this object have* is the server's answer to give. A
          client testing `kind === 'configmaps'` would be a second copy of
          `detail.HasData`, and the weaker one - a custom resource whose plural
          is `configmaps` in another group is not the core ConfigMap.

          It keeps its own read, so this section paints after the facts around
          it. That is deliberate and `dataAddressFor` argues it: a ConfigMap's
          values are unbounded, and a panel that waited for them would show a
          name and an age a round trip late. */}
      {(page.data ?? '') === '' ? null : (
        <Data
          cluster={cluster}
          kind={page.kind}
          namespace={page.namespace}
          name={page.name}
        />
      )}
      {extras}
      <Relations
        cluster={cluster}
        relations={relations}
        note={page.relationsNote}
        usedBy={page.usedBy}
        chrome={chrome}
      />
    </div>
  );
}

/**
 * The pods on this node, as a jump into the list that already draws them.
 *
 * The shape is a jump rather than a tab. A tab would keep the user on the node
 * and is a **contract change** - `TAB_ORDER` is closed and server-declared, so
 * it is a new `Tab`, a new endpoint in `api/openapi.yaml`, a new row schema and
 * a regenerated `schema.ts`. The jump costs a link, and it lands on a grid that
 * already answers the other questions: the Pods list carries CPU and Memory
 * beside Node, fed by one metrics-server call for the whole cluster cached for
 * ten seconds, and it caps nothing. So consumption and the full set of pods are
 * answered by the destination existing rather than by anything here.
 *
 * ## The link is exact, and that is the whole of its quality
 *
 * `?search=<node>` would be the obvious address and it is wrong in a way
 * nobody would catch: the box is a **substring** search, so on a cluster whose
 * nodes are `ip-10-0-1-7` and `ip-10-0-1-70` the first lands on a list holding
 * both, with a Node column quietly disagreeing with the heading. A user who
 * typed that would see it. A user handed it by a link did not compose the
 * query and has no reason to audit it, which is what makes a console-made
 * approximation worse than a user-made one.
 *
 * So it is the Node **filter**, `?filter=Node:<name>` - the list's filter
 * control's own parameter, equal rather than contained, and matched on the
 * server. An old address carrying the `?search=node:<name>` grammar is
 * translated by the list page. The column is named by its label, as the control
 * names it, because a key moves when a column is added before it. The address
 * is pasteable - the property relied on rather than merely respected, since the
 * whole feature *is* an address.
 *
 * ## No count on it, and no namespace in it
 *
 * **No count**, because the number of pods running here is a read this page does
 * not make. What the panel has is *allocatable of capacity*, which is how many
 * the node may hold; printing that as "110 pods" would answer a different
 * question in the same sentence, and the only honest count is a request this
 * link exists to avoid.
 *
 * **No `namespace` parameter**, so the list opens across all of them - which is
 * what `kubectl get pods --field-selector spec.nodeName=<node> -A` answers and
 * what a user expects. Inheriting a namespace would under-report
 * without saying it had.
 */
function NodePods({
  cluster,
  kind,
  name,
}: {
  cluster: string;
  /** The plural resource name, as the payload and the route both spell it. */
  kind: string;
  name: string;
}): ReactNode {
  if (kind !== 'nodes' || name === '') return null;

  // Built with URLSearchParams rather than by hand: a node name is a DNS label
  // and would survive concatenation, but the colon in the value would not, and
  // an address that only works for well-behaved names is the kind that fails on
  // the one cluster nobody tested.
  const query = new URLSearchParams();
  query.set('filter', `Node:${name}`);
  const to = `${clusterPath(cluster)}/pods?${query.toString()}`;

  return (
    <section data-section="node-pods">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Pods</h3>
      {/* The same box a relation draws, and deliberately: this is a navigation
          out of the panel to a list, which is exactly what a relation is, and
          two shapes for one gesture would be two things to learn. */}
      <Link
        to={to}
        className={`flex items-center gap-2 rounded-lg border border-line bg-bg px-2 py-[5px] text-sm text-fg hover:border-accent ${focusRing}`} // style-allow: `.rel`'s 5px, no step on the 4px scale
      >
        Show the pods running here
      </Link>
    </section>
  );
}

/**
 * What an Event says, when the object is one.
 *
 * ## Why it exists
 *
 * Without it, an Event's drawer would show its Kind, its API version, its
 * generated name and when it was created — everything about the object except
 * the sentence it exists to carry, which would be reachable only by reading the
 * Manifest tab's YAML.
 *
 * The server reads both API shapes as one — `core/v1`'s
 * `message`/`involvedObject`/`source` and `events.k8s.io/v1`'s
 * `note`/`regarding`/`reportingController`.
 *
 * ## Never clipped, and both instants
 *
 * The list clips the message to 140 characters because a table cell holding a
 * paragraph is a column nobody can read. This is the place the whole sentence
 * exists. And an event repeating for two days is a different fact from one that
 * fired forty-seven times in a minute: only `first` **and** `last` say which.
 */
function EventSaid({ event }: { event: EventSubject | undefined }): ReactNode {
  if (event === undefined) return null;
  const warning = (event.type ?? '').toLowerCase() === 'warning';
  return (
    <section
      className={`flex flex-col gap-1 border-l-2 py-1 pl-2 ${warning ? 'border-warn' : 'border-line'}`}
    >
      <div className="flex flex-wrap items-baseline gap-2 text-2xs">
        {event.reason === undefined || event.reason === '' ? null : (
          <span
            className={`rounded px-1.5 py-0.5 ${warning ? 'bg-warn/15 text-warn' : 'bg-panel-2 text-muted'}`}
          >
            {event.reason}
          </span>
        )}
        {event.object === undefined || event.object === '' ? null : (
          <span className="text-fg">{event.object}</span>
        )}
        {/* `spec.containers{istio-proxy}` — on a pod with a sidecar this is the
            difference between "the app failed" and "the mesh failed". */}
        {event.objectField === undefined || event.objectField === '' ? null : (
          <span className="text-muted">{event.objectField}</span>
        )}
        {event.source === undefined || event.source === '' ? null : (
          <span className="text-muted">{event.source}</span>
        )}
        {event.node === undefined || event.node === '' ? null : (
          // Which node refused to pull this is the next question.
          <span className="text-muted">{event.node}</span>
        )}
        {event.action === undefined || event.action === '' ? null : (
          <span className="text-muted">{event.action}</span>
        )}
        {event.count === undefined || event.count <= 1 ? null : (
          <span className="text-muted tabular-nums">×{event.count}</span>
        )}
      </div>
      {/* The whole thing, wrapping - and folded where it runs to several
          lines, as on the Events tab. */}
      <Message
        message={event.message}
        label={warning ? 'show error' : 'show message'}
        inline={(text) => <p className="text-sm break-words">{text}</p>}
      />
      <Span first={event.first} last={event.last} />
    </section>
  );
}

/** When it started and when it last happened — both, because the pair is what
 *  tells a two-day repeat from a burst. */
function Span({ first, last }: { first: string | undefined; last: string | undefined }): ReactNode {
  if (last === undefined || last === '') return null;
  const repeated = first !== undefined && first !== '' && first !== last;
  return (
    <p className="text-2xs text-muted tabular-nums">
      {repeated ? `${stamp(first)} → ${stamp(last)}` : stamp(last)}
    </p>
  );
}

function stamp(at: string): string {
  return formatInstant(at, { seconds: true });
}

/**
 * The labels, as chips.
 *
 * **Chips rather than a table**: a set is scanned and not read, and chips wrap
 * where a `nowrap` grid cell would not. What it is for is the set a Service's
 * selector is matched against, on the object being matched.
 *
 * A label with no value is drawn as the key alone: `app=api` and `standalone`
 * say different things, and `standalone=` says a third that is not true of
 * either.
 */
function Labels({
  labels,
  listOf,
}: {
  labels: Readonly<Record<string, string>>;
  /** Where a chip leads: this kind's list, selected on that label. */
  listOf: (key: string, value: string) => string;
}): ReactNode {
  if (Object.keys(labels).length === 0) return null;

  return (
    <section data-section="labels">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Labels</h3>
      <Chips of={labels} linkOf={listOf} />
    </section>
  );
}


/**
 * The chip row, and it is a component rather than two copies.
 *
 * Labels and annotations share it: **wherever a pattern exists, a fix carries
 * to every component that uses it.** Two chip rows would be two answers to "what
 * does a key with no value look like" the first time one of them was touched,
 * and the answer - the key alone - is the part that carries meaning: `app=api`
 * and `standalone` say different things, and `standalone=` says a third that is
 * true of neither.
 *
 * `truncate` on the value and the whole pair in `title`: a label is short by
 * convention and an annotation is not, and a chip holding a JSON document is the
 * wall this row must not become. The full text stays reachable by hovering and
 * by the Manifest tab.
 */
function Chips({
  of,
  linkOf,
}: {
  of: Readonly<Record<string, string>>;
  /** Where a chip leads, when it is a link. */
  linkOf?: ((key: string, value: string) => string) | undefined;
}): ReactNode {
  const keys = Object.keys(of).sort((a, b) => a.localeCompare(b));
  const chip = 'max-w-full truncate rounded-sm border border-line bg-panel-2 px-1.5 py-0.5 font-mono text-2xs';
  return (
    <div className="flex flex-wrap gap-1">
      {keys.map((key) => {
        const value = of[key] ?? '';
        const said = value === '' ? key : `${key}=${value}`;
        const text = (
          <>
            <b className="font-medium">{key}</b>
            {value === '' ? null : `=${value}`}
          </>
        );
        return linkOf === undefined ? (
          <span key={key} data-chip title={said} className={chip}>
            {text}
          </span>
        ) : (
          <Link
            key={key}
            data-chip
            to={linkOf(key, value)}
            title={said}
            className={`${chip} text-fg hover:border-accent ${focusRing}`}
          >
            {text}
          </Link>
        );
      })}
    </div>
  );
}

/**
 * The annotations, as the same chips and behind a disclosure.
 *
 * ## Why this is not one more line beside `Labels`
 *
 * A label is a short identifier a selector matches on. An annotation is free
 * text of unbounded length that a controller, a packaging tool or a person put
 * on the object - and on anything ever `kubectl apply`-ed there is one holding
 * the whole object again. Drawn open, the block is the tallest thing on the page
 * and pushes the facts off the first screenful, which is the ordering argument
 * this file already makes about a severity band: what the object is and whether
 * it is running is why the panel was opened.
 *
 * So: **closed by default**, one line saying how many there are. `<details>`
 * rather than a button and a conditional render, which is the same choice
 * `Menu.tsx` and `ManifestEditor.tsx` made - the browser owns the state, the
 * keyboard works, and find-in-page reaches inside it.
 *
 * This client applies no rule of its own - it does not filter, mask or decide
 * anything about a key. The treatment is the producer's, so a second client
 * cannot get it wrong.
 */
function Annotations({ annotations }: { annotations: Readonly<Record<string, string>> }): ReactNode {
  const keys = Object.keys(annotations);
  if (keys.length === 0) return null;

  return (
    <section data-section="annotations">
      <details>
        <summary
          className={`mb-1 cursor-pointer text-xs font-medium text-muted uppercase ${focusRing}`}
        >
          Annotations ({keys.length})
        </summary>
        <Chips of={annotations} />
      </details>
    </section>
  );
}

/**
 * One block, with its own state.
 *
 * The heading is drawn whatever the state: a section that could not be read must
 * still say *which* section could not be read, or the notice is a sentence about
 * nothing. That is the same argument `DataGrid` makes for keeping the headings
 * above an empty table.
 */
function Section({ section }: { section: DetailSection }): ReactNode {
  const state = section.state;
  const fields = section.fields ?? [];

  const body =
    state !== undefined && state !== 'ok' ? (
      isReasonOnlyState(state) || state === 'empty' ? (
        <StateNotice state={state} reason={section.reason} />
      ) : (
        // Unreachable under the contract's own enum, and written rather than
        // cast away: a state this client does not know is the console's API
        // answering out of contract, and saying so beats rendering nothing.
        <StateNotice state="unreadable" reason={section.reason} />
      )
    ) : fields.length === 0 ? (
      <StateNotice state="empty" reason={section.reason} />
    ) : (
      <Facts fields={fields} />
    );

  return (
    <section data-section={section.key}>
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">{section.label}</h3>
      {body}
    </section>
  );
}

/**
 * The fact table.
 *
 * `kt-grid` and not `grid`: Tailwind emits `.grid { display: grid }`, which takes
 * a table out of table layout entirely and leaves the `nowrap` rule of the
 * table class with nothing to act on. `theme.css` names this table - the drawer's
 * fact tables share the class - as one of the places where that rule applies but
 * the list tables' fixed layout deliberately does not: the fixed layout is
 * scoped to `#panel > .table-scroll > .kt-grid`, and content in a drawer should
 * size to itself.
 *
 * The `nowrap` rule is therefore live here: every cell is `nowrap` unless its
 * class says otherwise, **on the cell**. A fact table is not a list read down one
 * column, though: it is two columns side by side, and the labels hold one line
 * while the values are the column that gives way.
 *
 * **Labels on one line, capped at 40% of the table.** A label is a word or two
 * (`Kind`, `Namespace`), and the label column is as wide as the longest one.
 * A `wrap` class on the label would let it break anywhere, so its min-content
 * width would be one letter: any value wider than the drawer would then squeeze
 * the column to that, and `Kind` would read one letter per line. A namespace
 * condition type
 * (`NamespaceDeletionGroupVersionParsingFailure`) is the one label longer than
 * the cap; it is cut with an ellipsis and whole in its tooltip - and, since a
 * tooltip needs a mouse, a cut label is also a button that shows its whole text
 * in place to a keyboard or a finger (`FactLabel`).
 *
 * **Under 16rem (`@max-3xs`) the two columns stack.** At a 390px window the drawer is
 * `46vw`, 179px, and 154px of table: `Namespace` beside a Namespace's
 * `Terminating` badge is 174px, so no cap can keep both whole side by side.
 * Below a 16rem table each fact is two lines instead - its label, then its
 * value across the whole width - and no label is cut there but one longer than
 * the drawer. The table and its body stack too: a block row inside a table is
 * still sized by the table, which grows to the longest label's whole width.
 *
 * The cap is `40cqi` on a span, against the `@container` wrapper, and not a
 * `max-width` on the `<th>`: a table cell ignores `max-width`, and a percentage
 * inside a cell sized by its content is resolved against nothing. The wrapper's
 * width is the section's, so the cap does not move with what is in the table.
 *
 * **Each section sizes its own label column.** Every section is its own table,
 * so a section of short labels (the Summary) starts its values further left
 * than one holding `Restart policy` or a condition type: the value columns do
 * not line up from one section to the next. That is the intent -
 * each column as wide as the longest label *it* holds - and the price of not
 * giving every section the width of the drawer's longest label.
 *
 * **Values wrap.** Every value cell carries `wrap` - `white-space: normal` and a
 * break anywhere when a token has nowhere else to break - so a long image or
 * owner name takes a second line inside the drawer instead of pushing past its
 * edge. A sentence keeps `message` beside it: `prose` travels in the payload
 * and is still never re-decided from the label.
 */
function Facts({ fields }: { fields: readonly DetailField[] }): ReactNode {
  return (
    <div className="@container">
      <table className={`${TABLE} w-full @max-3xs:block`}>
        <tbody className="@max-3xs:block">
          {fields.map((field, at) => (
            // The label is not unique - a Containers panel repeats "Image" - so the
            // key carries its position. A label-keyed row would drop every repeat
            // but the first, silently.
            <tr key={`${field.label}-${String(at)}`} className="border-b border-line/50 @max-3xs:block">
              {/*
                One line, as wide as the label, never past 40% of the table; a
                longer label is cut and whole in its title.
              */}
              <th
                scope="row"
                className="w-px py-1 pr-3 text-left align-top font-normal text-muted @max-3xs:block @max-3xs:w-auto @max-3xs:pr-0 @max-3xs:pb-0"
              >
                <FactLabel label={field.label} />
              </th>
              {/*
                **The wrap class goes on the `<td>` and nowhere else.**
                `white-space` inherits, so `overflow-wrap` on anything inside this
                cell is inert - it says *where* a line may break and `nowrap` says
                there are none to break.

                `message` is one of the three wrap classes `theme.css` defines.
                `prose` travels in the payload precisely so this is not
                re-decided from the label.
              */}
              <td
                className={`${field.prose ? 'message ' : ''}wrap py-1 align-top @max-3xs:block @max-3xs:pt-0`}
              >
                <FieldValue field={field} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * One fact's label: one line, capped at `40cqi`, cut with an ellipsis past it.
 *
 * **A cut label is a button that opens in place.** Its whole text is in `title`,
 * which a mouse can show and a keyboard or a finger cannot reach; so a label the
 * cap actually cut - measured, `scrollWidth` past `clientWidth`, never guessed
 * from its length - becomes focusable, and pressing it lays the whole text out
 * across the cap's width, breaking anywhere, until it is pressed again. The
 * open label is given exactly `40cqi` (`w-`, not only `max-w-`): a label that
 * may break anywhere has a min-content width of one letter, the property that
 * let the column collapse, so its width is set rather than
 * left to how the table shares out the room.
 *
 * An uncut label is plain text with no tab stop: a table of a dozen facts is not
 * a dozen stops to tab through. While open it is not re-measured, since an open
 * label is never cut and would lose its button, and its focus, the moment it
 * opened. `ResizeObserver` is absent in jsdom, where nothing has a width.
 *
 * **A label that has the focus keeps its button.** Widen
 * the drawer while a cut label is focused, or close an open one once the drawer
 * is wide enough, and the label is no longer cut: dropping `role` and
 * `tabIndex` from the focused span there sent the focus to `<body>`. So it
 * stays a button while it is cut, open or focused, and takes its plain shape
 * on blur - not on the blur of a window switch, after which the focus returns
 * to it. Kept a button only because it is focused, it has nothing to open, and
 * Enter or a click on it does nothing. It names itself in `aria-controls`,
 * since what it expands is its own text.
 */
function FactLabel({ label }: { label: string }): ReactNode {
  const ref = useRef<HTMLSpanElement>(null);
  const id = useId();
  const [cut, setCut] = useState(false);
  const [open, setOpen] = useState(false);
  const [focused, setFocused] = useState(false);

  useEffect(() => {
    const node = ref.current;
    if (node === null || open) return;
    const read = (): void => {
      setCut(node.scrollWidth > node.clientWidth + 1);
    };
    read();
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(read);
    observer?.observe(node);
    return () => {
      observer?.disconnect();
    };
  }, [label, open]);

  const shape = open
    ? 'block w-[40cqi] whitespace-normal wrap-anywhere @max-3xs:w-auto'
    : 'block max-w-[40cqi] truncate @max-3xs:max-w-full';
  if (!cut && !open && !focused) {
    return (
      <span ref={ref} data-fact-label="" title={label} className={shape}>
        {label}
      </span>
    );
  }
  // A label kept a button only because it has the focus has nothing hidden:
  // opening it would only widen its column to the cap.
  //
  // Whether it is cut is measured at the press, not read from `cut`. While a
  // label is open it is not re-measured, so `cut` still holds the width it was
  // opened at; closing it measures again, but in an effect whose answer lands a
  // render later. An Enter between the two, after the drawer widened, opened a
  // label that was no longer cut. The DOM at the press is the closed label at
  // the current width, so it answers now.
  const toggle = (): void => {
    if (open) {
      setOpen(false);
      return;
    }
    const node = ref.current;
    const now = node !== null && node.scrollWidth > node.clientWidth + 1;
    setCut(now);
    if (now) setOpen(true);
  };
  return (
    <span
      ref={ref}
      id={id}
      data-fact-label=""
      data-fact-label-cut=""
      title={label}
      role="button"
      tabIndex={0}
      aria-expanded={open}
      aria-controls={id}
      onFocus={() => {
        setFocused(true);
      }}
      onBlur={(event) => {
        // Switching windows blurs the label but leaves it the active element,
        // and the focus comes back to it: it keeps its tab stop for that.
        if (document.activeElement === event.currentTarget) return;
        setFocused(false);
      }}
      onClick={toggle}
      onKeyDown={(event) => {
        if (event.key !== 'Enter' && event.key !== ' ') return;
        event.preventDefault();
        toggle();
      }}
      className={`${shape} cursor-pointer rounded-sm ${focusRingTight}`}
    >
      {label}
    </span>
  );
}

/**
 * What one fact says.
 *
 * **Nothing here is a Secret's value, and nothing here is a Secret's key.** The
 * server does not list a Secret's keys as facts, and `DetailField` has no flag
 * that marks a held-back value, so this has nothing to draw for it. The keys, and the one control that reveals a value
 * (`GET /api/c/{cluster}/env`, one key at a time), are the Data tab's -
 * `Data.tsx`, where the reveal is wired.
 */
function FieldValue({ field }: { field: DetailField }): ReactNode {
  const ref = field.ref;
  // **Through the two guards, like every other anchor the server writes.** A `Ref` here is a path on this origin in
  // every producer there has been, and an address out of the console is
  // allowed too, in a new tab; anything else - a `javascript:` above all - is
  // drawn as the text it is. It is the guard's job, not the producer's, to keep
  // an unsafe address from becoming a link.
  const inside = internalHref(ref?.href);
  const outside = inside === undefined ? externalHref(ref?.href) : undefined;
  if (ref !== undefined && (inside !== undefined || outside !== undefined)) {
    return (
      <a
        href={inside ?? outside}
        {...(outside === undefined ? {} : { target: '_blank', rel: 'noopener noreferrer' })}
        title={ref.hint ?? undefined}
        className={`rounded-xs text-accent underline-offset-2 hover:underline ${focusRing}`}
      >
        {ref.text}
      </a>
    );
  }
  if (ref?.href !== undefined && ref.href !== '') return <span>{ref.text}</span>;

  if (field.value === '') return <span className="text-muted">&#8212;</span>;

  const value = <Drawn field={field} />;
  const copy = field.copy ?? '';
  if (copy === '') return value;

  // Shown is not the same as obtainable - and per field rather
  // than on every one of them: `copy` is filled where the drawn value is not
  // what somebody needs on the clipboard, and a panel with an icon on every row
  // has taught its reader to stop seeing icons.
  return (
    <span className="flex items-baseline gap-1">
      {value}
      <CopyButton value={copy} label={`Copy this ${field.label.toLowerCase()}`} />
    </span>
  );
}

/**
 * The three renderings a fact has, and they are three rather than two.
 *
 * A **badge**, **colored text**, and **plain text**. Collapsing the first two would make "Running" and a pod's node name look like
 * the same class of thing, which is the distinction a status column exists for.
 *
 * The classes are full strings looked up in a table, never interpolated: Tailwind
 * finds utilities by scanning source text, so `text-${tone}` is a class that
 * exists in the DOM and in no stylesheet - which fails silently and looks exactly
 * like a color that was never chosen.
 */
const TONE: Readonly<Record<string, string>> = {
  normal: '',
  muted: 'text-muted',
  ok: 'text-ok',
  warn: 'text-warn',
  bad: 'text-bad',
};

function Drawn({ field }: { field: DetailField }): ReactNode {
  const tone = field.tone ?? 'normal';
  const hint = field.hint;

  /*
    The badge's shape is not decided here: `components/Pill.tsx` holds the
    console's one status vocabulary, so a pod's `Running` in the drawer is the
    same chip as the `Running` in the list behind it. `data-pill="<tone>"` is
    present on exactly the badges and on nothing else.

    **The three renderings stay three**, and that is the server's call rather than
    this component's: the pill flag asks for a badge, a tone without it asks for
    colored text, and the server sends a warning tone with no pill for a
    sentence precisely so the two do not collapse. A colored sentence is fine;
    it is a graded *reading* that must wear the chip, and the server already
    knows which is which.
  */
  if (field.pill) {
    return (
      <Pill tone={tone} neutral={tone === 'muted' ? 'muted' : 'normal'} title={hint ?? undefined}>
        {field.value}
      </Pill>
    );
  }

  return (
    <span title={hint ?? undefined} className={TONE[tone] ?? ''}>
      {field.value}
    </span>
  );
}

/**
 * The object page's title, declared for the topbar and the window.
 *
 * A component rather than a call in `ObjectDetail`, because only the **page**
 * frame may declare: the drawer is drawn beside a list, and the list is what the
 * topbar names there. A hook cannot be skipped on a condition, so the condition
 * is whether this is mounted.
 *
 * **The kind and the namespace link back to the list they belong to**:
 * the namespace to the list narrowed to it, and the kind
 * to its list **as the menu would open it** - carrying the namespace selection
 * remembered for this cluster (`shell/namespaceMemory.ts`), and only on a
 * namespaced kind, which is the menu's own rule. An object with a namespace is
 * one of a namespaced kind (`address.ts`), so that is the scope
 * read here. The object's own name is where you are and is not a link (the
 * topbar never links the last).
 */
/** The object's list, as the breadcrumb's first segment names it: narrowed to
 *  the namespaces chosen on this cluster, for a namespaced kind. */
function useKindList(identity: ObjectIdentity, namespace: string | undefined): string {
  const carried = useChosenNamespaces(identity.cluster);
  const list = kindListPath(identity.cluster, identity.kind);
  return namespace !== undefined && namespace !== '' ? withNamespaces(list, carried) : list;
}

/** The page's Back: where it was opened from, else the object's list. */
/** Back, on the tab the page shows now. */
function PageBack({
  identity,
  namespace,
  tab,
}: {
  identity: ObjectIdentity;
  namespace: string | undefined;
  tab: string;
}): ReactNode {
  return <BackLink fallback={useKindList(identity, namespace)} tab={tab} />;
}

function DeclarePage({
  identity,
  title,
  namespace,
}: {
  identity: ObjectIdentity;
  title: string;
  namespace: string | undefined;
}): null {
  const namespaced = namespace !== undefined && namespace !== '';
  const list = useKindList(identity, namespace);
  useDeclarePageFacts(
    trail(
      { label: title, to: list },
      namespaced ? { label: namespace, to: kindListPath(identity.cluster, identity.kind, namespace) } : undefined,
      { label: identity.name },
    ),
  );
  return null;
}
