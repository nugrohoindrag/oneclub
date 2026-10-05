import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { PAYOUTS_OPS_ROUTES, PAYOUTS_OPS_TILES, PAYOUTS_ROUTES } from './payouts';

// PRD P5 — payroll, statutory, service charge and payout runs (EP-09–15). Back Office routes, ops tiles and ops routes of the area
// (registered in p3/index.tsx and ops/p3.tsx).

export const PAYROLL_ROUTES: AreaRoute[] = [...PAYOUTS_ROUTES];
export const PAYROLL_OPS_TILES: OpsTile[] = [...PAYOUTS_OPS_TILES];
export const PAYROLL_OPS_ROUTES: OpsRoute[] = [...PAYOUTS_OPS_ROUTES];
