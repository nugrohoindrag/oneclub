/**
 * @license MIT
 * Enterprise Advanced Data Table — Morphic Design System
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { Checkbox } from '../selection/Checkbox.js';

export interface ColumnDef<T> {
  key: keyof T | string;
  header: string;
  sortable?: boolean;
  width?: string;
  render?: (row: T) => React.ReactNode;
}

export interface AdvancedDataTableProps<T extends { id: string }> {
  columns: ColumnDef<T>[];
  data: T[];
  title?: string;
  subtitle?: string;
  searchable?: boolean;
  selectable?: boolean;
  expandable?: boolean;
  renderExpandedRow?: (row: T) => React.ReactNode;
  onBulkDelete?: (selectedIds: string[]) => void;
  onBulkExport?: (selectedIds: string[]) => void;
  className?: string;
}

export function AdvancedDataTable<T extends { id: string; [key: string]: any }>({
  columns,
  data,
  title,
  subtitle,
  searchable = true,
  selectable = true,
  expandable = true,
  renderExpandedRow,
  onBulkDelete,
  onBulkExport,
  className = '',
}: AdvancedDataTableProps<T>) {
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [expandedIds, setExpandedIds] = useState<string[]>([]);
  const [sortKey, setSortKey] = useState<string | null>(null);
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('asc');
  const [searchQuery, setSearchQuery] = useState('');
  const [density, setDensity] = useState<'comfortable' | 'compact'>('comfortable');

  const handleSelectAll = (checked: boolean) => {
    if (checked) {
      setSelectedIds(data.map((d) => d.id));
    } else {
      setSelectedIds([]);
    }
  };

  const handleSelectRow = (id: string, checked: boolean) => {
    if (checked) {
      setSelectedIds((prev) => [...prev, id]);
    } else {
      setSelectedIds((prev) => prev.filter((i) => i !== id));
    }
  };

  const toggleExpandRow = (id: string) => {
    setExpandedIds((prev) =>
      prev.includes(id) ? prev.filter((i) => i !== id) : [...prev, id]
    );
  };

  const handleSort = (key: string) => {
    if (sortKey === key) {
      setSortOrder((prev) => (prev === 'asc' ? 'desc' : 'asc'));
    } else {
      setSortKey(key);
      setSortOrder('asc');
    }
  };

  // Filter & Sort
  const filteredData = data.filter((row) =>
    Object.values(row).some((val) =>
      String(val).toLowerCase().includes(searchQuery.toLowerCase())
    )
  );

  const sortedData = [...filteredData].sort((a, b) => {
    if (!sortKey) return 0;
    const valA = a[sortKey];
    const valB = b[sortKey];
    if (valA < valB) return sortOrder === 'asc' ? -1 : 1;
    if (valA > valB) return sortOrder === 'asc' ? 1 : -1;
    return 0;
  });

  const isAllSelected = data.length > 0 && selectedIds.length === data.length;
  const isPartiallySelected = selectedIds.length > 0 && selectedIds.length < data.length;

  const rowPadding = density === 'compact' ? '8px 16px' : '16px 20px';

  return (
    <div
      className={`morphic-advanced-table ${className}`}
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        overflow: 'hidden',
        boxShadow: 'var(--md-sys-elevation-level1)',
      }}
    >
      {/* Table Header Controls */}
      <div
        style={{
          padding: '20px 24px',
          borderBottom: '1px solid var(--md-sys-color-border)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: '16px',
        }}
      >
        <div>
          {title && <h3 style={{ margin: '0 0 4px', fontSize: '17px', fontWeight: 700 }}>{title}</h3>}
          {subtitle && <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>{subtitle}</div>}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
          {searchable && (
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
                padding: '6px 14px',
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-surface-container)',
                border: '1px solid var(--md-sys-color-border)',
              }}
            >
              <Icon name="search" size={16} color="var(--md-sys-color-on-surface-variant)" />
              <input
                type="text"
                placeholder="Search in table..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                style={{
                  border: 'none',
                  outline: 'none',
                  backgroundColor: 'transparent',
                  color: 'var(--md-sys-color-on-surface)',
                  fontSize: '12px',
                }}
              />
            </div>
          )}

          {/* Density Switcher */}
          <div style={{ display: 'flex', borderRadius: 'var(--radius-pill)', overflow: 'hidden', border: '1px solid var(--md-sys-color-border)' }}>
            <button
              onClick={() => setDensity('comfortable')}
              style={{
                padding: '6px 10px',
                border: 'none',
                backgroundColor: density === 'comfortable' ? 'var(--md-sys-color-surface-container-high)' : 'transparent',
                cursor: 'pointer',
              }}
              title="Comfortable Density"
            >
              <Icon name="density_medium" size={16} />
            </button>
            <button
              onClick={() => setDensity('compact')}
              style={{
                padding: '6px 10px',
                border: 'none',
                backgroundColor: density === 'compact' ? 'var(--md-sys-color-surface-container-high)' : 'transparent',
                cursor: 'pointer',
              }}
              title="Compact Density"
            >
              <Icon name="density_small" size={16} />
            </button>
          </div>
        </div>
      </div>

      {/* Floating Bulk Actions Bar */}
      <AnimatePresence>
        {selectedIds.length > 0 && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: 'auto' }}
            exit={{ opacity: 0, height: 0 }}
            style={{
              backgroundColor: 'var(--md-sys-color-primary-container)',
              color: 'var(--md-sys-color-on-primary-container)',
              padding: '10px 24px',
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              fontSize: '13px',
              fontWeight: 600,
            }}
          >
            <span>{selectedIds.length} rows selected</span>
            <div style={{ display: 'flex', gap: '8px' }}>
              <Button variant="tonal" size="sm" icon={<Icon name="download" size={14} />} onClick={() => onBulkExport?.(selectedIds)}>
                Export Selected
              </Button>
              <Button variant="tonal" size="sm" icon={<Icon name="delete" size={14} />} onClick={() => onBulkDelete?.(selectedIds)}>
                Delete Selected
              </Button>
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* Table Content */}
      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', textAlign: 'left', fontSize: '13px' }}>
          <thead>
            <tr style={{ backgroundColor: 'var(--md-sys-color-surface-container)', borderBottom: '1px solid var(--md-sys-color-border)' }}>
              {expandable && <th style={{ width: '40px', padding: '12px 8px 12px 20px' }} />}
              {selectable && (
                <th style={{ width: '40px', padding: '12px 8px' }}>
                  <Checkbox checked={isAllSelected} indeterminate={isPartiallySelected} onChange={handleSelectAll} />
                </th>
              )}
              {columns.map((col) => (
                <th
                  key={String(col.key)}
                  onClick={() => col.sortable && handleSort(String(col.key))}
                  style={{
                    padding: '12px 16px',
                    fontWeight: 600,
                    fontSize: '11px',
                    color: 'var(--md-sys-color-on-surface-variant)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.04em',
                    cursor: col.sortable ? 'pointer' : 'default',
                    userSelect: 'none',
                    width: col.width,
                  }}
                >
                  <div style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                    <span>{col.header}</span>
                    {col.sortable && sortKey === String(col.key) && (
                      <Icon name={sortOrder === 'asc' ? 'arrow_upward' : 'arrow_downward'} size={14} color="var(--md-sys-color-primary)" />
                    )}
                  </div>
                </th>
              ))}
            </tr>
          </thead>

          <tbody>
            {sortedData.map((row) => {
              const isSelected = selectedIds.includes(row.id);
              const isExpanded = expandedIds.includes(row.id);

              return (
                <React.Fragment key={row.id}>
                  <tr
                    style={{
                      borderBottom: '1px solid var(--md-sys-color-border)',
                      backgroundColor: isSelected ? 'var(--md-sys-color-primary-container)' : 'transparent',
                      transition: 'background-color 0.12s ease',
                    }}
                  >
                    {expandable && (
                      <td style={{ padding: '8px 8px 8px 20px' }}>
                        <button
                          onClick={() => toggleExpandRow(row.id)}
                          style={{
                            border: 'none',
                            backgroundColor: 'transparent',
                            cursor: 'pointer',
                            color: 'var(--md-sys-color-on-surface-variant)',
                            display: 'flex',
                            alignItems: 'center',
                          }}
                        >
                          <Icon name={isExpanded ? 'expand_less' : 'expand_more'} size={18} />
                        </button>
                      </td>
                    )}

                    {selectable && (
                      <td style={{ padding: '8px' }}>
                        <Checkbox checked={isSelected} onChange={(c) => handleSelectRow(row.id, c)} />
                      </td>
                    )}

                    {columns.map((col) => (
                      <td key={String(col.key)} style={{ padding: rowPadding }}>
                        {col.render ? col.render(row) : row[col.key]}
                      </td>
                    ))}
                  </tr>

                  {/* Expandable Sub-Row */}
                  {expandable && isExpanded && renderExpandedRow && (
                    <tr style={{ backgroundColor: 'var(--md-sys-color-surface-container-lowest)' }}>
                      <td colSpan={columns.length + (selectable ? 1 : 0) + 1} style={{ padding: '16px 24px', borderBottom: '1px solid var(--md-sys-color-border)' }}>
                        {renderExpandedRow(row)}
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
      </div>

      {/* Pagination Footer */}
      <div
        style={{
          padding: '14px 24px',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          fontSize: '12px',
          color: 'var(--md-sys-color-on-surface-variant)',
        }}
      >
        <span>Showing {sortedData.length} entities</span>
        <div style={{ display: 'flex', gap: '8px' }}>
          <Button variant="outlined" size="sm">Previous</Button>
          <Button variant="filled" size="sm">Next</Button>
        </div>
      </div>
    </div>
  );
}
