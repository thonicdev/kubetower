import type { ReactNode } from 'react';
import { Link } from 'react-router';
import { CopyButton, Failure, Pill, Tile, focusRing, btnSecondary } from '@kubetower/ui';

import {
  EVIDENCE_WORDS,
  KEY,
  ROLE_SOURCE,
  RedisProblem,
  engineOf,
  roleOf,
  stateOf,
  type Backend,
  type Deployment,
  type Door,
  type Evidence,
  type External,
  type Member,
  type ProbeState,
} from './data';

/** The inventory's page, under the segment the console reserves for an extra. */
export const PAGE_PATH = `x/${KEY}`;

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** The inventory's address, scoped to a namespace when one is given. */
export function inventoryHref(cluster: string, scope: string): string {
  const path = `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}`;
  return scope === '' ? path : `${path}?${new URLSearchParams({ ns: scope }).toString()}`;
}

/**
 * One instance's page. The inventory's own scope travels as `scope`, **not
 * `ns`**: on the instance page the namespace is part of what the page is
 * about, and one name for the two facts is how the way back would take its
 * scope from the instance rather than from the list it came from.
 */
export function instanceHref(cluster: string, door: Pick<Door, 'namespace' | 'service' | 'port'>, scope = ''): string {
  const path = `/c/${encodeURIComponent(cluster)}/${PAGE_PATH}/${encodeURIComponent(door.namespace)}/${encodeURIComponent(door.service)}/${door.port}`;
  return scope === '' ? path : `${path}?${new URLSearchParams({ scope }).toString()}`;
}

