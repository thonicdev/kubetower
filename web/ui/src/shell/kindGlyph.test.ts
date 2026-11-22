/// <reference types="node" />
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import { IconSprite } from '../components/Icon';
import { glyphForEntry, glyphForGroup, glyphForHref, glyphForKind, kindOfHref } from './kindGlyph';

/**
 * Every kind in `kinds.go` resolves to a symbol in the sprite or to
 * `resource`, checked by a test that walks the list.
 *
 * **It walks the server's file rather than a copy of it**, which is the whole
 * value: a copy is right on the day it is written and silently wrong the day a
 * kind is added, which is the case the test exists for. A hand-written list here
 * would assert that this repository agrees with itself.
 *
 * **It reads the file itself, and it has to say how.** Two shorter routes are
 * closed: `fileURLToPath(import.meta.url)`
 * throws `The URL must be of scheme file` under Vite, and a `?raw` import of a
 * path outside `web/` is refused by `server.fs.allow` - `Denied ID
 * .../internal/resources/kinds.go?raw`. So it is `node:fs`, and the
 * triple-slash reference above is what makes that type-check: `tsconfig.json`
 * gives `ui/src` only `vite/client`, and widening that to
 * `["vite/client", "node"]` would open the filesystem to every component in the
 * application in order to satisfy one test.
 *
 * The cost is stated rather than hidden: this test reaches outside `web/`. The
 * image's front-end stage copies `web/` alone, so a run from such a copy cannot
 * find the file - and then it **fails loudly** rather than passing on an empty
 * list: an empty answer and a missing one are different answers. Nothing runs
 * Vitest from that copy; if something does, the
 * failure will say exactly what it could not read.
 */
const KINDS_GO = join(repositoryRoot(), 'internal', 'resources', 'kinds.go');

/**
 * Upwards from the working directory until something holds `go.mod`.
 *
 * `go.mod` is the marker for `check-styles.mjs`'s reason: it is the one file
 * that says *this is the repository* and cannot be anywhere else. Walking up
 * rather than assuming `../` means the test does not depend on which directory
 * Vitest was started in.
 */
function repositoryRoot(): string {
  let at = resolve(process.cwd());
  for (;;) {
    if (existsSync(join(at, 'go.mod'))) return at;
    const up = dirname(at);
    if (up === at) return resolve(process.cwd(), '..');
    at = up;
  }
}

/** Every `Kind:` in the descriptor table, in file order. The indent is matched
 *  with `\s*` rather than the two tabs that are really there: an escaped tab in
 *  a regex literal is what ESLint's `no-control-regex` refuses, and a typed one
 *  is invisible in a diff. Nothing else in that file declares a `Kind:`. */
function everyKind(): string[] {
  let source: string;
  try {
    source = readFileSync(KINDS_GO, 'utf8');
  } catch (cause) {
    throw new Error(
      `cannot read ${KINDS_GO} - this test walks the server's own kind list and ` +
        'there is nothing it can honestly assert without it',
      { cause },
    );
  }
  return [...source.matchAll(/^\s*Kind:\s+"([^"]+)",$/gm)].map((match) => match[1] ?? '');
}

/** The ids the sheet actually defines, read out of the component's own markup
 *  rather than from the name union - a union member with no `<symbol>` behind it
 *  compiles and draws an empty box, which is the failure worth catching. */
function definedSymbols(): Set<string> {
  const element = IconSprite();
  const ids = new Set<string>();
  // Walking React elements rather than rendering: this file needs the list, not
  // a document, and `Icon.test.tsx` is where the sheet is rendered and pinned.
  const visit = (node: unknown): void => {
    if (Array.isArray(node)) {
      for (const child of node) visit(child);
      return;
    }
    if (typeof node !== 'object' || node === null) return;
    const element = node as { props?: { id?: unknown; children?: unknown } };
    const id = element.props?.id;
    if (typeof id === 'string') ids.add(id);
    visit(element.props?.children);
  };
  visit(element);
  return ids;
}

