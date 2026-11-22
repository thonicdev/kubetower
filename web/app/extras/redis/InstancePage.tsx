import { useQuery } from '@tanstack/react-query';
import { Fragment, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useLocation, useNavigate, useParams, useSearchParams } from 'react-router';
import {
  CloseButton,
  CopyButton,
  OverflowMenu,
  OverflowMenuItem,
  Windowed,
  Pending,
  Pill,
  copyText,
  BackArrow,
  focusRing,
  useDeclarePageFacts,
  btnSecondary,
} from '@kubetower/ui';

import {
  RedisProblem,
  getKey,
  instanceQuery,
  keyQuery,
  serviceQuery,
  type Door,
  type Entry,
  type InstanceAnswer,
  type KeyDetail,
  type KeyRow,
  type Scan,
} from './data';
import { HEX_LIMIT, decode, decodeNow, decodedMark, hexRows, preview, type Decoded, type Gzip } from './decode';
import { download, fileName, keysCsv, keysJson, scanPartialNote, valueJson, valueRaw, type Origin } from './export';
import { TYPE_TONE, bytes, folderPaths, isPartial, keyTree, loadedNote, typeWord, type TreeItem } from './keys';
import { ProblemNotice, instanceHref, inventoryHref, plural } from './parts';

/**
 * One instance, opened, at `/c/:cluster/x/redis/:namespace/:service/:port`.
 * This is the page that dials: one tunnel per read, closed with it.
 *
 * What the address carries is the allowlist the route reads - `db`, `match`,
 * `cursor` - and `scope`, the inventory's filter, kept so the way back lands
 * on the list the person came from. A password is never one of them.
 *
 * Four states and not one empty list: gone, could not open, connected and
 * empty, connected with keys. An empty keyspace looks exactly like a Redis that
 * is not there, and a page that drew both as no rows would say the wrong one.
 */
export function InstancePage(): ReactNode {
  const { cluster = '', namespace = '', service = '', port = '' } = useParams();
  const [params, setParams] = useSearchParams();
  const door = { namespace, service, port: Number(port) };
  const db = Math.max(0, Number.parseInt(params.get('db') ?? '0', 10) || 0);
  const match = (params.get('match') ?? '').trim();
  const cursor = params.get('cursor') ?? '';
  const scope = params.get('scope') ?? '';
  const label = `${namespace}/${service}:${port}`;
  useDeclarePageFacts({ title: label });

  const instance = useQuery(instanceQuery(cluster, door, { db, match, cursor }));
  const [opened, setOpened] = useState<KeyRow | undefined>(undefined);

  // **Replace, not push**: four patterns in turn are one page being looked at,
  // not four, and a Back key that walked them one at a time could not leave.
  const go = (next: { match?: string; cursor?: string; db?: number }) => {
    setOpened(undefined);
    setParams(
      (p) => {
        const out = new URLSearchParams(p);
        if (next.db !== undefined) {
          // Another database is another keyspace: the pattern stays, the
          // position does not.
          if (next.db === 0) out.delete('db');
          else out.set('db', String(next.db));
          out.delete('cursor');
        }
        if (next.match !== undefined) {
          if (next.match === '') out.delete('match');
          else out.set('match', next.match);
          // A new pattern is a new scan: the old position would resume halfway
          // through a keyspace the person has not seen the start of.
          out.delete('cursor');
        }
        if (next.cursor !== undefined) {
          if (next.cursor === '') out.delete('cursor');
          else out.set('cursor', next.cursor);
        }
        return out;
      },
      { replace: true },
    );
  };

  // **Back is back** (as the operator asked, as KubeCron's page): opened
  // from inside the console - a tile, a shortcut on a Service or a Pod, a link
  // - the arrow returns there, as the browser's Back does, with that page's
  // filters as they were. Opened any other way (a pasted link, a reload into
  // a fresh tab) there is nothing in the console behind it, and the arrow is a
  // link to the inventory.
  //
  // **Decided once, on arrival.** The page's own moves - a database, a
  // pattern, the next slice, another member - replace the address, and a
  // replace gets a fresh key too: read on every render, a page opened from a
  // pasted link turned its arrow into a Back out of the console after one
  // click (review of 9af5926).
  //
  // **And from the position, not the key, where there is one.** A redirect
  // that replaces before the page mounts - signing in and being sent on, an
  // old spelling of the cluster - also arrives with a fresh key, and nothing
  // of the console behind it (review of 2eda60a). The browser router keeps
  // the entry's index in `history.state.idx`, which a replace keeps: 0 is
  // the first entry of this tab. A memory router has no such state, and the
  // key decides there.
  const navigate = useNavigate();
  const arrival = useLocation().key;
  const [inConsole] = useState(() => {
    const idx: unknown = (window.history.state as { idx?: unknown } | null)?.idx;
    return typeof idx === 'number' ? idx > 0 : arrival !== 'default';
  });
  const data = instance.data;
  const gone = instance.error instanceof RedisProblem && instance.error.code === 'instance_gone';
  return (
    <div className="flex flex-col gap-4 p-4">
      <header className="flex flex-wrap items-center gap-2">
        {inConsole ? (
          <BackArrow onClick={() => void navigate(-1)} data-back="" />
        ) : (
          <BackArrow to={inventoryHref(cluster, scope)} label="Back to the Redis viewer" data-back="" />
        )}
        <h1 className="text-xl font-medium" title={data === undefined ? label : `${label} - offered because ${data.instance.why}`}>
          {label}
        </h1>
        {data !== undefined ? (
          <>
            {/* The tile's three readings, as the server gives them. */}
            <Pill title="What the server said it is (INFO server).">
              {data.server.engine} {data.server.version}
            </Pill>
            <Pill neutral="muted" title="The server's own mode and replication role.">
              {data.server.role === undefined ? data.server.mode : `${data.server.mode} · ${data.server.role}`}
            </Pill>
            <Pill tone={data.server.mode === 'standalone' ? 'ok' : 'warn'}>
              {data.server.mode === 'standalone' ? 'reachable' : 'not handled'}
            </Pill>
          </>
        ) : gone ? (
          <Pill tone="neutral">not here any more</Pill>
        ) : instance.isError ? (
          <Pill tone="bad">could not open</Pill>
        ) : null}
        <span className="flex-1" />
        {data?.scan === undefined ? null : <PatternBox key={match} match={match} onSend={(m) => go({ match: m })} />}
      </header>

      <Members cluster={cluster} door={door} scope={scope} />

      {data === undefined ? (
        instance.isError ? (
          gone ? (
            <div role="status" data-state="gone" className="rounded-lg border border-line bg-panel p-3 text-sm">
              <p className="font-medium">Nothing answers to {label} any more</p>
              <p className="mt-1 text-xs text-muted">Uninstalled, renamed or moved since this link was made.</p>
            </div>
          ) : (
            <ProblemNotice error={instance.error} onRetry={() => void instance.refetch()} />
          )
        ) : (
          <Pending what={`${label}: one tunnel, then a scan`} />
        )
      ) : (
        <Opened
          cluster={cluster}
          data={data}
          db={db}
          opened={opened}
          onOpen={setOpened}
          onMore={(next) => go({ cursor: next })}
          onDb={(next) => go({ db: next })}
          onRefresh={() => void instance.refetch()}
          refreshing={instance.isFetching}
          round={instance.dataUpdatedAt}
        />
      )}
    </div>
  );
}