/** A Service's Details, in the console. */
export function serviceHref(cluster: string, namespace: string, name: string): string {
  return `/c/${encodeURIComponent(cluster)}/services/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/** Where an ExternalName Service points: its host, and its port when it has one. */
export function externalAddress(door: Pick<External, 'host' | 'port'>): string {
  return door.port === undefined || door.port === 0 ? door.host : `${door.host}:${door.port}`;
}

/**
 * An ExternalName Service to a Redis outside the cluster: no probe, no keys,
 * no tunnel - a pod port-forward cannot reach it. `to` opens its Service.
 */
export function ExternalTile({ door, to }: { door: External; to?: string }): ReactNode {
  const address = externalAddress(door);
  return (
    <Tile
      data-external={`${door.namespace}/${door.service}`}
      title={door.service}
      {...(to === undefined ? {} : { to, openLabel: `Open Service ${door.service}` })}
      badges={
        <Pill neutral="muted" title={`Outside the cluster: ${door.why}`}>
          external
        </Pill>
      }
      detail={`${door.namespace} · ${address}`}
      action={<CopyButton value={address} label="Copy address" />}
    />
  );
}

/**
 * A route's problem, drawn as the kind it is: **refused** (a right or a
 * credential said no - nothing to retry until somebody changes something),
 * **could not read** (a failure, worth another try) or **not here** (an
 * absence). R5 §5: the three send the reader to three different people.
 */
export function ProblemNotice({ error, onRetry, compact = false }: { error: unknown; onRetry?: () => void; compact?: boolean }): ReactNode {
  if (!(error instanceof RedisProblem)) return <Failure error={error} onRetry={onRetry} />;
  const title = error.kind === 'refusal' ? 'Refused' : error.kind === 'absent' ? 'Not here' : 'Could not read';
  const tone = error.kind === 'refusal' ? 'text-warn' : error.kind === 'absent' ? 'text-muted' : 'text-bad';
  return (
    <div
      role="status"
      data-problem={error.code}
      data-kind={error.kind}
      className={compact ? 'flex flex-col gap-0.5 text-xs' : 'flex flex-col gap-1 rounded-lg border border-line bg-panel-2 p-3 text-sm'}
    >
      <p className={`font-medium ${tone}`}>{title}</p>
      <p>{error.message}</p>
      {error.detail === undefined ? null : <p className="text-xs text-muted">{error.detail}</p>}
      {onRetry === undefined || error.kind === 'absent' ? null : (
        <button
          type="button"
          onClick={onRetry}
          className={`self-start ${btnSecondary}`}
        >
          Try again
        </button>
      )}
    </div>
  );
}

/**
 * The probe: **the one control on the inventory that reaches a server**, and it
 * reaches exactly the one the person pointed at. Readiness is not liveness - a
 * pod can be Ready with a Redis inside it that answers nothing - and this is
 * the only thing that can tell. One tunnel, INFO and DBSIZE, closed.
 *
 * Never fired on load, on reveal or on an interval: the tunnels every extension
 * holds are capped together, and an inventory that probed each row by itself
 * would exhaust the cap and put this console in every Redis's connection log.
 */
export function Probe({ backend, state, onProbe }: { backend: Backend; state: ProbeState | undefined; onProbe: () => void }): ReactNode {
  if (!backend.reachable) return null;
  if (state === undefined) {
    return (
      <button
        type="button"
        data-probe=""
        onClick={onProbe}
        title="Open one tunnel to this instance and ask what it is. Nothing on this page reaches a server until you press it."
        className={btnSecondary}
      >
        Probe
      </button>
    );
  }
  if (state.status === 'running') {
    return (
      <span role="status" aria-busy="true" className="text-xs text-muted">
        Probing…
      </span>
    );
  }
  if (state.status === 'answered') {
    const { server, overview } = state.answer;
    return (
      <span data-probed={stateOf(backend, state)} className="flex flex-wrap items-center gap-1.5 text-xs">
        <Pill tone={stateOf(backend, state) === 'reachable' ? 'ok' : 'warn'}>
          {server.engine} {server.version}
        </Pill>
        {overview === undefined ? null : (
          <span className="text-muted">
            {plural(overview.keys, 'key')}
            {overview.usedMemory === undefined ? null : <> · {overview.usedMemory}</>}
          </span>
        )}
        {server.mode === 'standalone' ? null : <span className="text-warn">{server.mode}, not handled</span>}
      </span>
    );
  }
  return (
    <span data-probed={stateOf(backend, state)} className="flex flex-col gap-1">
      <ProblemNotice error={state.error} compact />
      <button
        type="button"
        data-probe=""
        onClick={onProbe}
        className={`self-start ${btnSecondary}`}
      >
        Try again
      </button>
    </span>
  );
}

/** One door onto a Redis: a real link to the instance's page, with the reason
 *  it was offered beside it rather than in a tooltip. */
export function DoorLink({ cluster, door, scope }: { cluster: string; door: Door; scope: string }): ReactNode {
  return (
    <Link
      to={instanceHref(cluster, door, scope)}
      data-door={`${door.service}:${door.port}`}
      title={`Open ${door.namespace}/${door.service}:${door.port} - offered because ${door.why}`}
      className={`inline-flex flex-wrap items-baseline gap-1.5 rounded-md px-1.5 py-0.5 hover:bg-panel-2 ${focusRing}`}
    >
      <span className="font-mono text-xs text-accent">
        {door.service}:{door.port}
      </span>
      <span className="text-2xs text-muted">{door.why}</span>
    </Link>
  );
}

/**
 * A member's role, and what said it. Once the member is probed the server's
 * own `INFO replication` answer replaces the guess, and a guess the server
 * contradicts is shown beside it rather than dropped (roleOf).
 */
export function RoleBadge({ member, probe }: { member: Member; probe: ProbeState | undefined }): ReactNode {
  const said = roleOf(member, probe);
  if (said.role === undefined) {
    return (
      <span data-role="" title="Nothing said what this member is: no role label, no tell-tale name." className="text-xs text-muted">
        —
      </span>
    );
  }
  const source = said.from === undefined ? '' : ROLE_SOURCE[said.from];
  const sentence =
    said.from === 'info'
      ? `${said.pod ?? 'The pod reached'} answers as ${said.role}${said.guessed === undefined ? '' : `; the labels or the name said ${said.guessed}`}`
      : `${said.role}, from ${source}${said.pod === undefined ? '' : ` - the probe reached ${said.pod}`}`;
  return (
    <span data-role={said.role} data-role-from={said.from ?? ''} title={sentence} className="inline-flex items-baseline gap-1">
      <Pill tone={said.role === 'primary' && said.from === 'info' ? 'ok' : undefined}>{said.role}</Pill>
      <span className="text-2xs text-muted">{said.from === 'info' ? 'INFO' : said.from}</span>
      {said.guessed === undefined ? null : <span className="text-2xs text-warn">labels: {said.guessed}</span>}
    </span>
  );
}

/** One member of a deployment, on one compact row: its role, its doors, what
 *  is behind it, and its probe. A Sentinel is drawn, never offered. */
export function MemberRow({
  cluster,
  member,
  scope,
  probe,
  onProbe,
}: {
  cluster: string;
  member: Member;
  scope: string;
  probe: ProbeState | undefined;
  onProbe: () => void;
}): ReactNode {
  const sentinel = member.sentinel === true;
  return (
    <li data-backend={member.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line py-1.5 first:border-t-0">
      <span className="w-28 shrink-0">
        <RoleBadge member={member} probe={probe} />
      </span>
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1 gap-y-0.5">
        {member.doors.map((door) =>
          sentinel ? (
            <span
              key={`${door.service}:${door.port}`}
              data-sentinel-door={`${door.service}:${door.port}`}
              className="px-1.5 font-mono text-xs text-muted"
            >
              {door.service}:{door.port}
            </span>
          ) : (
            <DoorLink key={`${door.service}:${door.port}`} cluster={cluster} door={door} scope={scope} />
          ),
        )}
      </span>
      <span className="max-w-xs truncate font-mono text-xs text-muted" title={member.behind}>
        {member.behind}
      </span>
      {member.reachable ? (
        <Pill tone="ok">{member.ready} ready</Pill>
      ) : (
        // Two facts must not wear one pill without the sentence beside it:
        // nothing behind, and not read, are told apart by `behind`.
        <Pill tone="warn">not serving</Pill>
      )}
      {sentinel ? (
        <span className="text-2xs text-muted" title="A Sentinel watches the Redis servers and holds no keys; this viewer does not open one.">
          Sentinel, not opened
        </span>
      ) : (
        <Probe backend={member} state={probe} onProbe={onProbe} />
      )}
      {member.doors.length > 1 && !sentinel ? (
        <p className="basis-full pl-28 text-2xs text-muted">{member.doors.length} Services, one Redis - the same data behind each name.</p>
      ) : null}
    </li>
  );
}

/** The sentence saying what a deployment's members were folded on. */
export function evidenceSentence(evidence: readonly Evidence[]): string | undefined {
  if (evidence.length === 0) return undefined;
  if (evidence.length === 1 && evidence[0] === 'name') {
    return 'Grouped on their names alone - a hint, not a fact: nothing else here says these Services belong together.';
  }
  return `Grouped by ${evidence.map((e) => EVIDENCE_WORDS[e]).join(', ')}.`;
}

/**
 * One installation of a Redis or a Valkey: its name, what
 * it was folded on, the release and workloads that say so, and one compact row
 * per member. The engine is what a probe found, never guessed from a name.
 */
export function DeploymentCard({
  cluster,
  deployment,
  members,
  scope,
  probes,
  onProbe,
}: {
  cluster: string;
  deployment: Deployment;
  /** The members to draw: all of them, or those a filter kept. */
  members: readonly Member[];
  scope: string;
  probes: Readonly<Record<string, ProbeState>>;
  onProbe: (member: Member) => void;
}): ReactNode {
  const engine = deployment.members.map((m) => engineOf(probes[m.key])).find((e) => e !== undefined);
  const evidence = evidenceSentence(deployment.evidence);
  const nameOnly = deployment.evidence.length === 1 && deployment.evidence[0] === 'name';
  const redis = deployment.members.filter((m) => m.sentinel !== true);
  const services = redis.reduce((n, m) => n + m.doors.length, 0);
  return (
    <article data-deployment={deployment.key} className="flex flex-col gap-1 rounded-lg border border-line bg-panel px-3 py-2">
      <header className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <h3 className="font-mono text-sm font-medium">{deployment.name}</h3>
        {engine === undefined ? null : <Pill>{engine}</Pill>}
        <span className="text-xs text-muted">
          {plural(redis.length, 'member')} · {plural(services, 'Service')}
          {deployment.release === undefined ? null : (
            <>
              {' '}
              · release <span className="font-mono">{deployment.release}</span>
            </>
          )}
          {deployment.chart === undefined ? null : (
            <>
              {' '}
              · chart <span className="font-mono">{deployment.chart}</span>
            </>
          )}
        </span>
        {deployment.workloads.length === 0 ? null : (
          <span data-workloads="" className="font-mono text-2xs text-muted">
            {deployment.workloads.join(' · ')}
          </span>
        )}
      </header>
      {evidence === undefined ? null : (
        <p data-evidence={deployment.evidence.join(' ')} className={`text-2xs ${nameOnly ? 'text-warn' : 'text-muted'}`}>
          {evidence}
        </p>
      )}
      <ul className="flex flex-col">
        {members.map((member) => (
          <MemberRow
            key={member.key}
            cluster={cluster}
            member={member}
            scope={scope}
            probe={probes[member.key]}
            onProbe={() => onProbe(member)}
          />
        ))}
      </ul>
    </article>
  );
}
