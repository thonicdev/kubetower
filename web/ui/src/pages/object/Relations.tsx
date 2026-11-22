import { Fragment, type ReactNode } from 'react';
import { Link } from 'react-router';

import type { Relation, UsedBy } from '../../api';
import { Icon, type IconName } from '../../components/Icon';
import { focusRing } from '../../focus';
import type { ObjectIdentity } from './address';
import { relatedPath, selectorPath } from './address';

/**
 * Everything this object points at.
 *
 * **The one thing a console gives that `kubectl get -o yaml` does not.** Which
 * Secret is mounted where, which node this pod is on, what created it, what a
 * Service selects - each one click away, so you can click a ConfigMap and read
 * it without losing the pod you were on. The graph behind it is
 * `internal/detail/relations.go`.
 *
 * ## Each relation is a box, and the box is measured
 *
 * `.rel-list`, `.rel`, `.rel-role`, `.rel-name`, `.rel-at` - drawn as
 * utilities here because no stylesheet sits beside a component. Four things
 * in a row, in this order and never another:
 *
 *   an **icon**, a **role**, a **name**, a **location**
 *
 * and the reason each is a box rather than a line of text is that a relation,
 * a port and a URL are three lists of the same shape, so one rule covers them.
 * A panel of related objects is **scanned**, not read. The role in a fixed column at the smallest
 * step is what makes the names line up under each other, and the border is what
 * makes each row a target rather than a run of blue text.
 *
 * One accent-colored line per relation, with the role and the name in the
 * same size, would not scan.
 *
 * ## A relation with nothing behind it is drawn inert, not omitted
 *
 * A relation worth listing must not look like a link that failed. An `emptyDir`, a `hostPath`, or a selector
 * rather than one object gets the same box, the same icon and the same role, no
 * href, and `opacity` (`.rel.inert`). **Dimmed reads as *not a
 * link*; the same brightness with no href reads as *a link that failed*.**
 *
 * An empty answer and a failed answer drawn differently, at the width of a
 * row; both wrong answers are invisible to
 * the reader: **a drawer that lists only the linkable relations says a pod has
 * three volumes when it has five, and one that links all of them gives two dead
 * links.**
 *
 * ## `linkable` is read and never re-derived
 *
 * `Relation.Linkable()` (`internal/detail/detail.go`) is the server's answer.
 * A client computing *kind is not empty and name is not empty* would be a second
 * copy of the rule, and the two would disagree the first time a third condition
 * is added - a kind this cluster does not serve is already one. So this file
 * branches on the flag and on nothing else.
 *
 * ## The icon is the client's
 *
 * Which glyph to draw is a presentation decision, not a field on `Relation`.
 * So the map is here, keyed by the role string the payload carries, and a role it does not know gets the general shape
 * rather than nothing.
 */

/**
 * The glyph per role. `components/Icon.tsx` carries the sprite, and **every
 * name this map can return is already in it** (`secret`, `config`, `node`,
 * `volume`, `resource`).
 *
 * `Service`, `Backend`, `Selects` and `Pod` are spelled out although they
 * resolve to the same shape as the fallback: a map that silently agreed with
 * the default would lose the record that they were considered.
 */
const ICON: Readonly<Record<string, IconName>> = {
  Secret: 'secret',
  'Image pull secret': 'secret',
  'TLS certificate': 'secret',
  Config: 'config',
  Node: 'node',
  Volume: 'volume',
  'Volume claim': 'volume',
  'Persistent volume': 'volume',
  'Storage class': 'volume',
  Service: 'resource',
  Backend: 'resource',
  Selects: 'resource',
  Pod: 'resource',
  // The one reverse role. `resource` deliberately and not a new glyph: the
  // referrer is a Deployment, a Job, an Ingress or a PersistentVolume depending
  // on the row, so any single shape would be wrong for most of them - and the
  // fallback would have given exactly this anyway. Spelled out so the record
  // that it was considered survives, on the rule the four above it follow.
  'Used by': 'resource',
  // Access control. Three roles and three shapes the menu already
  // draws for the same kinds (kindGlyph: roles → `role`, bindings → `binding`,
  // service accounts → `account`), so a row and the menu entry it opens wear
  // the same mark. `Bound by` is always a binding, whichever kind it binds;
  // `Grants` is always a Role or a ClusterRole; `Subject` is always a
  // ServiceAccount, because a User or a Group is said in the note and never
  // drawn as a row.
  Grants: 'role',
  Subject: 'account',
  'Bound by': 'binding',
  // What bounds a namespace: the glyph the menu already
  // draws for both kinds (kindGlyph: resourcequotas and limitranges → `quota`).
  'Resource quota': 'quota',
  'Limit range': 'quota',
};

