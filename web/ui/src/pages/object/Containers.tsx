import { useMutation } from '@tanstack/react-query';
import { Fragment, useId, useState, type ReactNode } from 'react';
import { Link, useInRouterContext } from 'react-router';

import {
  resourceOfSource,
  revealEnvValue,
  type EnvImport,
  type EnvSource,
  type EnvVar,
  type PodContainer,
  type ReasonCode,
} from '../../api';
import { btnSecondary } from '../../components/buttons';
import { focusRing } from '../../focus';
import { Icon } from '../../components/Icon';
import { relatedPath } from './address';
import { Command } from './Command';
import {
  commandSummary,
  probesSummary,
  ProbeChip,
  resourcesSummary,
  ResourceChips,
  Section,
  StatusLines,
  StatusPill,
} from './TileParts';
import { envFoldKey, useEnvFold } from './envFolds';
import { TABLE } from '../../components/tableLook';
import { ImageLine } from '../../components/ImageRef';

/**
 * What a pod is made of.
 *
 * What is drawn here is otherwise a hundred lines of YAML past `managedFields`,
 * which is the thing the panel exists to save.
 *
 * ## Init containers first, and marked
 *
 * The server sends them first because that is the order they run in. The mark is
 * not decoration: **an init container that will not start is the reason the pod
 * is stuck, and it looks identical to a crashing app container** if nothing
 * distinguishes them.
 *
 * ## "BestEffort" is written out, because a missing row reads as a panel that
 * did not look
 *
 * A container with neither requests nor limits is **BestEffort**, which decides
 * what the scheduler does with it under pressure and is exactly what nobody
 * scrolls a manifest for. Drawing nothing there says the panel has no opinion;
 * the chip says the pod has none.
 *
 * ## A ConfigMap's value is on the row; a Secret's is asked for
 *
 * The design is the pair rather than either half: a control that has to be
 * pressed to see a log level is one that gets pressed without reading the row,
 * and with none behind a control a production credential is in the DOM of a
 * window that gets screenshotted. So a ConfigMap value arrives resolved and is
 * drawn; a Secret value is **not in the payload at all** and is fetched one key
 * at a time through `GET /api/c/{cluster}/env`.
 *
 * **There is no control here that opens more than one.** Not a "reveal all", not
 * a per-container one. The gesture is per row because the exposure is per row.
 *
 * And the way back exists: *Hide* beside a revealed value clears
 * it and asks nothing - the audit line was written on the reveal and that is
 * the record. A revealed value is also kept with the address it answered, so a
 * drawer re-targeted at another pod never draws the previous one's credential
 * under a row of the same name.
 *
 * ## Whether the containers are healthy, which is the first question asked
 *
 * Everything else on this panel is what the container *was told*; the status
 * pill and the probe chips are how it is *doing*, which would otherwise have to
 * be read by hand from the raw manifest and the raw status.
 *
 * **Three different answers, and a blank cell is all three at once.** Each has
 * its own value in an enumeration rather than its own shade of empty, and the
 * drawing keeps them apart:
 *
 * | Answer | What it is | Drawn |
 * |---|---|---|
 * | probe `absent` | the container declares no such probe | `none`, muted, on a dashed chip |
 * | probe `unreadable` | something *is* declared and this console cannot say what | the reason, in `text-warn` |
 * | status `unreported` | the kubelet has said nothing about this container | `Unreported`, muted, the reason as its title, and **never a "not ready"** |
 *
 * `absent` is stated rather than colored, deliberately and against the first
 * reading of "no readiness probe is a finding". A color spent on the normal
 * case is a color the user stops seeing, and **most containers declare no
 * readiness probe** - so a warning tone there would light up most panels and
 * mean nothing by the second pod. `none` is written where an empty cell would
 * be; `text-warn` stays for the console failing to read something.
 *
 * The startup probe is a chip of its own beside the other two, never folded into
 * them: **it is the one that explains a container that never gets as far as
 * failing liveness**, because while it runs the kubelet holds the other two off.
 *
 * ## Four absences, and only three are a fault
 *
 * The finest-grained instance of the empty-versus-failed rule on this surface.
 * Drawing every absence with a warning pill reading "not read" would show a
 * healthy Argo CD server pod **thirty of them** - `optional: true` references to
 * keys nobody sets, which is that component working as shipped.
 *
 * | Absence | What it is | Drawn |
 * |---|---|---|
 * | `unread` | nobody looked - past the budget, refused, or not there on a required reference | warning, **and the only one that keeps a control**, because asking again may answer differently |
 * | `unset` | read, no such key, reference not optional | warning: the kubelet will not start the container until it exists |
 * | `expected` | reference optional, and the key is not in the object **or the object does not exist** | plain: the pod working as written |
 * | `unprintable` | read and not text | plain: nothing is wrong, this panel cannot draw a DER certificate |
 *
 * ## A long list is folded behind its count
 *
 * Listing every variable open would let a pod with dozens of them push the rest
 * of the panel - the next container, the ports, the addresses - off the screen.
 * So each of a
 * container's three lists (its own `Environment`, the `Imported from` objects
 * and the `Imported` variables they contributed) is **folded once it holds more
 * than `ENV_FOLD_AFTER` rows**, behind a header that says how many there are.
 * A short list stays open: folding three rows hides more than it saves.
 *
 * What the user opens stays open while they move between the object's tabs,
 * which unmount this panel; `envFolds.ts` holds the memory and says why it is a
 * module. And a fold never hides a fault: the header of a folded list says how
 * many of its rows carry a warning, because a required key that is missing is
 * why the container will not start and must stay visible.
 */
