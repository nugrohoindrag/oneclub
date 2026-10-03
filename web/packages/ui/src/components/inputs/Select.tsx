/**
 * @license MIT
 * Select & MultiSelect Components — Material Design 3 Inputs
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useState, useRef, useEffect } from 'react';
import { motion, AnimatePresence } from 'motion/react';
import { Icon } from '../communication/Icon.js';

export interface SelectOption {
  value: string;
  label: string;
  icon?: string;
  disabled?: boolean;
}

export interface SelectProps {
  label: string;
  options: SelectOption[];
  value?: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  error?: string;
  supportingText?: string;
  searchable?: boolean;
  className?: string;
}

export const Select: React.FC<SelectProps> = ({
  label,
  options,
  value,
  onChange,
  placeholder = 'Pilih salah satu...',
  disabled = false,
  error,
  supportingText,
  searchable = false,
  className = '',
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const [filterQuery, setFilterQuery] = useState('');
  const containerRef = useRef<HTMLDivElement>(null);

  const selectedOption = options.find((o) => o.value === value);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const filteredOptions = searchable
    ? options.filter((o) => o.label.toLowerCase().includes(filterQuery.toLowerCase()))
    : options;

  return (
    <div
      ref={containerRef}
      className={`morphic-select-container ${className}`}
      style={{ position: 'relative', width: '100%', zIndex: isOpen ? 50 : 1 }}
    >
      {/* Input Field Surface */}
      <div
        onClick={() => !disabled && setIsOpen((prev) => !prev)}
        style={{
          minHeight: '56px',
          padding: '8px 16px',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          border: `1px solid ${error ? 'var(--md-sys-color-error)' : isOpen ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-border)'}`,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          cursor: disabled ? 'not-allowed' : 'pointer',
          opacity: disabled ? 0.38 : 1,
          transition: 'all 0.15s ease',
        }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', width: '100%' }}>
          <span
            style={{
              fontSize: '11px',
              fontWeight: 600,
              color: error ? 'var(--md-sys-color-error)' : isOpen ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)',
            }}
          >
            {label}
          </span>
          <span
            style={{
              fontSize: '14px',
              color: selectedOption ? 'var(--md-sys-color-on-surface)' : 'var(--md-sys-color-on-surface-variant)',
              marginTop: '2px',
            }}
          >
            {selectedOption ? selectedOption.label : placeholder}
          </span>
        </div>

        <Icon
          name={isOpen ? 'arrow_drop_up' : 'arrow_drop_down'}
          size={22}
          color="var(--md-sys-color-on-surface-variant)"
        />
      </div>

      {/* Supporting Text / Error */}
      {(error || supportingText) && (
        <div
          style={{
            fontSize: '11px',
            color: error ? 'var(--md-sys-color-error)' : 'var(--md-sys-color-on-surface-variant)',
            marginTop: '4px',
            paddingLeft: '4px',
          }}
        >
          {error || supportingText}
        </div>
      )}

      {/* Dropdown Popover */}
      <AnimatePresence>
        {isOpen && (
          <motion.div
            initial={{ opacity: 0, y: 6, scale: 0.98 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 4, scale: 0.98 }}
            transition={{ duration: 0.15 }}
            style={{
              position: 'absolute',
              top: 'calc(100% + 4px)',
              left: 0,
              right: 0,
              maxHeight: '260px',
              overflowY: 'auto',
              borderRadius: 'var(--radius-lg)',
              backgroundColor: 'var(--md-sys-color-surface)',
              border: '1px solid var(--md-sys-color-border)',
              boxShadow: 'var(--md-sys-elevation-level3)',
              padding: '6px',
              zIndex: 990,
              display: 'flex',
              flexDirection: 'column',
              gap: '2px',
            }}
          >
            {searchable && (
              <div style={{ padding: '6px 8px', borderBottom: '1px solid var(--md-sys-color-border)', marginBottom: '4px' }}>
                <input
                  type="text"
                  placeholder="Cari opsi..."
                  value={filterQuery}
                  onChange={(e) => setFilterQuery(e.target.value)}
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    width: '100%',
                    padding: '6px 8px',
                    borderRadius: 'var(--radius-sm)',
                    border: '1px solid var(--md-sys-color-border)',
                    outline: 'none',
                    backgroundColor: 'var(--md-sys-color-surface-container)',
                    color: 'var(--md-sys-color-on-surface)',
                    fontSize: '12px',
                  }}
                />
              </div>
            )}

            {filteredOptions.map((opt) => {
              const isSelected = opt.value === value;
              return (
                <button
                  key={opt.value}
                  disabled={opt.disabled}
                  onClick={() => {
                    onChange(opt.value);
                    setIsOpen(false);
                  }}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    padding: '10px 12px',
                    borderRadius: 'var(--radius-sm)',
                    backgroundColor: isSelected ? 'var(--md-sys-color-primary-container)' : 'transparent',
                    color: isSelected ? 'var(--md-sys-color-on-primary-container)' : 'inherit',
                    border: 'none',
                    fontSize: '13px',
                    fontWeight: isSelected ? 700 : 500,
                    cursor: opt.disabled ? 'not-allowed' : 'pointer',
                    opacity: opt.disabled ? 0.4 : 1,
                    textAlign: 'left',
                    width: '100%',
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    {opt.icon && <Icon name={opt.icon} size={16} />}
                    <span>{opt.label}</span>
                  </div>
                  {isSelected && <Icon name="check" size={16} color="var(--md-sys-color-primary)" />}
                </button>
              );
            })}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};

