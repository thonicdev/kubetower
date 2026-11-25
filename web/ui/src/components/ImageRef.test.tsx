import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { ImageRef, imageParts, shortDigest } from './ImageRef';

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
    expect(badge?.className).toContain('shrink-0');
    expect(badge?.className).not.toContain('truncate');
    expect(document.querySelector('button[aria-label="Copy image"]')).not.toBeNull();
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
