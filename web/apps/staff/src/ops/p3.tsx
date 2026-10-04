import React from 'react';
import { Link } from 'react-router';
import { Icon, useAuth } from '@oneclub/shell';
import { CashierPage, CustomerFoliosPage } from '../p3/billing';

// Operational interfaces of PRD P3 (EP-21, §7.2): Front Desk cashier shift
// across lines and folios per customer; the event, tournament, POS and
// kitchen workstations of the P3 areas join here.

type Tile = [icon: string, label: string, to: string, perm: string];

const TILES: Tile[] = [
  ['point_of_sale', 'Cashier', '/ops/front-desk/cashier', 'billing.cashier_shift.operate'],
];

const ROUTES: { path: string; element: React.ReactNode }[] = [
  { path: 'front-desk/cashier', element: <CashierPage /> },
  { path: 'front-desk/customer-folios', element: <CustomerFoliosPage /> },
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