export interface MultiSelectProps {
  label: string;
  options: SelectOption[];
  values: string[];
  onChange: (values: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
}

export const MultiSelect: React.FC<MultiSelectProps> = ({
  label,
  options,
  values,
  onChange,
  placeholder = 'Pilih beberapa opsi...',
  disabled = false,
  className = '',
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const toggleOption = (val: string) => {
    if (values.includes(val)) {
      onChange(values.filter((v) => v !== val));
    } else {
      onChange([...values, val]);
    }
  };

  const removeTag = (val: string, e: React.MouseEvent) => {
    e.stopPropagation();
    onChange(values.filter((v) => v !== val));
  };

  return (
    <div
      ref={containerRef}
      className={`morphic-multiselect-container ${className}`}
      style={{ position: 'relative', width: '100%', zIndex: isOpen ? 50 : 1 }}
    >
      <div
        onClick={() => !disabled && setIsOpen((prev) => !prev)}
        style={{
          minHeight: '56px',
          padding: '8px 16px',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          border: `1px solid ${isOpen ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-border)'}`,
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          cursor: disabled ? 'not-allowed' : 'pointer',
          opacity: disabled ? 0.38 : 1,
        }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: '4px', width: '100%' }}>
          <span style={{ fontSize: '11px', fontWeight: 600, color: isOpen ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-on-surface-variant)' }}>
            {label}
          </span>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px' }}>
            {values.length === 0 ? (
              <span style={{ fontSize: '13px', color: 'var(--md-sys-color-on-surface-variant)' }}>{placeholder}</span>
            ) : (
              values.map((v) => {
                const opt = options.find((o) => o.value === v);
                return (
                  <span
                    key={v}
                    style={{
                      padding: '2px 8px',
                      borderRadius: 'var(--radius-pill)',
                      backgroundColor: 'var(--md-sys-color-primary-container)',
                      color: 'var(--md-sys-color-on-primary-container)',
                      fontSize: '11px',
                      fontWeight: 600,
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: '4px',
                    }}
                  >
                    <span>{opt ? opt.label : v}</span>
                    <span
                      onClick={(e) => removeTag(v, e)}
                      style={{ cursor: 'pointer', display: 'flex', alignItems: 'center' }}
                    >
                      <Icon name="close" size={12} />
                    </span>
                  </span>
                );
              })
            )}
          </div>
        </div>

        <Icon name={isOpen ? 'arrow_drop_up' : 'arrow_drop_down'} size={22} color="var(--md-sys-color-on-surface-variant)" />
      </div>

      <AnimatePresence>
        {isOpen && (
          <motion.div
            initial={{ opacity: 0, y: 6, scale: 0.98 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: 4, scale: 0.98 }}
            style={{
              position: 'absolute',
              top: 'calc(100% + 4px)',
              left: 0,
              right: 0,
              maxHeight: '240px',
              overflowY: 'auto',
              borderRadius: 'var(--radius-lg)',
              backgroundColor: 'var(--md-sys-color-surface)',
              border: '1px solid var(--md-sys-color-border)',
              boxShadow: 'var(--md-sys-elevation-level3)',
              padding: '6px',
              zIndex: 990,
            }}
          >
            {options.map((opt) => {
              const isChecked = values.includes(opt.value);
              return (
                <div
                  key={opt.value}
                  onClick={() => toggleOption(opt.value)}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    padding: '8px 12px',
                    borderRadius: 'var(--radius-sm)',
                    backgroundColor: isChecked ? 'var(--md-sys-color-surface-container)' : 'transparent',
                    cursor: 'pointer',
                    fontSize: '13px',
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    <div
                      style={{
                        width: '18px',
                        height: '18px',
                        borderRadius: 'var(--radius-pill)',
                        border: `2px solid ${isChecked ? 'var(--md-sys-color-primary)' : 'var(--md-sys-color-outline)'}`,
                        backgroundColor: isChecked ? 'var(--md-sys-color-primary)' : 'transparent',
                        color: 'var(--md-sys-color-on-media)',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                      }}
                    >
                      {isChecked && <Icon name="check" size={12} />}
                    </div>
                    <span>{opt.label}</span>
                  </div>
                </div>
              );
            })}
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  );
};
