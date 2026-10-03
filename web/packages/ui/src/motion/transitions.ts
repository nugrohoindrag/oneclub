/**
 * @license MIT
 * Morphic Design System — Durations, Springs & Transitions
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md §18 "Motion Design"
 *
 *   Hover              120–160ms
 *   Button state       120–180ms
 *   Card interaction   180–220ms
 *   Modal              220–280ms
 *   Page transition    250–350ms
 *   Chart update       400–700ms
 *
 * These are the SAME numbers as `--motion-duration-*` in contract.css. A
 * component animating in JS and a component animating in CSS must move at the
 * same speed, so both read from this table.
 *
 * Motion docs: https://motion.dev/docs/transitions
 *              https://motion.dev/docs/react-transitions
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import type { Transition } from 'motion/react';
import { M3_EASE } from './easing.js';
import { MOTION_DURATION_MS, MOTION_DURATION_SEC } from '../tokens/tokens.js';

/* ========================================================================= */
/* DURATIONS (§18)                                                           */
/* ========================================================================= */

/**
 * Semantic durations in seconds — the unit Motion expects.
 *
 * Re-exported from the Foundations layer (`tokens/tokens.ts`) rather than
 * restated, so `--motion-duration-*`, `MOTION_DURATION_SEC` and every Motion
 * transition can never drift apart.
 */
export const M3_DURATION = MOTION_DURATION_SEC;

export type M3DurationName = keyof typeof MOTION_DURATION_SEC;

/** The same durations in milliseconds, for `setTimeout` and CSS strings. */
export const M3_DURATION_MS = MOTION_DURATION_MS;

/**
 * The Material 3 duration scale, kept because M3 component specs reference
 * these token names directly. Prefer the semantic table above in product code.
 */
export const M3_DURATION_SCALE = {
  short1: 0.05,
  short2: 0.1,
  short3: 0.15,
  short4: 0.2,
  medium1: 0.25,
  medium2: 0.3,
  medium3: 0.35,
  medium4: 0.4,
  long1: 0.45,
  long2: 0.5,
  long3: 0.55,
  long4: 0.6,
  extraLong1: 0.7,
  extraLong2: 0.8,
  extraLong3: 0.9,
  extraLong4: 1.0,
} as const;

/* ========================================================================= */
/* SPRINGS                                                                   */
/* Motion docs: https://motion.dev/docs/react-transitions#spring             */
/* ========================================================================= */

/**
 * Spring presets. §18 says "avoid bouncy animation", so every preset here is
 * critically or near-critically damped — they settle rather than wobble.
 *
 * `playful` is the one exception and is deliberately NOT used by any Core
 * component; it exists for product teams who opt into a livelier feel.
 */
export const M3_SPRING = {
  /** Snappy and settled — switches, chips, checkboxes. */
  snappy: { type: 'spring', stiffness: 500, damping: 34, mass: 1 },
  /** Softer arrival — cards, panels, popovers. */
  gentle: { type: 'spring', stiffness: 260, damping: 30, mass: 1 },
  /** Slow and heavy — large surfaces, sheets. */
  smooth: { type: 'spring', stiffness: 170, damping: 26, mass: 1 },
  /** Follows a pointer or scroll without lag. */
  responsive: { type: 'spring', stiffness: 400, damping: 40, mass: 0.6 },
  /** Opt-in only. Overshoots, which §18 rules out for Core components. */
  playful: { type: 'spring', stiffness: 400, damping: 18, mass: 1 },
} as const satisfies Record<string, Transition>;

export type M3SpringName = keyof typeof M3_SPRING;

/** Spring options in the shape `useSpring` takes. */
export const M3_SPRING_OPTIONS = {
  snappy: { stiffness: 500, damping: 34, mass: 1 },
  gentle: { stiffness: 260, damping: 30, mass: 1 },
  smooth: { stiffness: 170, damping: 26, mass: 1 },
  responsive: { stiffness: 400, damping: 40, mass: 0.6 },
  playful: { stiffness: 400, damping: 18, mass: 1 },
} as const;

