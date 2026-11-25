import type { ReactNode } from 'react';

import { CopyButton } from './CopyButton';

/** An image reference split the way a person reads it: where it comes from,
 *  and which version. */
export interface ImageParts {
  /** The whole reference, as the spec wrote it. */
  full: string;
  /** Registry and repository: `ghcr.io/team/app`. */
  repository: string;
  /** The tag, empty when the reference has none. */
  tag: string;
  /** The digest, `sha256:…`, empty when the reference has none. */
  digest: string;
  /** What the badge says: the tag, else the short digest, else `latest`. */
  version: string;
  /** No tag and no digest: the runtime pulls `latest`. */
  implicit: boolean;
}

/** How many hex characters a short digest keeps. */
const SHORT_DIGEST = 8;

/**
 * Splits an image reference. The tag is a colon after the last slash, so a
 * registry's port (`registry:5000/app`) is never read as one; the digest is
 * after `@`. With neither, the version is the `latest` the runtime pulls.
 */
export function imageParts(image: string | undefined): ImageParts {
  const full = (image ?? '').trim();
  const at = full.indexOf('@');
  const name = at < 0 ? full : full.slice(0, at);
  const digest = at < 0 ? '' : full.slice(at + 1);
  const slash = name.lastIndexOf('/');
  const colon = name.lastIndexOf(':');
  const tagged = colon > slash;
  const repository = tagged ? name.slice(0, colon) : name;
  const tag = tagged ? name.slice(colon + 1) : '';
  const implicit = tag === '' && digest === '';
  return { full, repository, tag, digest, version: tag !== '' ? tag : digest !== '' ? shortDigest(digest) : 'latest', implicit };
}

/** `sha256:` and the first hex characters of a digest. */
export function shortDigest(digest: string): string {
  const colon = digest.indexOf(':');
  const algorithm = colon < 0 ? '' : digest.slice(0, colon + 1);
  const hex = colon < 0 ? digest : digest.slice(colon + 1);
  return algorithm + hex.slice(0, SHORT_DIGEST);
}

/**
 * An image as one line that never loses its version (#110): the repository,
 * cut from the left when it does not fit (`…team/app`), so its end stays
 * readable, then the tag - or the short digest - as a badge that is never cut,
 * then a copy of the whole reference. The full reference is the hover.
 */
export function ImageRef({ image, className = '' }: { image: string | undefined; className?: string }): ReactNode {
  const parts = imageParts(image);
  if (parts.full === '') return null;
  return (
    <span data-image={parts.full} className={`flex min-w-0 items-center gap-1.5 ${className}`} title={parts.full}>
      {/* rtl moves the ellipsis to the left; the bdi keeps the name itself
          left to right, slashes and dots in place. */}
      <span data-image-repository="" dir="rtl" className="min-w-0 truncate text-left font-mono text-xs text-muted">
        <bdi dir="ltr">{parts.repository}</bdi>
      </span>
      <span
        data-image-version={parts.implicit ? 'implicit' : parts.tag !== '' ? 'tag' : 'digest'}
        className={`shrink-0 rounded-sm border px-1 font-mono text-2xs leading-4 ${
          parts.implicit ? 'border-line text-muted' : 'border-accent/40 bg-accent/10 text-fg'
        }`}
        title={parts.implicit ? 'latest (implicit)' : parts.digest !== '' ? parts.digest : parts.tag}
      >
        {parts.version}
      </span>
      <span className="shrink-0">
        <CopyButton value={parts.full} label="Copy image" />
      </span>
    </span>
  );
}
