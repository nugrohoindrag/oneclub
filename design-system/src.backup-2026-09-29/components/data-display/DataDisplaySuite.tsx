/**
 * @license MIT
 * 08. Data Display Suite — Morphic Design System
 * 
 * Includes: Stat, StatusBadge, Avatar, AvatarGroup, Timeline, MetricCard, KPICard
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';

// 1. Stat (Minimal Stat Display)
export interface StatProps {
  label: string;
  value: string | number;
  change?: string;
  trend?: 'up' | 'down' | 'neutral';
  prefix?: string;
  suffix?: string;
  caption?: string;
  className?: string;
}

export const Stat: React.FC<StatProps> = ({
  label,
  value,
  change,
  trend = 'neutral',
  prefix,
  suffix,
  caption,
  className = '',
}) => {
  const trendColor = {
    up: 'var(--md-sys-color-primary)',
    down: 'var(--md-sys-color-error)',
    neutral: 'var(--md-sys-color-on-surface-variant)',
  }[trend];

  const trendIcon = {
    up: 'trending_up',
    down: 'trending_down',
    neutral: 'trending_flat',
  }[trend];

  return (
    <div className={`morphic-stat ${className}`} style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
      <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>{label}</span>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: '6px' }}>
        {prefix && <span style={{ fontSize: '18px', color: 'var(--md-sys-color-on-surface-variant)' }}>{prefix}</span>}
        <span style={{ fontSize: '28px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)', fontFeatureSettings: '"tnum" 1', letterSpacing: '-0.02em' }}>
          {value}
        </span>
        {suffix && <span style={{ fontSize: '14px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>{suffix}</span>}
      </div>

      {(change || caption) && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '4px', fontSize: '12px', marginTop: '2px' }}>
          {change && (
            <span style={{ color: trendColor, fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: '2px' }}>
              <Icon name={trendIcon} size={14} />
              {change}
            </span>
          )}
          {caption && <span style={{ color: 'var(--md-sys-color-on-surface-variant)' }}>{caption}</span>}
        </div>
      )}
    </div>
  );
};

// 2. MetricCard / KPICard
export interface MetricCardProps {
  label: string;
  value: string;
  delta?: string;
  deltaType?: 'positive' | 'negative' | 'neutral';
  icon?: React.ReactNode;
  sparklineData?: number[];
  className?: string;
}

export const MetricCard: React.FC<MetricCardProps> = ({
  label,
  value,
  delta,
  deltaType = 'positive',
  icon,
  sparklineData = [20, 45, 30, 60, 50, 80, 70],
  className = '',
}) => {
  const isPositive = deltaType === 'positive';
  const deltaColor = isPositive
    ? 'var(--md-sys-color-primary)'
    : deltaType === 'negative'
    ? 'var(--md-sys-color-error)'
    : 'var(--md-sys-color-on-surface-variant)';

  const max = Math.max(...sparklineData, 1);
  const min = Math.min(...sparklineData, 0);
  const points = sparklineData
    .map((val, idx) => {
      const x = (idx / (sparklineData.length - 1)) * 90;
      const y = 32 - ((val - min) / (max - min)) * 26;
      return `${x},${y}`;
    })
    .join(' ');

  return (
    <motion.div
      whileHover={{ y: -2, transition: { duration: 0.18 } }}
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '20px 24px',
        color: 'var(--md-sys-color-on-surface)',
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'space-between',
        height: '100%',
        minHeight: '136px',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`morphic-metric-card ${className}`}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>
          {label}
        </span>
        {icon && (
          <div
            style={{
              width: '32px',
              height: '32px',
              borderRadius: 'var(--radius-sm)',
              backgroundColor: 'var(--md-sys-color-surface-container-high)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              color: 'var(--md-sys-color-primary)',
              fontSize: '18px',
            }}
          >
            {icon}
          </div>
        )}
      </div>

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginTop: '12px' }}>
        <div>
          <div style={{ fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em', lineHeight: 1.1, fontFeatureSettings: '"tnum" 1' }}>
            {value}
          </div>
          {delta && (
            <div style={{ fontSize: '12px', fontWeight: 600, color: deltaColor, marginTop: '4px', display: 'flex', alignItems: 'center', gap: '2px' }}>
              <Icon name={isPositive ? 'trending_up' : 'trending_down'} size={14} />
              <span>{delta}</span>
            </div>
          )}
        </div>

        <div style={{ width: '90px', height: '34px' }}>
          <svg width="100%" height="100%" viewBox="0 0 90 34" fill="none">
            <polyline
              points={points}
              fill="none"
              stroke="var(--md-sys-color-primary)"
              strokeWidth="2.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </div>
      </div>
    </motion.div>
  );
};

export const KPICard = MetricCard;

// 3. StatusBadge (Status with Pulsing Marker)
export interface StatusBadgeProps {
  status: 'online' | 'offline' | 'warning' | 'idle' | 'busy';
  label?: string;
  size?: 'sm' | 'md';
  className?: string;
}

export const StatusBadge: React.FC<StatusBadgeProps> = ({
  status,
  label,
  size = 'md',
  className = '',
}) => {
  const config = {
    online: { color: 'var(--md-sys-color-primary)', bg: 'var(--md-sys-color-success-container)', label: label || 'Online' },
    offline: { color: 'var(--md-sys-color-error)', bg: 'var(--md-sys-color-error-container)', label: label || 'Offline' },
    warning: { color: 'var(--md-sys-color-warning)', bg: 'var(--md-sys-color-warning-container)', label: label || 'Degraded' },
    idle: { color: 'var(--md-sys-color-on-surface-variant)', bg: 'var(--md-sys-color-surface-container-high)', label: label || 'Idle' },
    busy: { color: 'var(--md-sys-color-chart-neutral)', bg: 'var(--md-sys-color-secondary-container)', label: label || 'Processing' },
  }[status];

  return (
    <span
      className={`morphic-status-badge ${className}`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '6px',
        padding: size === 'sm' ? '2px 8px' : '4px 10px',
        borderRadius: 'var(--radius-pill)',
        backgroundColor: config.bg,
        color: config.color,
        fontSize: size === 'sm' ? '11px' : '12px',
        fontWeight: 700,
      }}
    >
      <span
        style={{
          width: size === 'sm' ? '6px' : '8px',
          height: size === 'sm' ? '6px' : '8px',
          borderRadius: '50%',
          backgroundColor: config.color,
          boxShadow: `0 0 6px ${config.color}`,
        }}
      />
      {config.label}
    </span>
  );
};

// 4. Avatar & AvatarGroup
export interface AvatarProps {
  src?: string;
  name?: string;
  size?: 'xs' | 'sm' | 'md' | 'lg' | 'xl';
  status?: 'online' | 'offline' | 'busy';
  className?: string;
}

export const Avatar: React.FC<AvatarProps> = ({
  src,
  name = 'User',
  size = 'md',
  status,
  className = '',
}) => {
  const sizeMap = {
    xs: '24px',
    sm: '32px',
    md: '40px',
    lg: '48px',
    xl: '64px',
  };

  const getInitials = (n: string) => {
    return n
      .split(' ')
      .map((part) => part[0])
      .slice(0, 2)
      .join('')
      .toUpperCase();
  };

  return (
    <div
      className={`morphic-avatar ${className}`}
      style={{
        position: 'relative',
        display: 'inline-block',
        width: sizeMap[size],
        height: sizeMap[size],
        borderRadius: 'var(--radius-pill)',
        backgroundColor: 'var(--md-sys-color-surface-container-high)',
        color: 'var(--md-sys-color-primary)',
        overflow: 'hidden',
        border: '1.5px solid var(--md-sys-color-border)',
        flexShrink: 0,
      }}
    >
      {src ? (
        <img src={src} alt={name} style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
      ) : (
        <div
          style={{
            width: '100%',
            height: '100%',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontWeight: 700,
            fontSize: size === 'xs' ? '10px' : size === 'sm' ? '12px' : '14px',
          }}
        >
          {getInitials(name)}
        </div>
      )}

      {status && (
        <span
          style={{
            position: 'absolute',
            bottom: '1px',
            right: '1px',
            width: '8px',
            height: '8px',
            borderRadius: '50%',
            backgroundColor: status === 'online' ? 'var(--md-sys-color-primary)' : status === 'busy' ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface-variant)',
            border: '1.5px solid var(--md-sys-color-surface-container)',
          }}
        />
      )}
    </div>
  );
};

export const AvatarGroup: React.FC<{
  avatars: { src?: string; name: string }[];
  max?: number;
  size?: AvatarProps['size'];
}> = ({ avatars, max = 4, size = 'sm' }) => {
  const visible = avatars.slice(0, max);
  const remaining = avatars.length - max;

  return (
    <div style={{ display: 'flex', alignItems: 'center' }}>
      {visible.map((av, idx) => (
        <div key={idx} style={{ marginLeft: idx > 0 ? '-8px' : '0', zIndex: max - idx }}>
          <Avatar src={av.src} name={av.name} size={size} />
        </div>
      ))}
      {remaining > 0 && (
        <div
          style={{
            marginLeft: '-8px',
            width: '32px',
            height: '32px',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: 'var(--md-sys-color-surface-container-high)',
            color: 'var(--md-sys-color-on-surface-variant)',
            fontSize: '11px',
            fontWeight: 600,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            border: '1.5px solid var(--md-sys-color-border)',
            zIndex: 0,
          }}
        >
          +{remaining}
        </div>
      )}
    </div>
  );
};

// 5. Timeline (Vertical Activity & Audit Stream)
export interface TimelineEvent {
  id: string;
  title: string;
  description?: string;
  timestamp: string;
  icon?: string;
  type?: 'primary' | 'success' | 'warning' | 'error';
}

export const Timeline: React.FC<{
  events: TimelineEvent[];
  className?: string;
}> = ({ events, className = '' }) => {
  const colorMap = {
    primary: 'var(--md-sys-color-primary)',
    success: 'var(--md-sys-color-primary)',
    warning: 'var(--md-sys-color-warning)',
    error: 'var(--md-sys-color-error)',
  };

  return (
    <div className={`morphic-timeline ${className}`} style={{ display: 'flex', flexDirection: 'column', gap: '0' }}>
      {events.map((ev, index) => {
        const nodeColor = colorMap[ev.type || 'primary'];
        const isLast = index === events.length - 1;

        return (
          <div key={ev.id} style={{ display: 'flex', gap: '16px', position: 'relative' }}>
            <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
              <div
                style={{
                  width: '28px',
                  height: '28px',
                  borderRadius: '50%',
                  backgroundColor: 'var(--md-sys-color-surface-container)',
                  color: nodeColor,
                  border: `2px solid ${nodeColor}`,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  zIndex: 2,
                  boxShadow: 'var(--md-sys-elevation-level1)',
                }}
              >
                <Icon name={ev.icon || 'circle'} size={14} />
              </div>
              {!isLast && (
                <div
                  style={{
                    width: '2px',
                    flex: 1,
                    minHeight: '36px',
                    backgroundColor: 'var(--md-sys-color-border)',
                    margin: '4px 0',
                  }}
                />
              )}
            </div>

            <div style={{ paddingBottom: isLast ? '0' : '20px', paddingTop: '2px', flex: 1 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <h4 style={{ margin: 0, fontSize: '14px', fontWeight: 700 }}>{ev.title}</h4>
                <span style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontFeatureSettings: '"tnum" 1' }}>
                  {ev.timestamp}
                </span>
              </div>
              {ev.description && (
                <p style={{ margin: '4px 0 0', fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.45 }}>
                  {ev.description}
                </p>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
};