export function Containers({
  cluster,
  kind,
  namespace,
  name,
  containers,
  note,
  chrome = 'page',
}: {
  cluster: string;
  /** The object's kind and name, which with the cluster and the namespace are
   *  the address a fold is remembered under (`envFolds.ts`): a drawer moved to
   *  another pod must not open that pod's lists because this one's were. */
  kind: string;
  /** The object's namespace. The source of a variable lives beside the workload
   *  that names it, and `EnvSource` carries no namespace of its own. */
  namespace: string | undefined;
  name: string;
  containers: readonly PodContainer[];
  /** Which `envFrom` imports were not read, and why. Expanding one costs a round
   *  trip on the panel that opens by default, so the number is capped - and a
   *  capped list that does not admit it is a lie the user acts on. */
  note?: string | undefined;
  /** Where a link to a ConfigMap or a Secret leads: its list with it opened in a
   *  drawer, its own page on a page. */
  chrome?: 'drawer' | 'page';
}): ReactNode {
  if (containers.length === 0) return null;

  const object = { cluster, kind, namespace, name };

  return (
    <section data-section="containers">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Containers</h3>
      <div className="flex flex-col gap-3">
        {containers.map((container) => (
          <One
            key={`${container.init ? 'init' : 'app'}-${container.name}`}
            object={object}
            container={container}
            chrome={chrome}
          />
        ))}
      </div>
      {note === undefined || note === '' ? null : (
        <p data-env-note className="mt-2 text-xs text-muted">
          {note}
        </p>
      )}
    </section>
  );
}

/** The object a container belongs to, as a fold's memory addresses it. */
interface Owner {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  name: string;
}