/**
 * The pattern box. A glob the server applies as `MATCH`, **sent on Enter or
 * on the search control only** - each send is a port-forward and a SCAN round
 * against a production server, and a box that fired per keystroke would be
 * several of them per word.
 */
function PatternBox({ match, onSend }: { match: string; onSend: (match: string) => void }): ReactNode {
  // Keyed on `match` by its parent, so a pattern arriving from the address
  // starts a fresh box rather than being copied in by an effect.
  const [value, setValue] = useState(match);
  const box = useRef<HTMLInputElement>(null);
  // `search` is the event a type=search box fires when its clear control is
  // used, and React has no prop for it.
  useEffect(() => {
    const input = box.current;
    if (input === null) return;
    const onSearch = () => {
      if (input.value === '') onSend('');
    };
    input.addEventListener('search', onSearch);
    return () => input.removeEventListener('search', onSearch);
  }, [onSend]);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSend(value.trim());
  };
  return (
    <form role="search" onSubmit={submit} className="flex items-center gap-1">
      <input
        ref={box}
        type="search"
        name="match"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="key pattern, e.g. session:*"
        autoComplete="off"
        aria-label="Key pattern"
        title="A glob the server applies (SCAN MATCH). Sent on Enter: each send is one round trip."
        className={`w-64 rounded-md border border-line bg-panel px-2 py-1 font-mono text-xs ${focusRing}`}
      />
    </form>
  );
}

/**
 * The other members of this instance's deployment, one
 * link each: the inventory draws one tile per deployment and opens its
 * primary, so the replicas and the headless Service are reached from here.
 * Read from the Service route, which answers from discovery and dials
 * nothing. A Sentinel is named, never linked. Where the route could not say,
 * that is said rather than drawing a deployment of one.
 */
function Members({ cluster, door, scope }: { cluster: string; door: Pick<Door, 'namespace' | 'service' | 'port'>; scope: string }): ReactNode {
  const service = useQuery(serviceQuery(cluster, door.namespace, door.service));
  if (service.data === undefined) {
    if (!service.isError || (service.error instanceof RedisProblem && service.error.kind === 'absent')) return null;
    return (
      <p data-members="unread" className="text-2xs text-muted">
        The other members of this deployment could not be read.
      </p>
    );
  }
  const at = (d: Pick<Door, 'service' | 'port'>) => d.service === door.service && d.port === door.port;
  const deployment = service.data.deployments.find((d) => d.members.some((m) => m.doors.some(at)));
  if (deployment === undefined) return null;
  const doors = deployment.members.flatMap((m) => m.doors.map((d) => ({ member: m, door: d })));
  if (doors.length <= 1) return null;
  return (
    <nav aria-label="Members" data-members={deployment.name} className="flex flex-wrap items-center gap-1.5 text-xs">
      <span className="font-mono text-muted">{deployment.name}</span>
      {doors.map(({ member, door: d }) => {
        const role = member.role ?? 'member';
        const id = `${d.service}:${d.port}`;
        if (member.sentinel === true) {
          return (
            <span key={id} data-member-door={id} title="A Sentinel holds no keys; this viewer does not open one." className="rounded-full border border-line px-2 py-px text-muted">
              {role} <span className="font-mono">{id}</span>
            </span>
          );
        }
        const here = at(d);
        return (
          <Link
            key={id}
            to={instanceHref(cluster, d, scope)}
            // Replaced, not pushed: another member of the deployment is the
            // same page looked at from another door, and the arrow back
            // leaves the deployment rather than walking its members.
            replace
            data-member-door={id}
            aria-current={here ? 'page' : undefined}
            title={`${role}: ${d.namespace}/${id} - offered because ${d.why}`}
            className={`rounded-full border px-2 py-px ${here ? 'border-accent text-fg' : 'border-line text-muted hover:text-fg'} ${focusRing}`}
          >
            {role} <span className="font-mono">{id}</span>
          </Link>
        );
      })}
    </nav>
  );
}