/** The fallback: a shape, never a blank. A role the map above does not name
 *  gets this one. */
const FALLBACK_ICON: IconName = 'resource';

/**
 * The box itself (`.rel`).
 *
 * The 7px corner is `rounded-lg` and not a literal: `theme.css` names 7px
 * `--radius-lg` precisely because it is the console's commonest corner, and a
 * literal here would be the one place that stops moving when the scale moves.
 * The 5px of vertical padding has no such step - it is one of exactly four
 * lengths `theme.css` records as needing a literal, `--spacing` being 4px and
 * deliberately left alone - so it carries `style-allow:` and this is the reason.
 *
 * `text-sm` is chosen **by name and not by pixel**, so it follows the type
 * ladder in `theme.css` when that moves.
 */
const BOX =
  'flex items-center gap-2 rounded-lg border border-line bg-bg px-2 py-[5px] text-sm text-fg'; // style-allow: `.rel`'s 5px, no step on the 4px scale

/** `.rel.inert`. `opacity-70` is the step on Tailwind's scale nearest the
 *  `.72` this means. */
const INERT = `${BOX} opacity-70`;

export function Relations({
  cluster,
  relations,
  note,
  usedBy,
  chrome,
}: {
  cluster: string;
  relations: readonly Relation[];
  /** What was left out and why. A pod can mount forty volumes and a Service can
   *  select five hundred pods; **a capped list that does not admit it is a lie
   *  the user acts on.** */
  note?: string | undefined;
  /** Whether the reverse walk ran, on a ConfigMap and a Secret.
   *
   *  **It is the reason this component does not return `null` on an empty
   *  list**, and that is the whole of its job here. Those two kinds name nothing,
   *  so the panel would be absent by construction, and an absent panel is
   *  indistinguishable from one that was never built.
   *
   *  Three states, three sentences, and never two of them rendered the same:
   *  `ok` needs nothing drawn because the rows are the answer; `empty` draws the
   *  server's sentence, which says *nothing found* and never *unused* and names
   *  what no walk can see; anything else draws the refusal, because a walk that
   *  could not list is not a walk that found nothing. */
  usedBy?: UsedBy | undefined;
  /** Which frame this panel is in, because it decides where a relation goes.
   *
   *  **A relation is a navigation in both frames; what differs is the
   *  destination.** In the drawer it is the related kind's *list*, narrowed to
   *  the namespace with the object opened on arrival (`listPath`): click the
   *  ReplicaSet under a pod and you are on the ReplicaSets page, with that
   *  object open, because the list gives the context the drawer alone does not.
   *  The drawer empties on the way, as `Drawer.tsx` says every navigation must,
   *  and the list at the other end re-opens it; nothing is carried across. On
   *  the object's own page there is no list behind the reader and the link is
   *  the related object's own route (`objectPath`). */
  chrome: 'drawer' | 'page';
}): ReactNode {
  const walked = usedBy?.state;
  // `ok` says nothing beyond the rows, so it does not keep an otherwise empty
  // panel alive; the other states each have a sentence to draw.
  const says = walked !== undefined && walked !== 'ok' ? (usedBy?.reason?.message ?? '') : '';

  if (relations.length === 0 && (note === undefined || note === '') && says === '') return null;

  return (
    <section data-section="relations">
      <h3 className="mb-1 text-xs font-medium text-muted uppercase">Related</h3>
      {/* `.rel-list`: a grid with a 4px gap. Every box is the full width of the
          panel, so the gap is the only thing between one row and the next - and
          a smaller gap would let the boxes read as one block rather than as
          a list of them. */}
      <ul className="grid gap-1">
        {relations.map((relation, at) => (
          // Keyed by position as well as identity: two relations can name the
          // same object under different roles - a Secret both mounted and used
          // as an image pull secret - and a key that dropped the second would
          // drop it silently.
          <li key={`${relation.role}-${relation.name ?? ''}-${String(at)}`}>
            <Row cluster={cluster} relation={relation} chrome={chrome} />
          </li>
        ))}
      </ul>
      {/* What the walk established, above the cap note: the cap is about this
          panel's length and this is about the answer, and the answer is read
          first. `data-used-by` carries the state rather than a color, so a test
          can tell "nothing found" from "you may not list here" the way the
          contract does and not by reading a class name. The sentence is the
          server's whole string - it names what no walk can see, and a client
          that shortened it would be deciding on the user's behalf that the
          caveat did not matter. */}
      {says === '' ? null : (
        <p
          data-used-by={walked}
          className={`mt-1 text-2xs ${walked === 'empty' ? 'text-muted' : 'text-warn'}`}
        >
          {says}
        </p>
      )}
      {note === undefined || note === '' ? null : (
        <p data-relations-note className="mt-1 text-2xs text-muted">
          {note}
        </p>
      )}
    </section>
  );
}

