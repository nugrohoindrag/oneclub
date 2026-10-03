/**
 * @license MIT
 * Presence & Layout — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-animate-presence
 *              https://motion.dev/docs/react-layout-animations
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design      — modal 220–280ms, page 250–350ms
 *   §19 Micro-interactions — "selected item should smoothly transition its
 *                             background container"
 *   §9  Accessibility      — `prefers-reduced-motion`
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useId } from 'react';
import { AnimatePresence, LayoutGroup, motion, MotionConfig } from 'motion/react';
import {
  M3_TRANSITIONS,
  PRESENCE_PRESETS,
  useReducedMotionSafe,
  type PresencePreset,
} from '../../motion/index.js';

/* ========================================================================= */
/* Presence — AnimatePresence with the §18 presets applied                   */
/* ========================================================================= */

export interface PresenceProps {
  /** Render the child when true. */
  show: boolean;
  children: React.ReactNode;
  /** Which arrival/exit shape to use. */
  preset?: PresencePreset;
  /** `wait` holds the exit before the next enter — use for view switches. */
  mode?: 'sync' | 'wait' | 'popLayout';
  /** Run the enter animation on first mount. Default false, per M3. */
  initial?: boolean;
  as?: 'div' | 'span' | 'li' | 'section';
  className?: string;
  style?: React.CSSProperties;
  onExitComplete?: () => void;
}

/**
 * ```tsx
 * <Presence show={isOpen} preset="dialog">
 *   <DialogSurface />
 * </Presence>
 * ```
 *
 * With reduced motion on, the child appears and disappears without travel (§9).
 */
export const Presence: React.FC<PresenceProps> = ({
  show,
  children,
  preset = 'fadeUp',
  mode = 'sync',
  initial = false,
  as = 'div',
  className = '',
  style,
  onExitComplete,
}) => {
  const reduced = useReducedMotionSafe();
  const variants = reduced ? PRESENCE_PRESETS.fade : PRESENCE_PRESETS[preset];
  const Component = motion[as] as typeof motion.div;

  return (
    <AnimatePresence mode={mode} initial={initial} onExitComplete={onExitComplete}>
      {show && (
        <Component
          className={`morphic-presence ${className}`}
          style={style}
          variants={variants}
          initial="hidden"
          animate="visible"
          exit="exit"
        >
          {children}
        </Component>
      )}
    </AnimatePresence>
  );
};

/* ========================================================================= */
/* PresenceList — items entering and leaving a list                          */
/* ========================================================================= */

export interface PresenceListProps<T> {
  items: T[];
  /** Stable identity per item. Presence cannot animate without one. */
  getKey: (item: T, index: number) => React.Key;
  children: (item: T, index: number) => React.ReactNode;
  preset?: PresencePreset;
  /** Animate reflow of the remaining rows when one is removed. */
  layout?: boolean;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * A list where rows animate in, out, and reflow — transaction lists (§16),
 * alert feeds, filter results.
 */
export function PresenceList<T>({
  items,
  getKey,
  children,
  preset = 'fadeUp',
  layout = true,
  className = '',
  style,
}: PresenceListProps<T>) {
  const reduced = useReducedMotionSafe();
  const variants = reduced ? PRESENCE_PRESETS.fade : PRESENCE_PRESETS[preset];

  return (
    <div className={`morphic-presence-list ${className}`} style={style}>
      <AnimatePresence initial={false} mode="popLayout">
        {items.map((item, i) => (
          <motion.div
            key={getKey(item, i)}
            layout={layout && !reduced}
            variants={variants}
            initial="hidden"
            animate="visible"
            exit="exit"
            transition={M3_TRANSITIONS.layout}
          >
            {children(item, i)}
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  );
}

/* ========================================================================= */
/* SharedIndicator — the moving selection pill (§19 Navigation)              */
/* ========================================================================= */

export interface SharedIndicatorProps {
  /** True on the currently selected item. */
  active: boolean;
  /**
   * Shared id tying the group together. Two indicators with the same id
   * animate between each other. Auto-generated per group if omitted.
   */
  groupId?: string;
  color?: string;
  radius?: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * §19: "Selected item should smoothly transition its background container."
 *
 * Render inside a `position: relative` parent, behind the label:
 *
 * ```tsx
 * <LayoutGroup id="sidebar">
 *   {items.map(i => (
 *     <div key={i.id} style={{ position: 'relative' }}>
 *       <SharedIndicator active={i.id === current} groupId="sidebar" />
 *       <span style={{ position: 'relative' }}>{i.label}</span>
 *     </div>
 *   ))}
 * </LayoutGroup>
 * ```
 */
export const SharedIndicator: React.FC<SharedIndicatorProps> = ({
  active,
  groupId,
  color = 'var(--md-sys-color-primary-container)',
  radius = 'var(--radius-md)',
  className = '',
  style,
}) => {
  const fallbackId = useId();
  const reduced = useReducedMotionSafe();
  if (!active) return null;

  return (
    <motion.span
      aria-hidden="true"
      layoutId={reduced ? undefined : `morphic-indicator-${groupId ?? fallbackId}`}
      className={`morphic-shared-indicator ${className}`}
      transition={M3_TRANSITIONS.card}
      style={{
        position: 'absolute',
        inset: 0,
        backgroundColor: color,
        borderRadius: radius,
        zIndex: 0,
        ...style,
      }}
    />
  );
};

/* ========================================================================= */
/* MorphicMotionConfig — app-level motion settings                           */
/* ========================================================================= */

export interface MorphicMotionConfigProps {
  children: React.ReactNode;
  /**
   * `user` honours the OS setting (§9, the default and the correct choice).
   * `always` / `never` exist for a product-level override.
   */
  reducedMotion?: 'user' | 'always' | 'never';
}

/**
 * Wrap the application once. Sets the default transition to the §18 card
 * timing, so any `motion` element without an explicit `transition` still moves
 * at system speed.
 */
export const MorphicMotionConfig: React.FC<MorphicMotionConfigProps> = ({
  children,
  reducedMotion = 'user',
}) => (
  <MotionConfig reducedMotion={reducedMotion} transition={M3_TRANSITIONS.card}>
    {children}
  </MotionConfig>
);
