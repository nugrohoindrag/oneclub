/**
 * @license MIT
 * In-View & Motion Factory — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-motion-component#viewport
 *              https://motion.dev/docs/react-motion-component#motion.create
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design — restrained
 *   §9  Accessibility — `prefers-reduced-motion`
 *
 * `Reveal` (ScrollAnimation.tsx) drives its animation from the `useInView`
 * hook, which re-renders React on every enter/leave. `InView` here uses the
 * DECLARATIVE `whileInView` + `viewport` props instead: Motion handles the
 * intersection off the React render cycle, so a long list of revealing rows
 * costs nothing per frame. Prefer this one for lists.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { motion } from 'motion/react';
import { M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

/* ========================================================================= */
/* InView — declarative whileInView + viewport                               */
/* ========================================================================= */

export interface ViewportOptions {
  /** Animate once and stay. */
  once?: boolean;
  /** Scrollable ancestor. Omit to use the browser viewport. */
  root?: React.RefObject<Element | null>;
  /** Intersection margin, e.g. `'-10% 0px'`. */
  margin?: string;
  /** How much must be visible: `'some'`, `'all'`, or 0–1. */
  amount?: 'some' | 'all' | number;
}

export interface InViewProps {
  children: React.ReactNode;
  /** State while out of view. */
  from?: Parameters<typeof motion.div>[0]['initial'];
  /** State while in view. */
  to?: Parameters<typeof motion.div>[0]['whileInView'];
  viewport?: ViewportOptions;
  transition?: Parameters<typeof motion.div>[0]['transition'];
  onViewportEnter?: (entry: IntersectionObserverEntry | null) => void;
  onViewportLeave?: (entry: IntersectionObserverEntry | null) => void;
  as?: 'div' | 'section' | 'li' | 'span' | 'article';
  className?: string;
  style?: React.CSSProperties;
}

/**
 * ```tsx
 * <InView viewport={{ once: true, amount: 0.3 }}>
 *   <ChartCard />
 * </InView>
 * ```
 */
export const InView: React.FC<InViewProps> = ({
  children,
  from = { opacity: 0, y: 8 },
  to = { opacity: 1, y: 0 },
  viewport = { once: true, amount: 0.2 },
  transition = M3_TRANSITIONS.enter,
  onViewportEnter,
  onViewportLeave,
  as = 'div',
  className = '',
  style,
}) => {
  const reduced = useReducedMotionSafe();
  const Component = motion[as as 'div'] as typeof motion.div;

  return (
    <Component
      className={`morphic-in-view ${className}`}
      style={style}
      initial={reduced ? false : from}
      whileInView={reduced ? undefined : to}
      viewport={viewport}
      transition={transition}
      onViewportEnter={onViewportEnter}
      onViewportLeave={onViewportLeave}
    >
      {children}
    </Component>
  );
};

/* ========================================================================= */
/* createMotion — motion.create() for Morphic components                     */
/* ========================================================================= */

/**
 * Give an existing component motion superpowers.
 *
 * ```tsx
 * const MotionCard = createMotion(Card);
 * <MotionCard layout whileHover={{ y: -1 }} />
 * ```
 *
 * The wrapped component MUST forward its ref to the DOM element being
 * animated, or Motion has nothing to drive.
 *
 * Motion warns that `motion.create()` must not be called inside a render
 * function — it creates a new component type each time, which remounts the
 * subtree. Call it at module scope, as above.
 *
 * `forwardMotionProps: true` passes the motion props through to the wrapped
 * component instead of filtering them out; leave it off unless the component
 * reads them itself.
 */
export const createMotion: typeof motion.create = motion.create;

/* ========================================================================= */
/* Transform — transformTemplate and custom variant data                     */
/* ========================================================================= */

