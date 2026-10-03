/**
 * @license MIT
 * Advanced Data Visualization Suite — Morphic Design System
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';

export interface BarDataPoint {
  label: string;
  value: number;
  secondaryValue?: number;
  formattedValue?: string;
}

export const HorizontalBarChart: React.FC<{
  data: BarDataPoint[];
  maxValue?: number;
  title?: string;
  subtitle?: string;
}> = ({ data, maxValue = 100, title, subtitle }) => {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
    >
      {title && (
        <div style={{ marginBottom: '16px' }}>
          <h3 style={{ margin: 0, fontSize: '16px', fontWeight: 700 }}>{title}</h3>
          {subtitle && <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</div>}
        </div>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '14px' }}>
        {data.map((item, idx) => {
          const pct = Math.min(100, (item.value / maxValue) * 100);
          return (
            <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '12px' }}>
                <span style={{ fontWeight: 600 }}>{item.label}</span>
                <span style={{ fontWeight: 700, fontFeatureSettings: '"tnum" 1' }}>
                  {item.formattedValue || `${item.value}%`}
                </span>
              </div>
              <div style={{ height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', overflow: 'hidden' }}>
                <motion.div
                  initial={{ width: 0 }}
                  animate={{ width: `${pct}%` }}
                  transition={{ duration: 0.8, delay: idx * 0.1 }}
                  style={{
                    height: '100%',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: 'var(--md-sys-color-primary)',
                  }}
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

export const StackedBarChart: React.FC<{
  data: { label: string; segments: { value: number; color: string; name: string }[] }[];
  title?: string;
}> = ({ data, title }) => {
  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
    >
      {title && <h3 style={{ margin: '0 0 16px', fontSize: '16px', fontWeight: 700 }}>{title}</h3>}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
        {data.map((item, i) => {
          const total = item.segments.reduce((acc, s) => acc + s.value, 0);
          return (
            <div key={i} style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
              <div style={{ fontSize: '12px', fontWeight: 600 }}>{item.label}</div>
              <div style={{ height: '14px', borderRadius: 'var(--radius-xs)', display: 'flex', overflow: 'hidden', backgroundColor: 'var(--md-sys-color-surface-container)' }}>
                {item.segments.map((seg, sIdx) => {
                  const pct = total > 0 ? (seg.value / total) * 100 : 0;
                  return (
                    <div
                      key={sIdx}
                      style={{
                        width: `${pct}%`,
                        height: '100%',
                        backgroundColor: seg.color,
                      }}
                      title={`${seg.name}: ${seg.value}`}
                    />
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

export const PieChart: React.FC<{
  slices: { label: string; value: number; color: string }[];
  title?: string;
}> = ({ slices, title }) => {
  const total = slices.reduce((acc, s) => acc + s.value, 0);
  let accumulatedAngle = 0;

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
      }}
    >
      {title && <h3 style={{ margin: '0 0 16px', fontSize: '16px', fontWeight: 700, alignSelf: 'flex-start' }}>{title}</h3>}

      <div style={{ position: 'relative', width: '160px', height: '160px' }}>
        <svg width="160" height="160" viewBox="-1 -1 2 2" style={{ transform: 'rotate(-90deg)' }}>
          {slices.map((slice, i) => {
            const fraction = total > 0 ? slice.value / total : 0;
            const startAngle = accumulatedAngle;
            accumulatedAngle += fraction * 2 * Math.PI;
            const endAngle = accumulatedAngle;

            const x1 = Math.cos(startAngle);
            const y1 = Math.sin(startAngle);
            const x2 = Math.cos(endAngle);
            const y2 = Math.sin(endAngle);
            const largeArc = fraction > 0.5 ? 1 : 0;

            const pathData = `M 0 0 L ${x1} ${y1} A 1 1 0 ${largeArc} 1 ${x2} ${y2} Z`;

            return <path key={i} d={pathData} fill={slice.color} stroke="var(--md-sys-color-surface)" strokeWidth="0.04" />;
          })}
        </svg>
      </div>

      <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: '12px', marginTop: '16px' }}>
        {slices.map((s, idx) => (
          <div key={idx} style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '12px' }}>
            <span style={{ width: '8px', height: '8px', borderRadius: '50%', backgroundColor: s.color }} />
            <span>{s.label} ({Math.round((s.value / total) * 100)}%)</span>
          </div>
        ))}
      </div>
    </div>
  );
};

export const RadarChart: React.FC<{
  metrics: { label: string; value: number }[]; // 0 to 100
  title?: string;
}> = ({ metrics, title }) => {
  const size = 180;
  const radius = size / 2 - 20;
  const center = size / 2;
  const numPoints = metrics.length;

  const getCoordinates = (index: number, val: number) => {
    const angle = (Math.PI * 2 / numPoints) * index - Math.PI / 2;
    const r = (val / 100) * radius;
    return {
      x: center + r * Math.cos(angle),
      y: center + r * Math.sin(angle),
    };
  };

  const pointsString = metrics.map((m, i) => {
    const coords = getCoordinates(i, m.value);
    return `${coords.x},${coords.y}`;
  }).join(' ');

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
      }}
    >
      {title && <h3 style={{ margin: '0 0 12px', fontSize: '16px', fontWeight: 700, alignSelf: 'flex-start' }}>{title}</h3>}

      <svg width={size} height={size}>
        {/* Background Radar Rings */}
        {[0.25, 0.5, 0.75, 1].map((scale, idx) => (
          <circle
            key={idx}
            cx={center}
            cy={center}
            r={radius * scale}
            fill="none"
            stroke="var(--md-sys-color-border)"
            strokeDasharray="2 2"
          />
        ))}

        {/* Value Polygon */}
        <polygon
          points={pointsString}
          fill="var(--md-sys-color-primary)"
          fillOpacity="0.25"
          stroke="var(--md-sys-color-primary)"
          strokeWidth="2"
        />

        {/* Vertices Dots */}
        {metrics.map((m, i) => {
          const coords = getCoordinates(i, m.value);
          return (
            <circle
              key={i}
              cx={coords.x}
              cy={coords.y}
              r="4"
              fill="var(--md-sys-color-primary)"
            />
          );
        })}
      </svg>

      <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'center', gap: '8px', marginTop: '12px', fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)' }}>
        {metrics.map((m, i) => (
          <span key={i}><strong>{m.label}</strong>: {m.value}%</span>
        ))}
      </div>
    </div>
  );
};