function Opened({
  cluster,
  data,
  db,
  opened,
  onOpen,
  onMore,
  onDb,
  onRefresh,
  refreshing,
  round,
}: {
  cluster: string;
  data: InstanceAnswer;
  db: number;
  opened: KeyRow | undefined;
  onOpen: (key: KeyRow | undefined) => void;
  onMore: (cursor: string) => void;
  onDb: (db: number) => void;
  onRefresh: () => void;
  refreshing: boolean;
  /** When this answer was read: a new read is a new round, drawn afresh. */
  round: number;
}): ReactNode {
  const origin: Origin = { cluster, instance: data.instance, db };
  return (
    <>
      <Strip data={data} onRefresh={onRefresh} refreshing={refreshing} onDb={onDb} />
      {data.server.note === undefined ? null : (
        <p data-mode={data.server.mode} className="text-sm text-warn">
          {data.server.note}
        </p>
      )}
      {data.scan === undefined ? null : (
        <div className="flex flex-col gap-3 lg:flex-row lg:items-start">
          <div className="min-w-0 lg:w-2/5">
            <Keys key={round} scan={data.scan} origin={origin} opened={opened} onOpen={onOpen} onMore={onMore} />
          </div>
          {opened === undefined ? null : (
            // Keyed on the row: another key starts from its first page, with
            // nothing of the last key's pages, its failure or its folds.
            <KeyPanel key={rowKey(opened)} cluster={cluster} data={data} db={db} origin={origin} row={opened} onClose={() => onOpen(undefined)} />
          )}
        </div>
      )}
    </>
  );
}

/**
 * Everything the page knows about the instance, on one strip: what it is, how
 * big, why it was offered, and - when a password was needed - **which Secret
 * it came from and on the strength of what, never its value**.
 */
export function Strip({
  data,
  onRefresh,
  refreshing,
  onDb,
}: {
  data: InstanceAnswer;
  onRefresh?: () => void;
  refreshing?: boolean;
  /** Given, each database of the keyspace is a control that opens it. */
  onDb?: (db: number) => void;
}): ReactNode {
  const fact = (k: string, v: ReactNode, title?: string) => (
    <span data-fact={k} className="flex min-w-0 flex-col gap-0.5" {...(title === undefined ? {} : { title })}>
      <span className="text-2xs text-muted">{k}</span>
      <span className="text-sm">{v}</span>
    </span>
  );
  const o = data.overview;
  return (
    <section aria-label="This instance" className="flex flex-wrap items-start gap-x-6 gap-y-2 rounded-lg border border-line bg-panel px-3 py-2">
      {o === undefined ? null : fact(`keys · db${data.db}`, String(o.keys))}
      {o?.usedMemory === undefined ? null : fact('memory', o.usedMemory)}
      {data.pod === undefined || data.pod === '' ? null : fact('pod reached', <code className="font-mono text-xs">{data.pod}</code>)}
      {o === undefined || o.keyspace.length === 0
        ? null
        : fact(
            'keyspace',
            onDb === undefined ? (
              o.keyspace.map((d) => `db${d.index} ${plural(d.keys, 'key')}, ${d.expires} expiring`).join(' · ')
            ) : (
              // Every database that holds a key, one press away (the
              // operator asked to see every database). The one read
              // is pressed; the others open theirs, each one more scan.
              <span className="flex flex-wrap gap-1">
                {o.keyspace.map((d) => (
                  <button
                    key={d.index}
                    type="button"
                    data-db={d.index}
                    aria-pressed={d.index === data.db}
                    onClick={() => onDb(d.index)}
                    title={`${plural(d.keys, 'key')}, ${d.expires} expiring`}
                    className={`rounded-full border px-2 py-px font-mono text-xs ${
                      d.index === data.db ? 'border-accent text-fg' : 'border-line text-muted hover:text-fg'
                    } ${focusRing}`}
                  >
                    db{d.index} <span className="tabular-nums">{d.keys}</span>
                  </button>
                ))}
              </span>
            ),
          )}
      {data.credential === undefined
        ? null
        : fact(
            'password',
            <>
              <code className="font-mono text-xs">{data.credential.key}</code> from Secret{' '}
              <code className="font-mono text-xs">{data.credential.secret}</code>
            </>,
            // Where it was found, on the hover: the provenance, never the value.
            `Found as ${data.credential.source}`,
          )}
      {onRefresh === undefined ? null : (
        <button
          type="button"
          onClick={onRefresh}
          disabled={refreshing === true}
          title="Read this again: one more tunnel and one more scan round."
          className={`ml-auto self-center ${btnSecondary}`}
        >
          {refreshing === true ? 'Reading…' : 'Read again'}
        </button>
      )}
    </section>
  );
}


