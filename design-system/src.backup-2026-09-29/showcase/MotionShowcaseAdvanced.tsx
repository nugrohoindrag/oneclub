/**
 * @license MIT
 * Motion Showcase — the rest of the motion component API
 *
 * https://motion.dev/docs/react-motion-component covers far more than the
 * animation props: drag, pan, viewport, and the advanced escape hatches.
 * Those demos live here.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useRef, useState } from 'react';
import {
  Button,
  Draggable,
  DragHandle,
  Pannable,
  ReorderList,
  InView,
  LayoutBox,
  Transform,
  useDragControls,
  M3_TRANSITIONS,
} from '../index.js';

/* ------------------------------------------------------------------ shell */

const Section: React.FC<{
  title: string;
  children: React.ReactNode;
}> = ({ title, children }) => (
  <section
    style={{
      backgroundColor: 'var(--md-sys-color-surface)',
      border: '1px solid var(--md-sys-color-border)',
      borderRadius: 'var(--radius-card)',
      padding: 'var(--md-sys-padding-card)',
      display: 'flex',
      flexDirection: 'column',
      gap: 'var(--md-sys-spacing-4)',
    }}
  >
    <h3
      style={{
        margin: 0,
        fontSize: 'var(--md-sys-typescale-section-title-size)',
        fontWeight: 620,
        letterSpacing: 'var(--md-sys-typescale-section-title-tracking)',
      }}
    >
      {title}
    </h3>
    {children}
  </section>
);

const Stage: React.FC<{ children: React.ReactNode; height?: number }> = ({ children, height }) => (
  <div
    style={{
      backgroundColor: 'var(--md-sys-color-surface-container)',
      borderRadius: 'var(--radius-lg)',
      padding: 'var(--md-sys-spacing-5)',
      minHeight: height,
      display: 'flex',
      flexWrap: 'wrap',
      alignItems: 'center',
      justifyContent: 'center',
      gap: 'var(--md-sys-spacing-4)',
    }}
  >
    {children}
  </div>
);

const cardStyle: React.CSSProperties = {
  padding: 'var(--md-sys-spacing-4)',
  borderRadius: 'var(--radius-md)',
  backgroundColor: 'var(--md-sys-color-surface)',
  border: '1px solid var(--md-sys-color-border)',
  fontSize: 'var(--md-sys-typescale-body-size-sm)',
  fontWeight: 550,
};

const hintStyle: React.CSSProperties = {
  margin: 0,
  fontSize: 'var(--md-sys-typescale-meta-size)',
  color: 'var(--md-sys-color-on-surface-variant)',
};

const dragChipStyle: React.CSSProperties = {
  padding: 'var(--md-sys-spacing-3) var(--md-sys-spacing-4)',
  borderRadius: 'var(--radius-md)',
  backgroundColor: 'var(--md-sys-color-surface)',
  border: '1px solid var(--md-sys-color-border)',
  fontSize: 'var(--md-sys-typescale-body-size-sm)',
  fontWeight: 600,
  cursor: 'grab',
  userSelect: 'none',
};

/* ------------------------------------------------------------- 1. Drag & pan */

const DragDemo: React.FC = () => {
  const bounds = useRef<HTMLDivElement>(null);
  const controls = useDragControls();
  const [pan, setPan] = useState({ x: 0, y: 0 });

  return (
    <>
      <div
        ref={bounds}
        style={{
          position: 'relative',
          minHeight: 180,
          borderRadius: 'var(--radius-lg)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          overflow: 'hidden',
          display: 'flex',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: 'var(--md-sys-spacing-5)',
          padding: 'var(--md-sys-spacing-5)',
        }}
      >
        <Draggable constraints={bounds} elastic={0.3}>
          <div style={dragChipStyle}>Drag me — bounded</div>
        </Draggable>

        <Draggable axis="x" constraints={bounds} snapBack>
          <div style={dragChipStyle}>X only · snaps back</div>
        </Draggable>

        <Draggable constraints={bounds} controls={controls} listener={false}>
          <div style={{ ...dragChipStyle, display: 'flex', alignItems: 'center', gap: 8, cursor: 'default' }}>
            <DragHandle controls={controls} />
            Handle only
          </div>
        </Draggable>
      </div>

      <Pannable
        onPan={(_event, info) =>
          setPan({ x: Math.round(info.offset.x), y: Math.round(info.offset.y) })
        }
        onPanEnd={() => setPan({ x: 0, y: 0 })}
        style={{
          padding: 'var(--md-sys-spacing-4)',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          textAlign: 'center',
          fontSize: 'var(--md-sys-typescale-body-size-sm)',
          fontVariantNumeric: 'tabular-nums',
          cursor: 'grab',
        }}
      >
        Pan across this panel — offset x {pan.x}, y {pan.y}
      </Pannable>
    </>
  );
};

/* --------------------------------------------------------------- 2. Reorder */

