import { useQuery } from '@tanstack/react-query';

import type { ValueResult } from '../api';
import type { components } from '../api/schema';
import { localNoticesQuery, type LocalNotice } from './localNotices';

/**
 * The home's AWS SSO card: which sessions the machine's
 * AWS CLI holds, signed in and until when, or expired with the command that
 * signs in again.
 *
 * **The source is `GET /api/health`'s `sso`**, read under the notices' own
 * query key, so the card and the notices are one read every thirty seconds
 * rather than two.
 *
 * ## Four answers, and nothing drawn in the fifth
 *
 * - **sessions**: one card per session, furthest expiry first, as the server
 *   orders them - the home draws one card per thing it lists.
 * - **none**: the cache was read and holds no session. One dashed ghost card
 *   stands where a session's would be.
 * - **unread**: the read failed, or the cache could not be read
 *   (`measured: false`). The card says so, and is **never** the ghost: an
 *   unread answer is not an empty one.
 * - **waiting**: no answer yet. Nothing is drawn, rather than a guess.
 * - **hidden**: the server profile (`mode: shared`), where a pod has no
 *   operator and no `~/.aws/sso` - no card and no ghost - and a profile not
 *   yet known, since drawing before it answers would be a promise.
 */

export type SSOSession = components['schemas']['HealthSSOSession'];
type HealthReport = components['schemas']['HealthReport'];
type Mode = components['schemas']['Capabilities']['mode'];

export type SSOCards =
  | { kind: 'hidden' }
  | { kind: 'waiting' }
  | { kind: 'unread'; why?: string; paths?: readonly string[] }
  | { kind: 'none' }
  | { kind: 'sessions'; sessions: readonly SSOSession[]; lapsed?: string };

/** What the card says, given the profile and the health read. */
export function ssoCardsOf(
  mode: Mode | undefined,
  answer: ValueResult<HealthReport> | undefined,
  failed?: string,
): SSOCards {
  if (mode === undefined || mode === 'shared') return { kind: 'hidden' };
  if (failed !== undefined) return { kind: 'unread', why: failed };
  if (answer === undefined) return { kind: 'waiting' };
  if (answer.state !== 'ok') return { kind: 'unread', why: answer.reason.message };
  const sso = answer.data.sso;
  // A desktop answer without `sso` said nothing about the sessions: not read,
  // rather than none.
  if (sso === undefined) return { kind: 'unread', why: 'the server did not report the SSO sessions' };
  if (!sso.measured) return sso.problem === undefined ? { kind: 'unread' } : { kind: 'unread', why: sso.problem };
  if (sso.sessions.length === 0) return { kind: 'none' };
  return { kind: 'sessions', sessions: sso.sessions };
}

/** The home's read of the SSO sessions, for the profile it runs under. */
export function useSSOCards(mode: Mode | undefined): SSOCards {
  // The notices' own read, which the home makes in every profile anyway.
  const health = useQuery(localNoticesQuery());
  const failed = health.isError
    ? health.error instanceof Error
      ? health.error.message
      : String(health.error)
    : undefined;
  return ssoCardsOf(mode, health.data, failed);
}

/**
 * The card, given the `sso` notice no cluster card claims.
 *
 * The home does not draw that notice above the cards once the SSO card is
 * drawn (`ClusterList`, one fact, one surface), so the card carries what only
 * the notice held:
 *
 * - **an unreadable cache's directory** (`paths`): `HealthSSO` carries no path,
 *   and "permission denied" alone does not say which mount has the wrong owner;
 * - **the command for a renewable session the clusters contradict**: the CLI
 *   was meant to renew it and the clusters fail, so the card's "renews it on its
 *   next call" is the prediction the evidence beat. The notice is about the
 *   probe's session, `SSO()`, which is the server's first session
 *   (`health.SSOSessions`), so it goes on that card and only while it is
 *   `renewable`.
 *
 * A notice a cluster card claims stays there and adds nothing here.
 */
export function withUnclaimedNotice(cards: SSOCards, notice: LocalNotice | undefined): SSOCards {
  if (notice === undefined || notice.key !== 'sso') return cards;
  if (cards.kind === 'unread') {
    const paths = notice.measured === false ? (notice.paths ?? []) : [];
    return paths.length === 0 ? cards : { ...cards, paths };
  }
  if (cards.kind === 'sessions') {
    const command = notice.command ?? '';
    if (notice.measured === false || command === '' || cards.sessions[0]?.state !== 'renewable') return cards;
    return { ...cards, lapsed: command };
  }
  return cards;
}
