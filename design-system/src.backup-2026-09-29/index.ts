/**
 * @license MIT
 * Morphic Design System
 *
 * A Material 3-based UI system for building modern, responsive, accessible web
 * applications with a soft, premium, information-dense visual language. (§13)
 *
 * Architecture (§17 Final Architecture):
 *
 *   FOUNDATION → PRIMITIVE → COMPONENT → PATTERN → TEMPLATE → APPLICATION
 *
 *   01 Foundations          src/tokens
 *   02 Primitives           src/components/layout
 *   03 Actions              src/components/actions
 *   04 Navigation           src/components/navigation
 *   05 Forms & Inputs       src/components/inputs
 *   06 Selection            src/components/selection
 *   07 Surfaces             src/components/containment
 *   08 Data Display         src/components/data-display, src/components/entity
 *   09 Data Visualization   src/components/visualization
 *   10 Feedback             src/components/feedback, src/components/communication
 *   11 Overlays             src/components/overlays
 *   12 Layout               src/components/layout
 *   13 Utilities            src/components/utility
 *   14 Patterns             src/patterns
 *   15 Templates            src/templates
 *   16 Themes               src/tokens/themes.css
 *   17 Accessibility        enforced per component (§9)
 *
 * Core stays generic; industry-specific work lives in Patterns, Templates and
 * Examples (§4, §17). `src/examples` is deliberately NOT re-exported here —
 * import it from `@design-system/motion/examples` when you want it.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

// ── Core components (01–13) ───────────────────────────────────────────────
export * from './components/index.js';

// ── 14 Patterns ───────────────────────────────────────────────────────────
export * from './patterns/index.js';

// ── 15 Templates ──────────────────────────────────────────────────────────
export * from './templates/index.js';

// ── Motion engine ─────────────────────────────────────────────────────────
export * from './motion/index.js';

// ── Design tokens (JS/TS consumers) ───────────────────────────────────────
export * from './tokens/index.js';
