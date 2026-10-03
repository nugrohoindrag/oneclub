/**
 * @license MIT
 * Breadcrumbs Component — Material Design 3 Navigation
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §19 Micro-interactions — hover tint 120–160ms, the trail reveals in order
 *   §18 Motion Design      — a crumb changing is a navigation event
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';
import { AnimatePresence, motion } from 'motion/react';
import { Icon } from '../communication/Icon.js';
import { M3_TRANSITIONS, staggerFor, useReducedMotionSafe } from '../../motion/index.js';

export interface BreadcrumbItem {
  id?: string;
  label: string;
  href?: string;
  icon?: string;
  onClick?: () => void;
}

export interface BreadcrumbsProps {
  items: BreadcrumbItem[];
  separator?: React.ReactNode;
  maxItems?: number;
  className?: string;
}

export const Breadcrumbs: React.FC<BreadcrumbsProps> = ({
  items,
  separator = <Icon name="chevron_right" size={16} color="var(--md-sys-color-on-surface-variant)" />,
  maxItems = 4,
  className = '',
}) => {
  const reduced = useReducedMotionSafe();

  const displayItems =
    items.length > maxItems
      ? [
          items[0],
          { label: '...', id: 'ellipsis' },
          ...items.slice(items.length - (maxItems - 2)),
        ]
      : items;

  const step = staggerFor(displayItems.length, 'tight');

  return (
    <nav
      aria-label="Breadcrumb"
      className={`morphic-breadcrumbs ${className}`}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '6px',
        flexWrap: 'wrap',
        fontSize: '13px',
      }}
    >
      {/* Crumbs animate as the trail changes: drilling in slides the new leaf
          from the right, stepping back removes it the same way (§18). */}
      <AnimatePresence initial mode="popLayout">
        {displayItems.map((item, index) => {
          const isLast = index === displayItems.length - 1;
          const key = item.id || `${item.label}-${index}`;

          const enter = {
            initial: { opacity: 0, x: reduced ? 0 : -4 },
            animate: { opacity: 1, x: 0, transition: { ...M3_TRANSITIONS.enter, delay: index * step } },
            exit: { opacity: 0, x: reduced ? 0 : -4, transition: M3_TRANSITIONS.exit },
          };

          return (
            <motion.span
              key={key}
              layout
              {...enter}
              transition={M3_TRANSITIONS.layout}
              style={{ display: 'flex', alignItems: 'center', gap: '6px' }}
            >
              {index > 0 && (
                <span style={{ display: 'flex', alignItems: 'center', opacity: 0.7 }}>{separator}</span>
              )}

              {isLast ? (
                <span
                  style={{
                    fontWeight: 700,
                    color: 'var(--md-sys-color-on-surface)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '4px',
                  }}
                  aria-current="page"
                >
                  {item.icon && <Icon name={item.icon} size={16} />}
                  <span>{item.label}</span>
                </span>
              ) : item.label === '...' ? (
                <span style={{ color: 'var(--md-sys-color-on-surface-variant)' }}>...</span>
              ) : (
                /* The hover tint moves through Motion rather than through two
                   imperative mouse handlers — same 120–160ms as every other
                   hover in the system, and it survives keyboard focus. */
                <motion.a
                  href={item.href || '#'}
                  onClick={(e) => {
                    if (item.onClick) {
                      e.preventDefault();
                      item.onClick();
                    }
                  }}
                  initial={false}
                  whileHover={{ color: 'var(--md-sys-color-primary)' }}
                  whileFocus={{ color: 'var(--md-sys-color-primary)' }}
                  transition={M3_TRANSITIONS.hover}
                  style={{
                    color: 'var(--md-sys-color-on-surface-variant)',
                    textDecoration: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '4px',
                    fontWeight: 500,
                  }}
                >
                  {item.icon && <Icon name={item.icon} size={16} />}
                  <span>{item.label}</span>
                </motion.a>
              )}
            </motion.span>
          );
        })}
      </AnimatePresence>
    </nav>
  );
};
