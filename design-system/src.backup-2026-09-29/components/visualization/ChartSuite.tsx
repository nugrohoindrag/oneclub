/**
 * @license MIT
 * 09. Data Visualization Suite — Morphic Design System
 * 
 * Includes: LineChart, AreaChart, BarChart, DonutChart, GaugeChart, Sparkline, ChartLegend, ChartControls, ChartCard
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';

// 1. LineChart & AreaChart
export interface ChartSeriesPoint {
  label: string;
  value: number;
}

export interface LineChartProps {
  data?: ChartSeriesPoint[];
  width?: number | string;
  height?: number;
  color?: string;
  fillArea?: boolean;
  strokeWidth?: number;
  showPoints?: boolean;
  className?: string;
}

export const LineChart: React.FC<LineChartProps> = ({
  data = [
    { label: 'Mon', value: 35 },
    { label: 'Tue', value: 60 },
    { label: 'Wed', value: 45 },
    { label: 'Thu', value: 85 },
    { label: 'Fri', value: 70 },
    { label: 'Sat', value: 95 },
    { label: 'Sun', value: 80 },
  ],
  width = '100%',
  height = 180,
  color = 'var(--md-sys-color-primary)',
  fillArea = false,
  strokeWidth = 3,
  showPoints = true,
  className = '',
}) => {
  const values = data.map((d) => d.value);
  const max = Math.max(...values, 100);
  const min = Math.min(...values, 0);
  const range = max - min || 1;

  const points = data.map((d, i) => {
    const x = 30 + (i / (data.length - 1)) * 460;
    const y = 160 - ((d.value - min) / range) * 120;
    return `${x},${y}`;
  });

  const pathD = `M ${points[0]} ` + points.slice(1).map((p) => `L ${p}`).join(' ');
  const areaD = `${pathD} L ${points[points.length - 1].split(',')[0]},170 L 30,170 Z`;

  return (
    <div className={`morphic-line-chart ${className}`} style={{ width, height: `${height + 24}px` }}>
      <svg width="100%" height={height} viewBox="0 0 520 180" preserveAspectRatio="none">
        <defs>
          <linearGradient id="morphicLineGrad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity="0.3" />
            <stop offset="100%" stopColor={color} stopOpacity="0.0" />
          </linearGradient>
        </defs>

        <line x1="20" y1="40" x2="500" y2="40" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" strokeWidth="1" />
        <line x1="20" y1="100" x2="500" y2="100" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" strokeWidth="1" />
        <line x1="20" y1="160" x2="500" y2="160" stroke="var(--md-sys-color-border)" strokeWidth="1" />

        {fillArea && <path d={areaD} fill="url(#morphicLineGrad)" />}

        <motion.path
          d={pathD}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeLinejoin="round"
          initial={{ pathLength: 0 }}
          animate={{ pathLength: 1 }}
          transition={{ duration: 1, ease: [0.2, 0, 0, 1] }}
        />

        {showPoints &&
          points.map((p, idx) => {
            const [cx, cy] = p.split(',');
            return (
              <circle
                key={idx}
                cx={cx}
                cy={cy}
                r="4"
                fill="var(--md-sys-color-surface)"
                stroke={color}
                strokeWidth="2.5"
              />
            );
          })}
      </svg>

      <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0 12px', fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>
        {data.map((d, i) => (
          <span key={i}>{d.label}</span>
        ))}
      </div>
    </div>
  );
};

export const AreaChart: React.FC<LineChartProps> = (props) => {
  return <LineChart {...props} fillArea={true} />;
};

// 2. BarChart (Vertical Bar Chart)
export interface BarChartProps {
  data?: { label: string; value: number; color?: string }[];
  height?: number;
  maxValue?: number;
}

export const BarChart: React.FC<BarChartProps> = ({
  data = [
    { label: 'Jan', value: 65 },
    { label: 'Feb', value: 85 },
    { label: 'Mar', value: 45 },
    { label: 'Apr', value: 95 },
    { label: 'May', value: 75 },
    { label: 'Jun', value: 60 },
  ],
  height = 160,
  maxValue = 100,
}) => {
  return (
    <div style={{ width: '100%', height: `${height + 24}px`, display: 'flex', flexDirection: 'column', justifyContent: 'flex-end' }}>
      <div style={{ height: `${height}px`, display: 'flex', alignItems: 'flex-end', gap: '14px', paddingBottom: '8px', borderBottom: '1px solid var(--md-sys-color-border)' }}>
        {data.map((d, i) => {
          const heightPct = Math.min(100, (d.value / maxValue) * 100);
          return (
            <div key={i} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', height: '100%', justifyContent: 'flex-end' }}>
              <motion.div
                initial={{ height: 0 }}
                animate={{ height: `${heightPct}%` }}
                transition={{ duration: 0.6, delay: i * 0.08 }}
                style={{
                  width: '100%',
                  maxWidth: '32px',
                  borderRadius: 'var(--radius-xs) var(--radius-xs) 0 0',
                  backgroundColor: d.color || 'var(--md-sys-color-primary)',
                }}
                title={`${d.label}: ${d.value}`}
              />
            </div>
          );
        })}
      </div>

      <div style={{ display: 'flex', justifyContent: 'space-around', paddingTop: '6px', fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>
        {data.map((d, i) => (
          <span key={i} style={{ fontWeight: 600 }}>{d.label}</span>
        ))}
      </div>
    </div>
  );
};

// 3. DonutChart
export interface DonutSlice {
  label: string;
  value: number;
  color: string;
}

export const DonutChart: React.FC<{
  slices?: DonutSlice[];
  size?: number;
  strokeWidth?: number;
  centerText?: string;
  centerSubtext?: string;
}> = ({
  slices = [
    { label: 'Production', value: 45, color: 'var(--md-sys-color-primary)' },
    { label: 'Analytics', value: 30, color: 'var(--md-sys-color-info)' },
    { label: 'Disaster Recovery', value: 15, color: 'var(--md-sys-color-chart-neutral)' },
    { label: 'Edge Nodes', value: 10, color: 'var(--md-sys-color-warning)' },
  ],
  size = 180,
  strokeWidth = 18,
  centerText = '100%',
  centerSubtext = 'Total Health',
}) => {
  const total = slices.reduce((acc, s) => acc + s.value, 0) || 100;
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  let accumulated = 0;

  return (
    <div style={{ position: 'relative', width: size, height: size, margin: '0 auto' }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} style={{ transform: 'rotate(-90deg)' }}>
        <circle cx={size / 2} cy={size / 2} r={radius} fill="none" stroke="var(--md-sys-color-surface-container-high)" strokeWidth={strokeWidth} />
        {slices.map((slice, idx) => {
          const dashLength = (slice.value / total) * circumference;
          const dashOffset = -(accumulated / total) * circumference;
          accumulated += slice.value;

          return (
            <circle
              key={idx}
              cx={size / 2}
              cy={size / 2}
              r={radius}
              fill="none"
              stroke={slice.color}
              strokeWidth={strokeWidth}
              strokeDasharray={`${dashLength} ${circumference - dashLength}`}
              strokeDashoffset={dashOffset}
              strokeLinecap="round"
            />
          );
        })}
      </svg>

      <div
        style={{
          position: 'absolute',
          inset: 0,
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          textAlign: 'center',
          pointerEvents: 'none',
        }}
      >
        <span style={{ fontSize: '22px', fontWeight: 700, fontFeatureSettings: '"tnum" 1' }}>{centerText}</span>
        {centerSubtext && <span style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>{centerSubtext}</span>}
      </div>
    </div>
  );
};

// 4. Sparkline (Minimal SVG Sparkline)
export interface SparklineProps {
  data?: number[];
  width?: number;
  height?: number;
  color?: string;
  fill?: boolean;
}

export const Sparkline: React.FC<SparklineProps> = ({
  data = [12, 18, 15, 26, 22, 38, 34, 45],
  width = 120,
  height = 36,
  color = 'var(--md-sys-color-primary)',
  fill = true,
}) => {
  const min = Math.min(...data);
  const max = Math.max(...data);
  const range = max - min || 1;

  const points = data.map((val, idx) => {
    const x = (idx / (data.length - 1)) * (width - 8) + 4;
    const y = height - 4 - ((val - min) / range) * (height - 8);
    return `${x},${y}`;
  });

  const pointsString = points.join(' ');
  const areaPoints = `4,${height} ${pointsString} ${width - 4},${height}`;

  return (
    <svg width={width} height={height} style={{ overflow: 'visible' }}>
      {fill && <polygon points={areaPoints} fill={color} fillOpacity="0.15" />}
      <polyline points={pointsString} fill="none" stroke={color} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
};

// 5. GaugeChart (Radial Semi-Circle or Arc Gauge)
export interface GaugeChartProps {
  value?: number; // 0 - 100
  title?: string;
  subtitle?: string;
  size?: number;
  strokeWidth?: number;
  color?: string;
  unit?: string;
}

export const GaugeChart: React.FC<GaugeChartProps> = ({
  value = 74,
  title = 'System Efficiency',
  subtitle = 'Target: 85% SLA',
  size = 180,
  strokeWidth = 14,
  color = 'var(--md-sys-color-primary)',
  unit = '%',
}) => {
  const radius = (size - strokeWidth) / 2;
  const circumference = Math.PI * radius; // Half circle
  const progressOffset = circumference - (value / 100) * circumference;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
      <div style={{ position: 'relative', width: size, height: size / 2 + 16, overflow: 'hidden' }}>
        <svg width={size} height={size} style={{ transform: 'rotate(-180deg)' }}>
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke="var(--md-sys-color-surface-container-high)"
            strokeWidth={strokeWidth}
            strokeDasharray={`${circumference} ${circumference}`}
            strokeDashoffset="0"
          />

          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            fill="none"
            stroke={color}
            strokeWidth={strokeWidth}
            strokeDasharray={`${circumference} ${circumference}`}
            strokeDashoffset={progressOffset}
            strokeLinecap="round"
            style={{ transition: 'stroke-dashoffset 0.8s ease' }}
          />
        </svg>

        <div
          style={{
            position: 'absolute',
            bottom: '0',
            left: '50%',
            transform: 'translateX(-50%)',
            display: 'flex',
            alignItems: 'baseline',
            gap: '2px',
          }}
        >
          <span style={{ fontSize: '28px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)', fontFeatureSettings: '"tnum" 1' }}>
            {value}
          </span>
          <span style={{ fontSize: '14px', fontWeight: 700, color: 'var(--md-sys-color-on-surface-variant)' }}>{unit}</span>
        </div>
      </div>

      {title && <div style={{ fontSize: '14px', fontWeight: 700, marginTop: '8px' }}>{title}</div>}
      {subtitle && <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>{subtitle}</div>}
    </div>
  );
};

// 6. ChartLegend
export interface LegendItem {
  id: string;
  label: string;
  color: string;
  value?: string | number;
}

export const ChartLegend: React.FC<{
  items?: LegendItem[];
  activeIds?: string[];
  onToggle?: (id: string) => void;
}> = ({
  items = [
    { id: '1', label: 'Primary Compute', color: 'var(--md-sys-color-primary)', value: '64%' },
    { id: '2', label: 'Database Storage', color: 'var(--md-sys-color-info)', value: '28%' },
  ],
  activeIds = [],
  onToggle,
}) => {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '12px 16px', alignItems: 'center' }}>
      {items.map((it) => {
        const isMuted = activeIds.length > 0 && !activeIds.includes(it.id);
        return (
          <button
            key={it.id}
            onClick={() => onToggle?.(it.id)}
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: '6px',
              border: 'none',
              backgroundColor: 'transparent',
              cursor: onToggle ? 'pointer' : 'default',
              opacity: isMuted ? 0.35 : 1,
              transition: 'opacity 0.15s ease',
              fontSize: '12px',
              color: 'var(--md-sys-color-on-surface-variant)',
            }}
          >
            <span style={{ width: '8px', height: '8px', borderRadius: '50%', backgroundColor: it.color }} />
            <span style={{ fontWeight: 600 }}>{it.label}</span>
            {it.value !== undefined && <strong style={{ color: 'var(--md-sys-color-on-surface)', fontFeatureSettings: '"tnum" 1' }}>{it.value}</strong>}
          </button>
        );
      })}
    </div>
  );
};

// 7. ChartControls
export const ChartControls: React.FC<{
  periods?: string[];
  activePeriod?: string;
  onPeriodChange?: (p: string) => void;
}> = ({
  periods = ['1D', '7D', '1M', '1Y'],
  activePeriod = '7D',
  onPeriodChange,
}) => {
  return (
    <div style={{ display: 'inline-flex', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', padding: '2px', border: '1px solid var(--md-sys-color-border)' }}>
      {periods.map((p) => (
        <button
          key={p}
          onClick={() => onPeriodChange?.(p)}
          style={{
            padding: '4px 10px',
            borderRadius: 'var(--radius-pill)',
            border: 'none',
            backgroundColor: activePeriod === p ? 'var(--md-sys-color-surface)' : 'transparent',
            color: activePeriod === p ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
            fontWeight: activePeriod === p ? 700 : 500,
            fontSize: '11px',
            cursor: 'pointer',
            boxShadow: activePeriod === p ? 'var(--md-sys-elevation-level1)' : 'none',
            transition: 'all 0.15s ease',
          }}
        >
          {p}
        </button>
      ))}
    </div>
  );
};

// 8. ChartCard Container
export interface ChartCardProps {
  title: string;
  subtitle?: string;
  controls?: React.ReactNode;
  legend?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}

export const ChartCard: React.FC<ChartCardProps> = ({
  title,
  subtitle,
  controls,
  legend,
  children,
  className = '',
}) => {
  return (
    <div
      className={`morphic-chart-card ${className}`}
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
        display: 'flex',
        flexDirection: 'column',
        gap: '16px',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '12px' }}>
        <div>
          <h3 style={{ margin: 0, fontSize: '16px', fontWeight: 700 }}>{title}</h3>
          {subtitle && <p style={{ margin: '2px 0 0', fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</p>}
        </div>
        {controls && <div>{controls}</div>}
      </div>

      <div>{children}</div>

      {legend && <div style={{ paddingTop: '8px', borderTop: '1px solid var(--md-sys-color-border)' }}>{legend}</div>}
    </div>
  );
};
