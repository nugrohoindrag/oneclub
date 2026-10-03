/**
 * @license MIT
 * Operations Examples — Morphic Design System
 *
 * Spec: Morphic-Design-System-adjusted.md §4 "Domain-Neutral Design Rules"
 *
 *   Avoid in Core:  Asset Card · Employee Card · Invoice Card · …
 *   Example layer:  "Asset Management Resource Card"
 *
 * These three cards previously sat in Core (`containment/CardSuite`) with
 * ops vocabulary — nodeId, latency, SKU, warehouseLocation, SLA — baked into
 * their prop names and markup. They are now Examples built by configuring the
 * generic Core entity cards, which is the reuse story §4 is protecting.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { Icon } from '../../components/communication/Icon.js';
import { Button } from '../../components/actions/Button.js';
import {
  EntityCard,
  ResourceCard,
} from '../../components/entity/EntityCards.js';

/* ========================================================================= */
/* Node Status — an Example of the generic EntityCard                        */
/* ========================================================================= */

export type NodeStatus = 'Online' | 'Degraded' | 'Offline' | 'Maintenance';

export interface NodeStatusCardProps {
  title: string;
  nodeId: string;
  status: NodeStatus;
  latency: string;
  uptime: string;
  region: string;
}

const NODE_STATUS_COLOR: Record<NodeStatus, string> = {
  Online: 'var(--md-sys-color-primary)',
  Degraded: 'var(--md-sys-color-warning)',
  Offline: 'var(--md-sys-color-error)',
  Maintenance: 'var(--md-sys-color-on-surface-variant)',
};

export const NodeStatusCard: React.FC<NodeStatusCardProps> = ({
  title,
  nodeId,
  status,
  latency,
  uptime,
  region,
}) => {
  const statusColor = NODE_STATUS_COLOR[status];

  return (
    <EntityCard
      className="example-node-status-card"
      overline={`${nodeId} • ${region}`}
      title={title}
      headerAction={
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--md-sys-spacing-1)' }}>
          <span
            aria-hidden="true"
            style={{
              width: '8px',
              height: '8px',
              borderRadius: 'var(--radius-pill)',
              backgroundColor: statusColor,
              display: 'inline-block',
            }}
          />
          <span
            style={{
              fontSize: 'var(--md-sys-typescale-meta-size)',
              fontWeight: 600,
              color: statusColor,
            }}
          >
            {status}
          </span>
        </span>
      }
    >
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: '1fr 1fr',
          gap: 'var(--md-sys-spacing-3)',
          paddingTop: 'var(--md-sys-spacing-3)',
          borderTop: '1px solid var(--md-sys-color-border)',
        }}
      >
        {[
          { label: 'Response latency', value: latency },
          { label: 'Monthly uptime', value: uptime },
        ].map((metric) => (
          <div key={metric.label}>
            <div
              style={{
                fontSize: 'var(--md-sys-typescale-meta-size-sm)',
                color: 'var(--md-sys-color-on-surface-variant)',
              }}
            >
              {metric.label}
            </div>
            <div
              style={{
                fontSize: 'var(--md-sys-typescale-body-size)',
                fontWeight: 600,
                fontVariantNumeric: 'tabular-nums',
                color: 'var(--md-sys-color-on-surface)',
              }}
            >
              {metric.value}
            </div>
          </div>
        ))}
      </div>
    </EntityCard>
  );
};

/* ========================================================================= */
/* Asset Management Resource Card — §4's own worked example                   */
/* ========================================================================= */

export interface AssetManagementResourceCardProps {
  name: string;
  sku: string;
  warehouseLocation: string;
  /** Percentage of capacity used, 0–100. */
  capacityUsed: number;
  unitCount: string;
  category: string;
  onInspect?: () => void;
}

export const AssetManagementResourceCard: React.FC<AssetManagementResourceCardProps> = ({
  name,
  sku,
  warehouseLocation,
  capacityUsed,
  unitCount,
  category,
  onInspect,
}) => (
  <ResourceCard
    className="example-asset-resource-card"
    overline={`${category} • SKU ${sku}`}
    name={name}
    icon="inventory_2"
    utilization={capacityUsed}
    capacityLabel={unitCount}
    utilizationLabel="Capacity used"
    metricLabel="Location"
    metricValue={warehouseLocation}
    actionLabel={onInspect ? 'Inspect' : undefined}
    onAction={onInspect}
  />
);

/** @deprecated §4 names this as a Core anti-pattern. Use the name above. */
export const AssetCard = AssetManagementResourceCard;
export type AssetCardProps = AssetManagementResourceCardProps;

/* ========================================================================= */
/* Incident Card — an Example of the generic EntityCard                      */
/* ========================================================================= */

export type IncidentSeverity = 'Critical' | 'Warning' | 'Info';

export interface IncidentCardProps {
  id: string;
  title: string;
  severity: IncidentSeverity;
  slaTimeRemaining: string;
  affectedService: string;
  assignedTo: string;
  onResolve?: () => void;
}

const SEVERITY_TOKENS: Record<IncidentSeverity, { bg: string; fg: string; icon: string }> = {
  Critical: {
    bg: 'var(--md-sys-color-error-container)',
    fg: 'var(--md-sys-color-error)',
    icon: 'error',
  },
  Warning: {
    bg: 'var(--md-sys-color-warning-container)',
    fg: 'var(--md-sys-color-warning)',
    icon: 'warning',
  },
  Info: {
    bg: 'var(--md-sys-color-info-container)',
    fg: 'var(--md-sys-color-info)',
    icon: 'info',
  },
};

export const IncidentCard: React.FC<IncidentCardProps> = ({
  id,
  title,
  severity,
  slaTimeRemaining,
  affectedService,
  assignedTo,
  onResolve,
}) => {
  const tone = SEVERITY_TOKENS[severity];

  return (
    <EntityCard
      className="example-incident-card"
      overline={`${id} • ${affectedService}`}
      title={title}
      headerAction={
        <span
          style={{
            padding: '2px 8px',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: tone.bg,
            color: tone.fg,
            fontSize: 'var(--md-sys-typescale-meta-size-sm)',
            fontWeight: 600,
            display: 'inline-flex',
            alignItems: 'center',
            gap: '4px',
            whiteSpace: 'nowrap',
          }}
        >
          <Icon name={tone.icon} size={13} />
          <span>{severity}</span>
        </span>
      }
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          gap: 'var(--md-sys-spacing-3)',
          paddingTop: 'var(--md-sys-spacing-3)',
          borderTop: '1px solid var(--md-sys-color-border)',
        }}
      >
        <div
          style={{
            fontSize: 'var(--md-sys-typescale-meta-size)',
            color: 'var(--md-sys-color-on-surface-variant)',
          }}
        >
          SLA:{' '}
          <strong style={{ color: tone.fg, fontVariantNumeric: 'tabular-nums' }}>
            {slaTimeRemaining}
          </strong>{' '}
          • Assigned: {assignedTo}
        </div>
        <Button variant="tonal" size="sm" onClick={onResolve}>
          Resolve
        </Button>
      </div>
    </EntityCard>
  );
};