export const HeatmapGrid: React.FC<{
  title?: string;
}> = ({ title = 'Weekly Activity Density Matrix (7 Days x 12 Hours)' }) => {
  const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  const hours = ['08', '10', '12', '14', '16', '18', '20', '22'];

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
    >
      <h3 style={{ margin: '0 0 16px', fontSize: '16px', fontWeight: 700 }}>{title}</h3>

      <div style={{ display: 'grid', gridTemplateColumns: '40px repeat(8, 1fr)', gap: '4px', alignItems: 'center' }}>
        <div />
        {hours.map((h) => (
          <span key={h} style={{ fontSize: '11px', textAlign: 'center', color: 'var(--md-sys-color-on-surface-variant)' }}>
            {h}:00
          </span>
        ))}

        {days.map((d, dIdx) => (
          <React.Fragment key={d}>
            <span style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>{d}</span>
            {hours.map((_, hIdx) => {
              const intensity = ((dIdx * 3 + hIdx * 7) % 10) / 10;
              return (
                <div
                  key={hIdx}
                  style={{
                    height: '24px',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: 'var(--md-sys-color-primary)',
                    opacity: Math.max(0.12, intensity),
                  }}
                  title={`Density: ${Math.round(intensity * 100)}%`}
                />
              );
            })}
          </React.Fragment>
        ))}
      </div>
    </div>
  );
};
