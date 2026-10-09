import React, { useState } from 'react';
import { Link } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { useOnline } from '@oneclub/offline';
import { ErrorAlert, Icon, useAuth, useToast } from '@oneclub/shell';
import { MoneyInput } from '@oneclub/ui';
import { OUTLET_KEY, write } from '../offline';
import { OpenShift } from './pay';
import { CustomerSelect } from './tables';
import { money, useOutlet, useOutletId, useShift, type Row, type TableState } from './shared';

// POS Settings: the outlet of the terminal, the cashier shift (X report,
// cash in / out, close with the Z report) and today's table reservations.

/** First screen of a terminal: the outlet it sells for. */
export function OutletChooser({ onChosen }: { onChosen?: () => void }) {
  const outlets = useGet<Page<Row>>('/api/v1/commercial/outlets?filter[status]=active&limit=100');
  const [current, setCurrent] = useState(useOutletId());
  return (
    <div className="pos-outlets">
      {(outlets.data?.items ?? []).map((o) => (
        <button key={String(o.id)} className="pos-outlet" aria-pressed={current === o.id} onClick={() => {
          write(OUTLET_KEY, String(o.id));
          setCurrent(String(o.id));
          onChosen?.();
        }}>
          <span className="pos-tablebar-icon"><Icon name={o.outletType === 'retail' ? 'sports_golf' : o.outletType === 'bar' ? 'local_bar' : 'restaurant'} size={22} /></span>
          <span><strong>{String(o.name)}</strong><br /><span className="pos-muted">{String(o.code)}</span></span>
        </button>
      ))}
      {outlets.data?.items.length === 0 && <p className="pos-muted">No outlets are set up for this property.</p>}
    </div>
  );
}

function ShiftSection({ outletId }: { outletId: string }) {
  const toast = useToast();
  const { shift, refetch } = useShift(outletId);
  const sid = String(shift?.id ?? '');
  const report = useGet<Row>(sid ? `/api/v1/commercial/shifts/${sid}/report` : null);
  const move = useSend<Row, Row>('POST', () => `/api/v1/commercial/shifts/${sid}/cash-movements`, ['/api/v1/commercial/shifts']);
  const close = useSend<Row, Row>('POST', () => `/api/v1/commercial/shifts/${sid}:close`, ['/api/v1/commercial/shifts']);
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [counted, setCounted] = useState('');
  const [z, setZ] = useState<Row | null>(null);
  if (!shift) {
    return (
      <div className="pos-section">
        <h2>Cashier shift</h2>
        {z && <p className="pos-muted">Shift closed · Z report: sales {money(z.sales)}, expected cash {money(z.expectedCash)}.</p>}
        <OpenShift outletId={outletId} onOpened={() => { setZ(null); void refetch(); toast('Shift opened'); }} />
      </div>
    );
  }
  const r = report.data;
  return (
    <div className="pos-section">
      <h2>Cashier shift {String(shift.shiftNo)}</h2>
      <div className="pos-kv">
        <div><span>Opened</span><strong>{formatDateTime(String(shift.openedAt))}</strong></div>
        <div><span>Orders</span><strong>{String(r?.orders ?? '–')}</strong></div>
        <div><span>Sales</span><strong>{money(r?.sales)}</strong></div>
        <div><span>Cash payments</span><strong>{money(r?.cashPayments)}</strong></div>
        <div><span>Cash in / out</span><strong>{money(r?.cashIn)} / {money(r?.cashOut)}</strong></div>
        <div><span>Expected cash</span><strong>{money(r?.expectedCash)}</strong></div>
      </div>
      <h2 style={{ marginTop: 22 }}>Cash in / out</h2>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <MoneyInput className="pos-input" style={{ width: 180 }} placeholder="Amount" aria-label="Amount" value={amount} onChange={setAmount} />
        <input className="pos-input" style={{ flex: 1, minWidth: 200 }} placeholder="Reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        {(['cash_in', 'cash_out'] as const).map((k) => (
          <button key={k} className="pos-btn" data-variant="outline" disabled={!amount || !reason || move.isPending}
            onClick={() => move.mutate({ kind: k, amount, reason }, { onSuccess: () => { setAmount(''); setReason(''); void report.refetch(); toast('Recorded'); } })}>
            {k === 'cash_in' ? 'Cash In' : 'Cash Out'}</button>
        ))}
      </div>
      <h2 style={{ marginTop: 22 }}>Close shift</h2>
      <div style={{ display: 'flex', gap: 12 }}>
        <MoneyInput className="pos-input" style={{ width: 220 }} placeholder={`Counted cash (${money(r?.expectedCash)})`} aria-label="Counted cash" value={counted}
          onChange={setCounted} />
        <button className="pos-btn" disabled={counted === '' || close.isPending}
          onClick={() => close.mutate({ countedCash: counted }, { onSuccess: (res) => { setZ(res); setCounted(''); void refetch(); toast('Shift closed'); } })}>Close Shift</button>
      </div>
      <ErrorAlert error={move.error ?? close.error} />
    </div>
  );
}

