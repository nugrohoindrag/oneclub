/**
 * @license MIT
 * Morphic Design System — Design Token Contract (TypeScript)
 *
 * Spec: Morphic-Design-System-adjusted.md §8 "Design Token Contract"
 *
 *   "All components must reference tokens rather than raw values. This makes
 *    the system themeable, scalable, maintainable, AI-readable and
 *    framework-independent."
 *
 * Two kinds of export live here:
 *
 *   `*_VAR`   — CSS custom-property references. Use these in `style={{}}`, so
 *               the active theme (themes.css) resolves the actual value at
 *               runtime. A component must never read a colour any other way.
 *
 *   plain     — Numeric/literal values for code that genuinely needs a number:
 *               GSAP durations, media-query logic, canvas-based charts.
 *               These MIRROR foundations.css; change both together.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

/* ========================================================================= */
/* COLOUR (§7 Theme Token Groups)                                            */
/* Values are theme-dependent, so only the reference is exported.            */
/* ========================================================================= */

export const COLOR_VAR = {
  primary: 'var(--md-sys-color-primary)',
  onPrimary: 'var(--md-sys-color-on-primary)',
  primaryContainer: 'var(--md-sys-color-primary-container)',
  onPrimaryContainer: 'var(--md-sys-color-on-primary-container)',
  primarySoft: 'var(--md-sys-color-primary-soft)',

  secondary: 'var(--md-sys-color-secondary)',
  secondaryContainer: 'var(--md-sys-color-secondary-container)',
  onSecondaryContainer: 'var(--md-sys-color-on-secondary-container)',

  tertiary: 'var(--md-sys-color-tertiary)',
  tertiaryContainer: 'var(--md-sys-color-tertiary-container)',
  onTertiaryContainer: 'var(--md-sys-color-on-tertiary-container)',

  background: 'var(--md-sys-color-background)',
  onBackground: 'var(--md-sys-color-on-background)',
  surface: 'var(--md-sys-color-surface)',
  onSurface: 'var(--md-sys-color-on-surface)',
  onSurfaceVariant: 'var(--md-sys-color-on-surface-variant)',
  surfaceVariant: 'var(--md-sys-color-surface-variant)',

  surfaceContainerLowest: 'var(--md-sys-color-surface-container-lowest)',
  surfaceContainerLow: 'var(--md-sys-color-surface-container-low)',
  surfaceContainer: 'var(--md-sys-color-surface-container)',
  surfaceContainerHigh: 'var(--md-sys-color-surface-container-high)',
  surfaceContainerHighest: 'var(--md-sys-color-surface-container-highest)',

  outline: 'var(--md-sys-color-outline)',
  outlineVariant: 'var(--md-sys-color-outline-variant)',
  border: 'var(--md-sys-color-border)',

  success: 'var(--md-sys-color-success)',
  successContainer: 'var(--md-sys-color-success-container)',
  warning: 'var(--md-sys-color-warning)',
  warningContainer: 'var(--md-sys-color-warning-container)',
  error: 'var(--md-sys-color-error)',
  errorContainer: 'var(--md-sys-color-error-container)',
  info: 'var(--md-sys-color-info)',
  infoContainer: 'var(--md-sys-color-info-container)',

  inverseSurface: 'var(--md-sys-color-inverse-surface)',
  inverseOnSurface: 'var(--md-sys-color-inverse-on-surface)',
  inversePrimary: 'var(--md-sys-color-inverse-primary)',
  scrim: 'var(--md-sys-color-scrim)',
} as const;

/**
 * Data visualisation palette (§29).
 *
 * "Charts should generally use ONE dominant colour and a small semantic
 *  palette." Reach for CHART_VAR.primary first; only add another entry when a
 *  series carries a different *meaning*, never to make a chart more colourful
 *  (§32 forbids rainbow charts).
 */
