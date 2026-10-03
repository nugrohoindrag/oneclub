/**
 * @license MIT
 * HeroCard Component — Material Design 3 Expressive Morphic Surfaces
 * 07. Surfaces & Containers
 * 
 * Features:
 * - 🎨 Interactive Multi-Layer Vector Parallax on Hover (Internal artwork responds dynamically to mouse)
 * - 💡 Dynamic 120fps Cursor Spotlight (Hardware-accelerated via useMotionTemplate)
 * - 🛡️ Clean Flat Surface (No outer glowing neon shadow halos)
 * - 📐 Clean 2-Column Responsive Layout (Left Text, Right-Aligned Actions & Metrics)
 * - 🔘 Tactile Action Buttons with Micro-interactions
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState, useRef } from 'react';
import {
  motion,
  useMotionValue,
  useSpring,
  useTransform,
  useMotionTemplate,
  type Transition,
} from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { M3_EASE, useReducedMotionSafe } from '../../motion/index.js';

export type HeroCardVariant =
  | 'gradient'
  | 'lime'
  | 'brand'
  | 'aurora'
  | 'glass'
  | 'surface';

export interface HeroCardMetric {
  label: string;
  value: string;
  trend?: string;
}

export interface HeroCardProps {
  badgeLabel?: string;
  badgeIcon?: string;
  greeting?: string;
  userName?: string;
  title?: string;
  subtitle?: string;
  variant?: HeroCardVariant;
  decorated?: boolean;
  metric?: HeroCardMetric;
  actions?: React.ReactNode;
  primaryActionLabel?: string;
  onPrimaryAction?: () => void;
  secondaryActionLabel?: string;
  onSecondaryAction?: () => void;
  className?: string;
  children?: React.ReactNode;
}

interface HeroStyleConfig {
  background: string;
  color: string;
  badgeBg: string;
  badgeText: string;
  subtitleColor: string;
  border: string;
  decorFill: string;
  decorFillSoft: string;
  decorStroke: string;
  glowColor: string;
}

const resolveVariant = (variant: HeroCardVariant): HeroStyleConfig => {
  switch (variant) {
    case 'gradient':
    case 'lime':
      return {
        background: 'linear-gradient(135deg, #BCEE88 0%, #A4E664 42%, #82DC34 100%)',
        color: '#123403',
        badgeBg: 'rgba(18, 52, 3, 0.9)',
        badgeText: '#CBF68E',
        subtitleColor: 'rgba(18, 52, 3, 0.85)',
        border: '1px solid rgba(255, 255, 255, 0.45)',
        decorFill: 'rgba(255, 255, 255, 0.24)',
        decorFillSoft: 'rgba(255, 255, 255, 0.16)',
        decorStroke: 'rgba(255, 255, 255, 0.9)',
        glowColor: 'rgba(255, 255, 255, 0.3)',
      };

    case 'aurora':
      return {
        background: 'linear-gradient(135deg, #064E3B 0%, #047857 45%, #0284C7 100%)',
        color: '#FFFFFF',
        badgeBg: 'rgba(0, 0, 0, 0.35)',
        badgeText: '#A7F3D0',
        subtitleColor: 'rgba(255, 255, 255, 0.88)',
        border: '1px solid rgba(255, 255, 255, 0.3)',
        decorFill: 'rgba(255, 255, 255, 0.18)',
        decorFillSoft: 'rgba(255, 255, 255, 0.12)',
        decorStroke: 'rgba(255, 255, 255, 0.88)',
        glowColor: 'rgba(56, 189, 248, 0.3)',
      };

    case 'brand':
      return {
        background: 'linear-gradient(135deg, var(--md-sys-color-primary) 0%, var(--md-sys-color-primary-container) 100%)',
        color: 'var(--md-sys-color-on-primary)',
        badgeBg: 'rgba(0, 0, 0, 0.28)',
        badgeText: 'var(--md-sys-color-on-primary)',
        subtitleColor: 'var(--md-sys-color-on-primary)',
        border: '1px solid rgba(255, 255, 255, 0.3)',
        decorFill: 'rgba(255, 255, 255, 0.2)',
        decorFillSoft: 'rgba(255, 255, 255, 0.14)',
        decorStroke: 'rgba(255, 255, 255, 0.88)',
        glowColor: 'rgba(255, 255, 255, 0.25)',
      };

    case 'glass':
      return {
        background: 'linear-gradient(135deg, rgba(255, 255, 255, 0.15) 0%, rgba(255, 255, 255, 0.05) 100%)',
        color: 'var(--md-sys-color-on-surface)',
        badgeBg: 'var(--md-sys-color-primary-container)',
        badgeText: 'var(--md-sys-color-on-primary-container)',
        subtitleColor: 'var(--md-sys-color-on-surface-variant)',
        border: '1px solid var(--md-sys-color-border)',
        decorFill: 'rgba(255, 255, 255, 0.12)',
        decorFillSoft: 'rgba(255, 255, 255, 0.06)',
        decorStroke: 'var(--md-sys-color-outline-variant)',
        glowColor: 'var(--md-sys-color-primary)',
      };

    case 'surface':
    default:
      return {
        background: 'var(--md-sys-color-surface-container)',
        color: 'var(--md-sys-color-on-surface)',
        badgeBg: 'var(--md-sys-color-primary-container)',
        badgeText: 'var(--md-sys-color-on-primary-container)',
        subtitleColor: 'var(--md-sys-color-on-surface-variant)',
        border: '1px solid var(--md-sys-color-border)',
        decorFill: 'var(--md-sys-color-surface-container-high)',
        decorFillSoft: 'var(--md-sys-color-surface-container-low)',
        decorStroke: 'var(--md-sys-color-outline-variant)',
        glowColor: 'var(--md-sys-color-primary)',
      };
  }
};

/* ========================================================================= */
/* HeroCard Component                                                        */
/* ========================================================================= */

