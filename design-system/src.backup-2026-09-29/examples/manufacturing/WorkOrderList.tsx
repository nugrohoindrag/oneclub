import React from 'react';
import { motion } from 'motion/react';
import { Icon } from '../../components/communication/Icon.js';

export interface WorkOrderItem {
  id: string;
  sku: string;
  productName: string;
  line: string;
  completedUnits: number;
  targetUnits: number;
  operator: string;
  status: 'Sedang Berjalan' | 'Selesai' | 'Menunggu Bahan' | 'Maintenance';
}

export interface WorkOrderListProps {
  workOrders: WorkOrderItem[];
  title?: string;
  onViewAll?: () => void;
  className?: string;
}

export const WorkOrderList: React.FC<WorkOrderListProps> = ({
  workOrders,
  title = 'Daftar Eksekusi Work Order (Batch Produksi)',
  onViewAll,
  className = '',
}) => {
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
      className={`morphic-transaction-list ${className}`}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
        <div>
          <h3 style={{ margin: 0, fontSize: '17px', fontWeight: 600 }}>{title}</h3>
          <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>
            Pemantauan batch manufaktur & kepatuhan jadwal shift
          </div>
        </div>
        {onViewAll && (
          <button
            onClick={onViewAll}
            style={{
              background: 'none',
              border: 'none',
              color: 'var(--md-sys-color-primary)',
              fontSize: '13px',
              fontWeight: 600,
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: '4px',
            }}
          >
            <span>Semua Work Order</span>
            <Icon name="chevron_right" size={16} />
          </button>
        )}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
        {workOrders.map((wo) => {
          const progress = Math.round((wo.completedUnits / wo.targetUnits) * 100);

          const statusConfig = {
            'Sedang Berjalan': { bg: 'var(--md-sys-color-success-container)', color: 'var(--md-sys-color-primary)', icon: 'sync' },
            'Selesai': { bg: 'var(--md-sys-color-success-container)', color: 'var(--md-sys-color-success)', icon: 'check_circle' },
            'Menunggu Bahan': { bg: 'var(--md-sys-color-warning-container)', color: 'var(--md-sys-color-warning)', icon: 'hourglass_empty' },
            'Maintenance': { bg: 'var(--md-sys-color-error-container)', color: 'var(--md-sys-color-error)', icon: 'build' },
          }[wo.status];

          return (
            <motion.div
              key={wo.id}
              whileHover={{ backgroundColor: 'var(--md-sys-color-surface-container)' }}
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '14px 16px',
                borderRadius: 'var(--radius-md)',
                backgroundColor: 'transparent',
                border: '1px solid var(--md-sys-color-border)',
                transition: 'background-color 0.15s ease',
              }}
            >
              {/* Left: WO ID & Product Name */}
              <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
                <div
                  style={{
                    width: '42px',
                    height: '42px',
                    borderRadius: 'var(--radius-sm)',
                    backgroundColor: 'var(--md-sys-color-surface-container)',
                    color: 'var(--md-sys-color-primary)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: '20px',
                  }}
                >
                  <Icon name="precision_manufacturing" size={22} />
                </div>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    <span style={{ fontSize: '14px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)' }}>
                      {wo.id}
                    </span>
                    <span style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>
                      • {wo.sku}
                    </span>
                  </div>
                  <div style={{ fontSize: '13px', color: 'var(--md-sys-color-on-surface)', marginTop: '2px', fontWeight: 500 }}>
                    {wo.productName}
                  </div>
                  <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '2px' }}>
                    {wo.line} • Lead Op: {wo.operator}
                  </div>
                </div>
              </div>

              {/* Right: Progress & Status Badge */}
              <div style={{ textAlign: 'right', display: 'flex', alignItems: 'center', gap: '20px' }}>
                <div style={{ minWidth: '120px' }}>
                  <div style={{ fontSize: '13px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)' }}>
                    {wo.completedUnits.toLocaleString()} / {wo.targetUnits.toLocaleString()} Unit
                  </div>
                  {/* Progress Mini Bar */}
                  <div style={{ width: '100%', height: '5px', borderRadius: 'var(--radius-pill)', backgroundColor: 'var(--md-sys-color-surface-container)', marginTop: '6px', overflow: 'hidden' }}>
                    <div style={{ width: `${progress}%`, height: '100%', backgroundColor: 'var(--md-sys-color-primary)', borderRadius: 'var(--radius-pill)' }} />
                  </div>
                  <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', marginTop: '3px', fontWeight: 600 }}>
                    {progress}% Target Selesai
                  </div>
                </div>

                <span
                  style={{
                    padding: '6px 12px',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: statusConfig.bg,
                    color: statusConfig.color,
                    fontSize: '11px',
                    fontWeight: 600,
                    display: 'flex',
                    alignItems: 'center',
                    gap: '4px',
                    whiteSpace: 'nowrap',
                  }}
                >
                  <Icon name={statusConfig.icon} size={14} />
                  <span>{wo.status}</span>
                </span>
              </div>
            </motion.div>
          );
        })}
      </div>
    </div>
  );
};