export const CHART_VAR = {
  primary: 'var(--md-sys-color-chart-primary)',
  secondary: 'var(--md-sys-color-chart-secondary)',
  tertiary: 'var(--md-sys-color-chart-tertiary)',
  quaternary: 'var(--md-sys-color-chart-quaternary)',
  neutral: 'var(--md-sys-color-chart-neutral)',
  grid: 'var(--md-sys-color-chart-grid)',
  axis: 'var(--md-sys-color-chart-axis)',
  areaFrom: 'var(--md-sys-color-chart-area-from)',
  areaTo: 'var(--md-sys-color-chart-area-to)',
} as const;

/** Ordered series palette. Slice it — do not extend it with ad-hoc colours. */
export const CHART_SERIES = [
  CHART_VAR.primary,
  CHART_VAR.secondary,
  CHART_VAR.tertiary,
  CHART_VAR.quaternary,
  CHART_VAR.neutral,
] as const;

/** Semantic meaning → chart colour (§29). */
export const CHART_SEMANTIC = {
  positive: CHART_VAR.primary,
  positiveStrong: CHART_VAR.secondary,
  warning: CHART_VAR.tertiary,
  error: CHART_VAR.quaternary,
  neutral: CHART_VAR.neutral,
} as const;

/* ========================================================================= */
/* SPACING — 8px base grid (§23)                                             */
/* ========================================================================= */

export const SPACING = {
  1: 4,
  2: 8,
  3: 12,
  4: 16,
  5: 20,
  6: 24,
  8: 32,
  10: 40,
  12: 48,
} as const;

export const SPACING_VAR = {
  1: 'var(--md-sys-spacing-1)',
  2: 'var(--md-sys-spacing-2)',
  3: 'var(--md-sys-spacing-3)',
  4: 'var(--md-sys-spacing-4)',
  5: 'var(--md-sys-spacing-5)',
  6: 'var(--md-sys-spacing-6)',
  8: 'var(--md-sys-spacing-8)',
  10: 'var(--md-sys-spacing-10)',
  12: 'var(--md-sys-spacing-12)',
} as const;

/** Composed dashboard rhythm (§23). */
export const LAYOUT_RHYTHM_VAR = {
  gapCard: 'var(--md-sys-gap-card)',
  gapSection: 'var(--md-sys-gap-section)',
  paddingPage: 'var(--md-sys-padding-page)',
  paddingCard: 'var(--md-sys-padding-card)',
  paddingCardCompact: 'var(--md-sys-padding-card-compact)',
  paddingCardHero: 'var(--md-sys-padding-card-hero)',
} as const;

/* ========================================================================= */
/* SHAPE (§7) — do not mix unrelated radii                                   */
/* ========================================================================= */

export const RADIUS = {
  none: 0,
  xs: 6,
  sm: 10,
  md: 14,
  lg: 18,
  xl: 22,
  hero: 28,
  pill: 999,
} as const;

export const RADIUS_VAR = {
  none: 'var(--radius-none)',
  xs: 'var(--radius-xs)',
  sm: 'var(--radius-sm)',
  md: 'var(--radius-md)',
  lg: 'var(--radius-lg)',
  xl: 'var(--radius-xl)',
  hero: 'var(--radius-hero)',
  pill: 'var(--radius-pill)',
} as const;

/**
 * Intent-named radii (§7 Rules) so callers never guess:
 *   dashboard cards 18–22 · hero 24–30 · input/filter 10–14 · charts 18–22
 *   tables/lists 16–20 · small badges pill
 */
export const RADIUS_INTENT_VAR = {
  card: 'var(--radius-card)',
  cardNested: 'var(--radius-card-nested)',
  chart: 'var(--radius-chart)',
  table: 'var(--radius-table)',
  input: 'var(--radius-input)',
  button: 'var(--radius-button)',
  badge: 'var(--radius-badge)',
  overlay: 'var(--radius-overlay)',
} as const;

/* ========================================================================= */
/* ELEVATION (§21) — reserved for floating elements and hover                 */
/* ========================================================================= */

export const ELEVATION_VAR = {
  0: 'var(--md-sys-elevation-level0)',
  1: 'var(--md-sys-elevation-level1)',
  2: 'var(--md-sys-elevation-level2)',
  3: 'var(--md-sys-elevation-level3)',
  4: 'var(--md-sys-elevation-level4)',
  5: 'var(--md-sys-elevation-level5)',
} as const;

