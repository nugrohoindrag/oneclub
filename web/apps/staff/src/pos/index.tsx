import React, { useEffect, useState } from 'react';
import { NavLink, Outlet, useNavigate, useRoutes } from 'react-router';
import { useQueryClient } from '@tanstack/react-query';
import { useOnline } from '@oneclub/offline';
import { Icon, logoOf, useAuth, useBootstrap } from '@oneclub/shell';
import { HistoryPage, OnCoursePage, PaymentConfirmPage } from './orders';
import { MenuPage } from './menu';
import { OutletChooser, SettingsPage } from './settings';
import { TableViewPage } from './tables';
import { request } from '@oneclub/api-client';
import { clearLocal, rememberTaxFactor, useLocalOrders, usePendingSales } from './offline';
import { useOutletId } from './shared';
import './pos.css';

/*
 * POS Cashier (PRD P2 EP-20, product owner design 7 Oct 2026): a full-screen
 * app inside the Operational area — Table View, menu with Current Order,
 * Payment Confirm, Order History and Settings (outlet, shift, reservations).
 */

/** Offline support of the POS: the tax factor for offline estimates; once the sync queue is sent, the offline changes give way to the server data. */
function useSyncedLocal() {
  const outlet = useOutletId();
  const online = useOnline();
  const pending = usePendingSales();
  const locals = useLocalOrders(outlet);
  const { propertyId } = useAuth();
  const qc = useQueryClient();
  useEffect(() => { // tax & service of the outlet for offline estimates
    if (!online || !outlet) return;
    void request<{ total: string }>('POST', `/api/v1/commercial/outlets/${outlet}:quote`, { amount: '100000' })
      .then((b) => rememberTaxFactor(outlet, Number(b.total) / 100000), () => undefined);
  }, [online, outlet]);
  useEffect(() => {
    if (!online || pending > 0 || locals.length === 0) return;
    void clearLocal(propertyId, outlet).then(() => qc.invalidateQueries());
  }, [online, pending, locals.length, propertyId, outlet, qc]);
}

function PosLayout() {
  const b = useBootstrap();
  const nav = useNavigate();
  useSyncedLocal();
  return (
    <div className="pos">
      <nav className="pos-rail" aria-label="POS">
        <span className="pos-rail-logo"><img src={logoOf(b.branding)} alt={b.branding.appName} /></span>
        <NavLink to="/ops/pos" end aria-label="Table View" title="Table View"><Icon name="home" size={26} /></NavLink>
        <NavLink to="/ops/pos/payments" aria-label="Payment Confirm" title="Payment Confirm"><Icon name="receipt_long" size={26} /></NavLink>
        <NavLink to="/ops/pos/on-course" aria-label="On-course orders" title="On-course orders"><Icon name="sports_golf" size={26} /></NavLink>
        <NavLink to="/ops/pos/history" aria-label="Order History" title="Order History"><Icon name="history" size={26} /></NavLink>
        <NavLink to="/ops/pos/settings" aria-label="Settings" title="Settings"><Icon name="settings" size={26} /></NavLink>
        <span className="pos-spacer" />
        <button onClick={() => nav('/ops')} aria-label="Leave POS" title="Leave POS"><Icon name="logout" size={26} /></button>
      </nav>
      <main className="pos-main"><Outlet /></main>
    </div>
  );
}

function RequireOutlet({ children }: { children: React.ReactNode }) {
  const [, refresh] = useState(0);
  if (useOutletId()) return <>{children}</>;
  return (
    <>
      <div className="pos-head"><h1>Choose the outlet of this terminal</h1></div>
      <div className="pos-body"><OutletChooser onChosen={() => refresh((n) => n + 1)} /></div>
    </>
  );
}

const routes = [
  {
    element: <PosLayout />,
    children: [
      { index: true, element: <RequireOutlet><TableViewPage /></RequireOutlet> },
      { path: 'order', element: <RequireOutlet><MenuPage /></RequireOutlet> },
      { path: 'order/:id', element: <RequireOutlet><MenuPage /></RequireOutlet> },
      { path: 'payments', element: <RequireOutlet><PaymentConfirmPage /></RequireOutlet> },
      { path: 'history', element: <RequireOutlet><HistoryPage /></RequireOutlet> },
      { path: 'on-course', element: <RequireOutlet><OnCoursePage /></RequireOutlet> },
      { path: 'settings', element: <SettingsPage /> },
    ],
  },
];

export default function PosApp() {
  return useRoutes(routes);
}
