/**
 * @license MIT
 * Material Design 3 DatePicker Suite — Morphic Design System
 * Official Standards: https://m3.material.io/components/date-pickers/overview
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { M3_TRANSITIONS } from '../../motion/index.js';
import { Icon } from '../communication/Icon.js';
import { Button } from '../actions/Button.js';
import { IconButton } from '../actions/IconButton.js';

export interface DatePickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  selectedDate?: Date;
  onSelect?: (date: Date) => void;
}

export const DatePickerModal: React.FC<DatePickerModalProps> = ({
  isOpen,
  onClose,
  selectedDate = new Date(2026, 7, 27),
  onSelect,
}) => {
  const [currentMonth, setCurrentMonth] = useState(7); // August (0-indexed)
  const [currentYear, setCurrentYear] = useState(2026);
  const [activeDay, setActiveDay] = useState(selectedDate.getDate());

  const monthNames = [
    'January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December'
  ];

  const daysInMonth = new Date(currentYear, currentMonth + 1, 0).getDate();
  const firstDayIndex = new Date(currentYear, currentMonth, 1).getDay();

  const handlePrevMonth = () => {
    if (currentMonth === 0) {
      setCurrentMonth(11);
      setCurrentYear((y) => y - 1);
    } else {
      setCurrentMonth((m) => m - 1);
    }
  };

  const handleNextMonth = () => {
    if (currentMonth === 11) {
      setCurrentMonth(0);
      setCurrentYear((y) => y + 1);
    } else {
      setCurrentMonth((m) => m + 1);
    }
  };

  return (
    <AnimatePresence>
      {isOpen && (
        <>
          {/* Backdrop Scrim */}
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

          {/* Modal Container (Official M3 28px Radius & Surface-Container-High) */}
          <motion.div
            initial={{ scale: 0.9, opacity: 0, x: '-50%', y: '-50%' }}
            animate={{ scale: 1, opacity: 1, x: '-50%', y: '-50%' }}
            exit={{ scale: 0.92, opacity: 0, x: '-50%', y: '-50%' }}
            transition={M3_TRANSITIONS.enter}
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              width: '360px',
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
            {/* Header: Date Selection Overview */}
            <div style={{ borderBottom: '1px solid var(--md-sys-color-border)', paddingBottom: '14px' }}>
              <div style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                Select Date
              </div>
              <div style={{ fontSize: '26px', fontWeight: 700, color: 'var(--md-sys-color-primary)', marginTop: '2px' }}>
                {monthNames[currentMonth]} {activeDay}, {currentYear}
              </div>
            </div>

            {/* Month & Year Navigation */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0 4px' }}>
              <span style={{ fontSize: '14px', fontWeight: 700 }}>
                {monthNames[currentMonth]} {currentYear}
              </span>
              <div style={{ display: 'flex', gap: '4px' }}>
                <IconButton variant="standard" icon={<Icon name="chevron_left" size={20} />} onClick={handlePrevMonth} />
                <IconButton variant="standard" icon={<Icon name="chevron_right" size={20} />} onClick={handleNextMonth} />
              </div>
            </div>

            {/* Days of Week Row */}
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: '4px', textAlign: 'center' }}>
              {['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map((d, i) => (
                <span key={i} style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
                  {d}
                </span>
              ))}

              {/* Empty leading padding slots */}
              {Array.from({ length: firstDayIndex }).map((_, i) => (
                <div key={`empty-${i}`} />
              ))}

              {/* Day Number Buttons */}
              {Array.from({ length: daysInMonth }).map((_, idx) => {
                const dayNum = idx + 1;
                const isSelected = dayNum === activeDay;
                return (
                  <button
                    key={dayNum}
                    onClick={() => setActiveDay(dayNum)}
                    style={{
                      height: '36px',
                      borderRadius: 'var(--radius-pill)',
                      border: 'none',
                      backgroundColor: isSelected ? 'var(--md-sys-color-primary)' : 'transparent',
                      color: isSelected ? 'var(--md-sys-color-on-primary)' : 'var(--md-sys-color-on-surface)',
                      cursor: 'pointer',
                      fontWeight: isSelected ? 800 : 500,
                      fontSize: '13px',
                      fontFeatureSettings: '"tnum" 1',
                      transition: 'background-color 0.15s ease',
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

            {/* Actions: Cancel & Confirm */}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px', marginTop: '12px', paddingTop: '12px', borderTop: '1px solid var(--md-sys-color-border)' }}>
              <Button variant="text" onClick={onClose}>
                Cancel
              </Button>
              <Button
                variant="filled"
                onClick={() => {
                  onSelect?.(new Date(currentYear, currentMonth, activeDay));
                  onClose();
                }}
              >
                Apply
              </Button>
            </div>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
};

export const DatePickerInline: React.FC<{
  value?: Date;
  onChange?: (date: Date) => void;
}> = ({ value = new Date(2026, 7, 27), onChange }) => {
  const [currentMonth, setCurrentMonth] = useState(7);
  const [currentYear, setCurrentYear] = useState(2026);
  const [activeDay, setActiveDay] = useState(value.getDate());

  const monthNames = [
    'January', 'February', 'March', 'April', 'May', 'June',
    'July', 'August', 'September', 'October', 'November', 'December'
  ];

  const daysInMonth = new Date(currentYear, currentMonth + 1, 0).getDate();
  const firstDayIndex = new Date(currentYear, currentMonth, 1).getDay();

  return (
    <div
      style={{
        borderRadius: 'var(--radius-xl)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        padding: '20px 24px',
        boxShadow: 'var(--md-sys-elevation-level1)',
        maxWidth: '340px',
      }}
    >
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
        <span style={{ fontSize: '14px', fontWeight: 700 }}>
          {monthNames[currentMonth]} {currentYear}
        </span>
        <div style={{ display: 'flex', gap: '4px' }}>
          <IconButton variant="standard" icon={<Icon name="chevron_left" size={18} />} onClick={() => setCurrentMonth((m) => Math.max(0, m - 1))} />
          <IconButton variant="standard" icon={<Icon name="chevron_right" size={18} />} onClick={() => setCurrentMonth((m) => Math.min(11, m + 1))} />
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: '4px', textAlign: 'center' }}>
        {['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map((d, i) => (
          <span key={i} style={{ fontSize: '11px', fontWeight: 600, color: 'var(--md-sys-color-on-surface-variant)' }}>
            {d}
          </span>
        ))}

        {Array.from({ length: firstDayIndex }).map((_, i) => (
          <div key={`empty-inline-${i}`} />
        ))}

        {Array.from({ length: daysInMonth }).map((_, idx) => {
          const dayNum = idx + 1;
          const isSelected = dayNum === activeDay;
          return (
            <button
              key={dayNum}
              onClick={() => {
                setActiveDay(dayNum);
                onChange?.(new Date(currentYear, currentMonth, dayNum));
              }}
              style={{
                height: '34px',
                borderRadius: 'var(--radius-pill)',
                border: 'none',
                backgroundColor: isSelected ? 'var(--md-sys-color-primary)' : 'transparent',
                color: isSelected ? 'var(--md-sys-color-on-primary)' : 'var(--md-sys-color-on-surface)',
                cursor: 'pointer',
                fontWeight: isSelected ? 800 : 500,
                fontSize: '12px',
                fontFeatureSettings: '"tnum" 1',
                transition: 'background-color 0.15s ease',
              }}
            >
              {dayNum}
            </button>
          );
        })}
      </div>
    </div>
  );
};