/* ========================================================================= */
/* TYPOGRAPHY (§6) — compact, information-dense                               */
/* ========================================================================= */

export const TYPOGRAPHY_VAR = {
  fontFamily: 'var(--md-sys-typescale-font-family)',

  pageTitleSize: 'var(--md-sys-typescale-page-title-size)',
  pageTitleWeight: 'var(--md-sys-typescale-page-title-weight)',
  pageTitleLineHeight: 'var(--md-sys-typescale-page-title-line-height)',

  sectionTitleSize: 'var(--md-sys-typescale-section-title-size)',
  sectionTitleWeight: 'var(--md-sys-typescale-section-title-weight)',

  metricSize: 'var(--md-sys-typescale-metric-size)',
  metricSizeLg: 'var(--md-sys-typescale-metric-size-lg)',
  metricSizeSm: 'var(--md-sys-typescale-metric-size-sm)',
  metricWeight: 'var(--md-sys-typescale-metric-weight)',

  bodySize: 'var(--md-sys-typescale-body-size)',
  bodySizeSm: 'var(--md-sys-typescale-body-size-sm)',
  bodyWeight: 'var(--md-sys-typescale-body-weight)',

  metaSize: 'var(--md-sys-typescale-meta-size)',
  metaSizeSm: 'var(--md-sys-typescale-meta-size-sm)',

  navSize: 'var(--md-sys-typescale-nav-size)',
  navWeight: 'var(--md-sys-typescale-nav-weight)',

  labelSize: 'var(--md-sys-typescale-label-size)',
  labelSizeSm: 'var(--md-sys-typescale-label-size-sm)',
  labelWeight: 'var(--md-sys-typescale-label-weight)',
} as const;

/** §6 — metric values prefer tabular numerals so columns align. */
export const TABULAR_NUMS = { fontVariantNumeric: 'tabular-nums' } as const;

/* ========================================================================= */
/* ICONOGRAPHY (§28) — Material Symbols Rounded only                          */
/* ========================================================================= */

export const ICON_SIZE = {
  /** 16–18px compact actions */
  compact: 18,
  /** 18–20px navigation */
  nav: 20,
  /** 20–24px cards */
  card: 22,
  sm: 16,
  lg: 24,
} as const;

/* ========================================================================= */
/* LAYOUT & BREAKPOINTS (§8, §25)                                            */
/* ========================================================================= */

export const LAYOUT = {
  /** The reference viewport the design was specified against (§8). */
  referenceWidth: 1152,
  referenceHeight: 720,
  contentMaxWidth: 1440,
  /** §8 — 64–72px collapsed / 180–210px expanded */
  sidebarCollapsed: 68,
  sidebarExpanded: 196,
  /** §9 — the header must consume minimal vertical space */
  headerHeight: 60,
} as const;

export const BREAKPOINT = {
  mobile: 0,
  tablet: 768,
  desktop: 1200,
  wide: 1600,
} as const;

export type BreakpointName = keyof typeof BREAKPOINT;

/** §25 — mobile < 768 · tablet 768–1199 · desktop ≥ 1200 */
export const MEDIA = {
  mobile: `(max-width: ${BREAKPOINT.tablet - 1}px)`,
  tablet: `(min-width: ${BREAKPOINT.tablet}px) and (max-width: ${BREAKPOINT.desktop - 1}px)`,
  desktop: `(min-width: ${BREAKPOINT.desktop}px)`,
  wide: `(min-width: ${BREAKPOINT.wide}px)`,
  /** §9 — every component must honour this. */
  reducedMotion: '(prefers-reduced-motion: reduce)',
} as const;

/* ========================================================================= */
/* DENSITY (§24) & ACCESSIBILITY (§9)                                        */
/* ========================================================================= */

export const DENSITY = {
  controlHeightSm: 30,
  controlHeight: 36,
  controlHeightLg: 44,
  rowHeightCompact: 40,
  rowHeight: 48,
  rowHeightComfortable: 56,
} as const;

