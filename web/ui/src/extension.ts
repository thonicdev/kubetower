import type { ComponentType } from 'react';
import type { RouteObject } from 'react-router';

import type { IconName } from './components/Icon';

/**
 * The TypeScript half of the extension contract. Its Go half is
 * `extension.Extension` in this repository, and `key` is the same string in
 * both - it is persisted in the operator's state as `on[cluster][extra]`, so
 * changing it is a breaking change to somebody's saved settings and not only to
 * a compile.
 *
 * The extras in `app/extras/` implement it; the package is published nowhere.
 * Every change to it is still a Conventional Commit with an accurate type, and
 * a breaking one carries `!` and a `BREAKING CHANGE:` footer - an unmarked
 * break ships a core that an extension cannot build.
 *
 * **What the Shell shows is cosmetic. The server-side gate is the
 * enforcement**: a gate that lives *inside each handler body* rather than at
 * registration does not move when a route moves, and a route that answers on a
 * cluster where the page was never enabled is a page switched off still
 * answering, with nothing erroring and no test objecting. Leaving an extension
 * out of the menu is a courtesy to the operator; it is never the thing that
 * stops a request.
 */
export interface Extension {
  /** Stable, and identical to the Go side's `Key()`. **2 to 40 lowercase
   *  letters, digits or inner hyphens** - the API's `extensionPattern`
   *  (`internal/api/router.go`) - and unique: `buildRoutes` throws at start-up
   *  on any other key, or on a key two extensions share. */
  key: string;
  /** **Optional.** Its glyph on its card in Settings > Extra and in its About
   *  window. Absent, the generic `extra` glyph is drawn. */
  icon?: IconName;
  /** Every key it was published under before, each held to the same pattern.
   *  A bookmark to `/c/{cluster}/x/<old>/…` is redirected to `x/<key>/…`, and
   *  a `?tab=x.<old>.<tab>` opens `x.<key>.<tab>`: the Go side's `Legacy()`
   *  keeps the switch, this keeps the links. One-way, never written back. */
  legacyKeys?: readonly string[];
  /** Where the Shell offers it. `path` is relative to `/c/:cluster`.
   *
   *  **The menu does not read `path`: the server's href is authoritative.** The
   *  entry comes from `GET /api/c/{cluster}/nav`, where the server-side gate
   *  decides whether it exists and `extraPageURL` (`internal/web/extranav.go`)
   *  decides where it points - always `/c/{cluster}/x/{key}`. So `path` must be
   *  `x/<key>` and one of `routes` must answer it, or the entry lands on the
   *  console's 404. It is kept in the contract because another repository
   *  compiles against it, and removing it would be a breaking change. */
  nav: {
    label: string;
    path: string;
    /** **Optional.** `false`: no entry in the menu's Extra group. The extra is
     *  reached from the panel of the object it is about (its tab, its
     *  shortcut), and its page, still routed, only from there. Its tab and
     *  shortcut are drawn as for any extra that is on. */
    menu?: false;
  };
  /** The routes it contributes, mounted under `/c/:cluster`. Lazy, so an extra
   *  that is never opened is never downloaded - one chunk per page.
   *
   *  **Each `path` is `x/<key>` or begins with `x/<key>/`**, and `buildRoutes`
   *  throws at start-up otherwise, as it does for a pathless or index route:
   *  beside the core's pages a static path outranks the
   *  dynamic ones, so a route `pods` would replace the Pods page. */
  routes: RouteObject[];
  /**
   * Tabs it adds to the object panel - the drawer and the object's own page -
   * for the kinds each one names.
   *
   * **Optional**, so an extension that adds none compiles unchanged. A tab is
   * drawn only on its kinds and only while the extension is on for the
   * cluster, as the server's menu says (the Extra group of `GET
   * /api/c/{cluster}/nav`); off, it is absent - never a tab over a refusal. As
   * with `nav`, that is a courtesy: the data the tab reads comes from the
   * extension's own routes, which the server's gate wraps.
   */
  tabs?: readonly ExtensionTab[];
  /**
   * Columns it adds to the console's lists, for the kinds each one names
   * **Optional**, and drawn under the same rule as tabs:
   * only while the extension is on for the cluster, and only when some row on
   * the page has an answer.
   */
  columns?: readonly ExtensionColumn[];
  /**
   * Shortcuts it draws at the top of an object's Details, one tile each, to
   * its own page about that object. **Optional**, and drawn under the same
   * rule as tabs: only on its kinds, only while the extension is on for the
   * cluster.
   */
  shortcuts?: readonly ExtensionShortcut[];
  /**
   * Its view of what happened to an object, drawn in the Activity tab of the
   * object panel in place of the core's event lines, only on its kinds and only
   * while the extension is on for the cluster (the server's menu, the same
   * rule as tabs). The core's changes and rollouts stay below it. **Optional.**
   * When several extensions offer one for a kind, the first in the order the
   * extensions are handed to `createApp` wins.
   */
  activity?: ExtensionActivity;
  /**
   * What Settings → Extra says about it before anyone turns it on: the
   * preview a click on its tile opens, beside the button that turns it on.
   *
   * **Optional in the contract, required of every extra the console ships**:
   * `app/extras/about.test.ts` fails on a shipped extra without it. Kept
   * beside the extra's own UI half (`about.ts`), so an extra added later brings
   * its preview with it rather than leaving the page to describe it.
   *
   * Words for an operator deciding whether to turn it on, not for a developer:
   * what it shows, what it reads, what the cluster must grant, and what must
   * already run there. **The permissions are a claim about the Go half's
   * reads** - keep them in step with it, or the preview asks for less than the
   * page needs and the page answers with a refusal nobody was warned of.
   */
  about?: ExtensionAbout;
  /**
   * What it says about itself in its tile on the Settings page's Extra tab,
   * under its settings, while it is on for the cluster:
   * KubeCron's diagnosis of its collector. **Optional**. `revision` changes
   * after each settings write from the tile, so a component that reads what
   * those settings decide reads it again.
   */
  SettingsStatus?: ComponentType<ExtensionSettingsStatusProps>;
}

