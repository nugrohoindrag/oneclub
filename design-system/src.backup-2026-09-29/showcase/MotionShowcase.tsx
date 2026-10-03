/**
 * @license MIT
 * Motion Showcase — Morphic Design System
 *
 * One live demo per topic in https://motion.dev/docs, wired to the Morphic
 * motion layer.
 *
 * Copyright (c) 2026 Morphic Design System Contributors
 */

import React, { useEffect, useRef, useState } from 'react';
import {
  Icon,
  Button,
  Presence,
  PresenceList,
  SharedIndicator,
  Reveal,
  StaggerReveal,
  Parallax,
  ScrollScale,
  SplitText,
  ScrambleText,
  CountUp,
  DrawSVG,
  AnimatedCircularProgress,
  Magnetic,
  Pressable,
  Tilt,
  MotionScrubber,
  ParticleBurst,
  LayoutTransformDemo,
  SvgDrawMotion,
  StaggerList,
  SpringPlayground,
  M3_TRANSITIONS,
  M3_SPRING,
  animate,
  motion,
  LayoutGroup,
} from '../index.js';
import { MotionAdvancedSections } from './MotionShowcaseAdvanced.js';

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

const Stage: React.FC<{ children: React.ReactNode; height?: number }> = ({
  children,
  height,
}) => (
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

const Chip: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <span
    style={{
      padding: '4px 10px',
      borderRadius: 'var(--radius-pill)',
      backgroundColor: 'var(--md-sys-color-primary-container)',
      color: 'var(--md-sys-color-on-primary-container)',
      fontSize: 'var(--md-sys-typescale-meta-size-sm)',
      fontWeight: 600,
    }}
  >
    {children}
  </span>
);

/* ------------------------------------------------------- 1. React animation */

const ReactAnimationDemo: React.FC = () => {
  const [on, setOn] = useState(false);
  return (
    <>
      <Stage height={140}>
        <motion.div
          animate={{
            x: on ? 120 : 0,
            rotate: on ? 90 : 0,
            borderRadius: on ? '999px' : 'var(--radius-md)',
          }}
          transition={M3_TRANSITIONS.enter}
          style={{
            width: 64,
            height: 64,
            backgroundColor: 'var(--md-sys-color-primary)',
          }}
        />
      </Stage>
      <Button variant="tonal" size="sm" onClick={() => setOn((v) => !v)}>
        {on ? 'Reset' : 'Animate'}
      </Button>
    </>
  );
};

/* ------------------------------------------------------- 2. Motion component */

const MotionComponentDemo: React.FC = () => (
  <Stage height={140}>
    <Pressable lift as="div">
      <div
        style={{
          padding: 'var(--md-sys-spacing-4) var(--md-sys-spacing-5)',
          borderRadius: 'var(--radius-md)',
          backgroundColor: 'var(--md-sys-color-surface)',
          border: '1px solid var(--md-sys-color-border)',
          fontWeight: 600,
        }}
      >
        Pressable — hover lifts, press scales 0.98
      </div>
    </Pressable>

    <Magnetic strength={0.3}>
      <Button variant="filled" icon={<Icon name="near_me" size={16} />}>
        Magnetic
      </Button>
    </Magnetic>

    <Tilt max={8}>
      <div
        style={{
          width: 150,
          height: 88,
          borderRadius: 'var(--radius-lg)',
          background: 'var(--hero-banner-bg)',
          color: 'var(--hero-banner-text)',
          display: 'grid',
          placeItems: 'center',
          fontWeight: 600,
          fontSize: 'var(--md-sys-typescale-body-size-sm)',
        }}
      >
        Tilt (3D)
      </div>
    </Tilt>
  </Stage>
);

/* -------------------------------------------------------- 3. Transitions */

