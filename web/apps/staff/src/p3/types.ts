import type React from 'react';

/** A Back Office route of an area, guarded by its view permission. */
export type AreaRoute = { path: string; perm: string; element: React.ReactNode };

/** A workstation tile on the ops home: [icon, label, path, permission]. */
export type OpsTile = [icon: string, label: string, to: string, perm: string];

/** A route of the ops area (`/ops/...`). */
export type OpsRoute = { path: string; element: React.ReactNode };