/** An extension's view of an object's activity; see `Extension.activity`. */
export interface ExtensionActivity {
  /** The kinds it appears on, as the address spells them; `'*'` is every kind. */
  kinds: readonly string[];
  /** Drawn with the object's identity. Wrap it in `lazy`; the panel provides
   *  the `Suspense`. */
  Component: ComponentType<ExtensionTabProps>;
}

/** What an extra's preview in Settings → Extra says about it. */
export interface ExtensionAbout {
  /** What the extra does, three or four short sentences. */
  description: string;
  /** What it reads in the cluster, one item per kind of read. */
  reads: readonly string[];
  /** The Kubernetes permissions those reads need, one item each (e.g. "list services, endpointslices"). */
  permissions: readonly string[];
  /** What must be in the cluster for it to show anything (e.g. "Prometheus"). Empty when nothing. */
  requires: readonly string[];
  /** **Optional.** Short labels drawn as chips beside the extra's name, such as the
   *  policy engines it understands. Absent, none are drawn. */
  badges?: readonly string[];
  /** A screenshot of its page: an imported image URL. Absent, the preview draws
   *  no image at all - never a broken one. */
  screenshot?: string;
  /** **Optional.** More pictures, under the screenshot, each with its caption:
   *  what the extra adds outside its page - a tab, a tile. */
  gallery?: readonly { src: string; caption: string }[];
}

/** What the Settings tile hands an extension's `SettingsStatus`. */
export interface ExtensionSettingsStatusProps {
  cluster: string;
  /** Bumped after each settings write made from the tile. */
  revision: number;
}

