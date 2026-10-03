import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';

export interface PlantMetricCardProps {
  label: string;
  value: string;
  subValue?: string;
  statusBadge?: string;
  statusType?: 'success' | 'warning' | 'error' | 'neutral';
  icon?: React.ReactNode;
  sparklineData?: number[];
  className?: string;
}

export const PlantMetricCard: React.FC<PlantMetricCardProps> = ({
  label,
  value,
  subValue,
  statusBadge,
  statusType = 'success',
  icon,
  sparklineData = [80, 84, 82, 88, 86, 91, 88],
  className = '',
}) => {
  const statusColors = {
    success: 'var(--md-sys-color-primary)',
    warning: 'var(--md-sys-color-warning)',
    error: 'var(--md-sys-color-error)',
    neutral: 'var(--md-sys-color-on-surface-variant)',
  };

  // Generate SVG Sparkline
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
        borderRadius: 'var(--radius-xl)', // 22px
        backgroundColor: 'var(--md-sys-color-surface)',
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
      {/* Header: Label & Icon */}
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
              backgroundColor: 'var(--md-sys-color-surface-container)',
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

      {/* Main Metric Value & Telemetry Line */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-end', marginTop: '12px' }}>
        <div>
          <div style={{ fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em', lineHeight: 1.1 }}>
            {value}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px', marginTop: '4px' }}>
            {statusBadge && (
              <span
                style={{
                  fontSize: '11px',
                  fontWeight: 600,
                  color: statusColors[statusType],
                }}
              >
                {statusBadge}
              </span>
            )}
            {subValue && (
              <span style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                • {subValue}
              </span>
            )}
          </div>
        </div>

        {/* Telemetry Sparkline */}
        <div style={{ width: '90px', height: '34px' }}>
          <svg width="100%" height="100%" viewBox="0 0 90 34" fill="none">
            <polyline
              points={points}
              fill="none"
              stroke={statusColors[statusType]}
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
