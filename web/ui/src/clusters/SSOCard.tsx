import type { ReactNode } from 'react';

import { formatInstant } from '../components/instant';
import { BADGE } from './badge';
import type { SSOCards, SSOSession } from './ssoSessions';

/**
 * The AWS SSO session's own card on the home, beside the cluster cards.
 * What each answer draws is `ssoSessions.ts`; this is the
 * drawing. Not a link: there is no page behind a session, and signing in from
 * the console is not offered - the command is what to type, on the host.
 */

/** The AWS CLI's own command for setting up an SSO session. */
const CONFIGURE = 'aws configure sso';

const CARD = 'flex h-full min-w-0 flex-col gap-2 rounded-2xl border p-4';

const STATE: Readonly<Record<SSOSession['state'], { word: string; dot: string; text: string }>> = {
  valid: { word: 'signed in', dot: 'bg-ok', text: 'text-ok' },
  renewable: { word: 'renewable', dot: 'bg-warn', text: 'text-warn' },
  expired: { word: 'expired', dot: 'bg-bad', text: 'text-bad' },
};

/** The name a session is drawn under: the cache may carry neither a session
 *  name nor a start URL. */
export function sessionLabel(session: SSOSession): string {
  return session.name === '' ? 'Unnamed session' : session.name;
}

/** The `<li>`s the grid gains, or none. `container` is the containerized
 *  desktop, whose AWS CLI cache is the container's own or a mount. */
export function SSOCardItems({ cards, container = false }: { cards: SSOCards; container?: boolean }): ReactNode {
  switch (cards.kind) {
    case 'hidden':
    case 'waiting':
      return null;
    case 'unread':
      return (
        <li className="min-w-0">
          <UnreadCard why={cards.why} paths={cards.paths ?? []} />
        </li>
      );
    case 'none':
      return (
        <li className="min-w-0">
          <GhostCard container={container} />
        </li>
      );
    case 'sessions': {
      const keys = sessionKeys(cards.sessions);
      return cards.sessions.map((session, index) => (
        <li key={keys[index]} className="min-w-0">
          <SessionCard session={session} lapsed={index === 0 ? cards.lapsed : undefined} />
        </li>
      ));
    }
  }
}

/** A key per session that a reorder keeps: the name, which the server makes
 *  unique, and for the unnamed ones - whose name is empty, and which the server
 *  keeps apart - their expiry and their rank among the unnamed. */
export function sessionKeys(sessions: readonly SSOSession[]): string[] {
  const unnamed = new Map<string, number>();
  return sessions.map((session) => {
    if (session.name !== '') return `name:${session.name}`;
    const at = session.expiresAt ?? '';
    const seen = unnamed.get(at) ?? 0;
    unnamed.set(at, seen + 1);
    return `unnamed:${at}:${String(seen)}`;
  });
}

function Heading({ dot, name }: { dot: string; name: string }): ReactNode {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span aria-hidden="true" className={`inline-block size-2 shrink-0 rounded-full ${dot}`} />
      <h2 className="min-w-0 flex-1 truncate text-sm font-semibold" title={name}>
        {name}
      </h2>
      <span className={BADGE}>AWS SSO</span>
    </div>
  );
}

function SessionCard({ session, lapsed }: { session: SSOSession; lapsed: string | undefined }): ReactNode {
  const state = STATE[session.state];
  const at = session.expiresAt;
  return (
    <article
      data-sso-card={session.name}
      data-sso-state={session.state}
      className={`${CARD} border-line bg-panel`}
    >
      <Heading dot={state.dot} name={sessionLabel(session)} />
      <p className="flex flex-wrap items-baseline gap-x-2 pl-4 text-xs text-muted">
        <span className={`font-medium ${state.text}`}>{state.word}</span>
        {at === undefined ? null : (
          <span data-sso-expiry="">
            {session.state === 'valid' ? 'until' : 'since'} <time dateTime={at}>{formatInstant(at)}</time>
          </span>
        )}
      </p>
      {session.state === 'renewable' && lapsed === undefined ? (
        <p className="pl-4 text-xs text-muted">The AWS CLI renews it on its next call.</p>
      ) : null}
      {session.state === 'renewable' && lapsed !== undefined ? (
        <div className="flex flex-col gap-1 pl-4 text-xs" data-sso-lapsed="">
          <span className="text-muted">The AWS CLI did not renew it and the clusters fail. Sign in again on the host:</span>
          <code data-command="" className="break-all rounded-md bg-panel-2 px-2 py-1 font-mono text-fg">
            {lapsed}
          </code>
        </div>
      ) : null}
      {session.state === 'expired' && session.command !== undefined && session.command !== '' ? (
        <div className="flex flex-col gap-1 pl-4 text-xs">
          <span className="text-muted">Sign in again on the host:</span>
          <code data-command="" className="break-all rounded-md bg-panel-2 px-2 py-1 font-mono text-fg">
            {session.command}
          </code>
        </div>
      ) : null}
    </article>
  );
}

/** Read, and there is none: a dashed card where a session's would be, with the
 *  command that configures one. In a container the cache read is the one the
 *  container sees - its own, or the host's `~/.aws` where it is mounted - and
 *  the card says that rather than "this machine". */
function GhostCard({ container }: { container: boolean }): ReactNode {
  return (
    <article data-sso-card="" data-sso-state="none" className={`${CARD} border-dashed border-line text-muted`}>
      <Heading dot="bg-muted" name="No AWS SSO session" />
      <p className="pl-4 text-xs" data-sso-where="">
        {container
          ? 'No AWS SSO session is configured in the AWS CLI cache this container sees.'
          : 'No AWS SSO session is configured on this machine.'}
      </p>
      <div className="flex flex-col gap-1 pl-4 text-xs">
        <span>{container ? "Configure one where this container's ~/.aws comes from:" : 'Configure one:'}</span>
        <code data-command="" className="break-all rounded-md bg-panel-2 px-2 py-1 font-mono text-fg">
          {CONFIGURE}
        </code>
      </div>
    </article>
  );
}

/** Not read: says so, and is never the ghost - an unread answer is not an
 *  empty one. */
function UnreadCard({ why, paths }: { why: string | undefined; paths: readonly string[] }): ReactNode {
  return (
    <article
      data-sso-card=""
      data-sso-state="unread"
      data-state="incomplete"
      className={`${CARD} border-line bg-panel text-muted`}
    >
      <Heading dot="bg-warn" name="AWS SSO session" />
      <p className="pl-4 text-xs">
        <span aria-hidden="true">—</span> not read: the local checks did not say whether a session exists
      </p>
      {why === undefined || why === '' ? null : <p className="break-words pl-4 text-2xs">{why}</p>}
      {paths.length === 0 ? null : (
        <ul className="flex flex-col gap-1 pl-4 text-2xs" data-sso-paths="">
          {paths.map((path) => (
            <li key={path}>
              <code className="break-all rounded-md bg-panel-2 px-1.5 py-0.5 font-mono text-fg">{path}</code>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