/* ========================================================================= */
/* COMPOSED TRANSITIONS                                                      */
/* ========================================================================= */

/**
 * Ready-made transitions, one per interaction in the §18 table. Reach for
 * these before writing a `transition` object by hand — an ad-hoc duration is
 * how a system drifts out of rhythm.
 *
 * ```tsx
 * <motion.div animate={{ opacity: 1 }} transition={M3_TRANSITIONS.enter} />
 * ```
 */
export const M3_TRANSITIONS = {
  /** Hover tint, state layer. */
  hover: { duration: M3_DURATION.hover, ease: M3_EASE.standard },

  /** Button press and release. */
  button: { duration: M3_DURATION.button, ease: M3_EASE.standard },

  /** Card hover lift, panel expand. */
  card: { duration: M3_DURATION.card, ease: M3_EASE.standard },

  /** An element arriving — dialog, sheet, popover, toast. */
  enter: { duration: M3_DURATION.modal, ease: M3_EASE.emphasizedDecelerate },

  /** An element leaving. Faster than entering, per M3. */
  exit: { duration: M3_DURATION.button, ease: M3_EASE.emphasizedAccelerate },

  /** Route or view change. */
  page: { duration: M3_DURATION.page, ease: M3_EASE.emphasized },

  /** Shared-element / container transform. */
  containerTransform: { duration: M3_DURATION.page, ease: M3_EASE.emphasizedDecelerate },

  /** Chart draw and data update (§13, §19 "charts can animate"). */
  chart: { duration: M3_DURATION.chart, ease: M3_EASE.emphasizedDecelerate },

  /** Layout reflow driven by Motion's `layout` prop. */
  layout: { duration: M3_DURATION.card, ease: M3_EASE.emphasized },

  /** Springs, for interactions that should track input rather than a clock. */
  springSnappy: M3_SPRING.snappy,
  springGentle: M3_SPRING.gentle,
  springSmooth: M3_SPRING.smooth,
  springResponsive: M3_SPRING.responsive,
  /** @deprecated Overshoots; §18 rules out bouncy motion for Core. */
  springBouncy: M3_SPRING.playful,
} as const satisfies Record<string, Transition>;

export type M3TransitionName = keyof typeof M3_TRANSITIONS;

/* ========================================================================= */
/* STAGGER (§19 — lists reveal in sequence)                                  */
/* ========================================================================= */

/**
 * Per-item delay for staggered reveals, in seconds. Kept short: a long
 * stagger on a dense dashboard (§24) reads as sluggish, not premium.
 */
export const M3_STAGGER = {
  tight: 0.03,
  normal: 0.05,
  relaxed: 0.08,
} as const;

export type M3StaggerName = keyof typeof M3_STAGGER;

/**
 * Build a container transition that staggers its children.
 *
 * ```tsx
 * <motion.ul variants={{ show: staggerChildren('normal') }} animate="show">
 * ```
 */
export const staggerChildren = (
  amount: M3StaggerName | number = 'normal',
  delayChildren = 0,
): Transition => ({
  staggerChildren: typeof amount === 'number' ? amount : M3_STAGGER[amount],
  delayChildren,
});

/**
 * Cap the total stagger so a long list never crawls. Returns the per-item
 * delay that fits `count` items inside `maxTotal` seconds.
 */
export const staggerFor = (
  count: number,
  amount: M3StaggerName | number = 'normal',
  maxTotal = 0.4,
): number => {
  const base = typeof amount === 'number' ? amount : M3_STAGGER[amount];
  if (count <= 1) return 0;
  return Math.min(base, maxTotal / (count - 1));
};

/* ========================================================================= */
/* Re-exports — Motion's own transition primitives                           */
/* ========================================================================= */

export { stagger, spring, delay, frame, cancelFrame, time } from 'motion';
export type { Transition };