/**
 * One shortcut an extension draws on an object's Details: a tile that opens
 * the extension's page about that object - the Redis deployment a Service or a
 * Pod belongs to. A tab says everything; a shortcut says where to go.
 */
export interface ExtensionShortcut {
  /** Unique within the extension. */
  key: string;
  /** The kinds it appears on, as the address spells them; `'*'` is every kind. */
  kinds: readonly string[];
  /** As a tab's `when`: on the marks the panel already holds, no second read. */
  when?: (object: ExtensionTabObject) => boolean;
  /** Where the tile leads, from the extension's own route; `null` draws
   *  nothing. Asked once `when` holds, and cached per object. **A rejection
   *  draws nothing either**: a shortcut is a convenience, and the extension's
   *  tab is where "could not check" is said. */
  resolve: (object: ExtensionTabProps, signal: AbortSignal) => Promise<ExtensionShortcutTarget | null>;
  /** **Optional.** What pressing the tile opens in a dialog over the panel,
   *  for the same object, instead of following the target's `to`: a lazy
   *  component, downloaded on the first press. */
  dialog?: ComponentType<ExtensionTabProps>;
}

/** What a shortcut's tile shows, and where it goes. */
export interface ExtensionShortcutTarget {
  /** The thing the page is about: a deployment's name. */
  title: string;
  /** A few words drawn as chips: `Redis`, `sentinel`. Never a sentence. */
  badges?: readonly string[];
  /** One short secondary line. */
  detail?: string;
  /** A path inside the console: `/c/{cluster}/x/{key}/…`, or the object's own
   *  address with `?tab=x.<key>.<tab>` to open the extension's tab on it. */
  to: string;
  /** **Optional.** A graded word drawn first among the chips, in its tone, and
   *  the tile's left bar in the same tone: the Runtime Inspector's "OOMKill".
   *  Without it the tile is drawn as before, in the neutral info tone. */
  status?: ExtensionStatus;
  /** **Optional.** A small bar under the second line: what the tile is about,
   *  measured. Without it nothing is drawn. */
  meter?: ExtensionMeter;
}

/** A graded word: `ok`, `warn` or `bad`. */
export interface ExtensionStatus {
  text: string;
  tone: 'ok' | 'warn' | 'bad';
  /** The hover sentence: what the grade rests on. */
  hint?: string;
}

/**
 * A small horizontal bar an extension's shortcut draws: parts laid end to end
 * on a track, and at most one line across it.
 *
 * **The geometry is the extension's, in percentages of the track, and the
 * tile draws it as given** - it never rescales, so the figure in `text` and
 * the bar cannot disagree. A width is clamped to the track; a part with no
 * width draws nothing.
 */
export interface ExtensionMeter {
  /** What the bar measures, for a screen reader and the hover: "maximum heap
   *  against the memory limit". */
  label: string;
  /** The parts, from the left, each a percentage of the track. */
  segments: readonly ExtensionMeterSegment[];
  /** A line across the track at `at` percent: a threshold, named by `label`. */
  threshold?: { at: number; label: string };
  /** The reading, beside the bar: "1.5 GiB of 2Gi". */
  text?: string;
}

export interface ExtensionMeterSegment {
  width: number;
  /** `ok`, `warn`, `bad` or `info`; anything else, or none, is neutral. */
  tone?: string;
  /** What the part is, on hover. */
  label: string;
}

