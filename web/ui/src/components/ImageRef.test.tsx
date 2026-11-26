import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ImageLine, ImageRef, imageParts, shortDigest } from './ImageRef';

afterEach(cleanup);

const DIGEST = 'sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08';

/**
 * An image's version is never lost to its repository's length (#110): the tag,
 * or a short digest, is its own badge; the repository is cut from the left.
 */
describe('an image reference', () => {
  it('splits a long repository and its tag, a port never read as one', () => {
    const p = imageParts('registry.internal.example.com:5000/platform-team/payments/checkout-api-service:2.14.3-rc.2');
    expect(p.repository).toBe('registry.internal.example.com:5000/platform-team/payments/checkout-api-service');
    expect(p.tag).toBe('2.14.3-rc.2');
    expect(p.version).toBe('2.14.3-rc.2');
    expect(p.implicit).toBe(false);
  });

  it('shows a digest alone as a short digest', () => {
    const p = imageParts(`docker.io/envoyproxy/envoy@${DIGEST}`);
    expect(p.repository).toBe('docker.io/envoyproxy/envoy');
    expect(p.tag).toBe('');
    expect(p.digest).toBe(DIGEST);
    expect(p.version).toBe('sha256:9f86d081');
  });

  it('shows the tag of a tag and a digest, the digest kept', () => {
    const p = imageParts(`ghcr.io/team/log-shipper:1.8.0@${DIGEST}`);
    expect(p.repository).toBe('ghcr.io/team/log-shipper');
    expect(p.version).toBe('1.8.0');
    expect(p.digest).toBe(DIGEST);
  });

  it('names the latest an image with no tag pulls, as implicit', () => {
    for (const image of ['busybox', 'registry:5000/tools/busybox']) {
      const p = imageParts(image);
      expect(p.version).toBe('latest');
      expect(p.implicit).toBe(true);
      expect(p.repository).toBe(image);
    }
  });

  it('cuts a digest to its algorithm and eight characters', () => {
    expect(shortDigest(DIGEST)).toBe('sha256:9f86d081');
    expect(shortDigest('abcdef0123456789')).toBe('abcdef01');
  });

  it('draws the version as a badge apart from the repository, the whole reference on hover and in its copy', () => {
    const image = `registry.internal.example.com:5000/platform-team/payments/checkout-api-service:2.14.3-rc.2`;
    render(<ImageRef image={image} />);
    const root = document.querySelector('[data-image]');
    expect(root?.getAttribute('title')).toBe(image);
    const repo = document.querySelector('[data-image-repository]');
    // Cut from the left: the ellipsis falls before the end of the name.
    expect(repo?.getAttribute('dir')).toBe('rtl');
    expect(repo?.className).toContain('truncate');
    expect(repo?.textContent).toBe('registry.internal.example.com:5000/platform-team/payments/checkout-api-service');
    const badge = document.querySelector('[data-image-version]');
    expect(badge?.textContent).toBe('2.14.3-rc.2');
    // The repository gives way first: it shrinks far faster than the badge.
    expect(repo?.className).toContain('shrink-[999]');
    expect(document.querySelector('button[aria-label="Copy image"]')).not.toBeNull();
  });

  it('reads as a reference when selected: the separator is drawn', () => {
    for (const [image, read] of [
      ['ghcr.io/team/app:1.2.3', 'ghcr.io/team/app:1.2.3'],
      [`docker.io/envoyproxy/envoy@${DIGEST}`, 'docker.io/envoyproxy/envoy@sha256:9f86d081'],
      [`ghcr.io/team/app:1.8.0@${DIGEST}`, 'ghcr.io/team/app:1.8.0'],
      ['busybox', 'busybox:latest'],
    ] as const) {
      render(<ImageRef image={image} />);
      const line = document.querySelector('[data-image-repository]')?.parentElement;
      expect(line?.textContent).toBe(read);
      cleanup();
    }
  });

  it('cuts a tag longer than the line at its end, whole in its hover', () => {
    const tag = 'v'.repeat(128);
    render(<ImageRef image={`ghcr.io/team/app:${tag}`} />);
    const badge = document.querySelector('[data-image-version]');
    expect(badge?.className).toContain('truncate');
    expect(badge?.className).toContain('max-w-full');
    expect(badge?.getAttribute('title')).toBe(tag);
  });

  it('names a tag and its digest in the badge\'s hover', () => {
    render(<ImageRef image={`ghcr.io/team/app:1.8.0@${DIGEST}`} />);
    expect(document.querySelector('[data-image-version]')?.getAttribute('title')).toBe(`1.8.0@${DIGEST}`);
  });

  it('draws no copy button inside a control of its own', () => {
    render(<ImageRef image="ghcr.io/team/app:1" copy={false} />);
    expect(document.querySelector('button')).toBeNull();
    expect(document.querySelector('[data-image]')?.getAttribute('title')).toBe('ghcr.io/team/app:1');
  });

  it('marks the implicit latest, and draws nothing for no image', () => {
    render(<ImageRef image="busybox" />);
    expect(document.querySelector('[data-image-version]')?.getAttribute('data-image-version')).toBe('implicit');
    cleanup();
    render(<ImageRef image="  " />);
    expect(document.querySelector('[data-image]')).toBeNull();
    cleanup();
    // A container whose image the server did not send draws nothing, never a crash.
    render(<ImageRef image={undefined} />);
    expect(document.querySelector('[data-image]')).toBeNull();
  });
});

