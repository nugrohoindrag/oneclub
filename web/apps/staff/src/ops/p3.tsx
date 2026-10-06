import React from 'react';
import { Link } from 'react-router';
import { Icon, useAuth } from '@oneclub/shell';
import { BANQUET_OPS_ROUTES, BANQUET_OPS_TILES } from '../p3/banquet';
import { CashierPage, CustomerFoliosPage } from '../p3/billing';
import { COMMERCIAL_P3_OPS_ROUTES, COMMERCIAL_P3_OPS_TILES } from '../p3/commercial';
import { ENGAGEMENT_OPS_ROUTES, ENGAGEMENT_OPS_TILES } from '../p3/engagement';
import { TOURNAMENT_OPS_ROUTES, TOURNAMENT_OPS_TILES } from '../p3/tournament';
import type { OpsRoute, OpsTile } from '../p3/types';
import { ACCOUNTING_OPS_ROUTES, ACCOUNTING_OPS_TILES } from '../p4/accounting';
import { CMS_OPS_ROUTES, CMS_OPS_TILES } from '../p4/cms';
import { INVENTORY_OPS_ROUTES, INVENTORY_OPS_TILES } from '../p4/inventory';
import { PROCUREMENT_OPS_ROUTES, PROCUREMENT_OPS_TILES } from '../p4/procurement';
import { BI_OPS_ROUTES, BI_OPS_TILES } from '../p5/bi';
import { CRM_P5_OPS_ROUTES, CRM_P5_OPS_TILES } from '../p5/crm';
import { HR_OPS_ROUTES, HR_OPS_TILES } from '../p5/hr';
import { HR_TIME_OPS_ROUTES, HR_TIME_OPS_TILES } from '../p5/hr_time';
import { LEISURE_OPS_ROUTES, LEISURE_OPS_TILES } from '../p5/leisure';
import { PAYROLL_OPS_ROUTES, PAYROLL_OPS_TILES } from '../p5/payroll';

// Operational interfaces of PRD P3 (EP-21, §7.2) and PRD P4: Front Desk
// cashier shift across lines and folios per customer; the event, tournament,
// POS, kitchen and warehouse workstations of the areas join from their files.

const TILES: OpsTile[] = [
  ['point_of_sale', 'Cashier', '/ops/front-desk/cashier', 'billing.cashier_shift.operate'],
  ...BANQUET_OPS_TILES, ...TOURNAMENT_OPS_TILES, ...COMMERCIAL_P3_OPS_TILES, ...ENGAGEMENT_OPS_TILES,
  ...INVENTORY_OPS_TILES, ...PROCUREMENT_OPS_TILES, ...ACCOUNTING_OPS_TILES, ...CMS_OPS_TILES,
  ...HR_OPS_TILES, ...HR_TIME_OPS_TILES, ...PAYROLL_OPS_TILES, ...CRM_P5_OPS_TILES, ...BI_OPS_TILES, ...LEISURE_OPS_TILES,
];

const ROUTES: OpsRoute[] = [
  { path: 'front-desk/cashier', element: <CashierPage /> },
  { path: 'front-desk/customer-folios', element: <CustomerFoliosPage /> },
  ...BANQUET_OPS_ROUTES, ...TOURNAMENT_OPS_ROUTES, ...COMMERCIAL_P3_OPS_ROUTES, ...ENGAGEMENT_OPS_ROUTES,
  ...INVENTORY_OPS_ROUTES, ...PROCUREMENT_OPS_ROUTES, ...ACCOUNTING_OPS_ROUTES, ...CMS_OPS_ROUTES,
  ...HR_OPS_ROUTES, ...HR_TIME_OPS_ROUTES, ...PAYROLL_OPS_ROUTES, ...CRM_P5_OPS_ROUTES, ...BI_OPS_ROUTES, ...LEISURE_OPS_ROUTES,
];

export const P3_OPS_ROUTES = ROUTES;

/** Home tiles of the P3 workstations. */
export function P3Tiles() {
  const { can } = useAuth();
  const shown = TILES.filter((t) => can(t[3]));
  if (shown.length === 0) return null;
  return (
    <div className="oc-grid">
      {shown.map(([icon, label, to]) => (
        <Link key={label} to={to} className="oc-card" style={{ minHeight: 120, textDecoration: 'none' }}>
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name={icon} size={22} /></span><h3>{label}</h3></div>
        </Link>
      ))}
    </div>
  );
}