const TransitionsDemo: React.FC = () => {
  const [key, setKey] = useState(0);
  const rows: Array<[string, keyof typeof M3_TRANSITIONS, string]> = [
    ['hover', 'hover', '140ms'],
    ['button', 'button', '160ms'],
    ['card', 'card', '200ms'],
    ['enter', 'enter', '250ms'],
    ['page', 'page', '300ms'],
    ['chart', 'chart', '550ms'],
  ];

  return (
    <>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-2)' }}>
        {rows.map(([label, name, ms]) => (
          <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-3)' }}>
            <span
              style={{
                width: 72,
                fontSize: 'var(--md-sys-typescale-meta-size)',
                color: 'var(--md-sys-color-on-surface-variant)',
              }}
            >
              {label}
            </span>
            <div
              style={{
                flex: 1,
                height: 8,
                borderRadius: 'var(--radius-pill)',
                backgroundColor: 'var(--md-sys-color-surface-container-high)',
                overflow: 'hidden',
              }}
            >
              <motion.div
                key={`${label}-${key}`}
                initial={{ scaleX: 0 }}
                animate={{ scaleX: 1 }}
                transition={M3_TRANSITIONS[name]}
                style={{
                  height: '100%',
                  transformOrigin: 'left',
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor: 'var(--md-sys-color-primary)',
                }}
              />
            </div>
            <span
              style={{
                width: 48,
                textAlign: 'right',
                fontSize: 'var(--md-sys-typescale-meta-size-sm)',
                fontVariantNumeric: 'tabular-nums',
                color: 'var(--md-sys-color-on-surface-variant)',
              }}
            >
              {ms}
            </span>
          </div>
        ))}
      </div>
      <Button variant="tonal" size="sm" onClick={() => setKey((k) => k + 1)}>
        Replay
      </Button>
    </>
  );
};

/* ------------------------------------------------------------- 4. Springs */

const SpringsDemo: React.FC = () => {
  const [toggled, setToggled] = useState(false);
  const names = Object.keys(M3_SPRING) as Array<keyof typeof M3_SPRING>;

  return (
    <>
      <Stage height={150}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-3)', width: '100%' }}>
          {names.map((name) => (
            <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-3)' }}>
              <span
                style={{
                  width: 84,
                  fontSize: 'var(--md-sys-typescale-meta-size)',
                  color: 'var(--md-sys-color-on-surface-variant)',
                }}
              >
                {name}
              </span>
              <motion.div
                animate={{ x: toggled ? 200 : 0 }}
                transition={M3_SPRING[name]}
                style={{
                  width: 24,
                  height: 24,
                  borderRadius: 'var(--radius-pill)',
                  backgroundColor:
                    name === 'playful'
                      ? 'var(--md-sys-color-chart-tertiary)'
                      : 'var(--md-sys-color-primary)',
                }}
              />
            </div>
          ))}
        </div>
      </Stage>
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--md-sys-spacing-3)' }}>
        <Button variant="tonal" size="sm" onClick={() => setToggled((v) => !v)}>
          Toggle
        </Button>
        <span
          style={{
            fontSize: 'var(--md-sys-typescale-meta-size)',
            color: 'var(--md-sys-color-on-surface-variant)',
          }}
        >
          Expressive dynamic spring physics
        </span>
      </div>
      <SpringPlayground />
    </>
  );
};

/* ------------------------------------------------------ 5. AnimatePresence */

interface Row {
  id: number;
  label: string;
}

const PresenceDemo: React.FC = () => {
  const [open, setOpen] = useState(true);
  const [rows, setRows] = useState<Row[]>([
    { id: 1, label: 'Payment received' },
    { id: 2, label: 'Invoice issued' },
    { id: 3, label: 'Refund processed' },
  ]);
  const next = useRef(4);

  return (
    <>
      <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-2)', flexWrap: 'wrap' }}>
        <Button variant="tonal" size="sm" onClick={() => setOpen((v) => !v)}>
          {open ? 'Hide panel' : 'Show panel'}
        </Button>
        <Button
          variant="tonal"
          size="sm"
          onClick={() => {
            const id = next.current++;
            setRows((r) => [{ id, label: `Event ${id}` }, ...r]);
          }}
        >
          Add row
        </Button>
        <Button variant="text" size="sm" onClick={() => setRows((r) => r.slice(1))}>
          Remove first
        </Button>
      </div>

      <Presence show={open} preset="collapse">
        <div
          style={{
            padding: 'var(--md-sys-spacing-4)',
            borderRadius: 'var(--radius-lg)',
            backgroundColor: 'var(--md-sys-color-surface-container)',
            marginTop: 'var(--md-sys-spacing-3)',
          }}
        >
          A collapsing panel — height animates, and the exit actually plays.
        </div>
      </Presence>

      <PresenceList
        items={rows}
        getKey={(r) => r.id}
        preset="fadeUp"
        style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-2)' }}
      >
        {(row) => (
          <div
            style={{
              padding: 'var(--md-sys-spacing-3) var(--md-sys-spacing-4)',
              borderRadius: 'var(--radius-md)',
              backgroundColor: 'var(--md-sys-color-surface-container)',
              fontSize: 'var(--md-sys-typescale-body-size-sm)',
            }}
          >
            {row.label}
          </div>
        )}
      </PresenceList>
    </>
  );
};

/* -------------------------------------------------- 6. Layout animation */

