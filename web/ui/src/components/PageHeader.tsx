import type { ReactNode } from 'react';
import { Link, type LinkProps } from 'react-router';

import { focusRing } from '../focus';
import { Icon } from './Icon';

const arrow = `inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted hover:text-fg ${focusRing}`;

/**
 * The one way back, a plain arrow with no frame. A link where the way back is
 * an address (a middle-click opens it too), a button where it is a step back
 * in the tab's history.
 */
export function BackArrow({
  to,
  state,
  onClick,
  label = 'Back',
  ...data
}: {
  to?: LinkProps['to'];
  state?: unknown;
  onClick?: () => void;
  label?: string;
  [attribute: `data-${string}`]: string | undefined;
}): ReactNode {
  if (to === undefined) {
    return (
      <button type="button" onClick={onClick} aria-label={label} title={label} className={arrow} {...data}>
        <Icon name="back" />
      </button>
    );
  }
  return (
    <Link to={to} state={state} aria-label={label} title={label} className={arrow} {...data}>
      <Icon name="back" />
    </Link>
  );
}

/**
 * The header of a page, a drawer or an extra's sub-view: the way back at the
 * left, then the kind as a small label, the name in bold under it and the
 * namespace (and the cluster, where it helps) in muted type; the actions stay
 * at the right. A header with no way back (the side panel) lines up the same.
 *
 * `heading` is the heading's level (`h1` on a page, `h2` in a drawer).
 * `badges` sit beside the name, `children` under the title block.
 */
export function PageHeader({
  back,
  kind,
  name,
  namespace,
  cluster,
  badges,
  actions,
  heading: Heading = 'h2',
  className = 'border-b border-line px-3 py-2',
}: {
  back?: ReactNode;
  kind: string;
  name: string;
  namespace?: string | undefined;
  cluster?: string;
  badges?: ReactNode;
  actions?: ReactNode;
  heading?: 'h1' | 'h2' | 'h3';
  className?: string;
}): ReactNode {
  const context = [namespace, cluster].filter((part) => part !== undefined && part !== '').join(' · ');
  return (
    <header className={`flex items-start gap-2 ${className}`} data-page-header="">
      {back}
      <div className="min-w-0 flex-1">
        <p className="truncate text-2xs tracking-eyebrow text-muted uppercase" data-header-kind="">
          {kind}
        </p>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2">
          {/* `title` as well: a name is one unbreakable token and a narrow
              column truncates plenty of them. */}
          <Heading title={name} className="max-w-full shrink-0 truncate text-lg font-semibold">
            {name}
          </Heading>
          {badges}
        </div>
        {context === '' ? null : (
          <p className="truncate text-xs text-muted" data-header-context="">
            {context}
          </p>
        )}
      </div>
      {actions}
    </header>
  );
}
