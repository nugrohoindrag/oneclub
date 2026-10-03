/**
 * @license MIT
 * 02. Layout Components — Morphic Design System
 * 
 * Includes: Container, Stack, Row, Grid, SplitLayout, AppShell, Section
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React from 'react';

// 1. Container
export interface ContainerProps extends React.HTMLAttributes<HTMLDivElement> {
  maxWidth?: 'sm' | 'md' | 'lg' | 'xl' | '2xl' | 'full';
  padding?: 'none' | 'sm' | 'md' | 'lg';
  children: React.ReactNode;
}

export const Container: React.FC<ContainerProps> = ({
  maxWidth = 'xl',
  padding = 'md',
  children,
  style,
  className = '',
  ...props
}) => {
  const maxWidthMap = {
    sm: '640px',
    md: '768px',
    lg: '1024px',
    xl: '1280px',
    '2xl': '1440px',
    full: '100%',
  };

  const paddingMap = {
    none: '0',
    sm: '12px 16px',
    md: '24px 32px',
    lg: '36px 48px',
  };

  return (
    <div
      className={`morphic-container ${className}`}
      style={{
        width: '100%',
        maxWidth: maxWidthMap[maxWidth],
        margin: '0 auto',
        padding: paddingMap[padding],
        boxSizing: 'border-box',
        ...style,
      }}
      {...props}
    >
      {children}
    </div>
  );
};

// 2. Stack (Vertical Flex Layout)
export interface StackProps extends React.HTMLAttributes<HTMLDivElement> {
  gap?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' | number;
  align?: 'flex-start' | 'center' | 'flex-end' | 'stretch';
  justify?: 'flex-start' | 'center' | 'flex-end' | 'space-between';
  children: React.ReactNode;
}

export const Stack: React.FC<StackProps> = ({
  gap = 'md',
  align = 'stretch',
  justify = 'flex-start',
  children,
  style,
  className = '',
  ...props
}) => {
  const gapMap = {
    xs: '4px',
    sm: '8px',
    md: '16px',
    lg: '24px',
    xl: '32px',
  };

  const gapValue = typeof gap === 'number' ? `${gap}px` : gapMap[gap];

  return (
    <div
      className={`morphic-stack ${className}`}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: gapValue,
        alignItems: align,
        justifyContent: justify,
        ...style,
      }}
      {...props}
    >
      {children}
    </div>
  );
};

// 3. Row (Horizontal Flex Layout)
export interface RowProps extends React.HTMLAttributes<HTMLDivElement> {
  gap?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' | number;
  align?: 'flex-start' | 'center' | 'flex-end' | 'stretch' | 'baseline';
  justify?: 'flex-start' | 'center' | 'flex-end' | 'space-between' | 'space-around';
  wrap?: boolean;
  children: React.ReactNode;
}

export const Row: React.FC<RowProps> = ({
  gap = 'md',
  align = 'center',
  justify = 'flex-start',
  wrap = false,
  children,
  style,
  className = '',
  ...props
}) => {
  const gapMap = {
    xs: '4px',
    sm: '8px',
    md: '16px',
    lg: '24px',
    xl: '32px',
  };

  const gapValue = typeof gap === 'number' ? `${gap}px` : gapMap[gap];

  return (
    <div
      className={`morphic-row ${className}`}
      style={{
        display: 'flex',
        flexDirection: 'row',
        gap: gapValue,
        alignItems: align,
        justifyContent: justify,
        flexWrap: wrap ? 'wrap' : 'nowrap',
        ...style,
      }}
      {...props}
    >
      {children}
    </div>
  );
};

// 4. Grid (CSS Grid Wrapper)
export interface GridProps extends React.HTMLAttributes<HTMLDivElement> {
  columns?: number | string;
  gap?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' | number;
  minColWidth?: string;
  children: React.ReactNode;
}

export const Grid: React.FC<GridProps> = ({
  columns = 3,
  gap = 'md',
  minColWidth,
  children,
  style,
  className = '',
  ...props
}) => {
  const gapMap = {
    xs: '4px',
    sm: '8px',
    md: '16px',
    lg: '24px',
    xl: '32px',
  };

  const gapValue = typeof gap === 'number' ? `${gap}px` : gapMap[gap];

  const gridTemplate = minColWidth
    ? `repeat(auto-fill, minmax(${minColWidth}, 1fr))`
    : typeof columns === 'number'
    ? `repeat(${columns}, 1fr)`
    : columns;

  return (
    <div
      className={`morphic-grid ${className}`}
      data-cols={typeof columns === 'number' ? columns : undefined}
      style={{
        display: 'grid',
        gridTemplateColumns: gridTemplate,
        gap: gapValue,
        ...style,
      }}
      {...props}
    >
      {children}
    </div>
  );
};

// 5. SplitLayout (Two-column split view)
export interface SplitLayoutProps {
  primary: React.ReactNode;
  secondary: React.ReactNode;
  ratio?: '50/50' | '60/40' | '70/30' | '30/70' | '40/60';
  gap?: 'sm' | 'md' | 'lg';
  reverseOnMobile?: boolean;
  className?: string;
}

export const SplitLayout: React.FC<SplitLayoutProps> = ({
  primary,
  secondary,
  ratio = '60/40',
  gap = 'md',
  className = '',
}) => {
  const ratioMap = {
    '50/50': '1fr 1fr',
    '60/40': '1.5fr 1fr',
    '70/30': '2.3fr 1fr',
    '30/70': '1fr 2.3fr',
    '40/60': '1fr 1.5fr',
  };

  const gapMap = {
    sm: '12px',
    md: '20px',
    lg: '32px',
  };

  return (
    <div
      className={`morphic-split-layout ${className}`}
      style={{
        display: 'grid',
        gridTemplateColumns: ratioMap[ratio],
        gap: gapMap[gap],
        alignItems: 'start',
      }}
    >
      <div>{primary}</div>
      <div>{secondary}</div>
    </div>
  );
};

// 6. AppShell (Top Bar, Sidebar, Main Content, Footer)
export interface AppShellProps {
  header?: React.ReactNode;
  sidebar?: React.ReactNode;
  footer?: React.ReactNode;
  children: React.ReactNode;
  sidebarWidth?: string;
}

export const AppShell: React.FC<AppShellProps> = ({
  header,
  sidebar,
  footer,
  children,
  sidebarWidth = '260px',
}) => {
  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        flexDirection: 'column',
        backgroundColor: 'var(--md-sys-color-background)',
        color: 'var(--md-sys-color-on-background)',
      }}
    >
      {header && <header style={{ position: 'sticky', top: 0, zIndex: 100 }}>{header}</header>}

      <div style={{ display: 'flex', flex: 1 }}>
        {sidebar && (
          <aside
            style={{
              width: sidebarWidth,
              borderRight: '1px solid var(--md-sys-color-border)',
              backgroundColor: 'var(--md-sys-color-surface)',
              position: 'sticky',
              top: '64px',
              height: 'calc(100vh - 64px)',
              overflowY: 'auto',
              flexShrink: 0,
            }}
          >
            {sidebar}
          </aside>
        )}

        <main style={{ flex: 1, padding: '32px 40px', overflowX: 'hidden' }}>{children}</main>
      </div>

      {footer && <footer>{footer}</footer>}
    </div>
  );
};

// 7. Section (Structured Content Block)
export interface SectionProps extends React.HTMLAttributes<HTMLElement> {
  title?: string;
  subtitle?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}

export const Section: React.FC<SectionProps> = ({
  title,
  subtitle,
  action,
  children,
  style,
  className = '',
  ...props
}) => {
  return (
    <section
      className={`morphic-section ${className}`}
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: '20px',
        ...style,
      }}
      {...props}
    >
      {(title || action) && (
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '12px' }}>
          <div>
            {title && <h2 style={{ margin: '0 0 4px', fontSize: '20px', fontWeight: 700, letterSpacing: '-0.01em' }}>{title}</h2>}
            {subtitle && <p style={{ margin: 0, color: 'var(--md-sys-color-on-surface-variant)', fontSize: '13px' }}>{subtitle}</p>}
          </div>
          {action && <div>{action}</div>}
        </div>
      )}
      {children}
    </section>
  );
};