const LayoutDemo: React.FC = () => {
  const tabs = ['Overview', 'Activity', 'Settings'];
  const [active, setActive] = useState(tabs[0]);

  return (
    <>
      <LayoutGroup id="motion-showcase-tabs">
        <div
          style={{
            display: 'inline-flex',
            gap: 'var(--md-sys-spacing-1)',
            padding: 'var(--md-sys-spacing-1)',
            borderRadius: 'var(--radius-pill)',
            backgroundColor: 'var(--md-sys-color-surface-container)',
          }}
        >
          {tabs.map((tab) => (
            <button
              key={tab}
              type="button"
              onClick={() => setActive(tab)}
              style={{
                position: 'relative',
                border: 'none',
                background: 'none',
                cursor: 'pointer',
                padding: 'var(--md-sys-spacing-2) var(--md-sys-spacing-4)',
                borderRadius: 'var(--radius-pill)',
                fontSize: 'var(--md-sys-typescale-label-size)',
                fontWeight: 550,
                color:
                  active === tab
                    ? 'var(--md-sys-color-on-primary-container)'
                    : 'var(--md-sys-color-on-surface-variant)',
              }}
            >
              <SharedIndicator
                active={active === tab}
                groupId="motion-showcase-tabs"
                radius="var(--radius-pill)"
              />
              <span style={{ position: 'relative' }}>{tab}</span>
            </button>
          ))}
        </div>
      </LayoutGroup>

      <LayoutTransformDemo />
      <StaggerList />
    </>
  );
};

/* ------------------------------------------------------- 7. Text animation */

const TextDemo: React.FC = () => {
  const [seed, setSeed] = useState(0);
  return (
    <>
      <Stage height={160}>
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            gap: 'var(--md-sys-spacing-4)',
            width: '100%',
          }}
        >
          <SplitText
            key={`chars-${seed}`}
            type="chars"
            trigger="load"
            style={{
              fontSize: 'var(--md-sys-typescale-page-title-size)',
              fontWeight: 680,
              letterSpacing: '-0.02em',
            }}
          >
            Split by character
          </SplitText>

          <SplitText
            key={`words-${seed}`}
            type="words"
            stagger="normal"
            trigger="load"
            style={{
              fontSize: 'var(--md-sys-typescale-section-title-size)',
              fontWeight: 620,
              color: 'var(--md-sys-color-on-surface-variant)',
            }}
          >
            Split by word, staggered in sequence
          </SplitText>

          <div style={{ display: 'flex', alignItems: 'baseline', gap: 'var(--md-sys-spacing-4)', flexWrap: 'wrap' }}>
            <ScrambleText
              key={`scramble-${seed}`}
              text="RESOLVING"
              style={{ fontSize: 'var(--md-sys-typescale-body-size)', fontWeight: 600 }}
            />
            <span
              style={{
                fontSize: 'var(--md-sys-typescale-metric-size)',
                fontWeight: 680,
                letterSpacing: '-0.02em',
              }}
            >
              <CountUp key={`count-${seed}`} end={6702000} prefix="Rp " locale="id-ID" />
            </span>
          </div>
        </div>
      </Stage>
      <Button variant="tonal" size="sm" onClick={() => setSeed((s) => s + 1)}>
        Replay
      </Button>
    </>
  );
};

/* -------------------------------------------------------- 8. SVG animation */

const SvgDemo: React.FC = () => {
  const [seed, setSeed] = useState(0);
  const [progress, setProgress] = useState(72);

  return (
    <>
      <Stage height={170}>
        <DrawSVG
          key={`line-${seed}`}
          d="M 0 96 C 34 96, 44 24, 76 40 S 118 96, 148 52 S 186 12, 200 28"
          width={220}
          height={120}
          viewBox="0 0 200 120"
          strokeWidth={3}
          label="Revenue trend"
        />

        <AnimatedCircularProgress key={`ring-${seed}`} value={progress} size={104} strokeWidth={12} label="Completion">
          <span
            style={{
              fontSize: 'var(--md-sys-typescale-metric-size-sm)',
              fontWeight: 680,
              fontVariantNumeric: 'tabular-nums',
            }}
          >
            {progress}%
          </span>
        </AnimatedCircularProgress>

        <SvgDrawMotion trigger="hover" />
      </Stage>
      <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-2)', flexWrap: 'wrap' }}>
        <Button variant="tonal" size="sm" onClick={() => setSeed((s) => s + 1)}>
          Redraw
        </Button>
        <Button
          variant="text"
          size="sm"
          onClick={() => setProgress(Math.round(Math.min(100, Math.max(8, progress + (Math.random() * 60 - 30)))))}
        >
          Change value
        </Button>
      </div>
    </>
  );
};

