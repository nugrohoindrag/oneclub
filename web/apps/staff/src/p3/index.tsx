import React from 'react';
import { RequirePermission } from '@oneclub/shell';
import { BILLING_P3_ROUTES } from './billing';

// Back Office routes of PRD P3 (Commercial & Business Expansion) and PRD P4
// (Enterprise Back Office), one module per file at the paths of the server
// navigation (internal/platform/navigation).

type AreaRoute = { path: string; perm: string; element: React.ReactNode };

const AREAS: AreaRoute[][] = [BILLING_P3_ROUTES];

export const P3_ROUTES = AREAS.flat().map((r) => ({ path: r.path, element: <RequirePermission perm={r.perm}>{r.element}</RequirePermission> }));
