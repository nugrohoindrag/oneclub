/**
 * Morphic Design System — Motion components
 *
 * Spec §18 Motion Design, §19 Micro-interactions.
 * Every component here is powered by Motion (https://motion.dev) and honours
 * `prefers-reduced-motion` (§9).
 */

// React animation & AnimatePresence
export * from './Presence.js';

// Scroll animations
export * from './ScrollAnimation.js';

// Text animation
export * from './TextAnimation.js';

// SVG animation
export * from './SvgAnimation.js';

// Gestures & 3D transforms
export * from './Gestures.js';

// Drag, pan & reorder
export * from './Draggable.js';

// Viewport (whileInView), motion.create(), transformTemplate, layout props
export * from './InView.js';

// Playback controls
export * from './Playback.js';

// Layout animation demos
export * from './LayoutTransform.js';
export * from './StaggerList.js';
