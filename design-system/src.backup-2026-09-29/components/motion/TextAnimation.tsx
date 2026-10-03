/**
 * @license MIT
 * Text Animation — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-text-animation
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design — restrained, Material easing, never bouncy
 *   §9  Accessibility — `prefers-reduced-motion`
 *
 * Splitting text breaks it into per-character spans, which screen readers read
 * one letter at a time. Every component here therefore keeps the original
 * string in a visually-hidden node and marks the split output `aria-hidden`
 * (§9 "Screen reader label", "Semantic HTML").
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useEffect, useRef, useState } from 'react';
import { motion, useInView } from 'motion/react';
import {
  M3_TRANSITIONS,
  M3_STAGGER,
  staggerFor,
  useReducedMotionSafe,
  type M3StaggerName,
} from '../../motion/index.js';

/* ========================================================================= */
/* SplitText                                                                 */
/* ========================================================================= */

export type SplitUnit = 'chars' | 'words' | 'lines';

export interface SplitTextProps {
  children: string;
  /** What to split on. `lines` splits on newlines. */
  type?: SplitUnit;
  /** Per-unit delay. A name from the §18 stagger scale, or seconds. */
  stagger?: M3StaggerName | number;
  /** Seconds. Defaults to the §18 modal duration. */
  duration?: number;
  delay?: number;
  /** `load` runs once mounted, `view` when scrolled into view, `hover` on enter. */
  trigger?: 'load' | 'view' | 'hover';
  className?: string;
  style?: React.CSSProperties;
}

const splitInto = (text: string, type: SplitUnit): string[] => {
  if (type === 'chars') return Array.from(text);
  if (type === 'lines') return text.split('\n');
  return text.split(' ');
};

export const SplitText: React.FC<SplitTextProps> = ({
  children,
  type = 'chars',
  stagger = 'tight',
  duration,
  delay = 0,
  trigger = 'load',
  className = '',
  style,
}) => {
  const ref = useRef<HTMLSpanElement>(null);
  const inView = useInView(ref, { once: true, amount: 0.3 });
  const [hovered, setHovered] = useState(false);
  const reduced = useReducedMotionSafe();

  const units = splitInto(children, type);
  const perUnit =
    typeof stagger === 'number' ? stagger : staggerFor(units.length, stagger);

  const active =
    reduced ||
    (trigger === 'load' && true) ||
    (trigger === 'view' && inView) ||
    (trigger === 'hover' && hovered);

  return (
    <span
      ref={ref}
      className={`morphic-split-text ${className}`}
      onMouseEnter={() => trigger === 'hover' && setHovered(true)}
      style={{
        display: 'inline-flex',
        flexWrap: 'wrap',
        overflow: 'hidden',
        ...style,
      }}
    >
      {/* §9 — the readable string, hidden from sight but not from AT */}
      <span className="morphic-visually-hidden">{children}</span>

      <span aria-hidden="true" style={{ display: 'inline-flex', flexWrap: 'wrap' }}>
        {units.map((unit, i) => (
          <motion.span
            key={`${unit}-${i}`}
            initial={reduced ? false : { opacity: 0, y: 12 }}
            animate={active ? { opacity: 1, y: 0 } : { opacity: 0, y: 12 }}
            transition={{
              duration: duration ?? M3_TRANSITIONS.enter.duration,
              ease: M3_TRANSITIONS.enter.ease,
              delay: reduced ? 0 : delay + i * perUnit,
            }}
            style={{ display: 'inline-block', willChange: 'transform, opacity' }}
          >
            {unit === ' ' ? ' ' : unit}
            {type === 'words' && i < units.length - 1 ? ' ' : null}
          </motion.span>
        ))}
      </span>
    </span>
  );
};

/* ========================================================================= */
/* ScrambleText                                                              */
/* ========================================================================= */

export interface ScrambleTextProps {
  text: string;
  /** Seconds. */
  duration?: number;
  /** Glyph pool the unresolved characters are drawn from. */
  characters?: string;
  trigger?: 'load' | 'view' | 'hover';
  className?: string;
  style?: React.CSSProperties;
}

const DEFAULT_GLYPHS = '!<>-_\\/[]{}—=+*^?#________';

