/**
 * @license MIT
 * Insight Card — Morphic Design System
 * 07. Surfaces & Containers
 *
 * Spec §3.8 Data Display → Insight Card.
 *
 * The node-status, asset and incident cards that used to live here carried
 * industry vocabulary in their prop names and markup, which §4 forbids in
 * Core. They now live in `src/examples/operations` as thin configurations of
 * the generic entity cards.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';

export interface InsightCardProps {
  headline: string;
  description: string;
  confidenceScore?: number; // e.g. 94%
  impactType?: 'High Impact' | 'Medium Impact' | 'Opportunity';
  actionLabel?: string;
  onAction?: () => void;
  onDismiss?: () => void;
}

export const InsightCard: React.FC<InsightCardProps> = ({
  headline,
  description,
  confidenceScore = 96,
  impactType = 'High Impact',
  actionLabel = 'Apply Recommendation',
  onAction,
  onDismiss,
}) => {
  return (
    <motion.div
      whileHover={{ y: -3 }}
      style={{
        borderRadius: 'var(--radius-xl)',
        padding: '24px',
        background: 'linear-gradient(135deg, var(--md-sys-color-surface) 0%, var(--md-sys-color-surface-container) 100%)',
        border: '1px solid var(--md-sys-color-border)',
        boxShadow: 'var(--md-sys-elevation-level1)',
        display: 'flex',
        flexDirection: 'column',
        gap: '14px',
        position: 'relative',
        overflow: 'hidden',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
          <div
            style={{
              width: '28px',
              height: '28px',
              borderRadius: 'var(--radius-sm)',
              backgroundColor: 'var(--md-sys-color-primary-container)',
              color: 'var(--md-sys-color-on-primary-container)',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <Icon name="auto_awesome" size={16} />
          </div>
          <span style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-primary)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
            Smart AI Insight
          </span>
        </div>

        <span
          style={{
            padding: '2px 8px',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: 'var(--md-sys-color-success-container)',
            color: 'var(--md-sys-color-primary)',
            fontSize: '11px',
            fontWeight: 600,
          }}
        >
          {confidenceScore}% Confidence • {impactType}
        </span>
      </div>

      <div>
        <h3 style={{ margin: '0 0 6px', fontSize: '16px', fontWeight: 700, lineHeight: 1.35 }}>
          {headline}
        </h3>
        <p style={{ margin: 0, fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)', lineHeight: 1.5 }}>
          {description}
        </p>
      </div>

      <div style={{ display: 'flex', gap: '8px', marginTop: '6px', paddingTop: '12px', borderTop: '1px solid var(--md-sys-color-border)' }}>
        <Button variant="filled" size="sm" icon={<Icon name="bolt" size={16} />} onClick={onAction}>
          {actionLabel}
        </Button>
        {onDismiss && (
          <Button variant="text" size="sm" onClick={onDismiss}>
            Dismiss
          </Button>
        )}
      </div>
    </motion.div>
  );
};