function Row({
  cluster,
  relation,
  chrome,
}: {
  cluster: string;
  relation: Relation;
  chrome: 'drawer' | 'page';
}): ReactNode {
  const body = <Body relation={relation} />;

  // **A selector is a filtered list, not an object.** The server says
  // `linkable: false` for it because there is no one object to open, and that
  // stays its answer: no object route is built here. What the contract offers
  // instead is the kind beside the selector, *"so a client can open a filtered
  // list"* - without it a controller could reach its owner and never its pods.
  const selector = relation.selector ?? '';
  const selectedKind = relation.kind ?? '';
  if (!relation.linkable && selector !== '' && selectedKind !== '') {
    return (
      <Link
        to={selectorPath(cluster, selectedKind, relation.namespace, selector)}
        data-selects=""
        className={`${BOX} hover:border-accent ${focusRing}`}
      >
        {body}
      </Link>
    );
  }

  if (!relation.linkable) {
    // Inert. Same box, same icon, same role, no href, dimmed - and `data-inert`
    // so a test can tell the two apart without reading colors.
    return (
      <span data-inert="true" className={INERT}>
        {body}
      </span>
    );
  }

  // `linkable` is the server's answer, and the two fields it is an answer about
  // are read here rather than re-checked. TypeScript still requires them to be
  // present, so the honest expression of "the server said this is addressable"
  // is to take what it sent and fall back to nothing drawable if it did not.
  const kind = relation.kind ?? '';
  const name = relation.name ?? '';
  if (kind === '' || name === '') {
    // The server said linkable and did not send an address: a contract
    // violation, not a relation. Drawn inert rather than as a link to nowhere,
    // and marked so it is visible as the fault it is rather than as an emptyDir.
    return (
      <span data-inert="true" data-inconsistent="true" className={INERT}>
        {body}
      </span>
    );
  }

  const namespace = relation.namespace;
  const identity: ObjectIdentity =
    namespace === undefined || namespace === ''
      ? { cluster, kind, name }
      : { cluster, kind, namespace, name };

  // The **target's** namespace, which the server leaves empty for a
  // cluster-scoped target - a Node, a PersistentVolume, a StorageClass. A
  // namespace handed to a cluster-scoped kind passes every validator, reaches
  // the API server and comes back "not found" for a resource that is right
  // there. Both addresses below read it from `identity` for that reason.
  return (
    <Link
      // **An anchor, and nothing intercepts it.** The plain click follows the
      // address, and so do a middle-click, a right-click's copy and a paste
      // into a chat - the whole reason for an object having a route of its own.
      // A click handler that swallows the primary click is one modifier check away from being
      // the console where opening a thing in a second tab does not work, and
      // the router's `Link` already leaves every modified click to the browser.
      to={relatedPath(identity, chrome)}
      // `a.rel:hover`: the **border** turns accent and the text does not change
      // color at all. An accent-colored row throughout would make the panel a
      // list of links rather than a list of objects.
      className={`${BOX} hover:border-accent ${focusRing}`}
    >
      {body}
    </Link>
  );
}

