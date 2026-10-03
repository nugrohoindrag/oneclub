/**
 * @license MIT
 * Drag, Pan & Reorder — Morphic Design System
 * Powered by Motion (https://motion.dev — MIT License)
 *
 * Motion docs: https://motion.dev/docs/react-motion-component#drag
 *              https://motion.dev/docs/react-gestures#pan
 *              https://motion.dev/docs/react-reorder
 *
 * Spec: Morphic-Design-System-adjusted.md
 *   §3.8 Data Grid    — column reorder, row selection, bulk actions
 *   §9  Accessibility — "Keyboard behavior" is REQUIRED for every interactive
 *                        component. A pointer-only drag fails the contract, so
 *                        `ReorderList` ships arrow-key reordering alongside it.
 *   §18 Motion Design — restrained; drag uses `responsive` spring, not bounce
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useRef, useState } from 'react';
import {
  motion,
  Reorder,
  useDragControls,
  type PanInfo,
} from 'motion/react';
import { Icon } from '../communication/Icon.js';
import {
  M3_SPRING,
  M3_TRANSITIONS,
  useReducedMotionSafe,
} from '../../motion/index.js';

/* ========================================================================= */
/* Draggable                                                                 */
/* ========================================================================= */

export interface DraggableProps {
  children: React.ReactNode;
  /** `true` for both axes, or lock to one. */
  axis?: boolean | 'x' | 'y';
  /** Bounding box in pixels, or a ref to the element that bounds it. */
  constraints?:
    | { top?: number; left?: number; right?: number; bottom?: number }
    | React.RefObject<Element | null>;
  /** Resistance past the constraints, 0–1. Motion's default is 0.5. */
  elastic?: number;
  /** Carry velocity after release. */
  momentum?: boolean;
  /** Spring back to the starting point on release. */
  snapBack?: boolean;
  /** Commit to the first axis the pointer moves along. */
  directionLock?: boolean;
  /** Let a parent also receive the drag. */
  propagate?: boolean;
  /** Visual state while dragging. */
  whileDragging?: Parameters<typeof motion.div>[0]['whileDrag'];
  /** Drive the drag from elsewhere — see `DragHandle`. */
  controls?: ReturnType<typeof useDragControls>;
  /** With `controls`, set false so only the handle starts a drag. */
  listener?: boolean;
  onDragStart?: (event: PointerEvent, info: PanInfo) => void;
  onDrag?: (event: PointerEvent, info: PanInfo) => void;
  onDragEnd?: (event: PointerEvent, info: PanInfo) => void;
  onDirectionLock?: (axis: 'x' | 'y') => void;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * A draggable surface with Morphic defaults: it lifts slightly while held and
 * settles on the `responsive` spring rather than overshooting (§18).
 *
 * ```tsx
 * const bounds = useRef(null);
 * <div ref={bounds}>
 *   <Draggable constraints={bounds}>…</Draggable>
 * </div>
 * ```
 *
 * Drag is pointer-only by nature. Where reordering is the goal, prefer
 * `ReorderList`, which adds the keyboard path §9 requires.
 */
export const Draggable: React.FC<DraggableProps> = ({
  children,
  axis = true,
  constraints,
  elastic = 0.35,
  momentum = true,
  snapBack = false,
  directionLock = false,
  propagate = false,
  whileDragging,
  controls,
  listener,
  onDragStart,
  onDrag,
  onDragEnd,
  onDirectionLock,
  className = '',
  style,
}) => {
  const reduced = useReducedMotionSafe();

  return (
    <motion.div
      drag={axis}
      dragConstraints={constraints}
      dragElastic={elastic}
      dragMomentum={momentum && !reduced}
      dragSnapToOrigin={snapBack}
      dragDirectionLock={directionLock}
      dragPropagation={propagate}
      dragControls={controls}
      dragListener={listener}
      dragTransition={{ bounceStiffness: 400, bounceDamping: 40 }}
      whileDrag={whileDragging ?? { scale: 1.02, boxShadow: 'var(--md-sys-elevation-level3)' }}
      onDragStart={onDragStart}
      onDrag={onDrag}
      onDragEnd={onDragEnd}
      onDirectionLock={onDirectionLock}
      transition={M3_SPRING.responsive}
      className={`morphic-draggable ${className}`}
      style={{ touchAction: axis === 'x' ? 'pan-y' : axis === 'y' ? 'pan-x' : 'none', ...style }}
    >
      {children}
    </motion.div>
  );
};

/* ========================================================================= */
/* DragHandle — start a drag from a grip rather than the whole surface       */
/* ========================================================================= */

export interface DragHandleProps {
  controls: ReturnType<typeof useDragControls>;
  label?: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * ```tsx
 * const controls = useDragControls();
 * <Draggable controls={controls} listener={false}>
 *   <DragHandle controls={controls} />
 *   …
 * </Draggable>
 * ```
 */
export const DragHandle: React.FC<DragHandleProps> = ({
  controls,
  label = 'Drag to move',
  className = '',
  style,
}) => (
  <span
    role="button"
    aria-label={label}
    tabIndex={0}
    onPointerDown={(event) => controls.start(event)}
    className={`morphic-drag-handle ${className}`}
    style={{
      display: 'inline-flex',
      alignItems: 'center',
      justifyContent: 'center',
      cursor: 'grab',
      touchAction: 'none',
      color: 'var(--md-sys-color-on-surface-variant)',
      borderRadius: 'var(--radius-xs)',
      minWidth: 'var(--md-sys-density-control-height-sm)',
      minHeight: 'var(--md-sys-density-control-height-sm)',
      ...style,
    }}
  >
    <Icon name="drag_indicator" size={18} />
  </span>
);

/* ========================================================================= */
/* Pannable — raw pan gesture without moving the element                    */
/* ========================================================================= */

export interface PannableProps {
  children: React.ReactNode;
  onPanStart?: (event: PointerEvent, info: PanInfo) => void;
  onPan?: (event: PointerEvent, info: PanInfo) => void;
  onPanEnd?: (event: PointerEvent, info: PanInfo) => void;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Reports pointer travel without applying it — swipe-to-dismiss, a chart
 * brush, a custom slider. Motion's `info` carries `point`, `delta`, `offset`
 * and `velocity`.
 */
export const Pannable: React.FC<PannableProps> = ({
  children,
  onPanStart,
  onPan,
  onPanEnd,
  className = '',
  style,
}) => (
  <motion.div
    onPanStart={onPanStart}
    onPan={onPan}
    onPanEnd={onPanEnd}
    className={`morphic-pannable ${className}`}
    style={{ touchAction: 'none', ...style }}
  >
    {children}
  </motion.div>
);

/* ========================================================================= */
/* ReorderList — drag to reorder, with a keyboard path (§9)                  */
/* ========================================================================= */

export interface ReorderListProps<T> {
  items: T[];
  onReorder: (items: T[]) => void;
  /** Stable identity. Motion reorders by value, so this must be unique. */
  getKey: (item: T) => React.Key;
  children: (item: T, index: number) => React.ReactNode;
  axis?: 'x' | 'y';
  /** Show a grip and require it to start a drag. */
  handle?: boolean;
  /** Accessible label for the list. */
  label?: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * §3.8 lists column reorder as a Data Grid capability, and §9 requires
 * keyboard behaviour for every interactive component — so each row is
 * focusable and responds to ArrowUp/ArrowDown (or Left/Right on the x axis).
 * Pointer drag and keyboard both write through `onReorder`.
 */
export function ReorderList<T>({
  items,
  onReorder,
  getKey,
  children,
  axis = 'y',
  handle = false,
  label = 'Reorderable list',
  className = '',
  style,
}: ReorderListProps<T>) {
  const move = (from: number, to: number) => {
    if (to < 0 || to >= items.length) return;
    const next = [...items];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    onReorder(next);
  };

  const backward = axis === 'y' ? 'ArrowUp' : 'ArrowLeft';
  const forward = axis === 'y' ? 'ArrowDown' : 'ArrowRight';

  return (
    <Reorder.Group
      as="ul"
      axis={axis}
      values={items}
      onReorder={onReorder}
      aria-label={label}
      className={`morphic-reorder-list ${className}`}
      style={{
        listStyle: 'none',
        margin: 0,
        padding: 0,
        display: 'flex',
        flexDirection: axis === 'y' ? 'column' : 'row',
        gap: 'var(--md-sys-spacing-2)',
        ...style,
      }}
    >
      {items.map((item, index) => (
        <ReorderRow
          key={getKey(item)}
          item={item}
          index={index}
          total={items.length}
          handle={handle}
          backward={backward}
          forward={forward}
          onMove={move}
        >
          {children(item, index)}
        </ReorderRow>
      ))}
    </Reorder.Group>
  );
}

interface ReorderRowProps<T> {
  item: T;
  index: number;
  total: number;
  handle: boolean;
  backward: string;
  forward: string;
  onMove: (from: number, to: number) => void;
  children: React.ReactNode;
}

function ReorderRow<T>({
  item,
  index,
  total,
  handle,
  backward,
  forward,
  onMove,
  children,
}: ReorderRowProps<T>) {
  const controls = useDragControls();

  return (
    <Reorder.Item
      value={item}
      dragListener={!handle}
      dragControls={handle ? controls : undefined}
      transition={M3_TRANSITIONS.layout}
      whileDrag={{ scale: 1.02, boxShadow: 'var(--md-sys-elevation-level3)' }}
      /* §9 — keyboard reordering, so the feature is not pointer-only */
      tabIndex={0}
      role="option"
      aria-label={`Item ${index + 1} of ${total}. Use arrow keys to reorder.`}
      onKeyDown={(event: React.KeyboardEvent) => {
        if (event.key === backward) {
          event.preventDefault();
          onMove(index, index - 1);
        } else if (event.key === forward) {
          event.preventDefault();
          onMove(index, index + 1);
        }
      }}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 'var(--md-sys-spacing-3)',
        padding: 'var(--md-sys-spacing-3) var(--md-sys-spacing-4)',
        borderRadius: 'var(--radius-md)',
        backgroundColor: 'var(--md-sys-color-surface)',
        border: '1px solid var(--md-sys-color-border)',
        cursor: handle ? 'default' : 'grab',
        touchAction: 'none',
      }}
    >
      {handle && <DragHandle controls={controls} />}
      <div style={{ flex: 1, minWidth: 0 }}>{children}</div>
    </Reorder.Item>
  );
}

export { useDragControls };
export type { PanInfo };