function One({
  object,
  container,
  chrome,
}: {
  object: Owner;
  container: PodContainer;
  chrome: 'drawer' | 'page';
}): ReactNode {
  const { cluster, namespace } = object;
  const requests = container.requests ?? [];
  const limits = container.limits ?? [];
  const env = container.env ?? [];
  const imported = container.imported ?? [];
  const imports = container.envFrom ?? [];
  const probes = container.probes ?? [];

  const lists = (
    <>
      {env.length === 0 ? null : (
        <Env
          heading="Environment"
          cluster={cluster}
          namespace={namespace}
          chrome={chrome}
          container={container.name}
          rows={env}
          memory={envFoldKey(object, container, 'environment')}
        />
      )}
      {imports.length === 0 ? null : (
        <Imports
          imports={imports}
          cluster={cluster}
          namespace={namespace}
          chrome={chrome}
          memory={envFoldKey(object, container, 'imports')}
        />
      )}
      {imported.length === 0 ? null : (
        <Env
          heading="Imported"
          cluster={cluster}
          namespace={namespace}
          chrome={chrome}
          container={container.name}
          rows={imported}
          memory={envFoldKey(object, container, 'imported')}
        />
      )}
    </>
  );

  const name = (
    <>
      {container.init ? (
        <span data-init-mark className="rounded-sm border border-warn/40 px-1 text-2xs text-warn uppercase">
          init
        </span>
      ) : null}
      <span className="min-w-0 font-medium wrap-anywhere">{container.name}</span>
    </>
  );
  const pull =
    container.pullPolicy === undefined || container.pullPolicy === '' ? null : (
      <span data-pull-policy className="shrink-0 text-xs text-muted">
        pull {container.pullPolicy}
      </span>
    );

  // The tile: one header line ending in the status pill, then Command, Probes and
  // Resources as sections that fold, each showing its content in short when
  // closed. Command and Resources open by default, Probes closed - the pill
  // already says whether the container is ready. The environment lists keep
  // their own fold, which hides a revealed Secret with its rows.
  return (
    <article
      data-container={container.name}
      data-init={container.init ? 'true' : 'false'}
      className="rounded-md border border-line p-2"
    >
      {/* One line where it fits. Where it does not, the pill goes to a line of
          its own rather than past the panel's edge: it holds the restarts, and
          no width of name or font may cut them or widen the drawer. The image
          has the line under it (#136), so its repository is never squeezed
          out by the name and the pill. */}
      <header className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        {name}
        <span className="ml-auto flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          {pull}
          <StatusPill status={container.status} />
        </span>
      </header>
      <ImageLine image={container.image} />
      <StatusLines status={container.status} />

      <Section
        heading="Command"
        memory={envFoldKey(object, container, 'command')}
        defaultOpen
        summary={commandSummary(container)}
      >
        <Command container={container} memory={envFoldKey(object, container, 'script')} />
      </Section>

      {probes.length === 0 ? null : (
        <Section
          heading="Probes"
          memory={envFoldKey(object, container, 'probes')}
          defaultOpen={false}
          summary={probesSummary(probes)}
          faults={probes.some((probe) => probe.state === 'unreadable') ? ['unreadable'] : []}
        >
          <div className="flex flex-col items-start gap-1">
            {probes.map((probe) => (
              <ProbeChip key={probe.kind} probe={probe} />
            ))}
          </div>
        </Section>
      )}

      <Section
        heading="Resources"
        memory={envFoldKey(object, container, 'resources')}
        defaultOpen
        summary={resourcesSummary(requests, limits)}
      >
        <div className="flex flex-wrap gap-1">
          <ResourceChips requests={requests} limits={limits} />
        </div>
      </Section>

      {lists}
    </article>
  );
}

/**
 * The whole-object imports, and what came of resolving each.
 *
 * Each import names its ConfigMap or Secret as a link to that object, whether
 * or not it exists, with the prefix it is imported under and how many keys it
 * holds. `keys` and `problem` are drawn apart because **different answers look
 * identical on a panel that only lists rows**: an object with no keys, an
 * object this console may not read, and an object nobody looked at. An import
 * the server marks `expected` (an object that is not there) is drawn as the
 * link alone, with no sentence about it; every other `problem` keeps its tone.
 */