/**
 * A container's image on a line of its own (#136): the repository cut in its
 * middle, its last segment kept apart so it stays; the version as a badge; the
 * copy of the exact reference.
 */
describe('an image line', () => {
  const LONG = 'registry.internal.example.com:5000/platform-team/payments/checkout-api-service:2.14.3-rc.2';

  it('keeps the path start and the image name apart, so the cut falls in the middle', () => {
    render(<ImageLine image={LONG} />);
    const head = document.querySelector('[data-image-head]');
    const name = document.querySelector('[data-image-name]');
    expect(head?.textContent).toBe('registry.internal.example.com:5000/platform-team/payments');
    expect(name?.textContent).toBe('/checkout-api-service');
    // The start gives way far sooner than the name, and never to nothing.
    expect(head?.className).toContain('shrink-[999]');
    expect(head?.className).toContain('min-w-[2ch]');
    expect(head?.className).toContain('truncate');
    expect(name?.className).toContain('truncate');
    // Never a share of the shrink while the start can give way.
    expect(name?.className).toContain('shrink-0');
    expect(name?.className).toContain('max-w-[calc(100%-2ch)]');
    expect(document.querySelector('[data-image-separator]')?.textContent).toBe(':');
    expect(document.querySelector('[data-image-version]')?.textContent).toBe('2.14.3-rc.2');
    expect(document.querySelector('[data-image-line]')?.getAttribute('title')).toBe(LONG);
  });

  it('draws a name with no path as the name alone', () => {
    render(<ImageLine image="busybox" />);
    expect(document.querySelector('[data-image-head]')).toBeNull();
    expect(document.querySelector('[data-image-name]')?.textContent).toBe('busybox');
    expect(document.querySelector('[data-image-name]')?.className).toContain('min-w-0');
    expect(document.querySelector('[data-image-name]')?.className).not.toContain('shrink-0');
    expect(document.querySelector('[data-image-version]')?.getAttribute('data-image-version')).toBe('implicit');
  });

  it('writes @ before a digest, and puts tag and digest in the badge hover', () => {
    render(<ImageLine image={`docker.io/envoyproxy/envoy@${DIGEST}`} />);
    expect(document.querySelector('[data-image-separator]')?.textContent).toBe('@');
    expect(document.querySelector('[data-image-version]')?.textContent).toBe('sha256:9f86d081');
    cleanup();
    render(<ImageLine image={`ghcr.io/o/log-shipper:1.8.0@${DIGEST}`} />);
    expect(document.querySelector('[data-image-version]')?.getAttribute('title')).toBe(`1.8.0@${DIGEST}`);
  });

  it('wraps the version and the copy as one group, each part sized by its content', () => {
    render(<ImageLine image={LONG} />);
    const line = document.querySelector('[data-image-line]');
    expect(line?.className).toContain('flex-wrap');
    const groups = [...(line?.children ?? [])];
    expect(groups).toHaveLength(2);
    for (const group of groups) expect(group.className).toContain('flex-[0_1_auto]');
    expect(groups[1]?.querySelector('[data-image-version]')).not.toBeNull();
    expect(groups[1]?.querySelector('button')).not.toBeNull();
  });

  it('copies the reference exactly as the spec wrote it, not the text it shows', async () => {
    // Tag and digest: the line shows `…/log-shipper:1.8.0`, the copy is the
    // whole reference with its digest.
    const pinned = `ghcr.io/observability-collective/log-shipper:1.8.0@${DIGEST}`;
    const written: string[] = [];
    vi.stubGlobal('isSecureContext', true);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn(async (text: string) => void written.push(text)) },
    });
    try {
      render(<ImageLine image={pinned} />);
      fireEvent.click(screen.getByRole('button', { name: 'Copy image' }));
      await waitFor(() => expect(written).toEqual([pinned]));
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard');
      vi.unstubAllGlobals();
    }
  });

  it('draws nothing for no image', () => {
    render(<ImageLine image={undefined} />);
    expect(document.querySelector('[data-image-line]')).toBeNull();
  });
});