export const ScrambleText: React.FC<ScrambleTextProps> = ({
  text,
  duration = 1.2,
  characters = DEFAULT_GLYPHS,
  trigger = 'load',
  className = '',
  style,
}) => {
  const ref = useRef<HTMLSpanElement>(null);
  const inView = useInView(ref, { once: true, amount: 0.3 });
  const reduced = useReducedMotionSafe();
  const [display, setDisplay] = useState(reduced ? text : '');
  const running = useRef(false);

  const run = React.useCallback(() => {
    // §9 — scrambling glyphs is pure decoration; skip it entirely.
    if (reduced || running.current) {
      setDisplay(text);
      return;
    }
    running.current = true;

    const start = performance.now();
    const total = duration * 1000;
    let raf = 0;

    const tick = (now: number) => {
      const progress = Math.min(1, (now - start) / total);
      const revealed = Math.floor(progress * text.length);
      let out = '';
      for (let i = 0; i < text.length; i += 1) {
        out +=
          i < revealed
            ? text[i]
            : characters[Math.floor(Math.random() * characters.length)];
      }
      setDisplay(out);
      if (progress < 1) {
        raf = requestAnimationFrame(tick);
      } else {
        setDisplay(text);
        running.current = false;
      }
    };

    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [text, duration, characters, reduced]);

  useEffect(() => {
    if (trigger === 'load' || (trigger === 'view' && inView)) return run();
    if (reduced) setDisplay(text);
  }, [trigger, inView, run, reduced, text]);

  return (
    <span
      ref={ref}
      onMouseEnter={() => trigger === 'hover' && run()}
      className={`morphic-scramble-text ${className}`}
      style={{ display: 'inline-block', fontVariantNumeric: 'tabular-nums', ...style }}
    >
      {/* §9 — the settled string is what AT announces */}
      <span className="morphic-visually-hidden">{text}</span>
      <span aria-hidden="true">{display || text}</span>
    </span>
  );
};

/* ========================================================================= */
/* CountUp                                                                   */
/* ========================================================================= */

export interface CountUpProps {
  end: number;
  start?: number;
  /** Seconds. Defaults to the §18 chart duration — this is a data reveal. */
  duration?: number;
  prefix?: string;
  suffix?: string;
  decimals?: number;
  /** Locale for thousand separators. Defaults to the browser's. */
  locale?: string;
  trigger?: 'load' | 'view';
  className?: string;
  style?: React.CSSProperties;
}

/**
 * An animating metric value (§12 "primary value dominates", §6 tabular
 * numerals). The settled number is always in the DOM for screen readers.
 */
export const CountUp: React.FC<CountUpProps> = ({
  end,
  start = 0,
  duration,
  prefix = '',
  suffix = '',
  decimals = 0,
  locale,
  trigger = 'load',
  className = '',
  style,
}) => {
  const ref = useRef<HTMLSpanElement>(null);
  const inView = useInView(ref, { once: true, amount: 0.3 });
  const reduced = useReducedMotionSafe();
  const [value, setValue] = useState(reduced ? end : start);

  const active = trigger === 'load' || inView;

  useEffect(() => {
    if (!active) return;
    if (reduced) {
      setValue(end);
      return;
    }

    const from = value;
    const total = (duration ?? M3_TRANSITIONS.chart.duration ?? 0.55) * 1000;
    const began = performance.now();
    let raf = 0;

    const tick = (now: number) => {
      const t = Math.min(1, (now - began) / total);
      // Same curve as the CSS chart transition, so numbers and bars agree.
      const eased = 1 - (1 - t) ** 3;
      setValue(from + (end - from) * eased);
      if (t < 1) raf = requestAnimationFrame(tick);
      else setValue(end);
    };

    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
    // `value` is intentionally excluded: it is the animation's own output.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [end, active, duration, reduced]);

  const formatted = value.toLocaleString(locale, {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });

  return (
    <span
      ref={ref}
      className={`morphic-count-up ${className}`}
      style={{ fontVariantNumeric: 'tabular-nums', ...style }}
    >
      <span className="morphic-visually-hidden">
        {prefix}
        {end.toLocaleString(locale, {
          minimumFractionDigits: decimals,
          maximumFractionDigits: decimals,
        })}
        {suffix}
      </span>
      <span aria-hidden="true">
        {prefix}
        {formatted}
        {suffix}
      </span>
    </span>
  );
};
