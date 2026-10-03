/**
 * @license MIT
 * ButtonGroup Component — Morphic Design System
 * 
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';

export interface ButtonGroupProps {
  children: React.ReactNode;
  variant?: 'attached' | 'spaced';
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}

export const ButtonGroup: React.FC<ButtonGroupProps> = ({
  children,
  variant = 'attached',
  size = 'md',
  className = '',
}) => {
  if (variant === 'spaced') {
    return (
      <div
        className={`morphic-button-group-spaced ${className}`}
        style={{ display: 'inline-flex', alignItems: 'center', gap: '8px' }}
      >
        {children}
      </div>
    );
  }

  return (
    <div
      className={`morphic-button-group-attached ${className}`}
      style={{
        display: 'inline-flex',
        borderRadius: 'var(--radius-pill)',
        overflow: 'hidden',
        border: '1px solid var(--md-sys-color-border)',
      }}
    >
      {React.Children.map(children, (child, index) => {
        // React 19 types `isValidElement` props as `unknown`; narrow once so `style` reads on 18 and 19 alike.
        if (!React.isValidElement<{ style?: React.CSSProperties }>(child)) return child;
        return React.cloneElement(child, {
          style: {
            borderRadius: '0',
            border: 'none',
            borderRight: index < React.Children.count(children) - 1 ? '1px solid var(--md-sys-color-border)' : 'none',
            ...(child.props.style || {}),
          },
        });
      })}
    </div>
  );
};
