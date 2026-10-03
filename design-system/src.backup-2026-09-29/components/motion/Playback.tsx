/**
 * @license MIT
 * Playback — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/animate#controls
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §18 Motion Design — restrained durations, Material easing
 *   §9  Accessibility — keyboard behaviour, screen-reader labels, reduced motion
 *
 * `MotionScrubber` replaces the previous `GSDevToolsScrubber`, which bound to a
 * `gsap.core.Timeline`. It drives Motion's `AnimationPlaybackControls` — the
 * object `animate()` returns — so the whole system runs on one engine.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useEffect, useRef, useState } from 'react';
import { motion, useAnimationFrame } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

/* ========================================================================= */
/* MotionScrubber                                                            */
/* ========================================================================= */

/**
 * The subset of Motion's `AnimationPlaybackControls` this component needs.
 * Typed structurally so it accepts the return of `animate()` directly.
 */
export interface PlaybackControls {
  play: () => void;
  pause: () => void;
  time: number;
  duration: number;
  speed: number;
  complete?: () => void;
  cancel?: () => void;
}

export interface MotionScrubberProps {
  /** The controls returned by `animate(...)`. Null renders a disabled bar. */
  animation: PlaybackControls | null;
  label?: string;
  className?: string;
}

/**
 * Transport controls for a running animation — restart, play/pause, scrub.
 *
 * ```tsx
 * const [controls, setControls] = useState(null);
 * useEffect(() => setControls(animate(el, { x: 200 }, { duration: 2 })), []);
 * <MotionScrubber animation={controls} />
 * ```
 */
export const MotionScrubber: React.FC<MotionScrubberProps> = ({
  animation,
  label = 'Animation playback',
  className = '',
}) => {
  const [progress, setProgress] = useState(0);
  const [playing, setPlaying] = useState(true);
  const scrubbing = useRef(false);

  // Read progress on the animation frame rather than on a 50ms interval, so
  // the scrub head tracks the animation exactly.
  useAnimationFrame(() => {
    if (!animation || scrubbing.current || !animation.duration) return;
    setProgress(animation.time / animation.duration);
  });

  const seek = (value: number) => {
    if (!animation) return;
    scrubbing.current = true;
    animation.pause();
    animation.time = value * animation.duration;
    setProgress(value);
    setPlaying(false);
    scrubbing.current = false;
  };

  const toggle = () => {
    if (!animation) return;
    if (playing) animation.pause();
    else animation.play();
    setPlaying(!playing);
  };

  const restart = () => {
    if (!animation) return;
    animation.time = 0;
    animation.play();
    setProgress(0);
    setPlaying(true);
  };

  const disabled = !animation;

  return (
    <div
      role="group"
      aria-label={label}
      className={`morphic-motion-scrubber ${className}`}
      style={{
        padding: 'var(--md-sys-spacing-3) var(--md-sys-spacing-4)',
        borderRadius: 'var(--radius-md)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        border: '1px solid var(--md-sys-color-border)',
        display: 'flex',
        alignItems: 'center',
        gap: 'var(--md-sys-spacing-3)',
        width: '100%',
        opacity: disabled ? 'var(--md-sys-opacity-disabled)' : 1,
      }}
    >
      <button
        type="button"
        onClick={restart}
        disabled={disabled}
        aria-label="Restart"
        style={buttonStyle}
      >
        <Icon name="restart_alt" size={18} />
      </button>

      <button
        type="button"
        onClick={toggle}
        disabled={disabled}
        aria-label={playing ? 'Pause' : 'Play'}
        style={buttonStyle}
      >
        <Icon name={playing ? 'pause' : 'play_arrow'} size={20} />
      </button>

      <input
        type="range"
        min={0}
        max={1}
        step={0.005}
        value={progress}
        disabled={disabled}
        onChange={(e) => seek(Number(e.target.value))}
        aria-label="Scrub"
        style={{
          flex: 1,
          cursor: disabled ? 'not-allowed' : 'ew-resize',
          accentColor: 'var(--md-sys-color-primary)',
        }}
      />

      <span
        style={{
          fontSize: 'var(--md-sys-typescale-meta-size)',
          fontVariantNumeric: 'tabular-nums',
          color: 'var(--md-sys-color-on-surface-variant)',
          width: '42px',
          textAlign: 'right',
        }}
      >
        {Math.round(progress * 100)}%
      </span>
    </div>
  );
};

