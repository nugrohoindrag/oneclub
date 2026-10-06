import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { useGet, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { PageHeader, SelectField, StatusPill, TextField } from '@oneclub/shell';
import { today, type R } from '../p1/common';

// Shared helpers of the Accounting screens (accounting.tsx, ar-ap.tsx).

export const API = '/api/v1/accounting';
export const ACC = [API];
export const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
export const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
export const dt = (v: unknown) => (v ? formatDate(String(v)) : '—');
export const items = (v: unknown) => (Array.isArray(v) ? (v as R[]) : []);
export const monthStart = () => `${today().slice(0, 8)}01`;
/** Club date n days from today (YYYY-MM-DD, local calendar; toISOString would shift a day east of UTC). */
export const addDays = (n: number) => {
  const d = new Date(`${today()}T12:00:00`);
  d.setDate(d.getDate() + n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};
export const rowsOf = <T,>(d: { items?: T[] } | undefined) => d?.items ?? [];

/** Account picker over the chart of accounts (posting accounts). */
export function AccountSelect({ label: l, value, onChange, required }: { label: string; value: string; onChange: (v: string) => void; required?: boolean }) {
  const accounts = useGet<Page<R>>(`${API}/accounts?limit=500&filter[isPosting]=true`);
  return (
    <SelectField label={l} value={value} onChange={onChange} required={required} placeholder="Choose an account"
      options={rowsOf(accounts.data).map((a) => ({ value: a.id, label: `${String(a.code)} · ${String(a.name)}` }))} />
  );
}

export function BankSelect({ label: l, value, onChange, kind }: { label: string; value: string; onChange: (v: string) => void; kind?: string }) {
  const banks = useGet<Page<R>>(`${API}/bank-accounts?limit=200${kind ? `&filter[kind]=${kind}` : ''}`);
  return (
    <SelectField label={l} value={value} onChange={onChange} placeholder="Choose"
      options={rowsOf(banks.data).map((b) => ({ value: b.id, label: `${String(b.name)} (${label(b.kind)})` }))} />
  );
}

export function DateRange({ from, to, setFrom, setTo }: { from: string; to: string; setFrom: (v: string) => void; setTo: (v: string) => void }) {
  return (
    <>
      <div style={{ width: 170 }}><TextField label="From" type="date" value={from} onChange={setFrom} /></div>
      <div style={{ width: 170 }}><TextField label="To" type="date" value={to} onChange={setTo} /></div>
    </>
  );
}

export function useTab(def: string): [string, (v: string) => void] {
  const [params, setParams] = useSearchParams();
  return [params.get('tab') ?? def, (v) => setParams({ tab: v }, { replace: true })];
}

/** An item of a page whose parts are sidebar items (Revenue & Billing, Tax …). */
export type SidebarTab = {
  value: string; label: string; help?: string;
  /** The content is one titled list that serves as the page header itself. */
  own?: boolean;
};

/**
 * Header of a page whose parts are sidebar items: no tab bar (the sidebar is
 * the navigation); the page title is the open item.
 */
export function SidebarTabs({ tabs, tab, actions }: { tabs: SidebarTab[]; tab: string; actions?: ReactNode }) {
  const t = tabs.find((x) => x.value === tab) ?? tabs[0];
  return t.own ? null : <PageHeader title={t.label} help={t.help} actions={actions} />;
}
