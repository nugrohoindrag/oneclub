/**
 * @license MIT
 * DateRangePicker & ToggleGroup Components — Material Design 3 Selection
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { IconButton } from '../actions/IconButton.js';

export interface DateRangePickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  startDate?: Date;
  endDate?: Date;
  onSelectRange?: (start: Date, end: Date) => void;
}

export const DateRangePickerModal: React.FC<DateRangePickerModalProps> = ({
  isOpen,
  onClose,
  startDate = new Date(2026, 7, 1),
  endDate = new Date(2026, 7, 27),
  onSelectRange,
}) => {
  const [startDay, setStartDay] = useState<number | null>(startDate.getDate());
  const [endDay, setEndDay] = useState<number | null>(endDate.getDate());
  const [currentMonth, setCurrentMonth] = useState(7); // August
  const [currentYear, setCurrentYear] = useState(2026);

  const monthNames = [
    'January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December'
  ];

  const daysInMonth = new Date(currentYear, currentMonth + 1, 0).getDate();
  const firstDayIndex = new Date(currentYear, currentMonth, 1).getDay();

  const handleDayClick = (day: number) => {
    if (!startDay || (startDay && endDay)) {
      setStartDay(day);
      setEndDay(null);
    } else if (startDay && !endDay) {
      if (day < startDay) {
        setStartDay(day);
        setEndDay(null);
      } else {
        setEndDay(day);
      }
    }
  };

  return (
    <AnimatePresence>
      {isOpen && (
        <>
          <motion.div
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            onClick={onClose}
            style={{
              position: 'fixed',
              inset: 0,
              backgroundColor: 'var(--md-sys-color-scrim)',
              zIndex: 998,
              backdropFilter: 'blur(3px)',
            }}
          />

          <motion.div
            initial={{ scale: 0.9, opacity: 0, x: '-50%', y: '-50%' }}
            animate={{ scale: 1, opacity: 1, x: '-50%', y: '-50%' }}
            exit={{ scale: 0.92, opacity: 0, x: '-50%', y: '-50%' }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              width: '380px',
              borderRadius: 'var(--radius-hero)', // 28px
              backgroundColor: 'var(--md-sys-color-surface)',
              color: 'var(--md-sys-color-on-surface)',
              padding: '24px 28px',
              boxShadow: 'var(--md-sys-elevation-level3)',
              zIndex: 999,
              display: 'flex',
              flexDirection: 'column',
              gap: '16px',
              border: '1px solid var(--md-sys-color-border)',
            }}
          >
            {/* Header: Range Overview */}
            <div style={{ borderBottom: '1px solid var(--md-sys-color-border)', paddingBottom: '14px' }}>
              <div style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                Select Date Range
              </div>
              <div style={{ fontSize: '20px', fontWeight: 700, color: 'var(--md-sys-color-primary)', marginTop: '2px', fontFeatureSettings: '"tnum" 1' }}>
                {startDay ? `${monthNames[currentMonth].slice(0, 3)} ${startDay}, ${currentYear}` : 'Start'} — {endDay ? `${monthNames[currentMonth].slice(0, 3)} ${endDay}, ${currentYear}` : 'End'}
              </div>
            </div>

            {/* Month Nav */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span style={{ fontSize: '14px', fontWeight: 700 }}>
                {monthNames[currentMonth]} {currentYear}
              </span>
              <div style={{ display: 'flex', gap: '4px' }}>
                <IconButton variant="standard" icon={<Icon name="chevron_left" size={20} />} onClick={() => setCurrentMonth((m) => Math.max(0, m - 1))} />
                <IconButton variant="standard" icon={<Icon name="chevron_right" size={20} />} onClick={() => setCurrentMonth((m) => Math.min(11, m + 1))} />
              </div>
            </div>

            {/* Calendar Grid */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: '4px', textAlign: 'center' }}>
              {['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map((d, i) => (
                <span key={i} style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
                  {d}
                </span>
              ))}

              {Array.from({ length: firstDayIndex }).map((_, i) => (
                <div key={`empty-range-${i}`} />
              ))}

              {Array.from({ length: daysInMonth }).map((_, idx) => {
                const dayNum = idx + 1;
                const isStart = dayNum === startDay;
                const isEnd = dayNum === endDay;
                const isInRange = startDay && endDay && dayNum > startDay && dayNum < endDay;

                return (
                  <button
                    key={dayNum}
                    onClick={() => handleDayClick(dayNum)}
                    style={{
                      height: '36px',
                      borderRadius: isStart || isEnd ? 'var(--radius-pill)' : isInRange ? '0' : 'var(--radius-pill)',
                      border: 'none',
                      backgroundColor: isStart || isEnd
                        ? 'var(--md-sys-color-primary)'
                        : isInRange
                        ? 'var(--md-sys-color-primary-container)'
                        : 'transparent',
                      color: isStart || isEnd
                        ? 'var(--md-sys-color-on-primary)'
                        : isInRange
                        ? 'var(--md-sys-color-on-primary-container)'
                        : 'var(--md-sys-color-on-surface)',
                      cursor: 'pointer',
                      fontWeight: isStart || isEnd ? 800 : isInRange ? 600 : 400,
                      fontSize: '13px',
                      fontFeatureSettings: '"tnum" 1',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                  >
                    {dayNum}
                  </button>
                );
              })}
            </div>

            {/* Actions */}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px', marginTop: '8px', paddingTop: '12px', borderTop: '1px solid var(--md-sys-color-border)' }}>
              <Button variant="text" onClick={onClose}>Cancel</Button>
              <Button
                variant="filled"
                disabled={!startDay || !endDay}
                onClick={() => {
                  if (startDay && endDay) {
                    onSelectRange?.(new Date(currentYear, currentMonth, startDay), new Date(currentYear, currentMonth, endDay));
                    onClose();
                  }
                }}
              >
                Apply Range
              </Button>
            </div>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};

export interface ToggleGroupItem {
  value: string;
  label?: string;
  icon?: string;
}

export interface ToggleGroupProps {
  items: ToggleGroupItem[];
  value: string | string[];
  onChange: (value: any) => void;
  multiple?: boolean;
  size?: 'sm' | 'md';
  className?: string;
}

export const ToggleGroup: React.FC<ToggleGroupProps> = ({
  items,
  value,
  onChange,
  multiple = false,
  size = 'md',
  className = '',
}) => {
  const handleClick = (itemVal: string) => {
    if (multiple) {
      const arr = Array.isArray(value) ? value : [value];
      if (arr.includes(itemVal)) {
        onChange(arr.filter((v) => v !== itemVal));
      } else {
        onChange([...arr, itemVal]);
      }
    } else {
      onChange(itemVal);
    }
  };

  const isSelected = (itemVal: string) => {
    if (multiple) {
      return Array.isArray(value) && value.includes(itemVal);
    }
    return value === itemVal;
  };

  return (
    <div
      className={`morphic-toggle-group ${className}`}
      style={{
        display: 'inline-flex',
        borderRadius: 'var(--radius-pill)',
        padding: '3px',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        border: '1px solid var(--md-sys-color-border)',
        gap: '2px',
      }}
    >
      {items.map((item) => {
        const active = isSelected(item.value);
        return (
          <button
            key={item.value}
            onClick={() => handleClick(item.value)}
            style={{
              padding: size === 'sm' ? '4px 10px' : '6px 14px',
              borderRadius: 'var(--radius-pill)',
              border: 'none',
              backgroundColor: active ? 'var(--md-sys-color-surface)' : 'transparent',
              color: active ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
              fontWeight: active ? 700 : 500,
              fontSize: size === 'sm' ? '12px' : '13px',
              cursor: 'pointer',
              display: 'inline-flex',
              alignItems: 'center',
              gap: '6px',
              boxShadow: active ? 'var(--md-sys-elevation-level1)' : 'none',
              transition: 'all 0.15s ease',
            }}
          >
            {item.icon && <Icon name={item.icon} size={size === 'sm' ? 14 : 16} />}
            {item.label && <span>{item.label}</span>}
          </button>
        );
      })}
    </div>
  );
};
