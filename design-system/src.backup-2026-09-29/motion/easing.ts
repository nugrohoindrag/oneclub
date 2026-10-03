/**
 * @license MIT
 * Morphic Design System — Easing
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md §18 "Motion Design"
 *   standard:   cubic-bezier(0.2, 0, 0, 1)
 *   emphasized: cubic-bezier(0.2, 0, 0, 1)
 *   "Avoid bouncy animation."
 *
 * Motion docs: https://motion.dev/docs/easing-functions
 *
 * These are the ONLY curves the system uses. A component that reaches for
 * `backOut`, `anticipate` or a custom overshoot is producing the bouncy
 * character §18 rules out — the named exports below are re-exported from
 * Motion for completeness, but Morphic components use `M3_EASE`.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import { cubicBezier } from 'motion';
import type { Easing } from 'motion/react';

/** A cubic-bezier control-point tuple, the form Motion accepts inline. */
export type BezierDefinition = [number, number, number, number];

/**
 * Material 3 easing curves (§18).
 *
 * Emphasized curves carry the eye across a large change — a dialog opening, a
 * shared-element transform. Standard curves handle utility changes — a hover
 * tint, a chip toggling.
 */
export const M3_BEZIER = {
  emphasized: [0.2, 0.0, 0.0, 1.0],
  emphasizedDecelerate: [0.05, 0.7, 0.1, 1.0],
  emphasizedAccelerate: [0.3, 0.0, 0.8, 0.15],
  standard: [0.2, 0.0, 0.0, 1.0],
  standardDecelerate: [0.0, 0.0, 0.2, 1.0],
  standardAccelerate: [0.3, 0.0, 1.0, 1.0],
  linear: [0, 0, 1, 1],
} as const satisfies Record<string, BezierDefinition | readonly number[]>;

export type M3EasingName = keyof typeof M3_BEZIER;

/**
 * The same curves as Motion `Easing` values, ready to drop into a
 * `transition.ease`.
 *
 * ```tsx
 * <motion.div animate={{ opacity: 1 }} transition={{ ease: M3_EASE.standard }} />
 * ```
 */
export const M3_EASE = {
  emphasized: M3_BEZIER.emphasized as unknown as Easing,
  emphasizedDecelerate: M3_BEZIER.emphasizedDecelerate as unknown as Easing,
  emphasizedAccelerate: M3_BEZIER.emphasizedAccelerate as unknown as Easing,
  standard: M3_BEZIER.standard as unknown as Easing,
  standardDecelerate: M3_BEZIER.standardDecelerate as unknown as Easing,
  standardAccelerate: M3_BEZIER.standardAccelerate as unknown as Easing,
  linear: M3_BEZIER.linear as unknown as Easing,
} as const;

/**
 * The same curves as callable easing functions, for code that needs to sample
 * a curve directly — a canvas chart, a scroll mapper, a custom interpolation.
 *
 * ```ts
 * const eased = M3_EASE_FN.standard(0.5); // → 0.5 mapped through the curve
 * ```
 */
export const M3_EASE_FN = {
  emphasized: cubicBezier(...(M3_BEZIER.emphasized as unknown as BezierDefinition)),
  emphasizedDecelerate: cubicBezier(
    ...(M3_BEZIER.emphasizedDecelerate as unknown as BezierDefinition),
  ),
  emphasizedAccelerate: cubicBezier(
    ...(M3_BEZIER.emphasizedAccelerate as unknown as BezierDefinition),
  ),
  standard: cubicBezier(...(M3_BEZIER.standard as unknown as BezierDefinition)),
  standardDecelerate: cubicBezier(
    ...(M3_BEZIER.standardDecelerate as unknown as BezierDefinition),
  ),
  standardAccelerate: cubicBezier(
    ...(M3_BEZIER.standardAccelerate as unknown as BezierDefinition),
  ),
} as const;

/** The CSS strings, matching `--md-sys-motion-easing-*` in motion.css. */
export const M3_EASE_CSS = {
  emphasized: 'cubic-bezier(0.2, 0, 0, 1)',
  emphasizedDecelerate: 'cubic-bezier(0.05, 0.7, 0.1, 1)',
  emphasizedAccelerate: 'cubic-bezier(0.3, 0, 0.8, 0.15)',
  standard: 'cubic-bezier(0.2, 0, 0, 1)',
  standardDecelerate: 'cubic-bezier(0, 0, 0.2, 1)',
  standardAccelerate: 'cubic-bezier(0.3, 0, 1, 1)',
  linear: 'linear',
} as const;

/**
 * Motion's own easing library, re-exported so consumers never need a second
 * import. Prefer `M3_EASE` — §18 asks for Material-like easing, and the
 * `back*` / `anticipate` curves overshoot, which it explicitly rules out.
 *
 * https://motion.dev/docs/easing-functions
 */
export {
  cubicBezier,
  easeIn,
  easeOut,
  easeInOut,
  circIn,
  circOut,
  circInOut,
  backIn,
  backOut,
  backInOut,
  anticipate,
  steps,
  mirrorEasing,
  reverseEasing,
} from 'motion';
