/**
 * @license MIT
 * MapCard & Geographic Monitoring — Morphic Design System
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';
import { Button } from '../../components/actions/Button.js';

export interface LocationPin {
  id: string;
  name: string;
  region: string;
  x: number; // SVG percentage x
  y: number; // SVG percentage y
  status: 'Normal' | 'Warning' | 'Critical';
  nodeCount: number;
  trafficVolume: string;
}

export const MapCard: React.FC<{
  title?: string;
  subtitle?: string;
}> = ({
  title = 'Infrastructure Distribution & Regional Nodes',
  subtitle = 'Live status monitoring across 6 primary operational zones',
}) => {
  const pins: LocationPin[] = [
    { id: 'loc-1', name: 'Central Jakarta Hub', region: 'DKI Jakarta', x: 28, y: 48, status: 'Normal', nodeCount: 142, trafficVolume: '1.2 Gbps' },
    { id: 'loc-2', name: 'Surabaya Fulfillment Center', region: 'East Java', x: 48, y: 56, status: 'Normal', nodeCount: 86, trafficVolume: '840 Mbps' },
    { id: 'loc-3', name: 'Medan Gateway Vault', region: 'North Sumatra', x: 18, y: 26, status: 'Normal', nodeCount: 48, trafficVolume: '450 Mbps' },
    { id: 'loc-4', name: 'Makassar Transit Node', region: 'South Sulawesi', x: 62, y: 46, status: 'Warning', nodeCount: 32, trafficVolume: '320 Mbps' },
    { id: 'loc-5', name: 'Balikpapan Energy Hub', region: 'East Kalimantan', x: 46, y: 38, status: 'Normal', nodeCount: 54, trafficVolume: '510 Mbps' },
    { id: 'loc-6', name: 'Denpasar Edge Server', region: 'Bali', x: 54, y: 62, status: 'Normal', nodeCount: 68, trafficVolume: '620 Mbps' },
  ];

  const [activePin, setActivePin] = useState<LocationPin>(pins[0]);

  const getStatusColor = (st: LocationPin['status']) => {
    switch (st) {
      case 'Normal': return 'var(--md-sys-color-primary)';
      case 'Warning': return 'var(--md-sys-color-warning)';
      case 'Critical': return 'var(--md-sys-color-error)';
    }
  };

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
        gap: '20px',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <h3 style={{ margin: '0 0 4px', fontSize: '17px', fontWeight: 700 }}>{title}</h3>
          <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</div>
        </div>

        <div style={{ display: 'flex', gap: '8px' }}>
          <Button variant="tonal" size="sm" icon={<Icon name="filter_alt" size={14} />}>Filter Regions</Button>
          <Button variant="filled" size="sm" icon={<Icon name="add_location_alt" size={14} />}>Add Node</Button>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: '20px', alignItems: 'center' }}>
        {/* Interactive SVG Geographic Map Canvas */}
        <div
          style={{
            height: '240px',
            borderRadius: 'var(--radius-lg)',
            backgroundColor: 'var(--md-sys-color-surface-container)',
            position: 'relative',
            overflow: 'hidden',
            border: '1px solid var(--md-sys-color-border)',
          }}
        >
          {/* Subtle Grid Coordinates Background */}
          <svg width="100%" height="100%" style={{ position: 'absolute', inset: 0, opacity: 0.25 }}>
            <defs>
              <pattern id="grid" width="30" height="30" patternUnits="userSpaceOnUse">
                <path d="M 30 0 L 0 0 0 30" fill="none" stroke="var(--md-sys-color-on-surface-variant)" strokeWidth="0.5" />
              </pattern>
            </defs>
            <rect width="100%" height="100%" fill="url(#grid)" />
          </svg>

          {/* Location Markers */}
          {pins.map((pin) => {
            const isSelected = activePin.id === pin.id;
            const pinColor = getStatusColor(pin.status);

            return (
              <motion.div
                key={pin.id}
                onClick={() => setActivePin(pin)}
                whileHover={{ scale: 1.25 }}
                style={{
                  position: 'absolute',
                  left: `${pin.x}%`,
                  top: `${pin.y}%`,
                  transform: 'translate(-50%, -50%)',
                  cursor: 'pointer',
                  zIndex: isSelected ? 10 : 2,
                }}
              >
                {/* Ping Pulse Animation */}
                <div
                  style={{
                    position: 'absolute',
                    inset: '-6px',
                    borderRadius: '50%',
                    backgroundColor: pinColor,
                    opacity: isSelected ? 0.35 : 0.2,
                    animation: 'ping 2s cubic-bezier(0, 0, 0.2, 1) infinite',
                  }}
                />
                <div
                  style={{
                    width: isSelected ? '18px' : '14px',
                    height: isSelected ? '18px' : '14px',
                    borderRadius: '50%',
                    backgroundColor: pinColor,
                    border: '2px solid var(--md-sys-color-surface)',
                    boxShadow: 'var(--md-sys-elevation-level2)',
                    transition: 'all 0.15s ease',
                  }}
                />
              </motion.div>
            );
          })}
        </div>

        {/* Selected Location Detail Card */}
        <div
          style={{
            borderRadius: 'var(--radius-lg)',
            backgroundColor: 'var(--md-sys-color-surface-container)',
            border: '1px solid var(--md-sys-color-border)',
            padding: '20px',
            display: 'flex',
            flexDirection: 'column',
            gap: '12px',
          }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <span style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600 }}>{activePin.region}</span>
            <span
              style={{
                padding: '2px 8px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-success-container)',
                color: getStatusColor(activePin.status),
                fontSize: '11px',
                fontWeight: 600,
              }}
            >
              {activePin.status}
            </span>
          </div>

          <h4 style={{ margin: 0, fontSize: '16px', fontWeight: 700 }}>{activePin.name}</h4>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '8px', fontSize: '12px', borderTop: '1px solid var(--md-sys-color-border)', paddingTop: '10px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--md-sys-color-on-surface-variant)' }}>Node Capacity:</span>
              <strong style={{ fontFeatureSettings: '"tnum" 1' }}>{activePin.nodeCount} Servers</strong>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--md-sys-color-on-surface-variant)' }}>Active Traffic:</span>
              <strong style={{ fontFeatureSettings: '"tnum" 1' }}>{activePin.trafficVolume}</strong>
            </div>
          </div>

          <Button variant="filled" size="sm" icon={<Icon name="visibility" size={14} />}>
            Open Telemetry
          </Button>
        </div>
      </div>
    </div>
  );
};
