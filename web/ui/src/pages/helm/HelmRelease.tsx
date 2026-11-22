import { useMutation } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { Link, useLocation, useParams } from 'react-router';

import { backHere } from '../../components/back';
import { BackLink } from '../../components/BackLink';
import { PageHeader } from '../../components/PageHeader';
import { btnSecondary } from '../../components/buttons';
import { CloseButton } from '../../components/CloseButton';
import { Icon } from '../../components/Icon';
import { bodyCell, headCell, HEAD_ROW, ROW, TABLE } from '../../components/tableLook';
import { toolbarSquare } from '../../components/toolbar';
import { useUrlList } from '../../components/useUrlList';
import { focusRing } from '../../focus';
import { trail, useDeclarePageFacts } from '../../shell/pageFacts';
import { withNamespaces } from '../../shell/namespaceMemory';
import { kindListPath, OPEN_PARAMETER, openValue } from '../object/address';
import { ValueView } from '../../state/Result';
import type { ValueResult } from '../../api';
import {
  releasePath,
  revealHelmNotes,
  revealHelmValues,
  useHelmRelease,
  type HelmRelease as Release,
  type HelmReleaseDetail,
  type HelmRevision,
} from './data';
import { ReleaseStatus, StatusPill, Updated } from './Parts';

/**
 * One release, at `/c/:cluster/helm/:namespace/:name`.
 *
 * **The half a user opens a release for is the history**: the payload carries
 * every revision Helm kept (`HelmRevision`). A release page showing its values
 * and its manifest and no revisions is the list row again with more text.
 *
 * ## Two facts that usually coincide
 *
 * `current` is the highest revision Helm kept; `deployed` is the one Helm marks
 * as running. They are the same on an ordinary release and **different exactly
 * when it matters** - a failed upgrade - which is why the mark is a pill on the
 * row rather than an ordering the reader has to infer. The server sends one
 * field with three values so that no client combines two booleans and gets it
 * wrong in the second place.
 *
 * ## The page and the drawer are one panel
 *
 * A row of the releases list opens this panel in the drawer, as every resource
 * list does with `ObjectDetail`; the drawer's "Open as page" control is a link
 * to this route, which draws the same panel full size. `chrome` is the one
 * difference, as it is for an object: the drawer's panel carries open-as-page
 * and close, the page carries Back - to the list it was opened from, with the
 * release open in its drawer again, or to the releases list when the page was
 * reached directly.
 *
 * ## The way back keeps the selection
 *
 * Returning a user to forty rows after they narrowed to one namespace is
 * the same defect as forgetting a filter, so the selection travels in the link
 * that got here and travels back out.
 */
export function HelmReleasePage(): ReactNode {
  const { cluster, namespace, name } = useParams<{
    cluster: string;
    namespace: string;
    name: string;
  }>();
  // Every value of the repeated parameter the list writes, and the
  // comma-joined form of an older link alike (`useUrlList`).
  const [selection] = useUrlList('namespace');
  // The tab title and the breadcrumb's last segment. A page that declares
  // nothing leaves the window titled "KubeTower" alone, which is a taskbar of
  // identical entries.
  // The way back keeps the list's selection (below), and so does the
  // breadcrumb's "Helm releases": the same address as Back's, when the page
  // was not opened from the drawer.
  const back = helmListPath(cluster ?? '', selection);
  // Each segment that names a place links to it: the
  // releases as the list was left, and the releases narrowed to this one's
  // namespace.
  useDeclarePageFacts(
    trail(
      { label: 'Helm releases', to: back },
      namespace === undefined ? undefined : { label: namespace, to: kindListPath(cluster ?? '', 'helm', namespace) },
      name === undefined ? undefined : { label: name },
    ),
  );

  return <ReleasePanel cluster={cluster ?? ''} namespace={namespace ?? ''} name={name ?? ''} chrome="page" />;
}

/** An address with `open` set to one release, the rest of its query kept. */
function withOpen(pathname: string, search: string, open: string): string {
  const query = new URLSearchParams(search);
  query.set(OPEN_PARAMETER, open);
  return `${pathname}?${query.toString()}`;
}

/** The releases list under a selection, in the list's own spelling. */
function helmListPath(cluster: string, selection: readonly string[]): string {
  return withNamespaces(kindListPath(cluster, 'helm'), selection);
}

export interface ReleasePanelProps {
  cluster: string;
  namespace: string;
  name: string;
  chrome: 'drawer' | 'page';
  /** The drawer's close control. */
  onClose?: (() => void) | undefined;
}

