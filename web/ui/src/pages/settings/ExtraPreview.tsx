import { useState, type ReactNode } from 'react';

import type { ExtensionInfo } from '../../api';
import { btnSecondary } from '../../components/buttons';
import { Dialog } from '../../components/Dialog';
import { Pill } from '../../components/Pill';
import type { Extension } from '../../extension';
import { KeyLayer } from '../../keyboard/layers';
import { AboutBadges, badgesOf, ExtraGlyph, StateBadge } from './extraParts';

/**
 * About one extra, opened from its card in Settings → Extra: what it does,
 * what it reads and what that needs, what must already run in the cluster,
 * and pictures of its pages. **Close is its only button**: the extra is
 * switched from its card or from Edit, never from here.
 *
 * Its state for the cluster is drawn as the card draws it, from the row the
 * listing holds now. Where the build carries no words for the extra, the
 * server's own summary and need are said instead.
 */
export function ExtraPreview({
  cluster,
  item,
  own,
  onClose,
}: {
  cluster: string;
  item: ExtensionInfo;
  /** What the build carries for it: its glyph, its words, its tool badges. */
  own: Extension | undefined;
  onClose: () => void;
}): ReactNode {
  const about = own?.about;
  const description =
    about?.description ?? (item.summary === undefined || item.summary === '' ? undefined : item.summary);
  const requires = about?.requires ?? (item.needs === undefined || item.needs === '' ? undefined : [item.needs]);

  return (
    <Dialog title={item.label} onClose={onClose} layer={KeyLayer.Dialog} size="wide" showClose={false}>
      <div className="flex flex-col gap-4" data-extra-preview={item.key} data-on={item.enabled ? 'true' : 'false'}>
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
          <span data-extra-preview-icon="">
            <ExtraGlyph name={own?.icon} on={item.enabled} size="title" />
          </span>
          <span data-extra-preview-state="">
            <StateBadge on={item.enabled} cluster={cluster} />
          </span>
          <Pill neutral="muted" title={`Cluster ${cluster}`}>
            {cluster}
          </Pill>
          {item.pinned ? (
            <Pill neutral="muted" title="Set by the deployment (KT_EXTRAS)">
              pinned
            </Pill>
          ) : null}
          <AboutBadges names={badgesOf(own)} />
        </div>

        {description === undefined ? null : (
          <p className="text-sm text-fg" data-extra-preview-description="">
            {description}
          </p>
        )}
        {about === undefined ? null : (
          <div className="grid gap-4 sm:grid-cols-2">
            <Facts title="What it reads" items={about.reads} data="reads" />
            <Facts title="Permissions" items={about.permissions} data="permissions" mono />
          </div>
        )}
        {requires === undefined ? null : (
          <Facts title="Requires" items={requires.length === 0 ? ['Nothing beyond the cluster itself.'] : requires} data="requires" />
        )}
        {about?.screenshot === undefined ? null : <Screenshot src={about.screenshot} label={item.label} />}
        {(about?.gallery ?? []).map((picture) => (
          <figure key={picture.src} className="flex flex-col gap-1" data-extra-preview-picture="">
            <Screenshot src={picture.src} label={item.label} alt={picture.caption} />
            <figcaption className="text-xs text-muted">{picture.caption}</figcaption>
          </figure>
        ))}

        <div className="sticky -bottom-4 -mx-4 -mb-4 flex justify-end border-t border-line bg-panel px-4 py-3">
          <button type="button" className={btnSecondary} onClick={onClose} data-dialog-focus="" data-extra-preview-close="">
            Close
          </button>
        </div>
      </div>
    </Dialog>
  );
}

/** One heading and its list: what it reads, the permissions, what it needs. */
function Facts({
  title,
  items,
  data,
  mono = false,
}: {
  title: string;
  items: readonly string[];
  data: string;
  mono?: boolean;
}): ReactNode {
  return (
    <section className="flex flex-col gap-1" data-extra-preview-facts={data}>
      <h3 className="text-xs font-medium tracking-wide text-muted uppercase">{title}</h3>
      <ul className={`flex list-disc flex-col gap-0.5 pl-4 text-xs text-fg ${mono ? 'font-mono' : ''}`}>
        {items.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </section>
  );
}

/** The extra's page as a picture. An image that cannot be loaded draws
 *  nothing rather than the browser's broken-image box. */
function Screenshot({ src, label, alt }: { src: string; label: string; alt?: string }): ReactNode {
  const [broken, setBroken] = useState(false);
  if (broken) return null;
  return (
    <img
      src={src}
      alt={alt ?? `The ${label} page`}
      className="w-full rounded-md border border-line"
      data-extra-preview-screenshot=""
      onError={() => setBroken(true)}
    />
  );
}
