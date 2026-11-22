import { shortDuration } from '@kubetower/ui';
import type { Application } from './data';

/**
 * The tile's facts in a few characters each, so a tile says a little more
 * than its two words without growing.
 */

/** A git sha as Argo CD's UI shortens it; anything else (a chart version, a
 *  tag) as it is. */
export function shortRevision(revision: string): string {
  return /^[0-9a-f]{40}$/.test(revision) ? revision.slice(0, 7) : revision;
}

/**
 * Where the desired state is read from, in one line: `chart version` for a
 * chart, else the repository's last two path segments and the path, then the
 * revision asked for. The whole repository URL is on hover.
 */
export function sourceLine(app: Application): { text: string; title: string } | undefined {
  const src = app.source;
  if (src === undefined || (src.repoURL ?? '') === '') return undefined;
  const repo = src.repoURL ?? '';
  const more = (app.sources ?? 1) > 1 ? ` +${(app.sources ?? 1) - 1}` : '';
  const at = src.targetRevision === undefined || src.targetRevision === '' ? '' : src.targetRevision;
  let text: string;
  if (src.chart !== undefined && src.chart !== '') {
    text = at === '' ? src.chart : `${src.chart} ${at}`;
  } else {
    const short = repo
      .replace(/^[a-z]+:\/\//, '')
      .replace(/^git@[^:]+:/, '')
      .replace(/\.git$/, '')
      .split('/')
      .filter((p) => p !== '')
      .slice(-2)
      .join('/');
    const path = src.path === undefined || src.path === '' || src.path === '.' ? '' : `/${src.path}`;
    text = `${short}${path}${at === '' ? '' : ` @ ${at}`}`;
  }
  const title = [repo, src.path, src.chart, at].filter((p) => p !== undefined && p !== '').join(' · ');
  return { text: text + more, title: more === '' ? title : `${title} (and ${(app.sources ?? 1) - 1} more)` };
}

/** How long ago an instant was, on the console's scale: `34m ago`, `25h ago`. */
export function ago(iso: string, now: number = Date.now()): string {
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '';
  return `${shortDuration(now - at)} ago`;
}
