/**
 * @license MIT
 * Logistics Examples — Morphic Design System
 *
 * Spec: Morphic-Design-System-adjusted.md §4 "Domain-Neutral Design Rules"
 *
 *   "Industry-specific examples may exist in the Examples / Templates layer."
 *
 *       Generic:  Resource Card
 *       Example:  Asset Management Resource Card
 *
 * `WarehouseCard` and `DeliveryCard` are named on §4's explicit do-not-build
 * list for Core. They live here instead, and they are thin configurations of
 * the generic Core components — not forks. That is the whole point of the
 * rule: the industry vocabulary is a prop, never a new component.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import {
  ResourceCard,
  TransactionCard,
  EntityStatus,
} from '../../components/entity/EntityCards.js';

/* ========================================================================= */
/* Warehouse — an Example of the generic ResourceCard                        */
/* ========================================================================= */

export interface WarehouseCardProps {
  name: string;
  code: string;
  location: string;
  /** Percentage of storage in use, 0–100. */
  occupancyRate: number;
  activeShipments: number;
  totalCapacity: string;
  status?: EntityStatus;
  onInspect?: () => void;
}

export const WarehouseCard: React.FC<WarehouseCardProps> = ({
  name,
  code,
  location,
  occupancyRate,
  activeShipments,
  totalCapacity,
  status = 'online',
  onInspect,
}) => (
  <ResourceCard
    className="example-warehouse-card"
    overline={`${code} • ${location}`}
    name={name}
    icon="warehouse"
    status={status}
    utilization={occupancyRate}
    capacityLabel={totalCapacity}
    utilizationLabel="Storage utilization"
    warnAbove={75}
    errorAbove={85}
    metricLabel="Active shipments"
    metricValue={activeShipments}
    actionLabel="Inspect"
    onAction={onInspect}
  />
);

/* ========================================================================= */
/* Delivery — an Example of the generic TransactionCard                      */
/* ========================================================================= */

export type DeliveryStatus = 'In Transit' | 'Out for Delivery' | 'Delivered' | 'Exception';

export interface DeliveryCardProps {
  trackingId: string;
  destination: string;
  origin: string;
  eta: string;
  /** 0–100 */
  progress: number;
  carrier: string;
  status: DeliveryStatus;
}

/** Map the domain status onto a Core semantic tone (§11). */
const DELIVERY_TONE: Record<DeliveryStatus, 'success' | 'info' | 'warning' | 'error'> = {
  'In Transit': 'info',
  'Out for Delivery': 'info',
  Delivered: 'success',
  Exception: 'error',
};

export const DeliveryCard: React.FC<DeliveryCardProps> = ({
  trackingId,
  destination,
  origin,
  eta,
  progress,
  carrier,
  status,
}) => (
  <TransactionCard
    className="example-delivery-card"
    reference={trackingId}
    party={carrier}
    from={origin}
    to={destination}
    progress={progress}
    dueLabel="Estimated arrival"
    dueValue={eta}
    statusLabel={status}
    tone={DELIVERY_TONE[status]}
  />
);