export const HeroCard: React.FC<HeroCardProps> = ({
  badgeLabel = 'FACTORY VISION WORKSPACE',
  badgeIcon = 'auto_awesome',
  greeting = 'Good morning',
  userName = 'Alex',
  title,
  subtitle = 'Start your day with clean records and real-time operational telemetry.',
  variant = 'brand',
  decorated = true,
  metric,
  actions,
  primaryActionLabel,
  onPrimaryAction,
  secondaryActionLabel,
  onSecondaryAction,
  className = '',
  children,
}) => {
  const displayTitle = title || (userName ? `${greeting}, ${userName}` : greeting);
  const s = resolveVariant(variant);
  const reduced = useReducedMotionSafe();
  const cardRef = useRef<HTMLDivElement>(null);

  const [isHovered, setIsHovered] = useState(false);

  // 1. Raw Mouse Coordinates (0 to 1)
  const rawMouseX = useMotionValue(0.5);
  const rawMouseY = useMotionValue(0.5);

  // 2. Spring-smoothed Coordinates for Parallax & Spotlight
  const smoothX = useSpring(rawMouseX, { stiffness: 280, damping: 24 });
  const smoothY = useSpring(rawMouseY, { stiffness: 280, damping: 24 });

  // 3. Multi-Layer Dynamic Vector Parallax Offsets
  const blobParallaxX = useTransform(smoothX, [0, 1], reduced ? [0, 0] : [-16, 16]);
  const blobParallaxY = useTransform(smoothY, [0, 1], reduced ? [0, 0] : [-10, 10]);

  const outlineParallaxX = useTransform(smoothX, [0, 1], reduced ? [0, 0] : [20, -20]);
  const outlineParallaxY = useTransform(smoothY, [0, 1], reduced ? [0, 0] : [14, -14]);

  const circleParallaxX = useTransform(smoothX, [0, 1], reduced ? [0, 0] : [28, -28]);
  const circleParallaxY = useTransform(smoothY, [0, 1], reduced ? [0, 0] : [-16, 16]);

  const cloudParallaxX = useTransform(smoothX, [0, 1], reduced ? [0, 0] : [-22, 22]);
  const cloudParallaxY = useTransform(smoothY, [0, 1], reduced ? [0, 0] : [-12, 12]);

  // 4. Dynamic Spotlight Template in Percent
  const posX = useTransform(smoothX, (v) => `${(v * 100).toFixed(1)}%`);
  const posY = useTransform(smoothY, (v) => `${(v * 100).toFixed(1)}%`);
  const spotlightBackground = useMotionTemplate`radial-gradient(520px circle at ${posX} ${posY}, ${s.glowColor}, transparent 70%)`;

  const handlePointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (reduced) return;
    const rect = e.currentTarget.getBoundingClientRect();
    rawMouseX.set((e.clientX - rect.left) / rect.width);
    rawMouseY.set((e.clientY - rect.top) / rect.height);
  };

  const handlePointerEnter = (e: React.PointerEvent<HTMLDivElement>) => {
    setIsHovered(true);
    handlePointerMove(e);
  };

  const handlePointerLeave = () => {
    setIsHovered(false);
    rawMouseX.set(0.5);
    rawMouseY.set(0.5);
  };

  // Button tactile micro-interaction
  const actionMotion = {
    whileHover: reduced ? undefined : { y: -1, scale: 1.02 },
    whileTap: reduced ? undefined : { scale: 0.98 },
    transition: { duration: 0.15, ease: M3_EASE.standard },
  } as const;

  return (
    <motion.div
      ref={cardRef}
      initial={{ opacity: 0, y: reduced ? 0 : 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.35, ease: M3_EASE.emphasizedDecelerate }}
      onPointerMove={handlePointerMove}
      onPointerEnter={handlePointerEnter}
      onPointerLeave={handlePointerLeave}
      className={`factory-vision-hero-card ${className}`}
      style={{
        borderRadius: 'var(--radius-hero, 28px)',
        background: s.background,
        color: s.color,
        border: s.border,
        boxShadow: '0 1px 3px rgba(0, 0, 0, 0.08), 0 1px 2px rgba(0, 0, 0, 0.04)',
        position: 'relative',
        overflow: 'hidden',
      }}
    >
      {/* 🌟 1. Dynamic 120fps Cursor-Following Spotlight Glow */}
      <motion.div
        aria-hidden="true"
        animate={{ opacity: isHovered && !reduced ? 1 : 0 }}
        transition={{ duration: 0.25 }}
        style={{
          position: 'absolute',
          inset: 0,
          pointerEvents: 'none',
          zIndex: 1,
          borderRadius: 'inherit',
          background: spotlightBackground,
        }}
      />

      {/* 🌟 2. Dynamic Living Vector Artwork with Multi-Plane Parallax on Hover */}
      {decorated && (
        <svg
          aria-hidden="true"
          focusable="false"
          style={{
            position: 'absolute',
            inset: 0,
            width: '100%',
            height: '100%',
            pointerEvents: 'none',
            zIndex: 0,
          }}
          viewBox="0 0 900 160"
          preserveAspectRatio="xMaxYMid slice"
          fill="none"
        >
          {/* Layer 1: Background Soft Floating Blobs */}
          <motion.g
            style={{ x: blobParallaxX, y: blobParallaxY }}
            animate={{ scale: isHovered && !reduced ? 1.05 : 1 }}
            transition={{ duration: 0.4, ease: M3_EASE.standard }}
          >
            <rect x="420" y="80" width="54" height="90" rx="27" fill={s.decorFillSoft} />
            <rect x="520" y="-20" width="220" height="150" rx="75" fill={s.decorFill} />
          </motion.g>

          {/* Layer 2: Geometric Outline Capsule with Hover Shimmer & Parallax */}
          <motion.g
            style={{ x: outlineParallaxX, y: outlineParallaxY, transformOrigin: '560px 57px' }}
            animate={{
              scale: isHovered && !reduced ? 1.03 : 1,
            }}
            transition={{ duration: 0.35, ease: M3_EASE.standard }}
          >
            <motion.rect
              x="470"
              y="14"
              width="180"
              height="86"
              rx="43"
              fill="none"
              stroke={s.decorStroke}
              strokeWidth={3.2}
              strokeLinecap="round"
              initial={{ pathLength: reduced ? 1 : 0, opacity: 0 }}
              animate={{
                pathLength: 1,
                opacity: isHovered ? 1 : 0.85,
              }}
              transition={{ duration: 0.8, ease: M3_EASE.emphasizedDecelerate, delay: 0.1 }}
            />
          </motion.g>

          {/* Layer 3: Outline Circle with Dynamic Responsive Pulse */}
          <motion.g
            style={{ x: circleParallaxX, y: circleParallaxY, transformOrigin: '700px 50px' }}
            animate={{
              scale: isHovered && !reduced ? 1.08 : 1,
            }}
            transition={{ duration: 0.35, ease: M3_EASE.standard }}
          >
            <motion.circle
              cx="700"
              cy="50"
              r="22"
              fill="none"
              stroke={s.decorStroke}
              strokeWidth={3.2}
              strokeLinecap="round"
              initial={{ pathLength: reduced ? 1 : 0, opacity: 0 }}
              animate={{
                pathLength: 1,
                opacity: isHovered ? 1 : 0.85,
              }}
              transition={{ duration: 0.7, ease: M3_EASE.emphasizedDecelerate, delay: 0.2 }}
            />
          </motion.g>

          {/* Layer 4: Rightmost Organic Cloud with Floating Expansion */}
          <motion.g
            style={{ x: cloudParallaxX, y: cloudParallaxY, transformOrigin: '820px 85px' }}
            animate={{
              scale: isHovered && !reduced ? 1.04 : 1,
              opacity: isHovered && !reduced ? 0.95 : 0.75,
            }}
            transition={{ duration: 0.4, ease: M3_EASE.standard }}
          >
            <path
              d="M 820 40 
                 C 800 20, 770 30, 765 50 
                 C 745 50, 735 75, 748 95 
                 C 740 115, 760 140, 785 135 
                 C 800 150, 830 145, 840 125 
                 C 860 125, 875 105, 868 85 
                 C 880 65, 860 35, 835 40 Z"
              fill={s.decorFillSoft}
            />
          </motion.g>
        </svg>
      )}

      {/* 🌟 3. Left Content Column (Spacious, Clean Title & Subtitle) */}
      <div className="hero-card-left">
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '10px', flexWrap: 'wrap' }}>
          {badgeLabel && (
            <motion.div
              initial={{ opacity: 0, y: 4 }}
              animate={{ opacity: 1, y: 0 }}
              whileHover={{ scale: 1.03 }}
              transition={{ duration: 0.2, ease: M3_EASE.standard }}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '6px',
                padding: '4px 12px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: s.badgeBg,
                color: s.badgeText,
                fontSize: '11px',
                fontWeight: 700,
                letterSpacing: '0.04em',
                boxShadow: '0 1px 3px rgba(0,0,0,0.1)',
                cursor: 'default',
              }}
            >
              {badgeIcon && <Icon name={badgeIcon} size={14} />}
              <span>{badgeLabel}</span>
            </motion.div>
          )}

          {/* Live Operational Status Pulse */}
          <div
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: '6px',
              padding: '4px 10px',
              borderRadius: 'var(--radius-pill)',
              backgroundColor: 'rgba(255, 255, 255, 0.22)',
              backdropFilter: 'blur(8px)',
              fontSize: '11px',
              fontWeight: 600,
            }}
          >
            <span
              style={{
                width: '6px',
                height: '6px',
                borderRadius: '50%',
                backgroundColor: '#10B981',
                boxShadow: '0 0 6px #10B981',
              }}
            />
            <span>Live SLA 99.98%</span>
          </div>
        </div>

        <h1 className="hero-card-title">
          {displayTitle}
        </h1>

        {subtitle && (
          <p className="hero-card-subtitle" style={{ color: s.subtitleColor }}>
            {subtitle}
          </p>
        )}

        {children && <div style={{ marginTop: '16px' }}>{children}</div>}
      </div>

      {/* 🌟 4. Right Action Column (Cleanly Aligned to the Right) */}
      <div className="hero-card-right">
        {metric && (
          <div
            style={{
              display: 'flex',
              alignItems: 'baseline',
              gap: '6px',
              padding: '6px 14px',
              borderRadius: 'var(--radius-lg)',
              backgroundColor: 'rgba(255, 255, 255, 0.22)',
              backdropFilter: 'blur(10px)',
              border: '1px solid rgba(255, 255, 255, 0.35)',
              cursor: 'default',
              whiteSpace: 'nowrap',
            }}
          >
            <span style={{ fontSize: '11px', fontWeight: 600, opacity: 0.85 }}>{metric.label}:</span>
            <span style={{ fontSize: '16px', fontWeight: 800, fontFeatureSettings: '"tnum" 1' }}>{metric.value}</span>
            {metric.trend && <span style={{ fontSize: '11px', fontWeight: 700, color: '#047857' }}>{metric.trend}</span>}
          </div>
        )}

        <div className="hero-card-actions">
          {actions}
          {primaryActionLabel && (
            <Button variant="filled" onClick={onPrimaryAction} {...actionMotion}>
              {primaryActionLabel}
            </Button>
          )}
          {secondaryActionLabel && (
            <Button variant="tonal" onClick={onSecondaryAction} {...actionMotion}>
              {secondaryActionLabel}
            </Button>
          )}
        </div>
      </div>
    </motion.div>
  );
};