export interface TransformProps {
  children: React.ReactNode;
  /**
   * Override how Motion composes the transform string. Useful when order
   * matters, e.g. translating AFTER rotating.
   *
   * ```tsx
   * transformTemplate={({ rotate, x }) => `rotate(${rotate}) translateX(${x})`}
   * ```
   */
  transformTemplate?: Parameters<typeof motion.div>[0]['transformTemplate'];
  /** Data passed to dynamic variants — `variants={{ show: (i) => ({...}) }}`. */
  custom?: unknown;
  /** Set false so a parent's variant change does not cascade here. */
  inherit?: boolean;
  variants?: Parameters<typeof motion.div>[0]['variants'];
  initial?: Parameters<typeof motion.div>[0]['initial'];
  animate?: Parameters<typeof motion.div>[0]['animate'];
  transition?: Parameters<typeof motion.div>[0]['transition'];
  onUpdate?: (latest: Record<string, string | number>) => void;
  onAnimationStart?: () => void;
  onAnimationComplete?: () => void;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Surfaces the motion component's advanced props — `transformTemplate`,
 * `custom`, `inherit`, and the animation lifecycle callbacks — without asking
 * callers to drop down to a raw `motion.div`.
 */
export const Transform: React.FC<TransformProps> = ({
  children,
  transformTemplate,
  custom,
  inherit,
  variants,
  initial,
  animate,
  transition = M3_TRANSITIONS.card,
  onUpdate,
  onAnimationStart,
  onAnimationComplete,
  className = '',
  style,
}) => (
  <motion.div
    className={`morphic-transform ${className}`}
    style={style}
    transformTemplate={transformTemplate}
    custom={custom}
    inherit={inherit}
    variants={variants}
    initial={initial}
    animate={animate}
    transition={transition}
    onUpdate={onUpdate}
    onAnimationStart={onAnimationStart}
    onAnimationComplete={onAnimationComplete}
  >
    {children}
  </motion.div>
);

/* ========================================================================= */
/* LayoutBox — the full layout prop surface                                  */
/* ========================================================================= */

export interface LayoutBoxProps {
  children: React.ReactNode;
  /** `true` animates position and size; narrow it to reduce distortion. */
  layout?: boolean | 'position' | 'size';
  /** Shared-element id — two boxes with the same id animate between each other. */
  layoutId?: string;
  /** Re-measure only when this value changes. Cheaper than measuring always. */
  layoutDependency?: unknown;
  /** Mark a scrollable ancestor so scroll offset is accounted for. */
  layoutScroll?: boolean;
  /** Mark a `position: fixed` element so page scroll is accounted for. */
  layoutRoot?: boolean;
  onLayoutAnimationStart?: () => void;
  onLayoutAnimationComplete?: () => void;
  as?: 'div' | 'section' | 'li' | 'span';
  className?: string;
  style?: React.CSSProperties;
}

/**
 * §19: "Selected item should smoothly transition its background container."
 *
 * `SharedIndicator` covers the common navigation case. Reach for `LayoutBox`
 * when you need the rest of the layout surface — `layoutScroll` inside a
 * scrolling panel, `layoutRoot` on a fixed header, or `layoutDependency` to
 * stop a dense grid re-measuring on every render (§24).
 */
export const LayoutBox: React.FC<LayoutBoxProps> = ({
  children,
  layout = true,
  layoutId,
  layoutDependency,
  layoutScroll,
  layoutRoot,
  onLayoutAnimationStart,
  onLayoutAnimationComplete,
  as = 'div',
  className = '',
  style,
}) => {
  const reduced = useReducedMotionSafe();
  const Component = motion[as as 'div'] as typeof motion.div;

  return (
    <Component
      className={`morphic-layout-box ${className}`}
      style={style}
      layout={reduced ? false : layout}
      layoutId={reduced ? undefined : layoutId}
      layoutDependency={layoutDependency}
      layoutScroll={layoutScroll}
      layoutRoot={layoutRoot}
      onLayoutAnimationStart={onLayoutAnimationStart}
      onLayoutAnimationComplete={onLayoutAnimationComplete}
      transition={M3_TRANSITIONS.layout}
    >
      {children}
    </Component>
  );
};