/** A row's React key: its raw bytes or its name, never confusable with each other. */
function rowKey(row: KeyRow): string {
  return row.raw === undefined ? `n:${row.name}` : `r:${row.raw}`;
}

/** The tree's indent, looked up rather than built: Tailwind reads classes from the source. */
const INDENT = ['pl-2', 'pl-5', 'pl-8', 'pl-11', 'pl-14', 'pl-16'] as const;
function indent(depth: number): string {
  return INDENT[Math.min(depth, INDENT.length - 1)] ?? 'pl-2';
}


/** A key's type, as a short monospace badge in the type's own color. */
export function TypeBadge({ type }: { type: string }): ReactNode {
  return (
    <span
      data-type={type}
      title={type}
      className={`inline-block rounded-sm border border-line px-1 font-mono text-2xs ${TYPE_TONE[type] ?? 'text-muted'}`}
    >
      {typeWord(type)}
    </span>
  );
}

/** One act of a `⋯` menu: a file handed to the browser, or a text copied. */
type Action = { name: string; label: string; title: string } & (
  | { file: () => { text: string; file: string; type: string } }
  | { copy: () => string }
);

/**
 * The `⋯` that holds a list's or a value's copy and exports: each builds its
 * text from what the page holds and hands it to the browser or the clipboard.
 * Nothing is asked of the cluster. The partial mark is said beside it, because
 * a file that is a sample must not pass for the whole.
 */
function Exports({ label, actions, partial }: { label: string; actions: readonly Action[]; partial: string | undefined }): ReactNode {
  const [said, setSaid] = useState<string | undefined>(undefined);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );
  // What happened, said beside the menu and then let go: the menu has closed,
  // and a copy that left no trace would read as one that did nothing.
  const say = (text: string | undefined) => {
    setSaid(text);
    if (timer.current !== null) clearTimeout(timer.current);
    if (text !== undefined) timer.current = setTimeout(() => setSaid(undefined), 2500);
  };
  const run = (a: Action) => {
    if ('copy' in a) {
      copyText(a.copy(), (written) => say(written ? 'Copied' : 'This browser would not copy.'));
      return;
    }
    const built = a.file();
    say(download(built.text, built.file, built.type) ? undefined : 'This browser would not make a file.');
  };
  return (
    <div data-exports={label} className="flex items-center gap-1.5 text-2xs">
      {partial === undefined ? null : (
        <span data-export-partial="" className="text-warn" title={partial}>
          partial
        </span>
      )}
      {said === undefined ? null : (
        <span role="status" data-export-said="" className={said === 'Copied' ? 'text-muted' : 'text-warn'}>
          {said}
        </span>
      )}
      <OverflowMenu label={label} title={partial === undefined ? label : `${label}. ${partial}`}>
        {actions.map((a) => (
          <OverflowMenuItem key={a.name} label={a.label} title={a.title} onSelect={() => run(a)} />
        ))}
      </OverflowMenu>
    </div>
  );
}

/**
 * One round of the scan, compact: a tree by `:` prefix or a flat list, a type
 * badge, the expiry and the memory per key. **A scan is a sample in
 * progress, not a list**: a round may come back empty with the scan
 * unfinished, and the count of rows is not the size of the keyspace. Both are
 * said, and "More" carries the cursor the server handed back - the only thing
 * Redis accepts.
 */