/**
 * The four things, in order: icon, role, name, location.
 *
 * Shared by the link and by the inert span, which is the point of `.rel` and
 * `.rel.inert` being one rule and a modifier: the two differ in
 * the element and in the opacity, never in what they say.
 */
/**
 * A path, with a place to break after every slash.
 *
 * The name it sits in breaks anywhere, which a generated name needs and a
 * path does not: `/var/run/secrets/kubernetes.io/serviceaccount` broke as
 * `serviceacco` / `unt`. A `<wbr>` after each `/` is a break the line takes
 * first, so the path wraps between its parts and a part is cut only where it
 * is wider than the panel on its own.
 */
export function atSlashes(path: string): ReactNode {
  const parts = path.split('/');
  return parts.map((part, at) => (
    <Fragment key={at}>
      {part}
      {at < parts.length - 1 ? (
        <>
          /<wbr />
        </>
      ) : null}
    </Fragment>
  ));
}

function Body({ relation }: { relation: Relation }): ReactNode {
  return (
    <>
      {/* `.rel .icon { color: var(--muted) }`. `Icon` is `aria-hidden` already,
          and its 15px is `.icon`'s own size. */}
      <Icon name={ICON[relation.role] ?? FALLBACK_ICON} className="text-muted" />
      {/* `.rel-role`: the fixed column that makes the names align down the
          panel. 92px is `min-w-23` on the 4px spacing scale - a step rather
          than an arbitrary value, and not the shell's `--spacing-rail`, which
          means the rail and this column is not it.

          **A width and not a minimum**: CONTROLLED BY and SERVICE ACCOUNT are
          wider than 92px, so with a minimum their rows would push the name
          right and the names would stand at different x positions down one
          panel - the alignment this column exists for. A role longer than the
          column wraps inside it instead. */}
      <span
        data-rel-role
        className="w-28 shrink-0 text-2xs font-bold tracking-wider text-muted uppercase"
      >
        {relation.role}
      </span>
      {/* `.rel-name`: `min-width: 0` so it can shrink inside the flex row, and
          `overflow-wrap: anywhere` because a generated name has no space in it
          to break at - `api-7c9f8d6b54-x2k9p` would otherwise push the location
          off the edge of the panel instead of wrapping. */}
      <span data-rel-name className="min-w-0 wrap-anywhere">
        {/* `label` is what to write on screen and the producer always fills it;
            the selector is the fallback the template uses for a relation that is
            a match rather than an object. */}
        {relation.label === '' ? (relation.selector ?? relation.name ?? '') : relation.label}
        {relation.detail === undefined || relation.detail === '' ? null : (
          <span className="ml-1 text-muted">{relation.detail}</span>
        )}
        {/* The paths this object is mounted at, because "which secret is at
            /etc/creds" is the question being asked. The producer caps the list
            and puts a `+N more` element at the end of it rather than cutting
            silently, so the cap travels as one of these and is drawn as one. */}
        {(relation.mounts ?? []).map((mount) => (
          <span
            key={mount}
            data-mount
            className="ml-1 rounded-sm bg-panel-2 box-decoration-clone px-1 font-mono text-2xs text-muted"
          >
            {atSlashes(mount)}
          </span>
        ))}
      </span>
      {/* `.rel-at`: the location, pushed to the right edge.
          **The target's namespace, and nothing at all where there is none.** A
          Node, a PersistentVolume and a StorageClass are cluster-scoped; an
          empty box at the right edge of those rows would read as a namespace
          that could not be read rather than as one that does not exist - the
          same collapse the inert row exists to avoid, one column over. */}
      {relation.namespace === undefined || relation.namespace === '' ? null : (
        <span data-rel-at className="ml-auto shrink-0 text-xs text-muted">
          {relation.namespace}
        </span>
      )}
    </>
  );
}
