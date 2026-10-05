import React, { useState } from 'react';
import { request, useGet, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import { Card, DataTable, Drawer, ErrorAlert, PageHeader, Skeleton, StatusPill } from '@oneclub/shell';
import { CheckoutModal } from './p2';
import { BANQUET_MEMBER_ROUTES } from './areas/banquet';
import { COMMERCIAL_MEMBER_ROUTES } from './areas/commercial';
import { ENGAGEMENT_MEMBER_ROUTES } from './areas/engagement';
import { TOURNAMENT_MEMBER_ROUTES } from './areas/tournament';
import { CRM_P5_MEMBER_ROUTES } from './areas/crm_p5';
import { LEISURE_MEMBER_ROUTES } from './areas/leisure';

// Member App P3 (PRD P3 EP-19): Transactions → Invoices with payment
// schedules and online payment of invoices and schedule lines (DP, termin)
// (FR-APP-P3-07).

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);

/** e-Faktur (faktur pajak) of my invoices (PRD P4 §7.3); empty when Accounting is off. */
function useMyEFaktur() {
  const { data } = useGet<Page<Row>>('/api/v1/member/invoice-efaktur', { retry: false });
  return new Map((data?.items ?? []).map((e) => [String(e.invoiceId), e]));
}

function EFakturCell({ e }: { e?: Row }) {
  if (!e) return <span className="oc-muted">—</span>;
  return e.fakturNumber ? <span>{String(e.fakturNumber)}</span> : <StatusPill status={String(e.status)} label={`e-Faktur ${String(e.status)}`} />;
}

export function MyInvoicesPage() {
  const invoices = useGet<Page<Row>>('/api/v1/member/invoices?limit=100');
  const efaktur = useMyEFaktur();
  const schedules = useGet<Page<Row & { lines: Row[] }>>('/api/v1/member/payment-schedules?limit=50');
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  // Online payment (gateway checkout) of an invoice or of a DP / termin of a
  // payment schedule (FR-APP-P3-07); the body is the method only.
  const payOnline = async (path: string) => {
    setBusy(true);
    setError(null);
    try {
      setCheckout(await request<Schemas['Payment']>('POST', path, { method: 'qris' }));
      void invoices.refetch();
      void schedules.refetch();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Invoices" />
      <ErrorAlert error={error} />
      <Card title="My Invoices" icon="receipt_long">
        <DataTable rows={invoices.data?.items} loading={invoices.isLoading} onRowClick={(r) => setOpen(String(r.id))} columns={[
          { key: 'number', header: 'Invoice' }, { key: 'issueDate', header: 'Issued', render: (r) => formatDate(String(r.issueDate)) },
          { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) }, { key: 'total', header: 'Total', render: (r) => money(r.total) },
          { key: 'outstanding', header: 'To pay', render: (r) => money(r.outstanding) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> },
          { key: 'efaktur', header: 'Faktur pajak', render: (r) => <EFakturCell e={efaktur.get(String(r.id))} /> }]}
          actions={(r) => ['issued', 'partially_paid', 'overdue'].includes(String(r.status)) ? (
            <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={busy} onClick={() => void payOnline(`/api/v1/member/invoices/${String(r.id)}:pay-online`)}>Pay</button>
          ) : null} />
      </Card>
      {(schedules.data?.items.length ?? 0) > 0 && schedules.data?.items.map((s) => (
        <Card key={String(s.id)} title={`${String(s.title)} · ${String(s.number)}`} icon="event_repeat" actions={<StatusPill status={String(s.status)} />}>
          <DataTable rows={s.lines} columns={[{ key: 'label', header: 'Due item' }, { key: 'dueDate', header: 'Due', render: (l) => formatDate(String(l.dueDate)) },
            { key: 'amount', header: 'Amount', render: (l) => money(l.amount) }, { key: 'paidAmount', header: 'Paid', render: (l) => money(l.paidAmount) },
            { key: 'status', header: 'Status', render: (l) => <StatusPill status={String(l.status)} /> }]}
            actions={(l) => s.status === 'active' && ['pending', 'partially_paid', 'overdue'].includes(String(l.status)) ? (
              <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={busy} aria-label={`Pay ${String(l.label)}`}
                onClick={() => void payOnline(`/api/v1/member/payment-schedules/${String(s.id)}/lines/${String(l.id)}:pay-online`)}>Pay</button>
            ) : null} />
        </Card>
      ))}
      {open && <InvoiceDetail id={open} efaktur={efaktur.get(open)} onClose={() => setOpen(null)} />}
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </div>
  );
}

function InvoiceDetail({ id, efaktur, onClose }: { id: string; efaktur?: Row; onClose: () => void }) {
  const d = useGet<Row & { lines: Row[] }>(`/api/v1/member/invoices/${id}`);
  const x = d.data;
  return (
    <Drawer open onClose={onClose} title={x ? `Invoice ${String(x.number)}` : 'Invoice'}>
      {d.isLoading && <Skeleton />}
      {x && (
        <div className="oc-stack">
          <div className="oc-row"><StatusPill status={String(x.status)} /><span className="oc-spacer" /><strong>{money(x.total)}</strong></div>
          <DataTable rows={x.lines} columns={[{ key: 'description', header: 'Description' }, { key: 'total', header: 'Total', render: (l) => money(l.total) }]} />
          <div className="oc-row oc-small"><span className="oc-muted">Outstanding</span><span className="oc-spacer" /><strong>{money(x.outstanding)}</strong></div>
          {efaktur && <div className="oc-row oc-small"><span className="oc-muted">Faktur pajak (e-Faktur)</span><span className="oc-spacer" />
            {efaktur.fakturNumber ? <strong>{String(efaktur.fakturNumber)}</strong> : null}
            <StatusPill status={String(efaktur.status)} label={`e-Faktur ${String(efaktur.status)}`} /></div>}
        </div>
      )}
    </Drawer>
  );
}

/** Member App routes of PRD P3 (each area adds its pages from src/areas). */
export const P3_MEMBER_ROUTES = [
  { path: 'transactions/invoices', element: <MyInvoicesPage /> },
  ...BANQUET_MEMBER_ROUTES, ...TOURNAMENT_MEMBER_ROUTES, ...COMMERCIAL_MEMBER_ROUTES, ...ENGAGEMENT_MEMBER_ROUTES,
  ...CRM_P5_MEMBER_ROUTES, ...LEISURE_MEMBER_ROUTES,
];
