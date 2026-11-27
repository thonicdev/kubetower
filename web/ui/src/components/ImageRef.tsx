import type { ReactNode } from 'react';

import { CopyButton } from './CopyButton';
import { DetailTip } from './DetailTip';

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
export function ImageRef({
  image,
  className = '',
  copy = true,
  of,
}: {
  image: string | undefined;
  className?: string;
  /** Whose image it is (a container's name), for the copy's name. */
  of?: string | undefined;
  /** False inside a control of its own (a button), where a second button
   *  cannot go: the reference is then the hover only. */
  copy?: boolean;
}): ReactNode {
  const parts = imageParts(image);
  if (parts.full === '') return null;
  // The separator is drawn, so a selection made with the mouse reads as a
  // reference: `repo:tag`, `repo@sha256:…` (the digest's short form there).
  const separator = parts.tag === '' && parts.digest !== '' ? '@' : ':';
  return (
    <span data-image={parts.full} className={`flex min-w-0 items-center gap-1.5 ${className}`} title={parts.full}>
      <span className="flex min-w-0 items-center font-mono text-xs">
        {/* rtl moves the ellipsis to the left; the bdi keeps the name itself
            left to right, slashes and dots in place. */}
        <span data-image-repository="" dir="rtl" className="min-w-0 shrink-[999] truncate text-left text-muted">
          <bdi dir="ltr">{parts.repository}</bdi>
        </span>
        <span data-image-separator="" className="shrink-0 text-muted">
          {separator}
        </span>
        {/* Never cut while the repository can give way: the repository shrinks
            far faster. A tag longer than the line (up to 128
            characters) is then cut at its end, inside the line, whole in its
            hover - the separator and the copy button keep their room. */}
        <VersionBadge parts={parts} />
      </span>
      {copy ? (
        <span className="shrink-0 whitespace-nowrap">
          <VersionTip parts={parts} />
          <CopyButton value={parts.full} label={copyLabel(parts, of)} />
        </span>
      ) : null}
    </span>
  );
}

/** The repository cut in its middle where it does not fit: the start of the
 *  path, an ellipsis, then the last segment whole (`registry.int…/app`).
 *
 *  With a start to give way, the last segment does not shrink at all: a share
 *  of the shrink by weight, even a fraction of a pixel, is enough for the
 *  browser to draw its ellipsis. It is cut only when it alone is wider than the
 *  line less the start's two characters. With no start, it is the only part,
 *  and is cut at its end. */
function MiddleCut({ repository }: { repository: string }): ReactNode {
  const slash = repository.lastIndexOf('/');
  const head = slash < 0 ? '' : repository.slice(0, slash);
  const tail = slash < 0 ? repository : repository.slice(slash);
  return (
    <span data-image-repository="" className="flex min-w-0 font-mono text-xs text-muted">
      {head === '' ? null : (
        <span data-image-head="" className="min-w-[2ch] shrink-[999] truncate">
          {head}
        </span>
      )}
      <span
        data-image-name=""
        className={head === '' ? 'min-w-0 truncate' : 'max-w-[calc(100%-2ch)] shrink-0 truncate'}
      >
        {tail}
      </span>
    </span>
  );
}

/**
 * The version as a badge: the tag, else the short digest, else `latest`, with
 * tag and digest whole in its hover. A tag longer than the line (up to 128
 * characters) is cut at its end, never past the line.
 */
function versionTitleOf(parts: ImageParts): string {
  return parts.implicit
    ? 'latest (implicit)'
    : parts.tag !== '' && parts.digest !== ''
      ? `${parts.tag}@${parts.digest}`
      : parts.tag !== ''
        ? parts.tag
        : parts.digest;
}

/** The copy's name: whose image, or which. */
function copyLabel(parts: ImageParts, of: string | undefined): string {
  return of === undefined || of === '' ? `Copy image ${parts.full}` : `Copy image of ${of}`;
}

/** The whole version within a keyboard's reach, where the badge shows less
 *  of it (a digest, or a tag with its digest). */
function VersionTip({ parts }: { parts: ImageParts }): ReactNode {
  const whole = versionTitleOf(parts);
  if (parts.implicit || whole === parts.version) return null;
  return <DetailTip text={whole} label={`Version of ${parts.full}`} />;
}

function VersionBadge({ parts }: { parts: ImageParts }): ReactNode {
  const versionTitle = versionTitleOf(parts);
  return (
    <span
      data-image-version={parts.implicit ? 'implicit' : parts.tag !== '' ? 'tag' : 'digest'}
      className={`min-w-0 max-w-full shrink truncate rounded-sm border px-1 font-mono text-2xs leading-4 ${
        parts.implicit ? 'border-line text-muted' : 'border-accent/40 bg-accent/10 text-fg'
      }`}
      title={versionTitle}
    >
      {parts.version}
    </span>
  );
}

/**
 * A container's image on a line of its own (#136), the repository readable:
 * `repository:version ⧉` on one line where it fits. Where it does not, the
 * version and the copy wrap under it as one group, and the repository keeps
 * the whole line, cut in its middle only if it is still too long. The whole
 * reference is the line's hover, and the copy copies it exactly.
 */
export function ImageLine({ image, of }: { image: string | undefined; of?: string | undefined }): ReactNode {
  const parts = imageParts(image);
  if (parts.full === '') return null;
  const separator = parts.tag === '' && parts.digest !== '' ? '@' : ':';
  return (
    <div data-image={parts.full} data-image-line="" className="flex min-w-0 flex-wrap items-center gap-x-1 gap-y-0.5" title={parts.full}>
      {/* Content-sized, so the line breaks only where both do not fit. */}
      <span className="flex max-w-full min-w-0 flex-[0_1_auto] items-center">
        <MiddleCut repository={parts.repository} />
        <span data-image-separator="" className="shrink-0 font-mono text-xs text-muted">
          {separator}
        </span>
      </span>
      <span className="flex max-w-full min-w-0 flex-[0_1_auto] items-center gap-1">
        <VersionBadge parts={parts} />
        <span className="shrink-0 whitespace-nowrap">
          <VersionTip parts={parts} />
          <CopyButton value={parts.full} label={copyLabel(parts, of)} />
        </span>
      </span>
    </div>
  );
}