export function Keys({
  scan,
  origin,
  opened,
  onOpen,
  onMore,
}: {
  scan: Scan;
  origin: Origin;
  opened: KeyRow | undefined;
  onOpen: (key: KeyRow) => void;
  onMore: (cursor: string) => void;
}): ReactNode {
  const tree = useMemo(() => keyTree(scan.keys), [scan.keys]);
  // The tree is the default only where it folds something: a round whose
  // prefixes are each one key long is shorter as a flat list.
  const folds = tree.some((item) => item.kind === 'folder' && item.count > 1);
  const [view, setView] = useState<'tree' | 'flat' | undefined>(undefined);
  const shown = view ?? (folds ? 'tree' : 'flat');
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set(scan.keys.length <= 50 ? folderPaths(tree) : []));
  const partial = scanPartialNote(scan);
  const toggle = (path: string) =>
    setOpen((o) => {
      const next = new Set(o);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  const file = (ext: string) => fileName([origin.instance.namespace, origin.instance.service, `db${origin.db}`, 'keys'], ext);

  return (
    <div className="flex flex-col gap-2">
      {scan.keys.length === 0 ? (
        <p role="status" data-scan={scan.done ? 'done-empty' : 'round-empty'} className="text-sm text-muted">
          {scan.done ? (
            <>
              No keys{scan.match === '' ? '' : <> matching <code className="font-mono text-xs">{scan.match}</code></>} in
              this database: the scan is complete.
            </>
          ) : (
            <>No keys in this slice. The scan is not finished, so this is not an empty database.</>
          )}
        </p>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 text-2xs">
            <span className="text-muted">{plural(scan.keys.length, 'key')} in this round</span>
            <span className="flex rounded-sm border border-line" role="group" aria-label="Key list layout">
              {(['tree', 'flat'] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  aria-pressed={shown === v}
                  data-view={v}
                  onClick={() => setView(v)}
                  className={`px-1.5 py-px ${shown === v ? 'bg-panel-2 text-fg' : 'text-muted'} ${focusRing}`}
                >
                  {v}
                </button>
              ))}
            </span>
            {shown === 'tree' ? (
              <>
                <button type="button" onClick={() => setOpen(new Set(folderPaths(tree)))} className={`text-muted hover:text-fg ${focusRing}`}>
                  expand all
                </button>
                <button type="button" onClick={() => setOpen(new Set())} className={`text-muted hover:text-fg ${focusRing}`}>
                  collapse all
                </button>
              </>
            ) : null}
            <span className="flex-1" />
            <Exports
              label="Export keys"
              partial={partial}
              actions={[
                {
                  name: 'csv',
                  label: 'Export CSV',
                  title: `The ${plural(scan.keys.length, 'key')} of this round as CSV: name, type, expiry, memory.${partial === undefined ? '' : ` ${partial}`}`,
                  file: () => ({ text: keysCsv(scan.keys), file: file('csv'), type: 'text/csv' }),
                },
                {
                  name: 'json',
                  label: 'Export JSON',
                  title: `The ${plural(scan.keys.length, 'key')} of this round as JSON, with where they came from.${partial === undefined ? '' : ` ${partial}`}`,
                  file: () => ({ text: keysJson(origin, scan), file: file('json'), type: 'application/json' }),
                },
              ]}
            />
          </div>
          <div className="max-h-[70vh] overflow-auto rounded-lg border border-line bg-panel">
            <table className="w-full text-xs" data-keys="">
              <thead className="sticky top-0 bg-panel">
                <tr className="text-left text-2xs text-muted">
                  <th className="px-2 py-1 font-normal">Key</th>
                  <th className="px-1 py-1 font-normal">Type</th>
                  <th className="px-1 py-1 font-normal">Expires</th>
                  <th className="px-2 py-1 text-right font-normal">Memory</th>
                </tr>
              </thead>
              <tbody>
                {shown === 'tree' ? (
                  <TreeRows items={tree} depth={0} open={open} toggle={toggle} opened={opened} onOpen={onOpen} />
                ) : (
                  <Windowed
                    items={scan.keys}
                    more={(button) => (
                      <tr>
                        <td colSpan={4} className="p-2 text-center">
                          {button}
                        </td>
                      </tr>
                    )}
                  >
                    {(page) =>
                      page.map((row) => <KeyLine key={rowKey(row)} row={row} label={row.name} depth={0} opened={opened} onOpen={onOpen} />)
                    }
                  </Windowed>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}
      {scan.done ? (
        scan.keys.length === 0 ? null : (
          <p data-scan="done" className="text-xs text-muted">
            Scan complete.
          </p>
        )
      ) : (
        <div data-scan="sample" className="flex flex-wrap items-center gap-2 text-xs">
          <p
            className="flex-1 text-muted"
            title="SCAN reads a slice at a time; a key changed during the scan can be seen twice or missed. That is Redis's guarantee."
          >
            One slice of the keyspace, not all of it.
          </p>
          {scan.next === undefined ? null : (
            <button
              type="button"
              data-more={scan.next}
              onClick={() => onMore(scan.next ?? '')}
              className={btnSecondary}
            >
              Next slice
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function TreeRows({
  items,
  depth,
  open,
  toggle,
  opened,
  onOpen,
}: {
  items: readonly TreeItem[];
  depth: number;
  open: ReadonlySet<string>;
  toggle: (path: string) => void;
  opened: KeyRow | undefined;
  onOpen: (key: KeyRow) => void;
}): ReactNode {
  return items.map((item) => {
    if (item.kind === 'key') {
      return <KeyLine key={`k:${rowKey(item.row)}`} row={item.row} label={item.label} depth={depth} opened={opened} onOpen={onOpen} />;
    }
    const expanded = open.has(item.path);
    return (
      <Fragment key={`f:${item.path}`}>
        <tr data-folder={item.path} className="border-t border-line">
          <td colSpan={4} className={`py-0.5 pr-2 ${indent(depth)}`}>
            <button
              type="button"
              aria-expanded={expanded}
              onClick={() => toggle(item.path)}
              className={`flex items-baseline gap-1 font-mono text-xs text-muted hover:text-fg ${focusRing}`}
            >
              <span aria-hidden="true">{expanded ? '▾' : '▸'}</span>
              <span className="text-fg">{item.label}</span>
              <span className="text-2xs">{item.count}</span>
            </button>
          </td>
        </tr>
        {expanded ? <TreeRows items={item.children} depth={depth + 1} open={open} toggle={toggle} opened={opened} onOpen={onOpen} /> : null}
      </Fragment>
    );
  });
}

function KeyLine({
  row,
  label,
  depth,
  opened,
  onOpen,
}: {
  row: KeyRow;
  label: string;
  depth: number;
  opened: KeyRow | undefined;
  onOpen: (key: KeyRow) => void;
}): ReactNode {
  const on = opened !== undefined && opened.name === row.name && opened.raw === row.raw;
  return (
    <tr data-key={row.name} className={`border-t border-line ${on ? 'bg-panel-2' : 'hover:bg-panel-2'}`}>
      <td className={`py-0.5 pr-2 ${indent(depth)}`}>
        <button
          type="button"
          onClick={() => onOpen(row)}
          aria-pressed={on}
          title={`${row.name} - open this key: its value, bounded, through one more tunnel.`}
          className={`text-left font-mono text-xs break-all underline-offset-2 hover:underline ${focusRing}`}
        >
          {label}
        </button>
        {row.raw === undefined ? null : <span className="ml-1 text-2xs text-muted">(not text)</span>}
      </td>
      <td className="px-1 py-0.5">
        <TypeBadge type={row.type} />
      </td>
      <td className="px-1 py-0.5 font-mono text-2xs whitespace-nowrap text-muted">{row.ttl}</td>
      <td className="px-2 py-0.5 text-right font-mono text-2xs whitespace-nowrap text-muted">{bytes(row.memory)}</td>
    </tr>
  );
}

/**
 * One key, opened: its value, **bounded** - 64 KiB of a string, a page of
 * about a hundred elements of anything else, more pages on request - drawn
 * the way its type reads, with the note that says what was cut. Read through
 * one more tunnel when the row is pressed, never before; each further page is
 * one more tunnel, when « Load more » is pressed.
 */
function KeyPanel({
  cluster,
  data,
  db,
  origin,
  row,
  onClose,
}: {
  cluster: string;
  data: InstanceAnswer;
  db: number;
  origin: Origin;
  row: KeyRow;
  onClose: () => void;
}): ReactNode {
  const key = useQuery(keyQuery(cluster, data.instance, db, row));
  const first = key.data?.key;
  const name = useRef<HTMLHeadingElement>(null);

  // The pages read after the first, tied to the first page's read: when the
  // first page is read again, what was appended to the old one is dropped
  // rather than mixed into the new.
  const [pages, setPages] = useState<{ from: number; entries: Entry[]; next: string | undefined } | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState<unknown>(undefined);
  const inFlight = useRef<AbortController | null>(null);
  useEffect(() => () => inFlight.current?.abort(), []);

  const held = pages !== undefined && pages.from === key.dataUpdatedAt ? pages : undefined;
  const detail = useMemo<KeyDetail | undefined>(() => {
    if (first === undefined || held === undefined) return first;
    const whole: KeyDetail = { ...first, entries: [...first.entries, ...held.entries] };
    // The last page read says whether one is left: none, and `next` goes.
    if (held.next === undefined) delete whole.next;
    else whole.next = held.next;
    return whole;
  }, [first, held]);

  const more = async (page: string) => {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    const from = key.dataUpdatedAt;
    setLoading(true);
    setFailed(undefined);
    try {
      const answer = await getKey(cluster, data.instance, db, row, controller.signal, page);
      const read = answer.key;
      if (read === undefined) return;
      setPages((p) => {
        const before = p !== undefined && p.from === from ? p.entries : [];
        return { from, entries: [...before, ...read.entries], next: read.next };
      });
    } catch (error) {
      // A page that failed is said beside the button; what is loaded stays.
      if (!controller.signal.aborted) setFailed(error);
    } finally {
      if (inFlight.current === controller) {
        inFlight.current = null;
        setLoading(false);
      }
    }
  };

  return (
    <aside
      aria-label="Key"
      data-key-panel={row.name}
      className="flex w-full min-w-0 flex-col gap-2 rounded-lg border border-line bg-panel p-3 lg:sticky lg:top-2 lg:flex-1"
    >
      <header className="flex items-start gap-2">
        <h2 ref={name} className="min-w-0 flex-1 font-mono text-sm break-all">
          {row.name}
        </h2>
        <CopyButton value={row.name} label="Copy the key's name" selectRef={name} />
        <CloseButton onClick={onClose} label="Close the key" />
      </header>
      {detail === undefined ? (
        key.isError ? (
          <ProblemNotice error={key.error} onRetry={() => void key.refetch()} />
        ) : (
          <Pending what="this key" />
        )
      ) : (
        <Value detail={detail} origin={origin} onMore={(page) => void more(page)} loading={loading} failed={failed} />
      )}
    </aside>
  );
}

/** Why a page could not be read, in the route's own sentence when there is one. */
function failure(error: unknown): string {
  return error instanceof Error && error.message !== '' ? error.message : 'the read failed';
}

function Value({
  detail,
  origin,
  onMore,
  loading,
  failed,
}: {
  detail: KeyDetail;
  origin: Origin;
  onMore: (page: string) => void;
  loading: boolean;
  failed: unknown;
}): ReactNode {
  const loaded = loadedNote(detail);
  const partial = isPartial(detail)
    ? [detail.note, loaded].filter((t): t is string => t !== undefined && t !== '').join(' ')
    : undefined;
  const file = (ext: string) => fileName([origin.instance.namespace, origin.instance.service, `db${origin.db}`, detail.name], ext);
  const of = partial === undefined ? '' : ` Partial: ${partial}`;
  return (
    <>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
        <TypeBadge type={detail.type} />
        <span>expires: {detail.ttl}</span>
        {detail.length === undefined ? null : (
          <span>{detail.type === 'string' ? plural(detail.length, 'byte') : plural(detail.length, 'element')}</span>
        )}
        {detail.encoding === undefined ? null : <span>encoding {detail.encoding}</span>}
        {detail.memory === undefined ? null : <span>{detail.memory} in memory</span>}
        <span className="flex-1" />
        <Exports
          label="Value actions"
          partial={partial}
          actions={[
            {
              name: 'copy',
              label: 'Copy',
              // The same text Export RAW writes, so the two never differ: the
              // value as stored, not as decoded or indented on screen.
              title: `Copy what Export RAW holds: the value as stored, a hash as field and value per line.${of}`,
              copy: () => valueRaw(detail),
            },
            {
              name: 'json',
              label: 'Export JSON',
              title: `This value as JSON, with its type, expiry and where it came from.${of}`,
              file: () => ({ text: valueJson(origin, detail), file: file('json'), type: 'application/json' }),
            },
            {
              name: 'raw',
              label: 'Export RAW',
              title: `This value as text, as stored: a hash as field and value per line.${of}`,
              file: () => ({ text: valueRaw(detail), file: file('txt'), type: 'text/plain' }),
            },
          ]}
        />
      </div>
      {detail.note === undefined ? null : (
        <p data-note="" className="text-xs text-warn">
          {detail.note}
        </p>
      )}
      {detail.entries.length === 0 ? null : detail.type === 'string' ? (
        <StringValue text={detail.entries[0]?.value ?? ''} raw={detail.entries[0]?.raw} binary={detail.entries[0]?.binary === true} />
      ) : (
        <Entries detail={detail} />
      )}
      {detail.next === undefined || detail.next === '' ? null : (
        <div data-paging="" className="flex flex-wrap items-center gap-2 text-xs">
          <span data-loaded="" className="text-muted">
            {loaded}
          </span>
          <button
            type="button"
            data-load-more=""
            disabled={loading}
            onClick={() => onMore(detail.next ?? '')}
            title="Read the next page of this key: one more tunnel."
            className={btnSecondary}
          >
            {loading ? 'Loading…' : 'Load more'}
          </button>
          {failed === undefined ? null : (
            <span role="alert" data-page-failed="" className="text-warn">
              The next page could not be read: {failure(failed)}
            </span>
          )}
        </div>
      )}
    </>
  );
}

/**
 * A value decoded (see `decode.ts`): at once when no inflation is needed, and
 * after it when one is. `undefined` while a gzip is being inflated.
 */
function useDecoded(value: string, raw: string | undefined, binary: boolean | undefined): Decoded | undefined {
  const now = useMemo(() => decodeNow(value, raw, binary), [value, raw, binary]);
  const [later, setLater] = useState<{ of: Gzip; decoded: Decoded } | undefined>(undefined);
  useEffect(() => {
    if (now.kind !== 'gzip') return;
    let live = true;
    void decode(value, raw, binary).then((decoded) => {
      if (live) setLater({ of: now, decoded });
    });
    return () => {
      live = false;
    };
  }, [now, value, raw, binary]);
  if (now.kind !== 'gzip') return now;
  return later !== undefined && later.of === now ? later.decoded : undefined;
}

/** What was decoded, as a small muted mark; nothing for a value drawn as it came. */
function Mark({ decoded }: { decoded: Decoded }): ReactNode {
  const mark = decodedMark(decoded);
  return mark === undefined ? null : (
    <span data-decoded={mark} className="ml-1 font-sans text-2xs text-muted">
      {mark}
    </span>
  );
}

/**
 * Bytes that read as nothing: their size, muted, and on a click a hex view -
 * offset, sixteen bytes, their ASCII - bounded to its first few KiB. The bytes
 * are never drawn as characters.
 */
function BinaryValue({ data }: { data: Uint8Array }): ReactNode {
  const [open, setOpen] = useState(false);
  const rows = useMemo(() => (open ? hexRows(data) : []), [open, data]);
  return (
    <span className="flex flex-col items-start gap-1">
      <button
        type="button"
        data-binary={data.length}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        title={open ? 'Hide the hex view' : 'Show these bytes in hex'}
        className={`font-sans text-2xs text-muted hover:text-fg ${focusRing}`}
      >
        binary, {bytes(data.length)}
      </button>
      {open ? (
        <pre data-hex="" className="max-h-80 max-w-full overflow-auto rounded-sm border border-line bg-bg p-1 text-2xs whitespace-pre">
          {rows.map((r) => `${r.offset}  ${r.hex.padEnd(48)}  ${r.ascii}`).join('\n')}
          {data.length > HEX_LIMIT ? `\n… the first ${bytes(HEX_LIMIT)} of ${bytes(data.length)}` : ''}
        </pre>
      ) : null}
    </span>
  );
}

/** A text drawn whole when short, by its start with a control to unfold it when long. */
function Folded({ text }: { text: string }): ReactNode {
  const start = useMemo(() => preview(text), [text]);
  const [open, setOpen] = useState(false);
  if (start === undefined) return text;
  return (
    <>
      {open ? text : `${start}…`}
      <button
        type="button"
        data-unfold=""
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className={`ml-1 font-sans text-2xs text-muted underline-offset-2 hover:text-fg hover:underline ${focusRing}`}
      >
        {open ? 'fold' : `show all (${plural(text.length, 'character')})`}
      </button>
    </>
  );
}

/** One element's value in the table: decoded, folded when long, binary drawn as its size. */
function ValueCell({ entry }: { entry: Entry }): ReactNode {
  const decoded = useDecoded(entry.value, entry.raw, entry.binary);
  return (
    <>
      {decoded === undefined ? (
        <span className="font-sans text-2xs text-muted">inflating…</span>
      ) : decoded.kind === 'binary' ? (
        <BinaryValue data={decoded.bytes} />
      ) : (
        <>
          <Folded text={decoded.text} />
          <Mark decoded={decoded} />
          {decoded.lossy === true ? <span className="ml-1 font-sans text-2xs text-muted">not text</span> : null}
        </>
      )}
      {decoded?.kind === 'binary' && decodedMark(decoded) !== undefined ? <Mark decoded={decoded} /> : null}
      {entry.clipped === true ? (
        <span data-clipped="" className="ml-1 font-sans text-2xs text-warn">
          {entry.bytes !== undefined ? `clipped: ${bytes(entry.bytes)} in all` : 'clipped'}
        </span>
      ) : null}
    </>
  );
}

/**
 * A string: decoded when it is JSON, base64 or gzip, with the stored text one
 * click away; drawn as its size and in hex when nothing in it reads.
 */
function StringValue({ text, raw, binary }: { text: string; raw: string | undefined; binary: boolean }): ReactNode {
  const decoded = useDecoded(text, raw, binary);
  const [stored, setStored] = useState(false);
  if (decoded === undefined) return <Pending what="inflating this value" />;
  if (decoded.kind === 'binary') {
    return (
      <div data-string="binary" className="flex flex-col gap-1">
        <BinaryValue data={decoded.bytes} />
        <Mark decoded={decoded} />
      </div>
    );
  }
  const changed = decoded.json || decoded.via.length > 0;
  const shown = changed && !stored ? decoded.text : text;
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2 text-2xs">
        {!changed ? (
          <span className="text-muted">{decoded.lossy === true ? 'not text: bytes that are not UTF-8 are drawn as �' : 'text'}</span>
        ) : (
          <>
            <span className="flex rounded-sm border border-line" role="group" aria-label="String layout">
              {(['json', 'raw'] as const).map((v) => (
                <button
                  key={v}
                  type="button"
                  data-string-view={v}
                  aria-pressed={(v === 'raw') === stored}
                  onClick={() => setStored(v === 'raw')}
                  className={`px-1.5 py-px ${(v === 'raw') === stored ? 'bg-panel-2 text-fg' : 'text-muted'} ${focusRing}`}
                >
                  {v === 'raw' ? 'as stored' : decoded.json ? 'JSON, formatted' : 'decoded'}
                </button>
              ))}
            </span>
            <Mark decoded={decoded} />
          </>
        )}
      </div>
      <pre
        data-string={changed && !stored ? (decoded.json ? 'json' : 'decoded') : 'text'}
        className="max-h-[70vh] overflow-auto rounded-md border border-line bg-bg p-2 font-mono text-xs wrap-anywhere whitespace-pre-wrap"
      >
        {shown}
      </pre>
    </div>
  );
}

/**
 * A hash as a field table, a list with its indexes, a set, a sorted set with
 * its scores, a stream with its entry IDs.
 *
 * **A field or an ID is one line, at most 40 % of the table**, cut with « … »
 * and whole in its title: a name that wrapped onto as many lines as it had
 * characters made the table unreadable, and a name allowed its whole width
 * left the value no room. The cap is a container width (`cqw`), less the
 * cell's padding, because a table cell takes no percentage maximum; the value
 * cell is `w-full` so the width a short value leaves goes to it, not to the
 * name.
 */
function Entries({ detail }: { detail: KeyDetail }): ReactNode {
  const named = detail.type === 'hash' || detail.type === 'stream';
  const head =
    detail.type === 'hash'
      ? ['field', 'value']
      : detail.type === 'stream'
        ? ['id', 'entry']
        : detail.type === 'zset'
          ? ['score', 'member']
          : detail.type === 'list'
            ? ['#', 'element']
            : ['member'];
  return (
    <div className="@container max-h-[70vh] overflow-auto rounded-md border border-line">
      <table data-entries={detail.type} className="w-full font-mono text-xs">
        <thead className="sticky top-0 bg-panel">
          <tr className="text-left text-2xs text-muted">
            {head.map((h) => (
              <th key={h} className="px-2 py-1 font-sans font-normal">
                {h}
              </th>
            ))}
            <th className="w-8" />
          </tr>
        </thead>
        <tbody>
          {detail.entries.map((entry, i) => (
            <tr key={i} className="border-t border-line align-top hover:bg-panel-2">
              {named ? (
                <td className={`px-2 py-0.5 ${detail.type === 'hash' ? 'text-plate-green-pale' : 'text-muted'}`}>
                  <span
                    data-field=""
                    title={entry.field}
                    className="block max-w-[calc(40cqw-1rem)] truncate whitespace-nowrap" // style-allow: 40 % of the table less the cell's padding, a share no theme step names
                  >
                    {entry.field}
                  </span>
                </td>
              ) : null}
              {detail.type === 'zset' ? <td className="px-2 py-0.5 whitespace-nowrap text-muted">{entry.score}</td> : null}
              {detail.type === 'list' ? <td className="px-2 py-0.5 text-muted tabular-nums">{i}</td> : null}
              <td className="w-full px-2 py-0.5 wrap-anywhere whitespace-pre-wrap">
                <ValueCell entry={entry} />
              </td>
              <td className="px-1 py-0.5">
                <CopyButton value={entry.value} label={entry.field === undefined ? 'Copy this element' : `Copy the value of ${entry.field}`} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
