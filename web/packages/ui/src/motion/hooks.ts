/**
 * @license MIT
 * Morphic Design System — Motion Hooks
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §9  Accessibility — support `prefers-reduced-motion`
 *   §18 Motion Design — restrained durations, Material easing
 *   §19 Micro-interactions — charts animate on first render / filter change
 *
 * Motion docs: https://motion.dev/docs/react-scroll-animations
 *              https://motion.dev/docs/react-use-spring
 *              https://motion.dev/docs/react-use-in-view
 *
 * Every hook here honours `prefers-reduced-motion` (§9): with it on, values
 * jump to their resolved state instead of animating.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import { useEffect, useRef, useState, type RefObject } from 'react';
import {
  useInView,
  useMotionValueEvent,
  useReducedMotion,
  useScroll,
  useSpring,
  useTransform,
  useMotionValue,
  animate,
  type MotionValue,
  type UseInViewOptions,
} from 'motion/react';
import { M3_SPRING_OPTIONS, M3_TRANSITIONS, type M3SpringName } from './transitions.js';

/* ========================================================================= */
/* REDUCED MOTION (§9)                                                       */
/* ========================================================================= */

/**
 * `true` when the viewer has asked for reduced motion.
 *
 * Returns `false` rather than `null` on first render, so callers can use it in
 * a boolean position without a guard. Prefer this over Motion's
 * `useReducedMotion` inside Morphic components.
 */
export const useReducedMotionSafe = (): boolean => useReducedMotion() ?? false;

/** Imperative check, for code outside React. */
export const prefersReducedMotion = (): boolean => {
  if (typeof window === 'undefined' || !window.matchMedia) return false;
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
};

/* ========================================================================= */
/* SCROLL (Motion: useScroll)                                                */
/* ========================================================================= */

export interface ScrollProgressOptions {
  /** Element whose scroll drives the value. Omit to track the page. */
  target?: RefObject<HTMLElement | null>;
  /** Motion scroll offset, e.g. `['start end', 'end start']`. */
  offset?: UseScrollOffset;
  /** Smooth the raw progress with a spring. Defaults to `responsive`. */
  spring?: M3SpringName | false;
}

/** Motion's scroll offset tuple, re-typed so callers need not import it. */
export type UseScrollOffset = Parameters<typeof useScroll>[0] extends
  | { offset?: infer O }
  | undefined
  ? O
  : never;

/**
 * Scroll progress from 0 → 1, spring-smoothed so it never jitters.
 *
 * ```tsx
 * const progress = useScrollProgress();
 * <motion.div style={{ scaleX: progress, transformOrigin: 'left' }} />
 * ```
 */
export const useScrollProgress = ({
  target,
  offset,
  spring = 'responsive',
}: ScrollProgressOptions = {}): MotionValue<number> => {
  const { scrollYProgress } = useScroll(
    target ? ({ target, offset } as Parameters<typeof useScroll>[0]) : ({ offset } as Parameters<typeof useScroll>[0]),
  );
  const reduced = useReducedMotionSafe();
  const smoothed = useSpring(
    scrollYProgress,
    spring === false || reduced ? { duration: 0 } : M3_SPRING_OPTIONS[spring],
  );
  return spring === false || reduced ? scrollYProgress : smoothed;
};

/**
 * A parallax offset in pixels, driven by the element's travel through the
 * viewport.
 *
 * Keep `distance` small. §18 asks for subtle movement; a 200px parallax on a
 * dense dashboard (§24) fights the content.
 *
 * ```tsx
 * const y = useParallax(ref, 24);
 * <motion.img ref={ref} style={{ y }} />
 * ```
 */
export const useParallax = (
  target: RefObject<HTMLElement | null>,
  distance = 24,
  spring: M3SpringName | false = 'responsive',
): MotionValue<number> => {
  const reduced = useReducedMotionSafe();
  const { scrollYProgress } = useScroll({
    target,
    offset: ['start end', 'end start'],
  } as Parameters<typeof useScroll>[0]);
  const raw = useTransform(scrollYProgress, [0, 1], reduced ? [0, 0] : [distance, -distance]);
  const smoothed = useSpring(
    raw,
    spring === false || reduced ? { duration: 0 } : M3_SPRING_OPTIONS[spring],
  );
  return spring === false || reduced ? raw : smoothed;
};

