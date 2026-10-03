/**
 * @license MIT
 * Pagination & Navigation Item Components — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — nav selection: the indicator travels, it does not
 *        blink out on one page number and reappear on another
 *   §18 Motion Design      — 120–180ms for a button state change
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, LayoutGroup, motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { IconButton } from '../actions/IconButton.js';
import { M3_SPRING, M3_TRANSITIONS, useReducedMotionSafe } from '../../motion/index.js';

export interface PaginationProps {
  currentPage: number;
  totalPages: number;
  onPageChange: (page: number) => void;
  showQuickJumper?: boolean;
  totalEntities?: number;
  pageSize?: number;
  className?: string;
}

export const Pagination: React.FC<PaginationProps> = ({
  currentPage,
  totalPages,
  onPageChange,
  showQuickJumper = false,
  totalEntities,
  pageSize = 10,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();

  const getPageNumbers = () => {
    const pages: (number | string)[] = [];
    if (totalPages <= 7) {
      for (let i = 1; i <= totalPages; i++) pages.push(i);
    } else {
      if (currentPage <= 4) {
        pages.push(1, 2, 3, 4, 5, '...', totalPages);
      } else if (currentPage >= totalPages - 3) {
        pages.push(1, '...', totalPages - 4, totalPages - 3, totalPages - 2, totalPages - 1, totalPages);
      } else {
        pages.push(1, '...', currentPage - 1, currentPage, currentPage + 1, '...', totalPages);
      }
    }
    return pages;
  };

  const rangeStart = (currentPage - 1) * pageSize + 1;
  const rangeEnd = totalEntities === undefined ? 0 : Math.min(currentPage * pageSize, totalEntities);

  return (
    <div
      className={`morphic-pagination ${className}`}
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        flexWrap: 'wrap',
        gap: '16px',
        fontSize: '13px',
        color: 'var(--md-sys-color-on-surface-variant)',
      }}
    >
      {/* Total Entities Summary — the range crossfades on page change so the
          eye registers that it updated (§19). */}
      {totalEntities !== undefined && (
        <div style={{ fontFeatureSettings: '"tnum" 1' }}>
          <AnimatePresence mode="wait" initial={false}>
            <motion.span
              key={currentPage}
              initial={{ opacity: 0, y: reduced ? 0 : 4 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: reduced ? 0 : -4 }}
              transition={M3_TRANSITIONS.button}
              style={{ display: 'inline-block' }}
            >
              Showing {rangeStart}–{rangeEnd} of {totalEntities} entries
            </motion.span>
          </AnimatePresence>
        </div>
      )}

      {/* Pages Controls */}
      <LayoutGroup id="morphic-pagination">
        <div style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
          <IconButton
            variant="standard"
            icon={<Icon name="chevron_left" size={18} />}
            disabled={currentPage === 1}
            onClick={() => onPageChange(currentPage - 1)}
            aria-label="Previous page"
          />

          {getPageNumbers().map((p, idx) => {
            if (p === '...') {
              return (
                <span key={`ellipsis-${idx}`} style={{ padding: '0 6px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                  ...
                </span>
              );
            }

            const pageNum = Number(p);
            const isActive = currentPage === pageNum;

            return (
              <motion.button
                key={`page-${pageNum}`}
                layout
                onClick={() => onPageChange(pageNum)}
                whileTap={{ scale: reduced ? 1 : 0.94 }}
                whileHover={isActive ? undefined : { backgroundColor: 'var(--md-sys-color-surface-container-high)' }}
                transition={M3_TRANSITIONS.hover}
                aria-current={isActive ? 'page' : undefined}
                style={{
                  position: 'relative',
                  width: '32px',
                  height: '32px',
                  borderRadius: 'var(--radius-pill)',
                  border: 'none',
                  backgroundColor: 'transparent',
                  color: isActive ? 'var(--md-sys-color-on-primary)' : 'var(--md-sys-color-on-surface)',
                  fontWeight: isActive ? 700 : 500,
                  fontSize: '13px',
                  cursor: 'pointer',
                  fontFeatureSettings: '"tnum" 1',
                }}
              >
                {/* One shared pill, moved between buttons by layoutId — the
                    selection slides across the row (§19 nav selection). */}
                {isActive && (
                  <motion.span
                    layoutId="morphic-pagination-active"
                    transition={reduced ? { duration: 0 } : M3_SPRING.snappy}
                    style={{
                      position: 'absolute',
                      inset: 0,
                      borderRadius: 'var(--radius-pill)',
                      backgroundColor: 'var(--md-sys-color-primary)',
                    }}
                  />
                )}
                <span style={{ position: 'relative', zIndex: 1 }}>{pageNum}</span>
              </motion.button>
            );
          })}

          <IconButton
            variant="standard"
            icon={<Icon name="chevron_right" size={18} />}
            disabled={currentPage === totalPages}
            onClick={() => onPageChange(currentPage + 1)}
            aria-label="Next page"
          />
        </div>
      </LayoutGroup>
    </div>
  );
};