const buttonStyle: React.CSSProperties = {
  border: 'none',
  background: 'none',
  padding: 0,
  cursor: 'pointer',
  color: 'var(--md-sys-color-primary)',
  display: 'inline-flex',
  alignItems: 'center',
  /* §9 — minimum touch target */
  minWidth: 'var(--md-sys-density-control-height-sm)',
  minHeight: 'var(--md-sys-density-control-height-sm)',
  justifyContent: 'center',
};

/* ========================================================================= */
/* ParticleBurst                                                             */
/* ========================================================================= */

interface Particle {
  id: number;
  x: number;
  y: number;
  size: number;
  rotate: number;
  duration: number;
  color: string;
  round: boolean;
}

export interface ParticleBurstProps {
  particleCount?: number;
  /** Fire on mount instead of on click. */
  auto?: boolean;
  children?: React.ReactNode;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * A one-shot particle burst, rendered declaratively by Motion rather than by
 * appending DOM nodes.
 *
 * §32 rules out excessive effects, so this is not something to place on a
 * dashboard — it belongs to celebratory moments (onboarding complete, goal
 * reached). It is fully skipped under `prefers-reduced-motion` (§9).
 */
export const ParticleBurst: React.FC<ParticleBurstProps> = ({
  particleCount = 24,
  auto = false,
  children,
  className = '',
  style,
}) => {
  const reduced = useReducedMotionSafe();
  const [particles, setParticles] = useState<Particle[]>([]);
  const seed = useRef(0);

  const colors = [
    'var(--md-sys-color-primary)',
    'var(--md-sys-color-chart-secondary)',
    'var(--md-sys-color-chart-tertiary)',
    'var(--md-sys-color-chart-quaternary)',
    'var(--md-sys-color-tertiary)',
  ];

  const burst = React.useCallback(() => {
    if (reduced) return;
    seed.current += 1;
    const batch = seed.current * 1000;
    setParticles(
      Array.from({ length: particleCount }, (_, i) => {
        const angle = Math.random() * Math.PI * 2;
        const velocity = Math.random() * 140 + 60;
        return {
          id: batch + i,
          x: Math.cos(angle) * velocity,
          y: Math.sin(angle) * velocity + 90,
          size: Math.random() * 6 + 5,
          rotate: Math.random() * 540 - 270,
          duration: Math.random() * 0.6 + 0.6,
          color: colors[Math.floor(Math.random() * colors.length)],
          round: Math.random() > 0.5,
        };
      }),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [particleCount, reduced]);

  useEffect(() => {
    if (auto) burst();
  }, [auto, burst]);

  return (
    <div
      onClick={burst}
      className={`morphic-particle-burst ${className}`}
      style={{
        position: 'relative',
        overflow: 'hidden',
        borderRadius: 'var(--radius-lg)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        border: '1px solid var(--md-sys-color-border)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: '140px',
        cursor: reduced ? 'default' : 'pointer',
        userSelect: 'none',
        ...style,
      }}
    >
      <div aria-hidden="true" style={{ position: 'absolute', inset: 0, pointerEvents: 'none' }}>
        {particles.map((p) => (
          <motion.span
            key={p.id}
            initial={{ x: 0, y: 0, opacity: 1, rotate: 0 }}
            animate={{ x: p.x, y: p.y, opacity: 0, rotate: p.rotate }}
            transition={{ duration: p.duration, ease: M3_TRANSITIONS.exit.ease }}
            onAnimationComplete={() =>
              setParticles((current) => current.filter((c) => c.id !== p.id))
            }
            style={{
              position: 'absolute',
              left: '50%',
              top: '50%',
              width: p.size,
              height: p.size,
              borderRadius: p.round ? 'var(--radius-pill)' : 'var(--radius-xs)',
              backgroundColor: p.color,
            }}
          />
        ))}
      </div>

      <div style={{ pointerEvents: 'none', textAlign: 'center', position: 'relative' }}>
        {children ?? (
          <div
            style={{
              fontSize: 'var(--md-sys-typescale-body-size-sm)',
              fontWeight: 600,
              color: 'var(--md-sys-color-on-surface-variant)',
            }}
          >
            {reduced ? 'Particle burst disabled (reduced motion)' : 'Click to burst'}
          </div>
        )}
      </div>
    </div>
  );
};