function Imports({
  imports,
  cluster,
  namespace,
  chrome,
  memory,
}: {
  imports: readonly EnvImport[];
  cluster: string;
  namespace: string | undefined;
  chrome: 'drawer' | 'page';
  /** Where this list's fold is remembered (`envFolds.ts`). */
  memory: string;
}): ReactNode {
  // The imports drawn in the warning tone below: every `problem` but the
  // object that is simply not there.
  const unread = imports.filter(
    (entry) => entry.problem !== undefined && entry.problem !== '' && entry.absence !== 'expected',
  ).length;
  return (
    <div data-imports className="mt-2">
      <Fold
        heading="Imported from"
        count={imports.length}
        faults={unread === 0 ? [] : [`${String(unread)} not read`]}
        memory={memory}
      >
        <ul className="mt-0.5 flex flex-col gap-0.5 text-sm">
          {imports.map((entry) => (
            <li key={`${entry.kind}-${entry.name}-${entry.prefix ?? ''}`} className="flex gap-2">
              <span className="font-mono">
                <ObjectLink
                  cluster={cluster}
                  kind={entry.kind}
                  namespace={namespace}
                  name={entry.name}
                  chrome={chrome}
                >
                  {entry.kind}/{entry.name}
                </ObjectLink>
                {entry.prefix === undefined || entry.prefix === '' ? null : (
                  <span className="text-muted"> as {entry.prefix}*</span>
                )}
              </span>
              {entry.problem === undefined || entry.problem === '' ? (
                <span data-import-keys className="text-muted">
                  {String(entry.keys ?? 0)} keys
                </span>
              ) : entry.absence === 'expected' ? null : (
                <span data-import-problem data-absence={entry.absence ?? ''} className="text-warn">
                  {entry.problem}
                </span>
              )}
            </li>
          ))}
        </ul>
      </Fold>
    </div>
  );
}

/**
 * How many rows a list may hold and still be drawn open; past it the list is
 * folded behind its count.
 *
 * Eight, and a list of eight is still open. The bound is
 * about what the fold costs as much as what it saves: a folded list is a click
 * before any row can be read, and a container with a handful of variables -
 * which is most of them - should not pay that click to save four lines. Past
 * eight, the list is taller than the card's own facts above it, and that is the
 * point where the variables start to be what the panel looks like.
 */
export const ENV_FOLD_AFTER = 8;

/**
 * A list's heading, and below it the list - folded behind the heading when it
 * holds more than `ENV_FOLD_AFTER` rows.
 *
 * ## A button and a conditional render, not `<details>`
 *
 * The console's other disclosures are `<details>` (`Annotations`, the menu's
 * groups, `FoldedMessage`), and the reason this one is not is the row it may
 * hold: a revealed Secret value. A closed `<details>` keeps its content in the
 * page, and Chromium's find-in-page searches inside it and **opens it on a
 * match** - so a credential revealed and then folded away would still be in the
 * DOM of a window that gets screenshotted, one Ctrl+F from being drawn again.
 * Folded here, the rows are not rendered at all, and closing the list takes a
 * revealed value off the page with it; opening it again draws the control, and
 * pressing it is a new reveal, exactly as *Hide* has it. The price is that
 * find-in-page does not reach a folded list's names, and the header's count is
 * what says there is something to open.
 *
 * So the button carries what `<details>` would have given for free: it is a real
 * `<button>`, Enter and Space work on it, and `aria-expanded` with
 * `aria-controls` says what it opens and whether it is open.
 *
 * ## A short list draws the heading it always had
 *
 * Not a disabled button and not a count: a list of three is read at a glance,
 * and a control that does nothing is a control people press to find out.
 *
 * ## The faults are on the header
 *
 * `faults` is what the folded rows would have drawn in the warning tone - a
 * required key that is missing, an object nobody could read - said in a few
 * words beside the count while the list is closed. Folding exists to put the
 * ordinary rows away, and a missing required key is why the container will not
 * start, so it stays visible.
 */