/** One column an extension adds to the console's lists. */
export interface ExtensionColumn {
  /** Unique within the extension; the grid's key is `x.<extension>.<key>`. */
  key: string;
  label: string;
  /** The kinds it appears on, as the address spells them; `'*'` is every kind. */
  kinds: readonly string[];
  /** **One read for the whole list**, from the extension's own route: the
   *  answers keyed `namespace/name` (the name alone on a cluster-scoped kind).
   *  A row with no answer is an empty cell; a rejection draws no column. */
  cells: (list: { cluster: string; kind: string }, signal: AbortSignal) => Promise<Readonly<Record<string, ExtensionCell>>>;
  /** The key of one of the extension's own settings (the Go side's
   *  `extension.Configurable`) that decides whether this column is drawn on a
   *  cluster - Argo CD's "show the column in the lists".
   *  **Optional**: without it the column is drawn wherever the extension is on.
   *  With it, the column waits for the cluster's extension listing and is drawn
   *  only while that setting is true there. A listing that cannot be read, or
   *  that does not name the setting, falls back to `settingDefault`. */
  setting?: string;
  /** The setting's declared default (the Go side's `extension.Setting.Default`),
   *  drawn when the listing cannot say: a failed read, or a server that does
   *  not name the setting. **Optional, false when absent**: a gated column is
   *  asked for, never assumed - Argo CD's is off by default. Keep
   *  it equal to the Go default, or the two disagree exactly when a read fails. */
  settingDefault?: boolean;
}

/** One cell an extension's column draws, as a pill. */
export interface ExtensionCell {
  text: string;
  /** `ok`, `warn` or `bad` grade the pill; anything else is neutral. */
  tone?: string;
  /** The hover sentence. */
  hint?: string;
  /** Where a click on the cell goes, inside the console. */
  href?: string;
}

/**
 * One tab an extension adds to the object panel.
 *
 * **Drawn only on an object the extension concerns:**
 * `kinds` first, then `when` on the object's labels and annotations, then
 * `probe`, and the tab is drawn only where every one that is declared says
 * yes - a probe that is pending, throws or answers false draws nothing. A
 * shortcut follows the same rule with `resolve` in the probe's place: no
 * target, no tile. A mark that something other than the
 * extension's own system can set (a common label, an annotation that is
 * copied) is a hint, not proof: such a tab declares a `probe` that asks.
 */
export interface ExtensionTab {
  /** Unique within the extension. The address carries
   *  `?tab=x.<extension key>.<key>`, so a reload lands back on it. */
  key: string;
  label: string;
  /** The kinds it appears on, spelled as the console's address spells them:
   *  `pods`, `jobs`, `cronjobs`, `applications.argoproj.io`. `'*'` is every
   *  kind, for a tab whose objects are marked by what they carry rather than
   *  by their kind. */
  kinds: readonly string[];
  /** Drawn only when this answers true for the object, as the panel already
   *  holds it: its labels and its annotations, treated as the Details tab
   *  draws them. Optional: without it, the kind alone decides. **No second
   *  read**, and while the object is still being read the tab is not drawn - a
   *  reload onto it waits rather than landing on Details and jumping. */
  when?: (object: ExtensionTabObject) => boolean;
  /** Asked once `when` holds, for what the object's marks cannot say: an
   *  extension's own answer about this object, from its own route. The tab is
   *  drawn only when it resolves true - a Pod is Argo CD's only through its
   *  owner, which only the extension can walk to.
   *  Optional, and cached per object; a rejection draws no tab, never an
   *  error in the strip. */
  probe?: (object: ExtensionTabProps, signal: AbortSignal) => Promise<boolean>;
  /** Drawn in the panel with the object's identity. Wrap it in `lazy` so the
   *  extension's chunk loads only when the tab is opened; the panel provides
   *  the `Suspense`. */
  Component: ComponentType<ExtensionTabProps>;
}

/** What a tab's `when` is asked about: the object's marks, as the panel's
 *  payload carries them. An annotation's value may be masked (a Secret's are),
 *  so a condition is on the key's presence or on a label's value. */
export interface ExtensionTabObject {
  labels: Readonly<Record<string, string>>;
  annotations: Readonly<Record<string, string>>;
}

/** What an extension's tab is given: the object, as the panel addresses it. */
export interface ExtensionTabProps {
  cluster: string;
  kind: string;
  /** Absent on a cluster-scoped kind. */
  namespace: string | undefined;
  name: string;
}