export const A11Y = {
  /** §9 — minimum 44 × 44px touch target where applicable. */
  minTouchTarget: 44,
  focusRingWidth: 2,
  focusRingOffset: 2,
  /** §9 — WCAG AA. */
  minContrastNormal: 4.5,
  minContrastLarge: 3,
} as const;

/* ========================================================================= */
/* MOTION (§18) — restrained; never bouncy                                   */
/* ========================================================================= */

/** Durations in ms, straight from the §18 table (midpoint of each range). */
export const MOTION_DURATION_MS = {
  hover: 140,   // 120–160
  button: 160,  // 120–180
  card: 200,    // 180–220
  modal: 250,   // 220–280
  page: 300,    // 250–350
  chart: 550,   // 400–700
} as const;

/** Same durations in seconds, for GSAP / WAAPI. */
export const MOTION_DURATION_SEC = {
  hover: 0.14,
  button: 0.16,
  card: 0.2,
  modal: 0.25,
  page: 0.3,
  chart: 0.55,
} as const;

/**
 * §18 — easing similar to Material motion. Avoid bouncy animation.
 *
 * These are the CSS strings. The Motion-flavoured forms of the same curves
 * (tuples, callable functions) live in `src/motion/easing.ts` as `M3_EASE`
 * and `M3_EASE_FN`; all three describe the identical control points.
 */
export const MOTION_EASING = {
  standard: 'cubic-bezier(0.2, 0, 0, 1)',
  emphasized: 'cubic-bezier(0.2, 0, 0, 1)',
  decelerate: 'cubic-bezier(0.05, 0.7, 0.1, 1)',
  accelerate: 'cubic-bezier(0.3, 0, 0.8, 0.15)',
} as const;

export const MOTION_VAR = {
  fast: 'var(--motion-fast)',
  standard: 'var(--motion-standard)',
  emphasized: 'var(--motion-emphasized)',
  easingStandard: 'var(--motion-easing-standard)',
  easingEmphasized: 'var(--motion-easing-emphasized)',
} as const;

/* ========================================================================= */
/* Z-INDEX (§3.1)                                                            */
/* ========================================================================= */

export const Z_INDEX = {
  base: 0,
  raised: 10,
  sticky: 100,
  header: 200,
  drawer: 300,
  scrim: 400,
  dialog: 500,
  dropdown: 600,
  popover: 700,
  tooltip: 800,
  snackbar: 900,
  max: 1000,
} as const;

/* ========================================================================= */
/* THEMES (§2)                                                               */
/* ========================================================================= */

export const THEME_MODES = ['light', 'dark'] as const;
export type ThemeMode = (typeof THEME_MODES)[number];

/** §2 — Lime is the reference preset, not the mandatory product identity. */
export const THEME_ACCENTS = ['lime', 'blue', 'violet', 'orange', 'rose'] as const;
export type ThemeAccent = (typeof THEME_ACCENTS)[number];

export interface ThemeAccentMeta {
  id: ThemeAccent;
  label: string;
  /** Swatch colour for theme pickers. The only place a literal is acceptable. */
  swatch: string;
}

export const THEME_ACCENT_META: readonly ThemeAccentMeta[] = [
  { id: 'lime', label: 'Lime', swatch: '#6FAF39' },
  { id: 'blue', label: 'Blue', swatch: '#2F73B8' },
  { id: 'violet', label: 'Violet', swatch: '#7048BE' },
  { id: 'orange', label: 'Orange', swatch: '#A66220' },
  { id: 'rose', label: 'Rose', swatch: '#B23A52' },
] as const;

/**
 * Apply a theme by setting the two attributes the CSS keys off. §30: this
 * changes semantic colour only — geometry, spacing and radius are untouched.
 */
export const applyTheme = (
  mode: ThemeMode,
  accent: ThemeAccent = 'lime',
  root: HTMLElement | null = typeof document !== 'undefined' ? document.documentElement : null,
): void => {
  if (!root) return;
  root.setAttribute('data-theme', mode);
  root.setAttribute('data-accent', accent);
};
