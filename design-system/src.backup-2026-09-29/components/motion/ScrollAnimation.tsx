/**
 * @license MIT
 * Scroll Animation — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-scroll-animations
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design — subtle movement that communicates hierarchy and state
 *   §24 Density       — the reference is dense; scroll effects must not fight content
 *   §9  Accessibility — `prefers-reduced-motion` disables travel entirely
 *
 * Distances here are deliberately small. A dashboard is read, not scrolled
 * through as a story; §32 rules out "generic Tailwind dashboard aesthetics",
 * which is where long parallax drifts come from.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useRef } from 'react';
import { motion, useInView, useScroll, useSpring, useTransform } from 'motion/react';
import {
  M3_TRANSITIONS,
  M3_SPRING_OPTIONS,
  staggerFor,
  useReducedMotionSafe,
  type M3StaggerName,
  type PresenceDirection,
} from '../../motion/index.js';

/* ========================================================================= */
/* Reveal — animate in when scrolled into view                               */
/* ========================================================================= */

export interface RevealProps {
  children: React.ReactNode;
  /** Travel direction as it arrives. */
  direction?: PresenceDirection;
  /** Pixels of travel. Keep it small (§18). */
  distance?: number;
  /** Seconds added before the animation starts. */
  delay?: number;
  /** Fraction of the element that must be visible, 0–1. */
  amount?: number;
  /** Reveal once, or re-run every time it re-enters. */
  once?: boolean;
  as?: keyof React.JSX.IntrinsicElements;
  className?: string;
  style?: React.CSSProperties;
}

const TRAVEL: Record<PresenceDirection, { x: number; y: number }> = {
  up: { x: 0, y: 1 },
  down: { x: 0, y: -1 },
  left: { x: 1, y: 0 },
  right: { x: -1, y: 0 },
  none: { x: 0, y: 0 },
};

/**
 * ```tsx
 * <Reveal direction="up"><MetricCard … /></Reveal>
 * ```
 */
