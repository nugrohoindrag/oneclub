/**
 * @license MIT
 * Morphic Design System — Motion
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md §18 Motion Design, §19 Micro-interactions
 *
 * One animation engine. Durations and easing come from the same §18 table that
 * `--motion-duration-*` reads, so JS-driven and CSS-driven motion stay in sync.
 *
 *   easing.ts       M3 curves, as tuples, functions and CSS strings
 *   transitions.ts  §18 durations, springs, composed transitions, stagger
 *   variants.ts     AnimatePresence presets and micro-interaction variants
 *   hooks.ts        scroll, parallax, in-view, spring, count-up
 */
export * from './easing.js';
export * from './transitions.js';
export * from './variants.js';
export * from './hooks.js';

// Motion's vanilla JS API (https://motion.dev/docs/animate). Use it outside
// React — a canvas chart, an imperative reveal, a scroll-linked timeline.
// `animate` returns AnimationPlaybackControls, which <MotionScrubber> drives.
export { animate, scroll, inView, hover, press, transform, mix, wrap, interpolate } from 'motion';

// Motion's React surface — motion, AnimatePresence, LayoutGroup, MotionConfig,
// Reorder — re-exported so consumers need a single import.
export {
  motion,
  m,
  AnimatePresence,
  LayoutGroup,
  MotionConfig,
  Reorder,
  useDragControls,
  LazyMotion,
  domAnimation,
  domMax,
  type PanInfo,
} from 'motion/react';