export interface ShiftFilterBarProps {
  options?: string[];
  activeOption: string;
  onChange: (opt: string) => void;
  className?: string;
}

export const ShiftFilterBar: React.FC<ShiftFilterBarProps> = ({
  options = ['Shift 1 (Pagi)', 'Shift 2 (Siang)', 'Shift 3 (Malam)', 'Semua Lini (SMT & Assy)', 'Hanya Alert'],
  activeOption,
  onChange,
  className = '',
}) => {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '8px',
        overflowX: 'auto',
        padding: '4px 0',
        userSelect: 'none',
      }}
      className={`morphic-filter-bar ${className}`}
    >
      {options.map((opt) => {
        const isSelected = opt === activeOption;
        return (
          <button
            key={opt}
            onClick={() => onChange(opt)}
            style={{
              padding: '6px 16px',
              borderRadius: 'var(--radius-pill)',
              border: isSelected ? '1px solid transparent' : '1px solid var(--md-sys-color-border)',
              backgroundColor: isSelected
                ? 'var(--md-sys-color-primary)'
                : 'var(--md-sys-color-surface)',
              color: isSelected ? 'var(--md-sys-color-on-primary)' : 'var(--md-sys-color-on-surface)',
              fontSize: '12px',
              fontWeight: isSelected ? 600 : 500,
              cursor: 'pointer',
              transition: 'all 0.15s ease',
              whiteSpace: 'nowrap',
            }}
          >
            {opt}
          </button>
        );
      })}
    </div>
  );
};

export interface MachineStationProps {
  id: string;
  name: string;
  type: string;
  temperature: string;
  vibration: string;
  speedRpm: string;
  status: 'Running' | 'Idle' | 'Maintenance' | 'Error';
  onEmergencyStop?: () => void;
}

export const MachineTelemetryGrid: React.FC<{ stations: MachineStationProps[] }> = ({ stations }) => {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '16px' }}>
      {stations.map((m) => {
        const isRunning = m.status === 'Running';
        return (
          <div
            key={m.id}
            style={{
              borderRadius: 'var(--radius-xl)',
              backgroundColor: 'var(--md-sys-color-surface)',
              border: '1px solid var(--md-sys-color-border)',
              padding: '20px 24px',
              color: 'var(--md-sys-color-on-surface)',
              boxShadow: 'var(--md-sys-elevation-level1)',
            }}
          >
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '14px' }}>
              <div>
                <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 600, textTransform: 'uppercase' }}>
                  {m.id} • {m.type}
                </div>
                <h4 style={{ margin: '2px 0 0', fontSize: '16px', fontWeight: 700 }}>{m.name}</h4>
              </div>
              <span
                style={{
                  padding: '4px 10px',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: isRunning ? 'var(--md-sys-color-success-container)' : 'var(--md-sys-color-error-container)',
                  color: isRunning ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-error)',
                  fontSize: '11px',
                  fontWeight: 600,
                  display: 'flex',
                  alignItems: 'center',
                  gap: '4px',
                }}
              >
                <span style={{ width: '6px', height: '6px', borderRadius: '50%', backgroundColor: isRunning ? 'var(--md-sys-color-success)' : 'var(--md-sys-color-error)' }} />
                <span>{m.status}</span>
              </span>
            </div>

            {/* Sensor Telemetry Rows */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '10px', padding: '12px', borderRadius: 'var(--radius-md)', backgroundColor: 'var(--md-sys-color-surface-container)' }}>
              <div>
                <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>Suhu Operasi</div>
                <div style={{ fontSize: '14px', fontWeight: 700, marginTop: '2px' }}>{m.temperature}</div>
              </div>
              <div>
                <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>Getaran Sensor</div>
                <div style={{ fontSize: '14px', fontWeight: 700, marginTop: '2px' }}>{m.vibration}</div>
              </div>
              <div>
                <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>Putaran Motor</div>
                <div style={{ fontSize: '14px', fontWeight: 700, marginTop: '2px' }}>{m.speedRpm}</div>
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
};