/* ---------------------------------------------------- 9. Scroll animations */

const ScrollDemo: React.FC = () => (
  <>
    <p
      style={{
        margin: 0,
        fontSize: 'var(--md-sys-typescale-body-size-sm)',
        color: 'var(--md-sys-color-on-surface-variant)',
      }}
    >
      Scroll this panel — each block reacts to its own position.
    </p>
    <div
      style={{
        height: 300,
        overflowY: 'auto',
        borderRadius: 'var(--radius-lg)',
        backgroundColor: 'var(--md-sys-color-surface-container)',
        padding: 'var(--md-sys-spacing-5)',
        display: 'flex',
        flexDirection: 'column',
        gap: 'var(--md-sys-spacing-6)',
      }}
    >
      <div style={{ height: 120 }} />

      <Reveal direction="up">
        <div style={cardStyle}>Reveal — fades and rises into view</div>
      </Reveal>

      <StaggerReveal
        stagger="normal"
        style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-spacing-2)' }}
      >
        <div style={cardStyle}>StaggerReveal · row 1</div>
        <div style={cardStyle}>StaggerReveal · row 2</div>
        <div style={cardStyle}>StaggerReveal · row 3</div>
      </StaggerReveal>

      <Parallax distance={20}>
        <div style={cardStyle}>Parallax — drifts against the scroll</div>
      </Parallax>

      <ScrollScale>
        <div style={cardStyle}>ScrollScale — settles as it enters</div>
      </ScrollScale>

      <div style={{ height: 160 }} />
    </div>
  </>
);

const cardStyle: React.CSSProperties = {
  padding: 'var(--md-sys-spacing-4)',
  borderRadius: 'var(--radius-md)',
  backgroundColor: 'var(--md-sys-color-surface)',
  border: '1px solid var(--md-sys-color-border)',
  fontSize: 'var(--md-sys-typescale-body-size-sm)',
  fontWeight: 550,
};

/* ------------------------------------------------- 10. Imperative playback */

const PlaybackDemo: React.FC = () => {
  const boxRef = useRef<HTMLDivElement>(null);
  const [controls, setControls] = useState<ReturnType<typeof animate> | null>(null);

  useEffect(() => {
    if (!boxRef.current) return;
    const playback = animate(
      boxRef.current,
      { x: [0, 220, 0], rotate: [0, 180, 360] },
      { duration: 3, repeat: Infinity, ease: 'linear' },
    );
    setControls(playback);
    return () => playback.stop();
  }, []);

  return (
    <>
      <Stage height={120}>
        <div style={{ width: '100%' }}>
          <div
            ref={boxRef}
            style={{
              width: 48,
              height: 48,
              borderRadius: 'var(--radius-md)',
              backgroundColor: 'var(--md-sys-color-primary)',
            }}
          />
        </div>
      </Stage>
      <MotionScrubber animation={controls} />
      <ParticleBurst />
    </>
  );
};

/* ------------------------------------------------------------- showcase */

export const MotionShowcase: React.FC = () => (
  <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--md-sys-gap-section)' }}>
    <div>
      <h1
        style={{
          margin: 0,
          fontSize: 'var(--md-sys-typescale-page-title-size)',
          fontWeight: 680,
          letterSpacing: 'var(--md-sys-typescale-page-title-tracking)',
        }}
      >
        14. Motion
      </h1>
      <div style={{ display: 'flex', gap: 'var(--md-sys-spacing-2)', marginTop: 'var(--md-sys-spacing-3)', flexWrap: 'wrap' }}>
        <Chip>Motion 13</Chip>
        <Chip>prefers-reduced-motion aware</Chip>
      </div>
    </div>

    <Section title="React animation">
      <ReactAnimationDemo />
    </Section>

    <Section title="Motion component">
      <MotionComponentDemo />
    </Section>

    <Section title="Transitions">
      <TransitionsDemo />
    </Section>

    <Section title="Springs">
      <SpringsDemo />
    </Section>

    <Section title="AnimatePresence">
      <PresenceDemo />
    </Section>

    <Section title="Layout animation">
      <LayoutDemo />
    </Section>

    <Section title="Text animation">
      <TextDemo />
    </Section>

    <Section title="SVG animation">
      <SvgDemo />
    </Section>

    <Section title="Scroll animations">
      <ScrollDemo />
    </Section>

    <Section title="animate() & playback">
      <PlaybackDemo />
    </Section>

    <MotionAdvancedSections />
  </div>
);
