import type { ReactNode } from 'react';

import type { components } from '../api/schema';
import type { LocalNotice } from './localNotices';

type Remedy = components['schemas']['Remedy'];

/**
 * One machine notice, as short as the fix: what is wrong,
 * what was found, the files concerned and the command to type. The command is
 * absent where the server gave none - a notice it could not measure names no
 * command, since the fix may be a mount rather than a sign-in.
 */
const SEVERITY_TEXT: Readonly<Record<string, string>> = {
  bad: 'text-bad',
  warn: 'text-warn',
  info: 'text-muted',
};

export function LocalNoticeLine({ notice }: { notice: LocalNotice }): ReactNode {
  const paths = notice.paths ?? [];
  return (
    <div
      data-notice={notice.key}
      data-measured={notice.measured === false ? 'false' : 'true'}
      className="flex min-w-0 flex-col gap-1 rounded-md border border-line bg-panel-2 px-2 py-1.5 text-xs"
    >
      <p className={`font-medium ${SEVERITY_TEXT[notice.severity] ?? 'text-warn'}`}>{notice.title}</p>
      {notice.detail === undefined || notice.detail === '' ? null : (
        <p className="break-words text-muted">{notice.detail}</p>
      )}
      {paths.length === 0 ? null : (
        <ul className="flex flex-col font-mono text-2xs text-muted">
          {paths.map((path) => (
            <li key={path} className="break-all">
              {path}
            </li>
          ))}
        </ul>
      )}
      {notice.command === undefined || notice.command === '' ? null : (
        <code data-command="" className="break-all font-mono text-fg">
          {notice.command}
        </code>
      )}
    </div>
  );
}

/** Who fixes this cluster, and how: `Cluster.remedy`, one line. */
export function RemedyLine({ remedy }: { remedy: Remedy }): ReactNode {
  return (
    <p data-cluster-remedy="" className="break-words text-xs text-muted">
      <span className={remedy.actionable === true ? 'font-medium text-fg' : 'font-medium'}>{remedy.who}:</span>{' '}
      {remedy.what}
    </p>
  );
}
