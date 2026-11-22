// The theme, and the only stylesheet this repository hand-writes.
// Tailwind v4's `@theme` block *is* CSS custom properties, so every utility
// below resolves through it: `bg-panel`, `text-muted`, `rounded-lg`, `text-sm`.
// Nothing in this package defines a color, a size or a radius of its own.
import './theme.css';

// No terminal stylesheet here. xterm's own CSS is imported beside the emulator
// in `ui/src/dock/terminal.ts`, the chunk that arrives when a terminal is
// opened - from the package and never vendored, for the reason written there -
// so the sign-in view downloads neither the emulator nor its styling.

import { createApp } from '@kubetower/ui';

import { extras } from './extras';

const root = document.getElementById('root');
if (root === null) throw new Error('#root is missing from index.html');

// The extras this console ships: one written-out list (R-7), in
// `./extras/index.ts`, which says why and what it must agree with.
createApp(root, extras);
