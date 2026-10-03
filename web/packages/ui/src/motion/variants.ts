/**
 * @license MIT
 * Morphic Design System — Motion Variants & Presence Presets
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design      — restrained, Material easing, never bouncy
 *   §19 Micro-interactions — card hover, button press, nav selection
 *   §9  Accessibility      — support `prefers-reduced-motion`
 *
 * Motion docs: https://motion.dev/docs/react-animate-presence
 *              https://motion.dev/docs/react-motion-component#variants
 *
 * Every preset here moves a SHORT distance. §18's character is "subtle
 * movement" that "communicates hierarchy and state" — a 40px slide reads as a
 * page-builder animation, not an enterprise dashboard.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import type { Variants } from 'motion/react';
import { M3_TRANSITIONS, M3_STAGGER, type M3StaggerName } from './transitions.js';

/* ========================================================================= */
/* PRESENCE PRESETS — for <AnimatePresence>                                  */
/* ========================================================================= */

/**
 * The direction an element travels as it arrives and leaves.
 * `none` fades only, which is the reduced-motion fallback.
 */
export type PresenceDirection = 'up' | 'down' | 'left' | 'right' | 'none';

const OFFSET: Record<PresenceDirection, { x: number; y: number }> = {
  up: { x: 0, y: 8 },
  down: { x: 0, y: -8 },
  left: { x: 8, y: 0 },
  right: { x: -8, y: 0 },
  none: { x: 0, y: 0 },
};

/**
 * Fade + a short travel. The default arrival for cards, list rows, panels.
 *
 * ```tsx
 * <AnimatePresence>
 *   {open && <motion.div variants={fadeSlide('up')} initial="hidden" animate="visible" exit="exit" />}
 * </AnimatePresence>
 * ```
 */
export const fadeSlide = (direction: PresenceDirection = 'up', distance = 8): Variants => {
  const o = OFFSET[direction];
  const x = o.x === 0 ? 0 : Math.sign(o.x) * distance;
  const y = o.y === 0 ? 0 : Math.sign(o.y) * distance;
  return {
    hidden: { opacity: 0, x, y },
    visible: { opacity: 1, x: 0, y: 0, transition: M3_TRANSITIONS.enter },
    exit: { opacity: 0, x, y, transition: M3_TRANSITIONS.exit },
  };
};

/** Opacity only. The safest arrival, and the reduced-motion fallback. */
export const fade: Variants = {
  hidden: { opacity: 0 },
  visible: { opacity: 1, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, transition: M3_TRANSITIONS.exit },
};

/**
 * Scale from 96%. For overlays that own the viewport's attention — dialogs,
 * popovers, menus. M3 scales dialogs rather than sliding them.
 */
export const scaleIn = (from = 0.96): Variants => ({
  hidden: { opacity: 0, scale: from },
  visible: { opacity: 1, scale: 1, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, scale: from, transition: M3_TRANSITIONS.exit },
});

/** Dialog / modal. Scale plus a hint of rise. */
export const dialog: Variants = {
  hidden: { opacity: 0, scale: 0.96, y: 8 },
  visible: { opacity: 1, scale: 1, y: 0, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, scale: 0.96, y: 8, transition: M3_TRANSITIONS.exit },
};

/** The scrim behind a dialog or drawer. */
export const scrim: Variants = {
  hidden: { opacity: 0 },
  visible: { opacity: 1, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, transition: M3_TRANSITIONS.exit },
};

/**
 * A sheet sliding in from an edge. Unlike `fadeSlide`, this travels the full
 * height/width of the surface, so it takes a percentage rather than pixels.
 */
export const sheet = (edge: 'bottom' | 'top' | 'left' | 'right' = 'bottom'): Variants => {
  const horizontal = edge === 'left' || edge === 'right';
  const sign = edge === 'bottom' || edge === 'right' ? '100%' : '-100%';
  const away = horizontal ? { x: sign } : { y: sign };
  const home = horizontal ? { x: 0 } : { y: 0 };
  return {
    hidden: away,
    visible: { ...home, transition: M3_TRANSITIONS.enter },
    exit: { ...away, transition: M3_TRANSITIONS.exit },
  };
};

