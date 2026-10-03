/**
 * @license MIT
 * 12. Utility Components Suite — Morphic Design System
 * 
 * Includes: DateFilter, ExportButton, RefreshButton, ViewSwitcher, SortControl, BulkActionsBar
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { IconButton } from '../actions/IconButton.js';

// 1. DateFilter
export const DateFilter: React.FC<{
  activePeriod: string;
  onChange: (period: string) => void;
  options?: { id: string; label: string }[];
}> = ({
  activePeriod,
  onChange,
  options = [
    { id: 'today', label: 'Today' },
    { id: '7d', label: '7 Days' },
    { id: '30d', label: '30 Days' },
    { id: 'quarter', label: 'This Quarter' },
    { id: 'custom', label: 'Custom...' },
  ],
}) => {
  return (
    <div
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '4px',
        padding: '3px',
        borderRadius: 'var(--radius-pill)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        border: '1px solid var(--md-sys-color-border)',
      }}
    >
      {options.map((opt) => {
        const isActive = activePeriod === opt.id;
        return (
          <button
            key={opt.id}
            onClick={() => onChange(opt.id)}
            style={{
              padding: '5px 12px',
              borderRadius: 'var(--radius-pill)',
              border: 'none',
              backgroundColor: isActive ? 'var(--md-sys-color-surface)' : 'transparent',
              color: isActive ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
              fontWeight: isActive ? 700 : 500,
              fontSize: '12px',
              cursor: 'pointer',
              boxShadow: isActive ? 'var(--md-sys-elevation-level1)' : 'none',
              transition: 'all 0.15s ease',
            }}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
};

// 2. ExportButton (Split / Menu Export Trigger)
export const ExportButton: React.FC<{
  onExportExcel?: () => void;
  onExportPdf?: () => void;
  onExportCsv?: () => void;
}> = ({ onExportExcel, onExportPdf, onExportCsv }) => {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <div style={{ position: 'relative', display: 'inline-block' }}>
      <Button
        variant="tonal"
        size="sm"
        icon={<Icon name="download" size={16} />}
        onClick={() => setIsOpen(!isOpen)}
      >
        Export
        <Icon name={isOpen ? 'expand_less' : 'expand_more'} size={16} />
      </Button>

      <AnimatePresence>
        {isOpen && (
          <motion.div
            initial={{ opacity: 0, scale: 0.95, y: 4 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: 4 }}
            style={{
              position: 'absolute',
              top: 'calc(100% + 6px)',
              right: 0,
              backgroundColor: 'var(--md-sys-color-surface)',
              borderRadius: 'var(--radius-md)',
              padding: '6px',
              boxShadow: 'var(--md-sys-elevation-level2)',
              border: '1px solid var(--md-sys-color-border)',
              zIndex: 100,
              minWidth: '160px',
            }}
          >
            <button
              onClick={() => { onExportExcel?.(); setIsOpen(false); }}
              style={{ width: '100%', padding: '8px 12px', border: 'none', backgroundColor: 'transparent', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', textAlign: 'left' }}
            >
              <Icon name="table_view" size={16} color="var(--md-sys-color-success)" />
              <span>Excel (.xlsx)</span>
            </button>
            <button
              onClick={() => { onExportPdf?.(); setIsOpen(false); }}
              style={{ width: '100%', padding: '8px 12px', border: 'none', backgroundColor: 'transparent', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', textAlign: 'left' }}
            >
              <Icon name="picture_as_pdf" size={16} color="var(--md-sys-color-error)" />
              <span>PDF Document</span>
            </button>
            <button
              onClick={() => { onExportCsv?.(); setIsOpen(false); }}
              style={{ width: '100%', padding: '8px 12px', border: 'none', backgroundColor: 'transparent', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '8px', fontSize: '13px', textAlign: 'left' }}
            >
              <Icon name="description" size={16} color="var(--md-sys-color-info)" />
              <span>CSV Raw Data</span>
            </button>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};

// 3. RefreshButton
export const RefreshButton: React.FC<{
  onRefresh: () => void;
  isLoading?: boolean;
  size?: 'sm' | 'md';
}> = ({ onRefresh, isLoading = false, size = 'sm' }) => {
  return (
    <IconButton
      variant="standard"
      icon={
        <motion.div animate={{ rotate: isLoading ? 360 : 0 }} transition={{ repeat: isLoading ? Infinity : 0, duration: 1, ease: 'linear' }}>
          <Icon name="refresh" size={size === 'sm' ? 18 : 20} />
        </motion.div>
      }
      onClick={onRefresh}
      disabled={isLoading}
    />
  );
};

// 4. ViewSwitcher
export const ViewSwitcher: React.FC<{
  currentView: 'grid' | 'list' | 'table';
  onViewChange: (view: 'grid' | 'list' | 'table') => void;
}> = ({ currentView, onViewChange }) => {
  return (
    <div
      style={{
        display: 'inline-flex',
        borderRadius: 'var(--radius-pill)',
        overflow: 'hidden',
        border: '1px solid var(--md-sys-color-border)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
      }}
    >
      <button
        onClick={() => onViewChange('grid')}
        style={{
          padding: '6px 10px',
          border: 'none',
          backgroundColor: currentView === 'grid' ? 'var(--md-sys-color-surface)' : 'transparent',
          color: currentView === 'grid' ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
          cursor: 'pointer',
        }}
        title="Grid View"
      >
        <Icon name="grid_view" size={16} />
      </button>
      <button
        onClick={() => onViewChange('list')}
        style={{
          padding: '6px 10px',
          border: 'none',
          backgroundColor: currentView === 'list' ? 'var(--md-sys-color-surface)' : 'transparent',
          color: currentView === 'list' ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
          cursor: 'pointer',
        }}
        title="List View"
      >
        <Icon name="view_list" size={16} />
      </button>
      <button
        onClick={() => onViewChange('table')}
        style={{
          padding: '6px 10px',
          border: 'none',
          backgroundColor: currentView === 'table' ? 'var(--md-sys-color-surface)' : 'transparent',
          color: currentView === 'table' ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
          cursor: 'pointer',
        }}
        title="Table View"
      >
        <Icon name="table_chart" size={16} />
      </button>
    </div>
  );
};

// 5. SortControl
export const SortControl: React.FC<{
  sortKey: string;
  sortOrder: 'asc' | 'desc';
  options: { key: string; label: string }[];
  onSortChange: (key: string, order: 'asc' | 'desc') => void;
}> = ({ sortKey, sortOrder, options, onSortChange }) => {
  return (
    <div style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', fontSize: '13px' }}>
      <span style={{ color: 'var(--md-sys-color-on-surface-variant)', fontSize: '12px' }}>Sort by:</span>
      <select
        value={sortKey}
        onChange={(e) => onSortChange(e.target.value, sortOrder)}
        style={{
          padding: '6px 10px',
          borderRadius: 'var(--radius-pill)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          border: '1px solid var(--md-sys-color-border)',
          color: 'var(--md-sys-color-on-surface)',
          fontSize: '12px',
          fontWeight: 600,
          outline: 'none',
          cursor: 'pointer',
        }}
      >
        {options.map((opt) => (
          <option key={opt.key} value={opt.key}>{opt.label}</option>
        ))}
      </select>

      <IconButton
        variant="standard"
        icon={<Icon name={sortOrder === 'asc' ? 'arrow_upward' : 'arrow_downward'} size={16} />}
        onClick={() => onSortChange(sortKey, sortOrder === 'asc' ? 'desc' : 'asc')}
      />
    </div>
  );
};
