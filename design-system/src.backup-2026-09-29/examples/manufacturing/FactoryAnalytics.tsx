import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';

export interface ThroughputChartCardProps {
  title?: string;
  subtitle?: string;
  nominalRate?: string;
  data?: number[];
  labels?: string[];
  className?: string;
}

export const ThroughputChartCard: React.FC<ThroughputChartCardProps> = ({
  title = 'Throughput Produksi Real-time per Jam',
  subtitle = 'Lini SMT & Perakitan Otomatis (Unit/Jam)',
  nominalRate = '20.450 Unit/Jam',
  data = [18200, 19800, 20450, 20100, 21200, 20800, 21900],
  labels = ['08:00', '09:00', '10:00', '11:00', '12:00', '13:00', '14:00'],
  className = '',
}) => {
  const max = Math.max(...data);
  const min = Math.min(...data) * 0.9;

  const points = data.map((val, i) => {
    const x = 30 + (i / (data.length - 1)) * 460;
    const y = 160 - ((val - min) / (max - min)) * 120;
    return `${x},${y}`;
  });

  const pathD = `M ${points[0]} ` + points.slice(1).map((p) => `L ${p}`).join(' ');
  const areaD = `${pathD} L ${points[points.length - 1].split(',')[0]},170 L 30,170 Z`;

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        color: 'var(--md-sys-color-on-surface)',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`morphic-chart-card ${className}`}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '16px' }}>
        <div>
          <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>{title}</h3>
          <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>{subtitle}</div>
        </div>
        <div style={{ textAlign: 'right' }}>
          <div style={{ fontSize: '20px', fontWeight: 700 }}>{nominalRate}</div>
          <div style={{ fontSize: '11px', color: 'var(--md-sys-color-primary)', fontWeight: 600 }}>+4.8% di atas kapasitas standar</div>
        </div>
      </div>

      {/* SVG Native Line Chart */}
      <div style={{ width: '100%', height: '180px' }}>
        <svg width="100%" height="100%" viewBox="0 0 520 190" preserveAspectRatio="none">
          <defs>
            <linearGradient id="mesThroughputGradient" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="var(--md-sys-color-primary)" stopOpacity="0.3" />
              <stop offset="100%" stopColor="var(--md-sys-color-primary)" stopOpacity="0.0" />
            </linearGradient>
          </defs>

          {/* Low-Contrast Horizontal Grid */}
          <line x1="20" y1="40" x2="500" y2="40" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" strokeWidth="1" />
          <line x1="20" y1="100" x2="500" y2="100" stroke="var(--md-sys-color-border)" strokeDasharray="4 4" strokeWidth="1" />
          <line x1="20" y1="160" x2="500" y2="160" stroke="var(--md-sys-color-border)" strokeWidth="1" />

          {/* Area Fill */}
          <path d={areaD} fill="url(#mesThroughputGradient)" />

          {/* Stroke Path */}
          <motion.path
            d={pathD}
            fill="none"
            stroke="var(--md-sys-color-primary)"
            strokeWidth="3.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            initial={{ pathLength: 0 }}
            animate={{ pathLength: 1 }}
            transition={{ duration: 1.2, ease: [0.2, 0, 0, 1] }}
          />

          {/* Point Dots */}
          {points.map((p, idx) => {
            const [cx, cy] = p.split(',');
            return (
              <circle
                key={idx}
                cx={cx}
                cy={cy}
                r="4.5"
                fill="var(--md-sys-color-surface)"
                stroke="var(--md-sys-color-primary)"
                strokeWidth="2.5"
              />
            );
          })}
        </svg>
      </div>

      {/* X-Axis Labels */}
      <div style={{ display: 'flex', justifyContent: 'space-between', padding: '0 10px', marginTop: '4px', fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>
        {labels.map((l, i) => (
          <span key={i}>{l}</span>
        ))}
      </div>
    </div>
  );
};

export interface DefectParetoCardProps {
  title?: string;
  subtitle?: string;
  totalDefects?: string;
  totalUnit?: string;
  segments?: { label: string; percentage: number; color: string; count: string }[];
  className?: string;
}

