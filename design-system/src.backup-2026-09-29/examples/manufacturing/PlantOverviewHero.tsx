import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';
import { M3_TRANSITIONS } from '../../motion/index.js';

export interface PlantOverviewHeroProps {
  plantName?: string;
  activeShift?: string;
  oeeScore?: string;
  currentOutput?: string;
  targetOutput?: string;
  completionRate?: string;
  activeLinesCount?: string;
  onNewWorkOrder?: () => void;
  onQualityAudit?: () => void;
  onReportIncident?: () => void;
  className?: string;
}

export const PlantOverviewHero: React.FC<PlantOverviewHeroProps> = ({
  plantName = 'Smart Factory Plant #04 — Cikarang Gigafactory',
  activeShift = 'Shift 1 (Pagi) • 07:00 - 15:00 WIB',
  oeeScore = '88.4%',
  currentOutput = '142.850 Unit',
  targetOutput = '160.000 Unit Target',
  completionRate = '89.3% Tercapai',
  activeLinesCount = '7 dari 8 Lini Beroperasi Aktif',
  onNewWorkOrder,
  onQualityAudit,
  onReportIncident,
  className = '',
}) => {
  return (
    <motion.div
      initial={{ opacity: 0, y: 15 }}
      animate={{ opacity: 1, y: 0 }}
      transition={M3_TRANSITIONS.enter}
      style={{
        /* §7 — hero radius 24–30px */
        borderRadius: 'var(--radius-hero)',
        padding: 'var(--md-sys-padding-card-hero)',
        /* §6 — colour comes from the theme, never from a literal */
        background: 'var(--hero-banner-bg)',
        color: 'var(--hero-banner-text)',
        position: 'relative',
        overflow: 'hidden',
        /* §21 — a hero is a feature surface, not a floating one */
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
      className={`morphic-hero-surface ${className}`}
    >
      {/* Abstract Tech Industrial Grid Lines */}
      <svg
        style={{
          position: 'absolute',
          top: 0,
          right: 0,
          width: '420px',
          height: '100%',
          pointerEvents: 'none',
        }}
        viewBox="0 0 400 200"
        fill="none"
      >
        <path d="M 0 40 L 400 40 M 0 100 L 400 100 M 0 160 L 400 160" stroke="var(--hero-banner-arc)" strokeWidth="1" strokeDasharray="4 4" />
        <path d="M 100 0 L 100 200 M 200 0 L 200 200 M 300 0 L 300 200" stroke="var(--hero-banner-arc)" strokeWidth="1" strokeDasharray="4 4" />
        <circle cx="300" cy="100" r="48" stroke="var(--hero-banner-arc)" strokeWidth="2" />
        <circle cx="300" cy="100" r="8" fill="var(--hero-banner-arc)" />
      </svg>

      {/* Header Row: Factory Identity & Shift Status */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', position: 'relative', zIndex: 1 }}>
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <span
              style={{
                width: '8px',
                height: '8px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-success)',
                display: 'inline-block',
              }}
            />
            <span style={{ fontSize: '13px', fontWeight: 600, opacity: 0.95, letterSpacing: '0.04em', textTransform: 'uppercase' }}>
              {plantName}
            </span>
          </div>
          <div style={{ fontSize: '12px', opacity: 0.8, marginTop: '4px' }}>
            {activeShift} • <span style={{ color: 'var(--hero-banner-subtext)', fontWeight: 600 }}>{activeLinesCount}</span>
          </div>
        </div>

        {/* Global OEE Score Badge */}
        <div
          style={{
            padding: '6px 16px',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: 'var(--hero-banner-tag-bg)',
            fontSize: 'var(--md-sys-typescale-meta-size)',
            fontWeight: 600,
            color: 'var(--hero-banner-tag-text)',
            display: 'flex',
            alignItems: 'center',
            gap: '6px',
          }}
        >
          <Icon name="verified" size={16} color="var(--hero-banner-subtext)" />
          <span>OEE: {oeeScore} (World Class)</span>
        </div>
      </div>

      {/* Main Metric Output Row */}
      <div style={{ margin: '26px 0 28px', position: 'relative', zIndex: 1 }}>
        <div style={{ fontSize: '13px', opacity: 0.8, fontWeight: 500 }}>Throughput Produksi Real-time Shift Ini</div>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: '16px', marginTop: '4px', flexWrap: 'wrap' }}>
          <span style={{
            fontSize: 'var(--md-sys-typescale-metric-size-lg)',
            fontWeight: 'var(--md-sys-typescale-metric-weight)' as React.CSSProperties['fontWeight'],
            letterSpacing: 'var(--md-sys-typescale-metric-tracking)',
            lineHeight: 'var(--md-sys-typescale-metric-line-height)',
            fontVariantNumeric: 'tabular-nums',
          }}>
            {currentOutput}
          </span>
          <span style={{ fontSize: '16px', opacity: 0.85, fontWeight: 600 }}>
            / {targetOutput}
          </span>
          <span
            style={{
              fontSize: '12px',
              padding: '3px 10px',
              borderRadius: 'var(--radius-pill)',
              backgroundColor: 'var(--md-sys-color-success-container)',
              color: 'var(--hero-banner-subtext)',
              fontWeight: 600,
            }}
          >
            {completionRate}
          </span>
        </div>
      </div>

      {/* Action Pills Row */}
      <div style={{ display: 'flex', gap: '12px', flexWrap: 'wrap', position: 'relative', zIndex: 1 }}>
        <motion.button
          whileHover={{ scale: 1.04 }}
          whileTap={{ scale: 0.96 }}
          onClick={onNewWorkOrder}
          style={{
            height: '42px',
            padding: '0 20px',
            borderRadius: 'var(--radius-pill)',
            border: 'none',
            backgroundColor: 'var(--hero-banner-pill-bg)',
            color: 'var(--hero-banner-pill-text)',
            fontWeight: 700,
            fontSize: '13px',
            display: 'inline-flex',
            alignItems: 'center',
            gap: '8px',
            cursor: 'pointer',
            boxShadow: 'var(--md-sys-elevation-level1)',
          }}
        >
          <Icon name="add_task" size={18} />
          <span>Jadwalkan Work Order</span>
        </motion.button>

        <motion.button
          whileHover={{ scale: 1.04 }}
          whileTap={{ scale: 0.96 }}
          onClick={onQualityAudit}
          style={{
            height: '42px',
            padding: '0 20px',
            borderRadius: 'var(--radius-pill)',
            border: 'none',
            backgroundColor: 'var(--hero-banner-tag-bg)',
            color: 'var(--hero-banner-text)',
            fontWeight: 600,
            fontSize: '13px',
            display: 'inline-flex',
            alignItems: 'center',
            gap: '8px',
            cursor: 'pointer',
          }}
        >
          <Icon name="fact_check" size={18} />
          <span>Inspeksi Kualitas (QC)</span>
        </motion.button>

        <motion.button
          whileHover={{ scale: 1.04 }}
          whileTap={{ scale: 0.96 }}
          onClick={onReportIncident}
          style={{
            height: '42px',
            padding: '0 16px',
            borderRadius: 'var(--radius-pill)',
            border: 'none',
            backgroundColor: 'var(--hero-banner-tag-bg)',
            color: 'var(--hero-banner-text)',
            fontWeight: 600,
            fontSize: '13px',
            display: 'inline-flex',
            alignItems: 'center',
            gap: '6px',
            cursor: 'pointer',
          }}
        >
          <Icon name="warning_amber" size={18} />
          <span>Lapor Downtime Mesin</span>
        </motion.button>
      </div>
    </motion.div>
  );
};
