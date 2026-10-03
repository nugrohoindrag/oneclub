/**
 * Morphic Design System — Core Components
 *
 * Spec §17 Final Architecture. Core stays domain-neutral (§4): no industry
 * vocabulary lives here. Industry compositions belong to `src/examples`.
 */

// 02. Primitives & Layout
export * from './layout/index.js';

// 04. Navigation
export * from './navigation/index.js';

// 03. Actions
export * from './actions/index.js';

// 05. Forms & Inputs
export * from './inputs/index.js';

// 06. Selection
export * from './selection/index.js';

// 07. Surfaces & Containers
export * from './containment/index.js';

// 08. Data Display
export * from './data-display/index.js';
export * from './entity/index.js';

// 09. Data Visualization
export * from './visualization/index.js';

// 10. Feedback & Status
export * from './feedback/index.js';
export * from './communication/index.js';

// 11. Overlays
export * from './overlays/index.js';

// 13. Utilities
export * from './utility/index.js';

// Motion (§18) — components, scroll, layout, text, SVG, presence, gestures
export * from './motion/index.js';