describe("every kind the server can send has a shape, and it is one the sheet defines", () => {
  it('finds the descriptor table where it is, rather than believing an empty read', () => {
    const kinds = everyKind();
    // The count is read from the file, not recalled: 33 grouped
    // kinds plus the three `kinds.go` leaves ungrouped, which `apimount.go`
    // folds into a "Cluster" heading of its own. If this number moves, a kind
    // was added or removed and the assertions below are what say whether it was
    // given a glyph.
    expect(kinds.length).toBe(36);
    expect(kinds).toContain('pods');
    expect(kinds).toContain('events');
  });

  it('answers every one of them with a symbol that exists', () => {
    const defined = definedSymbols();
    const unresolved: string[] = [];
    for (const kind of everyKind()) {
      const glyph = glyphForKind(kind);
      if (!defined.has(`i-${glyph}`)) unresolved.push(`${kind} -> ${glyph}`);
    }
    expect(unresolved).toEqual([]);
  });

  it('gives each of the 36 a shape of its own rather than the generic one', () => {
    // The fallback is for custom resources, which this product has never seen.
    // A *built-in* kind reaching it means the map was not updated when the kind
    // was added - which draws correctly and says nothing, so nothing else would
    // catch it.
    const generic = everyKind().filter((kind) => glyphForKind(kind) === 'resource');
    expect(generic).toEqual([]);
  });

  it('answers every group heading, including the two the server makes itself', () => {
    const defined = definedSymbols();
    for (const key of [
      'workloads',
      'network',
      'configuration',
      'access-control',
      'storage',
      // `apimount.go` folds the ungrouped kinds under this one, and
      // `navKey("Custom Resources")` is the other.
      'cluster',
      'custom-resources',
    ]) {
      expect(defined.has(`i-${glyphForGroup(key)}`)).toBe(true);
    }
    // And the five real groups each have a shape of their own. Custom resources
    // are excluded on purpose: the generic one IS their answer, because what an
    // cluster's owner installed is not a category this product knows.
    for (const key of ['workloads', 'network', 'configuration', 'access-control', 'storage']) {
      expect(glyphForGroup(key)).not.toBe('resource');
    }
  });

  it('heads the Extra group with a shape of its own, not the custom resources one', () => {
    // Drawing the same cube as Custom Resources would make the extras read as
    // one more kind somebody installed.
    // `extra` is `navKey("Extra")`, the label `extranav.go` sends.
    expect(glyphForGroup('extra')).not.toBe('resource');
    expect(glyphForGroup('extra')).not.toBe(glyphForGroup('custom-resources'));
    expect(definedSymbols().has(`i-${glyphForGroup('extra')}`)).toBe(true);
  });

  it('draws a heading it does not know as the generic shape rather than nothing', () => {
    expect(glyphForGroup('something-the-server-added')).toBe('resource');
  });
});

describe('a custom resource takes its vendor, where the vendor is known', () => {
  it('reads the API group out of the slug and answers with a group shape', () => {
    expect(glyphForKind('issuers.cert-manager.io')).toBe('access');
    expect(glyphForKind('applications.argoproj.io')).toBe('workloads');
    expect(glyphForKind('ippools.crd.projectcalico.org')).toBe('network');
  });

  it('matches a sub-group of the same vendor, not just the exact name', () => {
    // One vendor owns several API groups. Listing them exhaustively is a list
    // that is wrong the next time one is added.
    expect(glyphForKind('orders.acme.cert-manager.io')).toBe('access');
    expect(glyphForKind('workflows.workflows.argoproj.io')).toBe('workloads');
  });

  it('does not match a vendor that merely ends with the same letters', () => {
    // `notcert-manager.io` is somebody else. The suffix test is on a dot
    // boundary for the reason `menuMemory` gives about `podsx`.
    expect(glyphForKind('things.notcert-manager.io')).toBe('resource');
  });

  it('draws anything else as the generic one, which is 140 rows on a busy cluster', () => {
    expect(glyphForKind('widgets.example.com')).toBe('resource');
    expect(glyphForKind('')).toBe('resource');
  });
});