function Fold({
  heading,
  count,
  faults,
  memory,
  children,
}: {
  heading: string;
  count: number;
  /** What the rows would draw in the warning tone, counted: `2 not read`. */
  faults: readonly string[];
  /** Where the open state is remembered (`envFolds.ts`). */
  memory: string;
  children: ReactNode;
}): ReactNode {
  const [open, remember] = useEnvFold(memory);
  const body = useId();

  if (count <= ENV_FOLD_AFTER) {
    return (
      <>
        <p className="text-2xs text-muted uppercase">{heading}</p>
        {children}
      </>
    );
  }

  return (
    <>
      {/* `items-center` and not `items-baseline`: the button's first box is the
          chevron, an SVG whose baseline is its bottom edge, so baselines put
          the warning a few pixels under the heading beside it. */}
      <div className="flex flex-wrap items-center gap-x-2">
        <button
          type="button"
          data-fold-control
          aria-expanded={open}
          aria-controls={body}
          onClick={() => {
            remember(!open);
          }}
          className={`inline-flex cursor-pointer items-center gap-1 rounded-sm text-2xs text-muted uppercase hover:text-fg ${focusRing}`}
        >
          {/* Rotated rather than swapped, as on the menu's groups: one shape
              turning is what the eye follows. */}
          <Icon name="chevron" className={`size-3 transition-transform ${open ? 'rotate-90' : ''}`} />
          {/* One box for the words, so the space before the count is text and
              not the flex gap: the button's name reads `Environment · 34`. */}
          <span>
            {heading}{' '}
            <span data-fold-count className="tabular-nums">
              · {count}
            </span>
          </span>
        </button>
        {open || faults.length === 0 ? null : (
          <span data-fold-faults className="text-2xs text-warn">
            {faults.join(' · ')}
          </span>
        )}
      </div>
      {/* The id is on a box that is always there, so `aria-controls` never
          names an element the page does not hold; the rows inside it are
          rendered only while it is open. */}
      <div id={body} data-fold-body>
        {open ? children : null}
      </div>
    </>
  );
}

