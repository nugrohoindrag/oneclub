/**
 * @license MIT
 * FilterBar Component — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — filter chips reflow, they do not jump
 *   §18 Motion Design      — 180–220ms for a layout change
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, LayoutGroup, motion } from 'motion/react';
import { Button } from '../actions/Button.js';
import { Chip } from './Chips.js';
import { M3_TRANSITIONS, staggerFor, useReducedMotionSafe } from '../../motion/index.js';

export interface FilterOption {
  id: string;
  label: string;
  count?: number;
}

export interface FilterBarProps {
  filters: FilterOption[];
  activeFilter: string;
  onFilterChange: (id: string) => void;
  onClearAll?: () => void;
  searchSlot?: React.ReactNode;
  rightSlot?: React.ReactNode;
  className?: string;
}

export const FilterBar: React.FC<FilterBarProps> = ({
  filters,
  activeFilter,
  onFilterChange,
  onClearAll,
  searchSlot,
  rightSlot,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();
  const step = staggerFor(filters.length, 'tight');

  return (
    <div
      className={`morphic-filter-bar ${className}`}
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        flexWrap: 'wrap',
        gap: '12px',
        padding: '12px 16px',
        borderRadius: 'var(--radius-lg)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexWrap: 'wrap', flex: 1 }}>
        {searchSlot}

        {/* LayoutGroup ties the chips and the Clear button together: when a
            chip appears or leaves, every neighbour slides to its new position
            instead of teleporting (§19). */}
        <LayoutGroup id="morphic-filter-bar">
          <div style={{ display: 'flex', alignItems: 'center', gap: '6px', flexWrap: 'wrap' }}>
            <AnimatePresence initial={false} mode="popLayout">
              {filters.map((f, i) => (
                <motion.div
                  key={f.id}
                  layout
                  initial={{ opacity: 0, scale: reduced ? 1 : 0.9 }}
                  animate={{ opacity: 1, scale: 1, transition: { ...M3_TRANSITIONS.card, delay: i * step } }}
                  exit={{ opacity: 0, scale: reduced ? 1 : 0.9, transition: M3_TRANSITIONS.exit }}
                  transition={M3_TRANSITIONS.layout}
                  style={{ display: 'inline-flex' }}
                >
                  <Chip
                    variant="filter"
                    selected={activeFilter === f.id}
                    onClick={() => onFilterChange(f.id)}
                  >
                    {f.label} {f.count !== undefined && `(${f.count})`}
                  </Chip>
                </motion.div>
              ))}
            </AnimatePresence>
          </div>

          {/* Clear Filters is a conditional control, so it earns an exit. */}
          <AnimatePresence initial={false}>
            {onClearAll && activeFilter !== 'all' && (
              <motion.div
                layout
                initial={{ opacity: 0, x: reduced ? 0 : -6 }}
                animate={{ opacity: 1, x: 0, transition: M3_TRANSITIONS.enter }}
                exit={{ opacity: 0, x: reduced ? 0 : -6, transition: M3_TRANSITIONS.exit }}
              >
                <Button variant="text" size="sm" onClick={onClearAll}>
                  Clear Filters
                </Button>
              </motion.div>
            )}
          </AnimatePresence>
        </LayoutGroup>
      </div>

      {rightSlot && <div>{rightSlot}</div>}
    </div>
  );
};
