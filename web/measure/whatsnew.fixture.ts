import type { Release } from '../ui/src/whatsnew/changelog';

/**
 * What's new (#80), for the `console-whats-new-<state>` scenes: fake releases
 * and the desktop's answers for a first visit, an upgrade across two releases
 * with highlights, and a release with nothing notable.
 */

export const WHATS_NEW_RELEASES: readonly Release[] = [
  { version: '0.3.0', highlights: [{ title: 'Saved views per kind and cluster', kind: 'added' }, { title: 'Pod logs that follow a restart', kind: 'fixed' }, { title: 'Node pressure on the overview', kind: 'added' }] },
  { version: '0.2.1', highlights: [] },
  { version: '0.2.0', highlights: [{ title: "What's new after an upgrade", kind: 'added' }, { title: 'Install updates from the console', kind: 'fixed' }] },
  { version: '0.1.2', highlights: [] },
];

const PREFERENCES = { rail: { barCount: 9, activeBar: 1, bars: [[]] }, columns: { density: 'comfortable', kinds: {} } };

/** The answers that put the desktop on `release`, having seen `seen`. */
function on(capabilities: Readonly<Record<string, unknown>>, release: string, seen?: string): Readonly<Record<string, unknown>> {
  return {
    '/api/capabilities': { ...capabilities, mode: 'desktop', preferences: true, release },
    '/api/preferences': { ...PREFERENCES, ...(seen === undefined ? {} : { seenVersion: seen }) },
  };
}

export function whatsNewAnswers(capabilities: Readonly<Record<string, unknown>>): Readonly<Record<string, Readonly<Record<string, unknown>>>> {
  return {
    welcome: on(capabilities, '0.3.0'),
    // The changelog link is drawn from the settings' repository address.
    news: { ...on(capabilities, '0.3.0', '0.1.2'), '/api/settings': { info: { repositoryUrl: 'https://github.com/thonicdev/kubetower' } } },
    tour: on(capabilities, '0.2.1', '0.2.0'),
  };
}
