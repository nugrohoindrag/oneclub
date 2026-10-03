/**
 * @license MIT
 * SVG Animation — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-svg-animation
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §13 Charts        — thin line, rounded stroke, integrated into the UI
 *   §18 Motion Design — restrained; chart draw sits in the 400–700ms band
 *   §9  Accessibility — decorative SVG is hidden from AT; `prefers-reduced-motion`
 *
 * Motion animates `pathLength` natively, so a path draws without measuring
 * `getTotalLength()` or juggling dash offsets by hand.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useRef, useState } from 'react';
import { motion, useInView } from 'motion/react';
import { M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

/* ========================================================================= */
/* DrawSVG                                                                   */
/* ========================================================================= */

export interface DrawSVGProps {
  /** Path data. */
  d: string;
  width?: number | string;
  height?: number | string;
  viewBox?: string;
  /** Defaults to the theme primary — never a literal (§6). */
  strokeColor?: string;
  strokeWidth?: number;
  /** Seconds. Defaults to the §18 chart duration. */
  duration?: number;
  delay?: number;
  trigger?: 'load' | 'view' | 'hover';
  /** Accessible name. Omit for decoration; the SVG is then `aria-hidden` (§9). */
  label?: string;
  className?: string;
}

/**
 * A stroke that draws itself on.
 *
 * ```tsx
 * <DrawSVG d="M 0 60 C 50 10, 110 90, 200 20" trigger="view" />
 * ```
 */
export const DrawSVG: React.FC<DrawSVGProps> = ({
  d,
  width = 200,
  height = 120,
  viewBox = '0 0 200 120',
  strokeColor = 'var(--md-sys-color-primary)',
  strokeWidth = 3,
  duration,
  delay = 0,
  trigger = 'load',
  label,
  className = '',
}) => {
  const ref = useRef<SVGSVGElement>(null);
  const inView = useInView(ref, { once: true, amount: 0.3 });
  const [hovered, setHovered] = useState(false);
  const reduced = useReducedMotionSafe();

  const active =
    reduced ||
    trigger === 'load' ||
    (trigger === 'view' && inView) ||
    (trigger === 'hover' && hovered);

  return (
    <svg
      ref={ref}
      width={width}
      height={height}
      viewBox={viewBox}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      onMouseEnter={() => trigger === 'hover' && setHovered(true)}
      className={`morphic-draw-svg ${className}`}
      style={{ overflow: 'visible' }}
    >
      <motion.path
        d={d}
        fill="none"
        stroke={strokeColor}
        strokeWidth={strokeWidth}
        /* §13 — rounded stroke */
        strokeLinecap="round"
        strokeLinejoin="round"
        initial={reduced ? false : { pathLength: 0 }}
        animate={{ pathLength: active ? 1 : 0 }}
        transition={{
          duration: duration ?? M3_TRANSITIONS.chart.duration,
          ease: M3_TRANSITIONS.chart.ease,
          delay: reduced ? 0 : delay,
        }}
      />
    </svg>
  );
};

/* ========================================================================= */
/* DrawPath — the same behaviour as a child of an existing <svg>             */
/* ========================================================================= */

export interface DrawPathProps extends React.ComponentProps<typeof motion.path> {
  d: string;
  duration?: number;
  delay?: number;
  /** Set false to hold the path undrawn, e.g. until a filter resolves. */
  draw?: boolean;
}

/**
 * Use inside a chart's own `<svg>` so a series draws with the §18 chart
 * timing (§19: "charts can animate when first rendered or when filters
 * change").
 */
export const DrawPath: React.FC<DrawPathProps> = ({
  d,
  duration,
  delay = 0,
  draw = true,
  ...rest
}) => {
  const reduced = useReducedMotionSafe();
  return (
    <motion.path
      d={d}
      fill="none"
      strokeLinecap="round"
      strokeLinejoin="round"
      initial={reduced ? false : { pathLength: 0 }}
      animate={{ pathLength: draw ? 1 : 0 }}
      transition={{
        duration: reduced ? 0 : (duration ?? M3_TRANSITIONS.chart.duration),
        ease: M3_TRANSITIONS.chart.ease,
        delay: reduced ? 0 : delay,
      }}
      {...rest}
    />
  );
};

/* ========================================================================= */
/* AnimatedCircularProgress                                                  */
/* ========================================================================= */

export interface AnimatedCircularProgressProps {
  /** 0–100. */
  value: number;
  size?: number;
  /** §13 — "thick donut ring". */
  strokeWidth?: number;
  trackColor?: string;
  color?: string;
  label?: string;
  className?: string;
  children?: React.ReactNode;
}

/**
 * A ring that sweeps to its value — the donut/gauge primitive from §3.9,
 * drawn with `pathLength` rather than dash maths.
 */
export const AnimatedCircularProgress: React.FC<AnimatedCircularProgressProps> = ({
  value,
  size = 96,
  strokeWidth = 10,
  trackColor = 'var(--md-sys-color-surface-container-high)',
  color = 'var(--md-sys-color-primary)',
  label,
  className = '',
  children,
}) => {
  const reduced = useReducedMotionSafe();
  const clamped = Math.max(0, Math.min(100, value));
  const r = (size - strokeWidth) / 2;
  const c = size / 2;

  return (
    <div
      className={`morphic-circular-progress ${className}`}
      style={{ position: 'relative', width: size, height: size }}
      role="progressbar"
      aria-valuenow={Math.round(clamped)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={label}
    >
      <svg width={size} height={size} aria-hidden="true" style={{ transform: 'rotate(-90deg)' }}>
        <circle cx={c} cy={c} r={r} fill="none" stroke={trackColor} strokeWidth={strokeWidth} />
        <motion.circle
          cx={c}
          cy={c}
          r={r}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          initial={reduced ? false : { pathLength: 0 }}
          animate={{ pathLength: clamped / 100 }}
          transition={{
            duration: reduced ? 0 : M3_TRANSITIONS.chart.duration,
            ease: M3_TRANSITIONS.chart.ease,
          }}
        />
      </svg>
      {children && (
        <div
          style={{
            position: 'absolute',
            inset: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          {children}
        </div>
      )}
    </div>
  );
};