const ReorderDemo: React.FC = () => {
  const [items, setItems] = useState(['Revenue', 'Active users', 'Conversion rate', 'Churn']);

  return (
    <>
      <p style={hintStyle}>
        Drag a row, or focus one and press ArrowUp / ArrowDown for accessible keyboard reordering.
      </p>
      <ReorderList
        items={items}
        onReorder={setItems}
        getKey={(item) => item}
        handle
        label="Reorder metrics"
      >
        {(item, index) => (
          <span style={{ display: 'flex', gap: 'var(--md-sys-spacing-3)', alignItems: 'baseline' }}>
            <span
              style={{
                color: 'var(--md-sys-color-on-surface-variant)',
                fontVariantNumeric: 'tabular-nums',
              }}
            >
              {index + 1}
            </span>
            <strong style={{ fontWeight: 600 }}>{item}</strong>
          </span>
        )}
      </ReorderList>
    </>
  );
};

/* -------------------------------------------------- 3. Viewport (declarative) */

const ViewportDemo: React.FC = () => {
  const [log, setLog] = useState('waiting…');
  const rows = [
    { label: 'once: true — reveals and stays', once: true, amount: 0.3 },
    { label: 'once: false — re-runs each pass', once: false, amount: 0.3 },
    { label: 'amount: 0.9 — waits until nearly full', once: false, amount: 0.9 },
  ];

  return (
    <>
      <p style={hintStyle}>
        <code>whileInView</code> + <code>viewport</code> run off the React render
        cycle, unlike the <code>useInView</code> hook behind <code>Reveal</code>.
        Prefer this for long lists.
      </p>
      <div
        style={{
          height: 220,
          overflowY: 'auto',
          borderRadius: 'var(--radius-lg)',
          backgroundColor: 'var(--md-sys-color-surface-container)',
          padding: 'var(--md-sys-spacing-5)',
          display: 'flex',
          flexDirection: 'column',
          gap: 'var(--md-sys-spacing-4)',
        }}
      >
        <div style={{ height: 120 }} />
        {rows.map((row) => (
          <InView
            key={row.label}
            viewport={{ once: row.once, amount: row.amount }}
            onViewportEnter={() => setLog(`entered · ${row.label}`)}
            onViewportLeave={() => setLog(`left · ${row.label}`)}
          >
            <div style={cardStyle}>{row.label}</div>
          </InView>
        ))}
        <div style={{ height: 160 }} />
      </div>
      <span style={hintStyle}>Last viewport event: {log}</span>
    </>
  );
};

/* ------------------------------------------------------- 4. Advanced props */

const AdvancedDemo: React.FC = () => {
  const [spun, setSpun] = useState(false);
  const [frames, setFrames] = useState(0);

  return (
    <>
      <Stage height={150}>
        <Transform
          custom={2}
          variants={{
            rest: { rotate: 0, x: 0 },
            spin: (factor: number) => ({ rotate: 180 * factor, x: 40 * factor }),
          }}
          initial="rest"
          animate={spun ? 'spin' : 'rest'}
          transformTemplate={({ rotate, x }) => `translateX(${x}) rotate(${rotate})`}
          onUpdate={() => setFrames((f) => f + 1)}
          transition={M3_TRANSITIONS.page}
        >
          <div
            style={{
              width: 64,
              height: 64,
              borderRadius: 'var(--radius-md)',
              backgroundColor: 'var(--md-sys-color-primary)',
            }}
          />
        </Transform>
      </Stage>

      <div
        style={{
          display: 'flex',
          gap: 'var(--md-sys-spacing-3)',
          alignItems: 'center',
          flexWrap: 'wrap',
        }}
      >
        <Button variant="tonal" size="sm" onClick={() => setSpun((v) => !v)}>
          Toggle dynamic variant
        </Button>
        <span style={hintStyle}>
          <code>custom</code> feeds the variant function · <code>transformTemplate</code>{' '}
          forces translate-before-rotate · <code>onUpdate</code> fired {frames} frames
        </span>
      </div>

      <LayoutBox
        layout
        layoutDependency={spun}
        style={{ display: 'flex', gap: 'var(--md-sys-spacing-3)' }}
      >
        <div style={{ ...cardStyle, flex: spun ? 2 : 1 }}>layout</div>
        <div style={{ ...cardStyle, flex: 1 }}>layoutDependency</div>
      </LayoutBox>
    </>
  );
};

/* --------------------------------------------------------------- sections */

export const MotionAdvancedSections: React.FC = () => (
  <>
    <Section title="Drag & pan">
      <DragDemo />
    </Section>

    <Section title="Reorder">
      <ReorderDemo />
    </Section>

    <Section title="Viewport (whileInView)">
      <ViewportDemo />
    </Section>

    <Section title="Advanced props">
      <AdvancedDemo />
    </Section>
  </>
);