function ReservationsSection({ outletId }: { outletId: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const day = new Date();
  day.setHours(0, 0, 0, 0);
  const list = useGet<Page<Row>>(`/api/v1/commercial/table-reservations${qs({ 'filter[outletId]': outletId, 'filter[status]': 'booked', limit: 100 })}`);
  const tables = useGet<Page<TableState>>(`/api/v1/commercial/outlets/${outletId}/tables`);
  const create = useSend<Row, Row>('POST', '/api/v1/commercial/table-reservations', ['/api/v1/commercial/table-reservations']);
  const [patchError, setPatchError] = useState<unknown>(null);
  const setStatus = (id: string, status: string) => request('PATCH', `/api/v1/commercial/table-reservations/${id}`, { status })
    .then(() => { setPatchError(null); void list.refetch(); void tables.refetch(); }, setPatchError);
  const [form, setForm] = useState({ time: '19:00', guests: 2, name: '', customerId: '', phone: '', tableIds: [] as string[] });
  const code = new Map((tables.data?.items ?? []).map((t) => [t.id, t.code]));
  const upcoming = (list.data?.items ?? []).filter((r) => new Date(String(r.reservedFor)) >= day);
  const submit = () => {
    const at = new Date();
    const [h, m] = form.time.split(':').map(Number);
    at.setHours(h, m, 0, 0);
    create.mutate({ outletId, reservedFor: at.toISOString(), guestCount: form.guests, guestName: form.name || undefined, customerId: form.customerId || undefined,
      phone: form.phone || undefined, tableIds: form.tableIds }, { onSuccess: () => { setForm({ ...form, name: '', customerId: '', phone: '', tableIds: [] }); toast('Table reserved'); } });
  };
  return (
    <div className="pos-section">
      <h2>Table reservations today</h2>
      {upcoming.length === 0 && <p className="pos-muted">No reservations.</p>}
      {upcoming.map((r) => (
        <div key={String(r.id)} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '8px 0', borderBottom: '1px solid var(--pos-line)' }}>
          <strong style={{ width: 60 }}>{new Date(String(r.reservedFor)).toTimeString().slice(0, 5)}</strong>
          <span style={{ flex: 1 }}>{String(r.guestName)} · {String(r.guestCount)} guests · {((r.tableIds as string[]) ?? []).map((t) => code.get(t) ?? '?').join(', ') || 'no table'}</span>
          {can('commercial.table_reservation.update') && <>
            <button className="pos-btn" data-variant="soft" data-size="sm" onClick={() => void setStatus(String(r.id), 'no_show')}>No-show</button>
            <button className="pos-btn" data-variant="soft" data-size="sm" onClick={() => void setStatus(String(r.id), 'cancelled')}>Cancel</button>
          </>}
        </div>
      ))}
      {can('commercial.table_reservation.create') && (
        <>
          <h2 style={{ marginTop: 22 }}>New reservation</h2>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 12 }}>
            <input className="pos-input" type="time" value={form.time} onChange={(e) => setForm({ ...form, time: e.target.value })} aria-label="Time" />
            <input className="pos-input" type="number" min={1} value={form.guests} onChange={(e) => setForm({ ...form, guests: Number(e.target.value) || 1 })} aria-label="Guests" />
            <input className="pos-input" placeholder="Guest name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            <input className="pos-input" placeholder="Phone" value={form.phone} onChange={(e) => setForm({ ...form, phone: e.target.value })} />
          </div>
          {can('crm.customer.view') && <div style={{ marginTop: 12 }}><CustomerSelect value={form.customerId} label={form.name}
            onChange={(id, name) => setForm({ ...form, customerId: id, name })} /></div>}
          <div className="pos-guests" style={{ flexWrap: 'wrap', marginTop: 12 }} role="group" aria-label="Tables">
            {(tables.data?.items ?? []).map((t) => (
              <button key={t.id} type="button" className="pos-guest" data-wide aria-pressed={form.tableIds.includes(t.id)}
                onClick={() => setForm({ ...form, tableIds: form.tableIds.includes(t.id) ? form.tableIds.filter((x) => x !== t.id) : [...form.tableIds, t.id] })}>{t.code}</button>
            ))}
          </div>
          <button className="pos-btn" style={{ marginTop: 14 }} disabled={(!form.name && !form.customerId) || create.isPending} onClick={submit}>Reserve</button>
        </>
      )}
      <ErrorAlert error={create.error ?? patchError ?? list.error} />
    </div>
  );
}

export function SettingsPage() {
  const outletId = useOutletId();
  const outlet = useOutlet(outletId);
  const online = useOnline();
  const { logout } = useAuth();
  const [, refresh] = useState(0);
  return (
    <>
      <div className="pos-head"><h1>Settings</h1><span className="pos-spacer" />
        <span className="pos-muted">{online ? 'Online' : 'Offline'}</span>
        <Link className="pos-btn" data-variant="soft" data-size="sm" to="/ops/sync">Sync Queue</Link>
        <button className="pos-btn" data-variant="soft" data-size="sm" onClick={() => void logout()}><Icon name="logout" size={18} />Log out</button>
      </div>
      <div className="pos-body">
        <div className="pos-section">
          <h2>Outlet{outlet.data ? `: ${String(outlet.data.name)}` : ''}</h2>
          <OutletChooser onChosen={() => refresh((n) => n + 1)} />
        </div>
        {outletId && <ShiftSection key={`s${outletId}`} outletId={outletId} />}
        {outletId && <ReservationsSection key={`r${outletId}`} outletId={outletId} />}
      </div>
    </>
  );
}