describe('the slug comes out of the href, and the query is cut off first', () => {
  it('reads a bare kind path', () => {
    expect(kindOfHref('/c/prod/pods')).toBe('pods');
  });

  it('survives the sort `viewMemory` appends, which is most of the menu', () => {
    // A slug read with `?sort=age` still on it matches nothing in the map, and
    // every kind somebody has sorted would silently drop to `resource`.
    expect(kindOfHref('/c/prod/pods?sort=age')).toBe('pods');
    expect(glyphForKind(kindOfHref('/c/prod/secrets?sort=age&dir=desc') ?? '')).toBe('secret');
  });

  it('answers nothing for a href that is not a kind route', () => {
    expect(kindOfHref(undefined)).toBeUndefined();
    expect(kindOfHref('')).toBeUndefined();
    expect(kindOfHref('/health')).toBeUndefined();
    // And a kind with no href at all still draws a row - `Entry` asks for the
    // generic shape rather than skipping the icon, or that row alone would sit
    // 15px left of the thirty-five above it.
    expect(glyphForKind(kindOfHref(undefined) ?? '')).toBe('resource');
  });
});

describe("an extra's entry draws the Extra heading's shape, whichever extra it is", () => {
  it('answers an `/x/` href with the same glyph as the heading', () => {
    // One common glyph: the extension contract carries no icon, so every
    // extra draws this one.
    const extra = glyphForGroup('extra');
    expect(glyphForHref('/c/prod/x/delivery')).toBe(extra);
    expect(glyphForHref('/c/prod/x/kubecron')).toBe(extra);
    // `extraPageURL` builds the href; `viewMemory` may append a query to it.
    expect(glyphForHref('/c/prod/x/kubecron?sort=age')).toBe(extra);
    expect(glyphForHref('/c/arn%3Aaws%3Aeks%2Fprod/x/delivery/runs/42')).toBe(extra);
  });

  it('still answers every other href as its kind', () => {
    expect(glyphForHref('/c/prod/pods?sort=age')).toBe('pod');
    expect(glyphForHref('/c/prod/issuers.cert-manager.io')).toBe('access');
    expect(glyphForHref('/c/prod/widgets.example.com')).toBe('resource');
    expect(glyphForHref(undefined)).toBe('resource');
    // A kind whose slug merely starts with an x is a kind, not an extra.
    expect(glyphForHref('/c/prod/xwidgets.example.com')).toBe('resource');
  });
});

describe('a Kubernetes kind filed under a heading draws that heading', () => {
  it('draws the heading a project kind is filed under', () => {
    expect(glyphForEntry('/c/prod/gateways.gateway.networking.k8s.io', 'network')).toBe('network');
    expect(glyphForEntry('/c/prod/volumeattachments.storage.k8s.io', 'storage')).toBe('disk');
    expect(glyphForEntry('/c/prod/podtemplates', 'workloads')).toBe('workloads');
  });

  it('keeps a shape of its own where the kind has one', () => {
    expect(glyphForEntry('/c/prod/services', 'network')).toBe('service');
  });

  it('leaves the Cluster heading and Custom Resources to glyphForHref', () => {
    expect(glyphForEntry('/c/prod/helm', 'cluster')).toBe(glyphForHref('/c/prod/helm'));
    expect(glyphForEntry('/c/prod/widgets.example.io', 'custom-resources')).toBe('resource');
    expect(glyphForEntry('/c/prod/widgets.example.io', undefined)).toBe('resource');
    expect(glyphForEntry(undefined, 'network')).toBe('resource');
  });
});