function Env({
  heading,
  cluster,
  namespace,
  chrome,
  container,
  rows,
  memory,
}: {
  heading: string;
  cluster: string;
  namespace: string | undefined;
  chrome: 'drawer' | 'page';
  container: string;
  rows: readonly EnvVar[];
  /** Where this list's fold is remembered (`envFolds.ts`). */
  memory: string;
}): ReactNode {
  // The absence drawn in the warning tone (`ABSENCE_TONE`), which a closed fold
  // says on its header rather than hiding. A reference whose key or object is
  // not there is drawn as a reference and nothing more, so it is no fault here.
  const unread = rows.filter((row) => row.absence === 'unread').length;
  const faults = unread === 0 ? [] : [`${String(unread)} not read`];
  return (
    <div data-env={heading.toLowerCase()} className="mt-2">
      <Fold heading={heading} count={rows.length} faults={faults} memory={memory}>
        {/* `kt-grid` and not `grid`: Tailwind's `.grid` is `display: grid`, which
            would take this out of table layout and leave the `wrap` rule below
            with nothing to act on. `theme.css` names the drawer's tables as one
            of the places that rule applies. */}
        <table className={`${TABLE} w-full`}>
          <tbody>
            {rows.map((row, at) => (
              <tr key={`${row.name}-${String(at)}`}>
                {/* `wrap` on the name cell too, so `w-48` is a bound rather than a
                    hope. Argo CD's application controller has variables 40 to 56
                    characters long, and `break-all` here would not work: `.kt-grid
                    th` is `white-space: nowrap`, `nowrap` leaves no line to break,
                    and `word-break` on a cell that may not wrap is inert. Each
                    name would be drawn on one line and its column would grow to
                    the longest of them, squeezing the value column into many
                    lines a row.

                    With `wrap` the cell may break, so its narrowest is one
                    character and the 192px `w-48` asks for is what the table gives
                    it. A name breaks first, inside that width: two lines for 47
                    characters. The value column keeps the rest, whatever the
                    names are. `web/measure/env.spec.ts` holds the numbers,
                    because jsdom lays out nothing and the unit tests cannot see
                    this. */}
                <th
                  scope="row"
                  className="wrap w-48 py-0.5 pr-2 text-left align-top font-mono font-normal"
                >
                  {atTheSeparators(row.name)}
                </th>
                {/* `wrap` on the cell: a value is one unbreakable token that may be
                    broken anywhere, which is the third of theme.css's three names
                    and the right one here. On the cell, never on a descendant. */}
                <td className="wrap py-0.5 align-top">
                  <Value
                    cluster={cluster}
                    namespace={namespace}
                    container={container}
                    row={row}
                  />
                  {/* Beside the value rather than inside it: the branch that swaps
                      a control for a credential must not be able to take the
                      source away, and that is the moment the user most wants
                      to know which object they are looking at the inside of. */}
                  {row.source === undefined ? null : (
                    <Source
                      source={row.source}
                      cluster={cluster}
                      namespace={namespace}
                      chrome={chrome}
                    />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Fold>
    </div>
  );
}

/**
 * Which object a value is read out of, and under which key there.
 *
 * `EnvSource` carries `kind`, `name` and `key`. A reader of `DB_PASSWORD` has to
 * be able to tell a literal from a Secret key from a ConfigMap key, and to go
 * and change it: the object is a link to its own page, whether or not it
 * exists, and nothing here says whether it does.
 *
 * The object is named on every row, as a link, and so is the key: a reader
 * going to edit the object should not have to look upward for its name.
 */
function Source({
  source,
  cluster,
  namespace,
  chrome,
}: {
  source: EnvSource;
  cluster: string;
  namespace: string | undefined;
  chrome: 'drawer' | 'page';
}): ReactNode {
  const object = `${source.kind}/${source.name}`;
  return (
    <span
      data-source={`${object}:${source.key}`}
      // The whole address on every row, elided or not, so a hover answers what
      // the run costs a glance upward.
      title={`${object}:${source.key}`}
      className="mt-0.5 flex flex-wrap items-baseline gap-x-2 text-sm text-muted"
    >
      {/* One token and not two: the object and the key are read together, so
          the flex gap above must not land between them. */}
      <span className="font-mono">
        <span data-source-object>
          <ObjectLink
            cluster={cluster}
            kind={source.kind}
            namespace={namespace}
            name={source.name}
            chrome={chrome}
          >
            {object}
          </ObjectLink>
        </span>
        <span data-source-key className="font-medium">
          :{atTheSeparators(source.key)}
        </span>
      </span>
    </span>
  );
}

/**
 * A ConfigMap or a Secret a container names, as a link to that object - drawn
 * the same when the object is not there, because the reference is what the
 * manifest says and this panel does not judge it.
 *
 * Plain text where there is nothing to link to: a kind this console has no
 * route for, a source with no namespace, or a panel drawn outside a router.
 */
function ObjectLink({
  cluster,
  kind,
  namespace,
  name,
  chrome,
  children,
}: {
  cluster: string;
  kind: string;
  namespace: string | undefined;
  name: string;
  chrome: 'drawer' | 'page';
  children: ReactNode;
}): ReactNode {
  const routed = useInRouterContext();
  const resource = resourceOfSource(kind);
  if (!routed || resource === undefined || namespace === undefined || namespace === '') {
    return children;
  }
  return (
    <Link
      to={relatedPath({ cluster, kind: resource, namespace, name }, chrome)}
      data-object-link
      className={`rounded-xs text-accent underline-offset-2 hover:underline ${focusRing}`}
    >
      {children}
    </Link>
  );
}

/**
 * Text with a break offered after each `.`, `_` and `-`, so that when it has
 * to wrap it wraps between its words: `controller.resource.health.` and
 * `persist`, `ARGOCD_APPLICATION_` and `CONTROLLER_STATUS_PROCESSORS`, never a
 * lone `t` on a line of its own.
 *
 * The cells are `wrap`, which is `overflow-wrap: anywhere`, and a ConfigMap key
 * or a variable name has no space in it, so one a character or two too long for
 * its line would be broken at the last character that fitted, leaving a few
 * characters alone on a line. `<wbr>` is an ordinary break
 * opportunity, so the browser takes it before it falls back to breaking
 * anywhere. It adds no text: what is copied, and what a test reads, is the
 * text as it is spelt.
 *
 * Used on the variable's name and on the key in its source. The object's name
 * needs nothing: a hyphen is already a break opportunity.
 */
function atTheSeparators(text: string): ReactNode {
  const parts = text.split(/(?<=[._-])/);
  return parts.map((part, at) => (
    <Fragment key={at}>
      {part}
      {at < parts.length - 1 ? <wbr /> : null}
    </Fragment>
  ));
}

/** How each absence reads. Full class strings, looked up: Tailwind scans source
 *  text, so an interpolated class exists in the DOM and in no stylesheet. */
const ABSENCE_TONE: Readonly<Record<string, string>> = {
  unread: 'text-warn',
  unset: 'text-warn',
  expected: 'text-muted',
  unprintable: 'text-muted',
};

/** What a refusal that arrived with no words is drawn as.
 *
 *  The rule is an invariant and not a branch: a secret-sourced row
 *  carries a control or a reason, **never neither**. Every branch below holds it
 *  by drawing `reason.message` - except the one that withdraws the control,
 *  `undecodable`, which is final. A refusal that carries no message there leaves
 *  the row at `held back` with nothing to press and nothing to read, which is
 *  indistinguishable from a value nobody has asked for yet.
 *
 *  `Reason.message` is required in the contract and *required* is not
 *  *non-empty*; `firstLine(err.Error())` is what fills it on the server and an
 *  error whose first line is blank fills it with nothing. So the sentence is
 *  here rather than a guarantee asked of every producer - this is the one row
 *  where the empty-versus-failed collapse hides a credential behind a blank
 *  cell.
 */
const REFUSED_WITHOUT_WORDS = 'the reveal was refused, and no reason came back with it';

function worded(message: string): string {
  return message === '' ? REFUSED_WITHOUT_WORDS : message;
}

function Value({
  cluster,
  namespace,
  container,
  row,
}: {
  cluster: string;
  namespace: string | undefined;
  container: string;
  row: EnvVar;
}): ReactNode {
  const source = row.source;

  // What a revealed value belongs to, and it is kept beside the value. This cell is the same component at the same position when the drawer
  // is re-targeted at another pod, and a page already in the cache arrives
  // without passing through *Pending* - so a `DB_PASSWORD` row in a container
  // named `api` would go on drawing the previous pod's credential under the next
  // pod's name. A reveal answers one address; a row whose address moved on has
  // nothing to draw until it is asked again.
  const address = [
    cluster,
    namespace ?? '',
    container,
    row.name,
    source?.kind ?? '',
    source?.name ?? '',
    source?.key ?? '',
  ].join('/');
  const [revealed, setRevealed] = useState<{ at: string; value: string } | null>(null);
  // The refusal is the same shape, for the same reason: a "forbidden" written
  // for one pod's row is not the next pod's answer. The code rides with it
  // because one refusal is final - see `askable`.
  const [refused, setRefused] = useState<{
    at: string;
    message: string;
    code?: ReasonCode;
  } | null>(null);
  const shown = revealed !== null && revealed.at === address ? revealed.value : null;

  const reveal = useMutation({
    mutationFn: async () => {
      if (source === undefined) throw new Error('no source to read from');
      return revealEnvValue(cluster, namespace, source, { container, variable: row.name });
    },
    onSuccess: (answer) => {
      // An `ok` carrying the empty string is a revealed empty value, and the
      // envelope is the only thing that says so. Reading it as "nothing came
      // back" is the collapse this whole contract exists to remove, on the one
      // payload where being wrong hides or exposes a credential.
      if (answer.state === 'ok') {
        setRevealed({ at: address, value: answer.data.value });
        setRefused(null);
        return;
      }
      setRefused({ at: address, message: worded(answer.reason.message), code: answer.reason.code });
    },
    onError: (error: Error) => {
      setRefused({ at: address, message: worded(error.message) });
    },
  });

  if (shown !== null) {
    return (
      <span className="flex flex-wrap items-baseline gap-1">
        <span data-revealed="true" className="font-mono">
          {shown === '' ? (
            // A revealed empty value, said rather than drawn as a blank cell.
            <span className="text-muted italic">the value is empty</span>
          ) : (
            shown
          )}
        </span>
        {/* The way back. The gesture is one per key and there was
            no control to press once a password was on a shared screen. Hiding
            asks nothing - the audit line was written on the reveal and that is
            the record; the control below is offered again and pressing it is a
            new reveal. */}
        <button
          type="button"
          data-hide
          onClick={() => {
            setRevealed(null);
          }}
          className={btnSecondary}
        >
          Hide
        </button>
      </span>
    );
  }

  // A literal, which is in the pod spec and already one click away on the
  // Manifest tab, so drawing it here is not a new exposure.
  if (row.value !== undefined && row.value !== '') {
    return <span className="font-mono">{row.value}</span>;
  }

  // A source that cannot be followed: a field reference, a resource reference.
  // Neither a value nor an absence - a third thing that says what decides the
  // value instead.
  if (row.from !== undefined && row.from !== '') {
    return <span className="text-muted">{row.from}</span>;
  }

  // A ConfigMap value, resolved on the panel open. `held` and not
  // `resolved !== ''`: a key holding the empty string is an ordinary and
  // confusing thing to meet, and drawing it as a key nobody read is the
  // empty-versus-failed collapse on one row.
  if (row.held) {
    return (
      <span data-held="true" className="font-mono">
        {row.resolved === '' ? (
          <span className="font-sans text-muted italic">the value is empty</span>
        ) : (
          row.resolved
        )}
        {row.clipped ? <span className="ml-1 text-muted">&#8230;</span> : null}
      </span>
    );
  }

  const absence = row.absence;

  // A reference to a key the object does not carry, or to an object that is not
  // there: drawn as a bare dash. The reference itself is the link beside it, and
  // this panel does not say whether it resolves - the pod is what the manifest
  // says, and a sentence per row would repeat that dozens of times on a pod
  // such as Argo CD's controller.
  if ((absence === 'expected' || absence === 'unset') && row.secret !== true) {
    return (
      <span data-absence={absence} className="text-muted">
        &mdash;
      </span>
    );
  }

  const problem =
    (refused !== null && refused.at === address ? refused.message : undefined) ?? row.problem ?? '';

  // A Secret, or an absence somebody can do something about. The control is
  // offered on exactly two of the five cases and the reason differs:
  //
  //  - a Secret with a source: the value exists and was deliberately not sent;
  //  - `unread`: nobody looked, so asking again may answer differently.
  //
  // It is not offered on `unset`, `expected` and `unprintable`: a row whose
  // object *was* read and definitively lacks the key can only produce the same
  // sentence, and a control that can only repeat itself is one people stop reading.
  //
  // And it is withdrawn once the door has said `undecodable`, for that same
  // reason: a key whose value is not text is refused, the bytes will not be
  // text on the next click, and the server says so itself with `retryable:
  // false`. This row cannot know in advance the way the Data tab can - a
  // Secret's values are not in this panel at all - so the first click is how it
  // finds out, and the answer is kept rather than asked for again.
  const final = refused !== null && refused.at === address && refused.code === 'undecodable';
  const askable =
    source !== undefined &&
    resourceOfSource(source.kind) !== undefined &&
    (row.secret === true || absence === 'unread') &&
    !final;

  return (
    <span className="flex flex-wrap items-baseline gap-1">
      {problem === '' ? (
        <span className="text-muted">
          {row.secret === true ? 'held back' : '—'}
        </span>
      ) : (
        <span
          data-absence={absence ?? ''}
          className={absence === undefined ? 'text-muted' : (ABSENCE_TONE[absence] ?? 'text-muted')}
        >
          {problem}
        </span>
      )}

      {askable ? (
        <button
          type="button"
          data-reveal
          disabled={reveal.isPending}
          onClick={() => {
            reveal.mutate();
          }}
          className={btnSecondary}
        >
          {/* **Decode where the stored value is base64 and Show where it is
              not.** A control whose label does not name its
              operation is one people write a second one beside. A Secret's
              stored form is always base64; a ConfigMap's is not. */}
          {source !== undefined && source.kind === 'Secret' ? 'Decode' : 'Show'}
        </button>
      ) : null}
    </span>
  );
}
