/**
 * @license Apache-2.0
 * Icon — Powered by Google Material Symbols
 * Copyright Google LLC
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §28 Iconography — Material Symbols Rounded is THE icon family. Do not mix
 *       in Font Awesome, Lucide, Heroicons, or ad-hoc SVG packs. `rounded` is
 *       the default; `outlined` exists only for a specific product requirement.
 *       Sizes: 18–20 navigation · 16–18 compact actions · 20–24 cards.
 *   §9  Accessibility — an icon is decorative by default (aria-hidden). Pass
 *       `label` when the icon IS the content, e.g. inside an icon-only button;
 *       it then exposes role="img" with an accessible name.
 *
 * Morphic Design System wrapper component licensed under MIT.
 */

import React from 'react';

export interface IconProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** Official Google Material Symbols name, e.g. 'search', 'home', 'settings'. */
  name: string;
  variant?: 'rounded' | 'outlined';
  filled?: boolean;
  size?: number | string; // e.g. 20, 24, 40, 48
  weight?: 100 | 200 | 300 | 400 | 500 | 600 | 700;
  grade?: -25 | 0 | 200;
  opticalSize?: 20 | 24 | 40 | 48;
  color?: string;
  /**
   * Accessible name (§9). Omit for decorative icons that sit beside a text
   * label; provide it when the icon carries the meaning on its own.
   */
  label?: string;
}

export const Icon: React.FC<IconProps> = ({
  name,
  variant = 'rounded',
  filled = false,
  size = 24,
  weight = 400,
  grade = 0,
  opticalSize = 24,
  color = 'currentColor',
  label,
  className = '',
  style,
  ...props
}) => {
  const fontClass = variant === 'rounded' ? 'material-symbols-rounded' : 'material-symbols-outlined';
  const sizePx = typeof size === 'number' ? `${size}px` : size;

  return (
    <span
      className={`${fontClass} ${className}`}
      style={{
        fontSize: sizePx,
        width: sizePx,
        height: sizePx,
        color,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        userSelect: 'none',
        fontVariationSettings: `'FILL' ${filled ? 1 : 0}, 'wght' ${weight}, 'GRAD' ${grade}, 'opsz' ${opticalSize}`,
        ...style,
      }}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      {...props}
    >
      {name}
    </span>
  );
};