export type ScrollDirection = 'up' | 'down' | 'idle';

/**
 * Which way the page last moved. Useful for hiding a compact header on the way
 * down and restoring it on the way up (§9 — the header must stay compact).
 */
export const useScrollDirection = (threshold = 4): ScrollDirection => {
  const { scrollY } = useScroll();
  const [direction, setDirection] = useState<ScrollDirection>('idle');
  const previous = useRef(0);

  useMotionValueEvent(scrollY, 'change', (latest) => {
    const delta = latest - previous.current;
    if (Math.abs(delta) < threshold) return;
    previous.current = latest;
    setDirection(delta > 0 ? 'down' : 'up');
  });

  return direction;
};

/* ========================================================================= */
/* IN VIEW (Motion: useInView)                                               */
/* ========================================================================= */

export interface RevealOptions {
  /** Animate once and stay revealed. Default `true`. */
  once?: boolean;
  /** How much of the element must be visible, 0–1 or 'some' | 'all'. */
  amount?: UseInViewOptions['amount'];
  /** Root margin, e.g. `'-10% 0px'`. */
  margin?: UseInViewOptions['margin'];
}

/**
 * Reveal-on-scroll wiring: attach the ref, spread the props.
 *
 * ```tsx
 * const { ref, props } = useReveal();
 * <motion.section ref={ref} {...props}>…</motion.section>
 * ```
 *
 * With reduced motion on, the element renders visible immediately (§9).
 */
export const useReveal = ({ once = true, amount = 0.2, margin }: RevealOptions = {}) => {
  const ref = useRef<HTMLElement>(null);
  const inView = useInView(ref, { once, amount, margin } as UseInViewOptions);
  const reduced = useReducedMotionSafe();

  return {
    ref,
    inView,
    props: {
      initial: reduced ? false : ({ opacity: 0, y: 8 } as const),
      animate: inView || reduced ? { opacity: 1, y: 0 } : { opacity: 0, y: 8 },
      transition: M3_TRANSITIONS.enter,
    },
  } as const;
};

/* ========================================================================= */
/* SPRINGS & VALUES                                                          */
/* ========================================================================= */

/**
 * A spring-tracked number. Set the target with `.set()`; read it in a style.
 *
 * ```tsx
 * const x = useSpringNumber(0, 'responsive');
 * <motion.div style={{ x }} onPointerMove={e => x.set(e.clientX)} />
 * ```
 */
export const useSpringNumber = (
  initial = 0,
  preset: M3SpringName = 'gentle',
): MotionValue<number> => {
  const reduced = useReducedMotionSafe();
  const value = useMotionValue(initial);
  return useSpring(value, reduced ? { duration: 0 } : M3_SPRING_OPTIONS[preset]);
};

/**
 * A number that animates to `value` whenever it changes — metric cards, KPI
 * counters (§12: "primary value dominates").
 *
 * Returns a plain number so it can be formatted before rendering.
 *
 * ```tsx
 * const shown = useCountUp(revenue);
 * <span>{shown.toLocaleString('id-ID')}</span>
 * ```
 */
export const useCountUp = (
  value: number,
  { duration, decimals = 0 }: { duration?: number; decimals?: number } = {},
): number => {
  const reduced = useReducedMotionSafe();
  const [display, setDisplay] = useState(value);
  const previous = useRef(value);

  useEffect(() => {
    if (reduced) {
      previous.current = value;
      setDisplay(value);
      return;
    }
    const factor = 10 ** decimals;
    const controls = animate(previous.current, value, {
      duration: duration ?? M3_TRANSITIONS.chart.duration,
      ease: M3_TRANSITIONS.chart.ease,
      onUpdate: (latest) => setDisplay(Math.round(latest * factor) / factor),
      onComplete: () => {
        previous.current = value;
      },
    });
    return () => controls.stop();
  }, [value, duration, decimals, reduced]);

  return display;
};

/* ========================================================================= */
/* Re-exports — Motion's own hooks, so one import serves                     */
/* ========================================================================= */

export {
  useScroll,
  useSpring,
  useTransform,
  useInView,
  useMotionValue,
  useMotionValueEvent,
  useMotionTemplate,
  useAnimate,
  useAnimationFrame,
  useAnimationControls,
  useDragControls,
  useReducedMotion,
  useTime,
  useVelocity,
} from 'motion/react';
export type { MotionValue };