export const DefectParetoCard: React.FC<DefectParetoCardProps> = ({
  title = 'Pareto Cacat Kualitas (Quality QC)',
  subtitle = 'Klasifikasi Cacat Shift Berjalan',
  totalDefects = '114',
  totalUnit = 'Unit Defect',
  segments = [
    { label: 'Solder Bridge', percentage: 48, color: 'var(--md-sys-color-primary)', count: '55 unit' },
    { label: 'Misalignment SMT', percentage: 26, color: 'var(--md-sys-color-primary-soft)', count: '30 unit' },
    { label: 'Goresan Permukaan', percentage: 16, color: 'var(--md-sys-color-warning)', count: '18 unit' },
    { label: 'Toleransi Dimensi', percentage: 10, color: 'var(--md-sys-color-error)', count: '11 unit' },
  ],
  className = '',
}) => {
  const circumference = 2 * Math.PI * 60;
  let accumulatedPercent = 0;

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        color: 'var(--md-sys-color-on-surface)',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`morphic-donut-card ${className}`}
    >
      <div style={{ marginBottom: '16px' }}>
        <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>{title}</h3>
        <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>{subtitle}</div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-around', gap: '20px' }}>
        {/* SVG Donut Ring */}
        <div style={{ position: 'relative', width: '160px', height: '160px', flexShrink: 0 }}>
          <svg width="160" height="160" viewBox="0 0 160 160" style={{ transform: 'rotate(-90deg)' }}>
            <circle cx="80" cy="80" r="58" fill="none" stroke="var(--md-sys-color-surface-container)" strokeWidth="16" />
            {segments.map((seg, idx) => {
              const circ = 2 * Math.PI * 58;
              const dashLength = (seg.percentage / 100) * circ;
              const dashOffset = -(accumulatedPercent / 100) * circ;
              accumulatedPercent += seg.percentage;

              return (
                <circle
                  key={idx}
                  cx="80"
                  cy="80"
                  r="58"
                  fill="none"
                  stroke={seg.color}
                  strokeWidth="16"
                  strokeDasharray={`${dashLength} ${circ - dashLength}`}
                  strokeDashoffset={dashOffset}
                  strokeLinecap="round"
                />
              );
            })}
          </svg>

          {/* Center Info Proporsional */}
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
            <span
              style={{
                fontSize: '26px',
                fontWeight: 700,
                color: 'var(--md-sys-color-error)',
                lineHeight: 1,
                letterSpacing: '-0.02em',
                fontFeatureSettings: '"tnum" 1',
              }}
            >
              {totalDefects}
            </span>
            <span
              style={{
                fontSize: '11px',
                fontWeight: 600,
                color: 'var(--md-sys-color-on-surface-variant)',
                marginTop: '4px',
                textTransform: 'uppercase',
                letterSpacing: '0.04em',
              }}
            >
              {totalUnit}
            </span>
          </div>
        </div>

        {/* Legend List */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', flex: 1 }}>
          {segments.map((s, i) => (
            <div key={i} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', fontSize: '12px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                <span style={{ width: '8px', height: '8px', borderRadius: '50%', backgroundColor: s.color }} />
                <span>{s.label}</span>
              </div>
              <span style={{ fontWeight: 600 }}>{s.percentage}% ({s.count})</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
};

export interface ShiftEfficiencyCardProps {
  title?: string;
  subtitle?: string;
  className?: string;
}

export const ShiftEfficiencyCard: React.FC<ShiftEfficiencyCardProps> = ({
  title = 'Perbandingan Efisiensi Shift & Output',
  subtitle = 'Kapasitas Aktual vs Target Shift (K Unit)',
  className = '',
}) => {
  const data = [
    { label: 'Shift 1 (Pagi)', val1: 94, val2: 89 },
    { label: 'Shift 2 (Siang)', val1: 88, val2: 85 },
    { label: 'Shift 3 (Malam)', val1: 82, val2: 78 },
  ];

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '24px 28px',
        color: 'var(--md-sys-color-on-surface)',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`morphic-bar-card ${className}`}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
        <div>
          <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>{title}</h3>
          <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>{subtitle}</div>
        </div>
        <div style={{ display: 'flex', gap: '16px', fontSize: '11px', fontWeight: 600 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
            <span style={{ width: '8px', height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-primary)' }} />
            <span>Output Aktual</span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
            <span style={{ width: '8px', height: '8px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-primary-soft)' }} />
            <span>Target Produksi</span>
          </div>
        </div>
      </div>

      <div style={{ display: 'flex', justifyContent: 'space-around', alignItems: 'flex-end', height: '140px', padding: '0 8px' }}>
        {data.map((item, idx) => (
          <div key={idx} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '8px' }}>
            <div style={{ display: 'flex', alignItems: 'flex-end', gap: '8px', height: '110px' }}>
              <motion.div
                initial={{ height: 0 }}
                animate={{ height: `${item.val1}%` }}
                transition={{ duration: 0.8, delay: idx * 0.1 }}
                style={{
                  width: '24px',
                  borderRadius: '6px 6px 0 0',
                  backgroundColor: 'var(--md-sys-color-primary)',
                }}
              />
              <motion.div
                initial={{ height: 0 }}
                animate={{ height: `${item.val2}%` }}
                transition={{ duration: 0.8, delay: idx * 0.1 + 0.05 }}
                style={{
                  width: '24px',
                  borderRadius: '6px 6px 0 0',
                  backgroundColor: 'var(--md-sys-color-primary-soft)',
                }}
              />
            </div>
            <span style={{ fontSize: '11px', fontWeight: 500, color: 'var(--md-sys-color-on-surface-variant)' }}>{item.label}</span>
          </div>
        ))}
      </div>
    </div>
  );
};
