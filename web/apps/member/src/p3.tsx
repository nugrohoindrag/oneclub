import React, { useState } from 'react';
import { useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import { Card, DataTable, Drawer, ErrorAlert, PageHeader, Skeleton, StatusPill } from '@oneclub/shell';
import { CheckoutModal } from './p2';

// Member App P3 (PRD P3 EP-19): Transactions → Invoices with payment
// schedules and online payment (FR-APP-P3-07).

type Row = Record<string, unknown>;
const money = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : `Rp ${formatNumber(Number(v))}`);

export function MyInvoicesPage() {
  const invoices = useGet<Page<Row>>('/api/v1/member/invoices?limit=100');
  const schedules = useGet<Page<Row & { lines: Row[] }>>('/api/v1/member/payment-schedules?limit=50');
  const pay = useSend<Row, Schemas['Payment']>('POST', (b) => `/api/v1/member/invoices/${String(b.id)}:pay-online`, ['/api/v1/member/invoices']);
  const [checkout, setCheckout] = useState<Schemas['Payment'] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Invoices" />
      <ErrorAlert error={pay.error} />
      <Card title="My Invoices" icon="receipt_long">
        <DataTable rows={invoices.data?.items} loading={invoices.isLoading} onRowClick={(r) => setOpen(String(r.id))} columns={[
          { key: 'number', header: 'Invoice' }, { key: 'issueDate', header: 'Issued', render: (r) => formatDate(String(r.issueDate)) },
          { key: 'dueDate', header: 'Due', render: (r) => formatDate(String(r.dueDate)) }, { key: 'total', header: 'Total', render: (r) => money(r.total) },
          { key: 'outstanding', header: 'To pay', render: (r) => money(r.outstanding) }, { key: 'status', header: 'Status', render: (r) => <StatusPill status={String(r.status)} /> }]}
          actions={(r) => ['issued', 'partially_paid', 'overdue'].includes(String(r.status)) ? (
            <button className="oc-btn oc-btn-primary oc-btn-sm" disabled={pay.isPending} onClick={() => pay.mutate({ id: r.id, method: 'qris' }, { onSuccess: (c) => setCheckout(c) })}>Pay</button>
          ) : null} />
      </Card>
      {(schedules.data?.items.length ?? 0) > 0 && schedules.data?.items.map((s) => (
        <Card key={String(s.id)} title={`${String(s.title)} · ${String(s.number)}`} icon="event_repeat" actions={<StatusPill status={String(s.status)} />}>
          <DataTable rows={s.lines} columns={[{ key: 'label', header: 'Due item' }, { key: 'dueDate', header: 'Due', render: (l) => formatDate(String(l.dueDate)) },
            { key: 'amount', header: 'Amount', render: (l) => money(l.amount) }, { key: 'paidAmount', header: 'Paid', render: (l) => money(l.paidAmount) },
            { key: 'status', header: 'Status', render: (l) => <StatusPill status={String(l.status)} /> }]} />
        </Card>
      ))}
      {open && <InvoiceDetail id={open} onClose={() => setOpen(null)} />}
      <CheckoutModal checkout={checkout} onClose={() => setCheckout(null)} />
    </div>
  );
}

function InvoiceDetail({ id, onClose }: { id: string; onClose: () => void }) {
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
        </div>
      )}
    </Drawer>
  );
}

/** Member App routes of PRD P3. */
export const P3_MEMBER_ROUTES = [
  { path: 'transactions/invoices', element: <MyInvoicesPage /> },
];