export const Reveal: React.FC<RevealProps> = ({
  children,
  direction = 'up',
  distance = 8,
  delay = 0,
  amount = 0.2,
  once = true,
  as = 'div',
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const inView = useInView(ref, { once, amount });
  const reduced = useReducedMotionSafe();
  const t = TRAVEL[direction];

  const Component = motion[as as 'div'] as typeof motion.div;
  const hidden = { opacity: 0, x: t.x * distance, y: t.y * distance };

  return (
    <Component
      ref={ref}
      className={`morphic-reveal ${className}`}
      style={style}
      initial={reduced ? false : hidden}
      animate={inView || reduced ? { opacity: 1, x: 0, y: 0 } : hidden}
      transition={{ ...M3_TRANSITIONS.enter, delay: reduced ? 0 : delay }}
    >
      {children}
    </Component>
  );
};

/* ========================================================================= */
/* StaggerReveal — a list whose rows arrive in sequence (§19)                */
/* ========================================================================= */

export interface StaggerRevealProps {
  children: React.ReactNode;
  stagger?: M3StaggerName | number;
  direction?: PresenceDirection;
  distance?: number;
  amount?: number;
  once?: boolean;
  className?: string;
  style?: React.CSSProperties;
}

export const StaggerReveal: React.FC<StaggerRevealProps> = ({
  children,
  stagger = 'normal',
  direction = 'up',
  distance = 8,
  amount = 0.15,
  once = true,
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const inView = useInView(ref, { once, amount });
  const reduced = useReducedMotionSafe();
  const items = React.Children.toArray(children);
  const perItem =
    typeof stagger === 'number' ? stagger : staggerFor(items.length, stagger);
  const t = TRAVEL[direction];
  const hidden = { opacity: 0, x: t.x * distance, y: t.y * distance };

  return (
    <div ref={ref} className={`morphic-stagger-reveal ${className}`} style={style}>
      {items.map((child, i) => (
        <motion.div
          key={i}
          initial={reduced ? false : hidden}
          animate={inView || reduced ? { opacity: 1, x: 0, y: 0 } : hidden}
          transition={{ ...M3_TRANSITIONS.enter, delay: reduced ? 0 : i * perItem }}
        >
          {child}
        </motion.div>
      ))}
    </div>
  );
};

/* ========================================================================= */
/* Parallax                                                                  */
/* ========================================================================= */

export interface ParallaxProps {
  children: React.ReactNode;
  /** Pixels of drift across the full pass through the viewport. */
  distance?: number;
  axis?: 'x' | 'y';
  className?: string;
  style?: React.CSSProperties;
}

export const Parallax: React.FC<ParallaxProps> = ({
  children,
  distance = 24,
  axis = 'y',
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const reduced = useReducedMotionSafe();
  const { scrollYProgress } = useScroll({
    target: ref,
    offset: ['start end', 'end start'],
  });
  const raw = useTransform(
    scrollYProgress,
    [0, 1],
    reduced ? [0, 0] : [distance, -distance],
  );
  const offset = useSpring(raw, reduced ? { duration: 0 } : M3_SPRING_OPTIONS.responsive);

  return (
    <motion.div
      ref={ref}
      className={`morphic-parallax ${className}`}
      style={{ ...style, [axis]: offset }}
    >
      {children}
    </motion.div>
  );
};

/* ========================================================================= */
/* ScrollProgressBar                                                         */
/* ========================================================================= */

export interface ScrollProgressBarProps {
  /** Element to track. Omit to track the page. */
  target?: React.RefObject<HTMLElement | null>;
  height?: number;
  color?: string;
  /** Pin to the top of the viewport. */
  fixed?: boolean;
  className?: string;
}

/**
 * A reading-progress rail. Pairs with the compact header (§9) — it adds
 * orientation without adding height.
 */
export const ScrollProgressBar: React.FC<ScrollProgressBarProps> = ({
  target,
  height = 2,
  color = 'var(--md-sys-color-primary)',
  fixed = true,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();
  const { scrollYProgress } = useScroll(
    target ? { target } : undefined,
  );
  const scaleX = useSpring(
    scrollYProgress,
    reduced ? { duration: 0 } : M3_SPRING_OPTIONS.responsive,
  );

  return (
    <motion.div
      aria-hidden="true"
      className={`morphic-scroll-progress ${className}`}
      style={{
        scaleX,
        transformOrigin: 'left',
        height,
        backgroundColor: color,
        borderRadius: 'var(--radius-pill)',
        position: fixed ? 'fixed' : 'relative',
        top: fixed ? 0 : undefined,
        left: fixed ? 0 : undefined,
        right: fixed ? 0 : undefined,
        zIndex: fixed ? 'var(--md-sys-z-sticky)' : undefined,
      }}
    />
  );
};

/* ========================================================================= */
/* ScrollScale — a surface that settles as it enters                         */
/* ========================================================================= */

export interface ScrollScaleProps {
  children: React.ReactNode;
  /** Starting scale. Stays close to 1 — §18 is restrained. */
  from?: number;
  className?: string;
  style?: React.CSSProperties;
}

export const ScrollScale: React.FC<ScrollScaleProps> = ({
  children,
  from = 0.98,
  className = '',
  style,
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const reduced = useReducedMotionSafe();
  const { scrollYProgress } = useScroll({
    target: ref,
    offset: ['start end', 'center center'],
  });
  const scale = useTransform(scrollYProgress, [0, 1], reduced ? [1, 1] : [from, 1]);
  const opacity = useTransform(scrollYProgress, [0, 0.6], reduced ? [1, 1] : [0, 1]);

  return (
    <motion.div
      ref={ref}
      className={`morphic-scroll-scale ${className}`}
      style={{ ...style, scale, opacity }}
    >
      {children}
    </motion.div>
  );
};