/**
 * One release's panel, in the drawer or as the page. The header is
 * `ObjectDetail`'s: the kind as an eyebrow, the name, the namespace, then the
 * chrome's controls in the same boxes (`components/toolbar.ts`).
 */
export function ReleasePanel({
  cluster,
  namespace,
  name,
  chrome,
  onClose,
}: ReleasePanelProps): ReactNode {
  const release = useHelmRelease(cluster, namespace, name);
  const location = useLocation();
  // Read from the address as it is now, not handed in when the drawer
  // opened: the namespace menu may change under an open drawer, and its
  // open-as-page link follows the list as it is.
  const [selection] = useUrlList('namespace');
  // One level under the page's `h1` in the drawer, whose page already has one.
  const Heading = chrome === 'page' ? 'h1' : 'h2';
  const level = chrome === 'page' ? 2 : 3;

  return (
    <section
      className="flex h-full min-h-0 flex-col"
      aria-label={`Helm release ${name}`}
      data-object-chrome={chrome}
      data-helm-release={`${namespace}/${name}`}
    >
      <PageHeader
        back={chrome === 'page' ? <BackLink fallback={helmListPath(cluster, selection)} /> : undefined}
        kind="Helm release"
        name={name === '' ? 'Release' : name}
        namespace={namespace}
        heading={Heading}
        actions={
          <>
            {chrome === 'drawer' ? (
              // A plain link, so a middle-click opens the page in a tab of its own.
              // It carries the list it was followed from, naming this release, for
              // the page's Back to open it there again.
              <Link
                to={releasePath(cluster, namespace, name, selection)}
                state={backHere(withOpen(location.pathname, location.search, openValue({ namespace, name })))}
                aria-label="Open as page"
                title="Open as page"
                data-open-as-page=""
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

      <div className="min-h-0 flex-1 overflow-auto p-3">
        <ValueView query={release} what="this release">
          {(detail) => <Detail detail={detail} cluster={cluster} namespace={namespace} name={name} level={level} />}
        </ValueView>
      </div>
    </section>
  );
}

function Detail({
  detail,
  cluster,
  namespace,
  name,
  level,
}: {
  detail: HelmReleaseDetail;
  cluster: string;
  namespace: string;
  name: string;
  /** The sections' heading level: under the page's `h1`, or the drawer's `h2`. */
  level: 2 | 3;
}): ReactNode {
  const revisions = detail.revisions ?? [];
  const Section = level === 2 ? 'h2' : 'h3';

  return (
    <div className="flex flex-col gap-4">
      <Facts release={detail.release} />

      <section className="rounded-lg border border-line bg-panel">
        <div className="flex flex-wrap items-baseline gap-2 border-b border-line px-3 py-2">
          <Section className="text-base font-medium">Revisions</Section>
          {/* How many are kept, which is `--history-max` made visible and the
              answer to "how far back can I look" - rather than a reader
              inferring it from a list that stops. */}
          {detail.historyMax === undefined ? null : (
            <span className="text-xs text-muted">
              {detail.historyMax} kept · newest first
            </span>
          )}
        </div>

        {revisions.length === 0 ? (
          // Not an empty table: no revision kept is a real state, apart from a
          // page that failed to read them.
          <p data-empty="revisions" className="p-3 text-sm text-muted">
            No revision kept.
          </p>
        ) : (
          <Revisions revisions={revisions} />
        )}
      </section>

      {/* **Always drawn, both of them.** A panel that is missing makes the page look
          like a console that does not do notes or values, and says nothing about
          whether anyone looked; an empty one says "there are none" whether or not
          that was established.

          **Both behind a reveal**: values routinely carry passwords, and charts print
          generated ones in their notes. Keyed by the release and its revision:
          a page refetched onto a newer revision, or a navigation to another
          release at the same revision, drops what was revealed rather than
          showing it under other facts. */}
      <Revealed
        key={`notes:${cluster}/${namespace}/${name}@${String(detail.release.revision)}`}
        part="notes"
        level={level}
        title="Notes"
        present={detail.notesRendered === true}
        unread={detail.payloadNote}
        revision={detail.release.revision}
        reveal={(revision) => revealHelmNotes(cluster, namespace, name, revision).then((a) => textOf(a, (d) => d.notes))}
        none="No notes rendered."
        emptyOnRead={(revision) => <>Revision {revision} carries no notes.</>}
      />
      <Revealed
        key={`values:${cluster}/${namespace}/${name}@${String(detail.release.revision)}`}
        part="values"
        level={level}
        title="Values"
        present={detail.valuesSupplied === true}
        unread={detail.payloadNote}
        revision={detail.release.revision}
        reveal={(revision) => revealHelmValues(cluster, namespace, name, revision).then((a) => textOf(a, (d) => d.values))}
        none="No values supplied."
        emptyOnRead={(revision) => <>Revision {revision} carries no values.</>}
      />
    </div>
  );
}

/**
 * The facts table.
 *
 * The deployed-revision line sits **immediately under
 * the status it contradicts** rather than at the bottom with the rest: a status
 * of `failed` with the correction three rows away is a status of `failed`.
 */
function Facts({ release }: { release: Release }): ReactNode {
  const rows: { key: string; label: string; value: ReactNode; prose?: boolean }[] = [
    { key: 'status', label: 'Status', value: <ReleaseStatus release={release} /> },
    { key: 'revision', label: 'Revision', value: release.revision ?? '—' },
    { key: 'namespace', label: 'Namespace', value: release.namespace },
    {
      key: 'chart',
      label: 'Chart',
      value: (
        <>
          {release.chart ?? '—'}
          {release.deprecated === true ? (
            <span
              title="Deprecated in its Chart.yaml"
              className="ml-1 rounded-sm bg-warn/15 px-1.5 py-0.5 text-2xs text-warn"
            >
              deprecated
            </span>
          ) : null}
        </>
      ),
    },
    { key: 'chartVersion', label: 'Chart version', value: release.chartVersion ?? '—' },
    { key: 'appVersion', label: 'App version', value: release.appVersion ?? '—' },
    { key: 'updated', label: 'Last deployed', value: <Updated at={release.updatedAt} age={release.age} /> },
    // Prose, and on a failed upgrade it is the whole refusal.
    { key: 'description', label: 'Description', value: release.description ?? '—', prose: true },
  ];

  return (
    <div className="table-scroll min-w-0 overflow-x-auto rounded-lg border border-line bg-panel">
      <table className={`${TABLE} w-full`} aria-label="What this release is">
        <tbody>
          {rows.map((row) => (
            <tr key={row.key} data-fact={row.key} className={ROW}>
              <th scope="row" className={`${bodyCell()} w-40 text-left font-medium text-muted`}>
                {row.label}
              </th>
              {/* `.prose` goes on the **cell**, because `white-space` inherits
                  and an `overflow-wrap` on anything inside it is inert. It is a
                  property of the value and never of the label. */}
              <td className={`${bodyCell()}${row.prose === true ? ' prose' : ''}`}>{row.value}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Revisions({ revisions }: { revisions: readonly HelmRevision[] }): ReactNode {
  return (
    <div className="table-scroll min-w-0 overflow-x-auto">
      <table className={`${TABLE} w-full`} aria-label="Every revision Helm kept">
        <thead>
          <tr className={HEAD_ROW}>
            <th scope="col" className={`${headCell()} text-right`}>
              Revision
            </th>
            <th scope="col" className={headCell()}>
              Status
            </th>
            <th scope="col" className={headCell()}>
              Updated
            </th>
            <th scope="col" className={headCell()}>
              Chart version
            </th>
            <th scope="col" className={headCell()}>
              App version
            </th>
            <th scope="col" className={headCell()}>
              Description
            </th>
          </tr>
        </thead>
        <tbody>
          {revisions.map((revision) => (
            <tr
              key={revision.revision}
              data-revision={revision.revision}
              data-mark={revision.mark}
              className={ROW}
            >
              <td className={`${bodyCell()} text-right tabular-nums`}>
                {revision.revision}
                {/* Drawn as a pill because the running revision has to be
                    findable at a glance in a list of ten rows all saying
                    "Upgrade complete". */}
                {revision.mark === undefined ? null : (
                  <span className="ml-1 rounded-sm bg-accent/15 px-1.5 py-0.5 text-2xs text-accent">
                    {MARK[revision.mark] ?? revision.mark}
                  </span>
                )}
              </td>
              <td className={bodyCell()}>
                <StatusPill status={revision.status ?? 'unknown'} tone={revision.tone} />
              </td>
              <td className={`${bodyCell()} text-muted`}>
                <Updated at={revision.updatedAt} />
              </td>
              <td className={bodyCell()}>{revision.chartVersion ?? '—'}</td>
              <td className={bodyCell()}>{revision.appVersion ?? '—'}</td>
              {/* Helm's own sentence, and - where a revision's Secret would not
                  decode - the reason the two version cells beside it are em
                  dashes. A row renumbered around the gap would be a history
                  nobody could check against `helm history`. */}
              <td className={`${bodyCell()} prose`}>
                {revision.description ?? '—'}
                {revision.note === undefined ? null : (
                  <span className="mt-0.5 block text-xs text-muted">{revision.note}</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The three marks, spelled for a reader. `current-and-deployed` is the ordinary
 *  case and says both, because a pill saying only "current" on the row that is
 *  also running would leave the reader looking for the deployed one. */
const MARK: Readonly<Record<string, string>> = {
  current: 'current',
  deployed: 'running',
  'current-and-deployed': 'current · running',
};

/**
 * The answer of a reveal, as the text it carries, or the server's own words
 * for why not - thrown, so the panel draws a refusal the one way it draws a
 * network failure.
 */
function textOf<T>(answer: ValueResult<T>, take: (data: T) => string): string {
  if (answer.state === 'ok') return take(answer.data);
  throw new Error(answer.reason.message);
}

/**
 * A panel **behind a reveal**: the values and the notes.
 *
 * Supplied values routinely carry passwords and tokens, and charts print
 * generated ones in their notes, so the page does not receive either on every
 * view. The release's answer says only whether there are any; the text comes from the reveal on
 * a press, which the server reviews and records - who, which release, which
 * revision, which part - and it lives in this component's state until Hide, a
 * navigation or a newer revision.
 *
 * Four answers and never fewer: unread (`payloadNote`), none, present and not
 * shown, and shown. A refusal is said in the panel, in the server's own words,
 * and the control stays offered: a person who may not read the release's
 * Secrets is told so rather than shown an empty box.
 *
 * `<pre>` and never `dangerouslySetInnerHTML`: this is a chart author's output
 * and it reaches the browser as text.
 */
function Revealed({
  part,
  level,
  title,
  present,
  unread,
  revision,
  reveal,
  none,
  emptyOnRead,
}: {
  part: 'values' | 'notes';
  level: 2 | 3;
  title: string;
  present: boolean;
  unread: string | undefined;
  revision: number | undefined;
  reveal: (revision: number) => Promise<string>;
  none: ReactNode;
  emptyOnRead: (revision: number | undefined) => ReactNode;
}): ReactNode {
  const Section = level === 2 ? 'h2' : 'h3';
  const [shown, setShown] = useState<string | null>(null);
  const [refused, setRefused] = useState<string | null>(null);
  const asked = useMutation({
    mutationFn: async () => {
      // The record names the revision before the read, so a release with no
      // revision number has nothing to reveal against.
      if (revision === undefined) throw new Error('this release carries no revision number to reveal against');
      return reveal(revision);
    },
    onSuccess: (text) => {
      setShown(text);
      setRefused(null);
    },
    onError: (error: Error) => setRefused(error.message),
  });
  // The two data attributes per part are what the tests find each control by.
  const revealAttr = { [`data-reveal-${part}`]: true };
  const hideAttr = { [`data-hide-${part}`]: true };

  let body: ReactNode;
  if (unread !== undefined) {
    body = <p className="prose p-3 text-sm text-muted">{unread}</p>;
  } else if (!present) {
    body = <p className="prose p-3 text-sm text-muted">{none}</p>;
  } else if (shown !== null) {
    body =
      shown === '' ? (
        <p className="prose p-3 text-sm text-muted">{emptyOnRead(revision)}</p>
      ) : (
        <pre data-revealed={part} className="overflow-x-auto p-3 font-mono text-xs whitespace-pre-wrap">
          {shown}
        </pre>
      );
  } else {
    body = (
      <div className="flex flex-col gap-2 p-3 text-sm">
        {refused === null ? null : (
          <p role="alert" className="text-bad">
            {refused}
          </p>
        )}
        <div className="flex flex-wrap items-center gap-2">
          {/* The record is said on the control rather than in a sentence over
              it: pressing it is what is recorded. */}
          <button
            type="button"
            {...revealAttr}
            disabled={asked.isPending}
            onClick={() => {
              asked.mutate();
            }}
            title="Revealing is recorded under your name"
            className={btnSecondary}
          >
            {asked.isPending ? 'Revealing…' : `Reveal ${part}`}
          </button>
          {revision === undefined ? null : (
            <span className="text-xs text-muted">revision {revision}</span>
          )}
        </div>
      </div>
    );
  }

  return (
    <section className="rounded-lg border border-line bg-panel">
      <div className="flex flex-wrap items-baseline gap-2 border-b border-line px-3 py-2">
        <Section className="flex-1 text-base font-medium">{title}</Section>
        {shown === null ? null : (
          // The way back: hiding asks nothing - the record was written on the
          // reveal - and the control above is offered again, a new reveal.
          <button
            type="button"
            {...hideAttr}
            onClick={() => {
              setShown(null);
            }}
            className={`rounded-sm border border-line px-1 text-xs text-muted ${focusRing}`}
          >
            Hide
          </button>
        )}
      </div>
      {body}
    </section>
  );
}