/** A dropdown or menu unfolding from its trigger edge. */
export const dropdown = (origin: 'top' | 'bottom' = 'top'): Variants => ({
  hidden: { opacity: 0, scaleY: 0.92, y: origin === 'top' ? -4 : 4 },
  visible: { opacity: 1, scaleY: 1, y: 0, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, scaleY: 0.92, y: origin === 'top' ? -4 : 4, transition: M3_TRANSITIONS.exit },
});

/** A tooltip. Fastest of the overlays — it must not lag the pointer. */
export const tooltip: Variants = {
  hidden: { opacity: 0, scale: 0.94 },
  visible: { opacity: 1, scale: 1, transition: M3_TRANSITIONS.hover },
  exit: { opacity: 0, scale: 0.94, transition: M3_TRANSITIONS.hover },
};

/** A snackbar or toast rising from the bottom. */
export const toast: Variants = {
  hidden: { opacity: 0, y: 16, scale: 0.98 },
  visible: { opacity: 1, y: 0, scale: 1, transition: M3_TRANSITIONS.enter },
  exit: { opacity: 0, y: 16, scale: 0.98, transition: M3_TRANSITIONS.exit },
};

/** An accordion or expandable row measuring itself open. */
export const collapse: Variants = {
  hidden: { height: 0, opacity: 0, overflow: 'hidden' },
  visible: { height: 'auto', opacity: 1, transition: M3_TRANSITIONS.card },
  exit: { height: 0, opacity: 0, transition: M3_TRANSITIONS.exit },
};

/**
 * Every presence preset, keyed by name, so a component can take a
 * `preset="dialog"` prop instead of importing each one.
 */
export const PRESENCE_PRESETS = {
  fade,
  fadeUp: fadeSlide('up'),
  fadeDown: fadeSlide('down'),
  fadeLeft: fadeSlide('left'),
  fadeRight: fadeSlide('right'),
  scale: scaleIn(),
  dialog,
  scrim,
  sheetBottom: sheet('bottom'),
  sheetTop: sheet('top'),
  sheetLeft: sheet('left'),
  sheetRight: sheet('right'),
  dropdown: dropdown('top'),
  tooltip,
  toast,
  collapse,
} as const satisfies Record<string, Variants>;

export type PresencePreset = keyof typeof PRESENCE_PRESETS;

/* ========================================================================= */
/* STAGGERED LISTS (§19)                                                     */
/* ========================================================================= */

/**
 * Container + item variant pair for a staggered reveal.
 *
 * ```tsx
 * const { container, item } = staggerVariants('normal', 'up');
 * <motion.ul variants={container} initial="hidden" animate="visible">
 *   {rows.map(r => <motion.li key={r.id} variants={item} />)}
 * </motion.ul>
 * ```
 */
export const staggerVariants = (
  amount: M3StaggerName | number = 'normal',
  direction: PresenceDirection = 'up',
  distance = 8,
): { container: Variants; item: Variants } => ({
  container: {
    hidden: {},
    visible: {
      transition: {
        staggerChildren: typeof amount === 'number' ? amount : M3_STAGGER[amount],
      },
    },
    exit: { transition: { staggerChildren: 0.02, staggerDirection: -1 } },
  },
  item: fadeSlide(direction, distance),
});

/* ========================================================================= */
/* MICRO-INTERACTIONS (§19)                                                  */
/* ========================================================================= */

/**
 * Card hover, exactly as §19 describes it:
 *   rest → slightly brighter surface → subtle elevation → translateY(-1px)
 *
 * The surface and shadow shifts live in CSS (`.morphic-card:hover`), because
 * they are token-driven. This supplies the transform half.
 */
export const cardHover = {
  rest: { y: 0 },
  hover: { y: -1, transition: M3_TRANSITIONS.card },
} as const;

/** Button press, per §19: `pressed scale ~0.98`. */
export const buttonPress = {
  rest: { scale: 1 },
  hover: { scale: 1 },
  tap: { scale: 0.98, transition: M3_TRANSITIONS.button },
} as const;

/**
 * Convenience props for a pressable element. Spread onto any `motion.*`.
 *
 * ```tsx
 * <motion.button {...pressable}>Save</motion.button>
 * ```
 */
export const pressable = {
  whileTap: { scale: 0.98 },
  transition: M3_TRANSITIONS.button,
} as const;

/** Convenience props for a card that lifts on hover (§19). */
export const liftable = {
  whileHover: { y: -1 },
  transition: M3_TRANSITIONS.card,
} as const;

export type { Variants };
