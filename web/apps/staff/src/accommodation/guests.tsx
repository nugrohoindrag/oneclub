import React, { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SearchBox, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField,
  useAuth, useDebounced, useToast,
} from '@oneclub/shell';
import { KV, Tabs } from '../p1/common';
import { INV, StayDrawer, dayOf, label, money, useStayColumns, type GuestRequest, type Row, type Stay } from './shared';
import './accommodation.css';

// Guests (requirements §14): the CRM customer is the central record; the
// accommodation profile (address, identity, date of birth, nationality,
// preferences, notes, VIP) and the guest history — stays, nights, spending,
// favourite room type, requests, cancellations, no-shows, payments.

interface GuestSummary { customerId: string; name: string; phone: string | null; email: string | null; vip: boolean; nationality: string | null; totalStays: number;
  totalNights: number; lastStay: string | null; nextStay: string | null; totalSpending: string; cancellations: number; noShows: number; inHouse: boolean }
interface Profile { customerId: string; address: string | null; idType: string | null; idNumberMasked: string | null; dateOfBirth: string | null; nationality: string | null;
  preferences: string | null; notes: string | null; vip: boolean }
interface GuestDetail { customer: Row & { name: string; code: string; email: string; phone: string; birthDate: string | null }; profile: Profile; summary: GuestSummary;
  favoriteRoomType: string | null; stays: Stay[]; requests: GuestRequest[]; payments: (Row & { number: string; stayNo: string; methodType: string; purpose: string; amount: string;
  refunded: string; status: string; paidAt: string | null })[]; crmPreferences: { category: string; key: string; value: string | null }[] }

export function GuestsPage() {
  const nav = useNavigate();
  const [q, setQ] = useState('');
  const [vip, setVip] = useState(false);
  const query = useDebounced(q);
  const list = useGet<Page<GuestSummary>>(`/api/v1/stay/guests${qs({ q: query, vip: vip || undefined, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Guests" help="Guests who booked a bungalow, with their stay history. New guests are created from a reservation (central customer record)." />
      <div className="oc-row-wrap" style={{ alignItems: 'center' }}>
        <SearchBox value={q} onChange={setQ} placeholder="Name, phone, e-mail" />
        <Checkbox label="VIP only" checked={vip} onChange={setVip} />
      </div>
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Row[]} loading={list.isLoading} error={list.error} rowKey={(g) => String(g.customerId)}
          onRowClick={(g) => nav(`/accommodation/guests/${g.customerId}`)} empty={<Empty title="No guests yet" icon="person" />}
          columns={[
            { key: 'name', header: 'Guest', render: (g) => <>{g.vip ? <span className="acc-vip">VIP</span> : null}<strong>{String(g.name)}</strong>{g.inHouse ? <span className="oc-small"> · in-house</span> : null}
              <div className="oc-small oc-muted">{[g.phone, g.email].filter(Boolean).join(' · ')}</div></> },
            { key: 'totalStays', header: 'Stays', align: 'right' }, { key: 'totalNights', header: 'Nights', align: 'right' },
            { key: 'lastStay', header: 'Last stay', render: (g) => dayOf(g.lastStay as string | null) }, { key: 'nextStay', header: 'Next stay', render: (g) => dayOf(g.nextStay as string | null) },
            { key: 'totalSpending', header: 'Spending', align: 'right', render: (g) => money(g.totalSpending) },
            { key: 'cancellations', header: 'Cancel / no-show', render: (g) => `${g.cancellations} / ${g.noShows}` }]} />
      </div>
    </div>
  );
}

export function GuestProfilePage() {
  const { id } = useParams();
  const { can } = useAuth();
  const [tab, setTab] = useState('stays');
  const [edit, setEdit] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const g = useGet<GuestDetail>(`/api/v1/stay/guests/${id}`);
  const cols = useStayColumns({ payment: false });
  if (g.isLoading) return <Skeleton rows={10} />;
  if (!g.data) return <ErrorAlert error={g.error} />;
  const x = g.data;
  const p = x.profile;
  return (
    <div className="oc-stack">
      <PageHeader title={x.customer.name} help={[x.customer.code, x.customer.phone, x.customer.email].filter(Boolean).join(' · ')}
        actions={<>
          {can('crm.customer_overview.view') && <Link className="oc-btn oc-btn-neutral" to={`/crm/customer-360?id=${id}`}>Customer 360</Link>}
          {can('stay.guest.update') && <button className="oc-btn oc-btn-primary" onClick={() => setEdit(true)}><Icon name="edit" size={18} /> Edit profile</button>}
          <Link className="oc-btn oc-btn-neutral" to="/accommodation/guests">Guests</Link></>} />
      <div className="oc-row-wrap">
        <StatTile label="Total stays" value={x.summary.totalStays} icon="hotel" />
        <StatTile label="Total nights" value={x.summary.totalNights} icon="bedtime" />
        <StatTile label="Total spending" value={money(x.summary.totalSpending)} icon="payments" />
        <StatTile label="Last stay" value={dayOf(x.summary.lastStay)} icon="history" />
        <StatTile label="Favourite room type" value={x.favoriteRoomType ?? '—'} icon="star" />
        <StatTile label="Cancellations / no-shows" value={`${x.summary.cancellations} / ${x.summary.noShows}`} icon="event_busy" />
      </div>
      <div className="oc-grid-2">
        <Card title="Profile" icon="badge">
          <KV items={[['VIP', p.vip ? <StatusPill key="v" status="vip" label="VIP" tone="warning" /> : 'No'], ['Address', p.address ?? '—'], ['Identity', p.idType ? `${p.idType.toUpperCase()} ${p.idNumberMasked ?? ''}` : '—'],
            ['Date of birth', p.dateOfBirth ? formatDate(p.dateOfBirth) : x.customer.birthDate ? formatDate(x.customer.birthDate) : '—'], ['Nationality', p.nationality ?? '—']]} />
        </Card>
        <Card title="Preferences & notes" icon="favorite">
          <KV items={[['Preferences', p.preferences ?? '—'], ['Notes', p.notes ?? '—'],
            ...x.crmPreferences.map((c) => [`${label(c.category)} · ${c.key}`, c.value ?? '—'] as [string, React.ReactNode])]} />
        </Card>
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'stays', label: `Bookings & stays (${x.stays.length})` }, { value: 'requests', label: `Requests (${x.requests.length})` },
        { value: 'payments', label: `Payments (${x.payments.length})` }]} />
      {tab === 'stays' && <div className="oc-card"><DataTable rows={x.stays} rowKey={(s) => s.id} columns={cols} onRowClick={(s) => setOpen(s.id)} /></div>}
      {tab === 'requests' && (
        <div className="oc-card">
          <DataTable rows={x.requests} rowKey={(r) => r.id} empty={<Empty title="No requests" icon="support_agent" />} columns={[
            { key: 'requestedAt', header: 'When', render: (r) => formatDateTime(r.requestedAt) }, { key: 'stayNo', header: 'Stay' },
            { key: 'requestType', header: 'Request', render: (r) => `${label(r.requestType)} × ${r.quantity}${r.description ? ` · ${r.description}` : ''}` },
            { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.status.replace(/_/g, '-')} /> }]} />
        </div>
      )}
      {tab === 'payments' && (
        <div className="oc-card">
          <DataTable rows={x.payments} rowKey={(p) => p.number} empty={<Empty title="No payments" icon="payments" />} columns={[
            { key: 'paidAt', header: 'When', render: (p) => (p.paidAt ? formatDateTime(p.paidAt) : '—') }, { key: 'number', header: 'Payment' }, { key: 'stayNo', header: 'Stay' },
            { key: 'methodType', header: 'Method', render: (p) => label(p.methodType) }, { key: 'purpose', header: 'Purpose', render: (p) => label(p.purpose) },
            { key: 'amount', header: 'Amount', align: 'right', render: (p) => money(p.amount) }, { key: 'refunded', header: 'Refunded', align: 'right', render: (p) => money(p.refunded) },
            { key: 'status', header: 'Status', render: (p) => <StatusPill status={p.status} /> }]} />
        </div>
      )}
      {edit && <ProfileModal id={String(id)} profile={p} onClose={() => setEdit(false)} />}
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function ProfileModal({ id, profile, onClose }: { id: string; profile: Profile; onClose: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ address: profile.address ?? '', idType: profile.idType ?? '', idNumber: '', dateOfBirth: profile.dateOfBirth?.slice(0, 10) ?? '',
    nationality: profile.nationality ?? '', preferences: profile.preferences ?? '', notes: profile.notes ?? '', vip: profile.vip });
  const send = useSend<Row>('PUT', `/api/v1/stay/guests/${id}/profile`, INV);
  return (
    <Modal open wide onClose={onClose} title="Guest profile" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate({ address: v.address, idType: v.idType || undefined, idNumber: v.idNumber || undefined,
        dateOfBirth: v.dateOfBirth || undefined, nationality: v.nationality, preferences: v.preferences, notes: v.notes, vip: v.vip }, { onSuccess: () => { toast('Saved'); onClose(); } })}>Save</button></>}>
      <div className="oc-form">
        <TextArea label="Address" span rows={2} value={v.address} onChange={(x) => setV({ ...v, address: x })} />
        <SelectField label="Identity" value={v.idType} onChange={(x) => setV({ ...v, idType: x })} placeholder="—"
          options={['ktp', 'passport', 'sim', 'kitas', 'other'].map((t) => ({ value: t, label: t.toUpperCase() }))} />
        <TextField label={`ID number${profile.idNumberMasked ? ` (now ${profile.idNumberMasked})` : ''}`} value={v.idNumber} onChange={(x) => setV({ ...v, idNumber: x })} help="Stored masked" />
        <TextField label="Date of birth" type="date" value={v.dateOfBirth} onChange={(x) => setV({ ...v, dateOfBirth: x })} />
        <TextField label="Nationality" value={v.nationality} onChange={(x) => setV({ ...v, nationality: x })} />
        <TextArea label="Preferences" span rows={2} value={v.preferences} onChange={(x) => setV({ ...v, preferences: x })} />
        <TextArea label="Notes" span rows={2} value={v.notes} onChange={(x) => setV({ ...v, notes: x })} />
      </div>
      <Checkbox label="VIP / priority guest" checked={v.vip} onChange={(x) => setV({ ...v, vip: x })} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── waitlist (§30) ────────────────────────────────────────────────────────

interface Entry { id: string; waitlistNo: string; guestName: string; guestPhone: string | null; guestEmail: string | null; typeName: string; arrivalDate: string; nights: number;
  adults: number; children: number; priority: string; status: string; offeredAt: string | null; stayNo: string | null; available?: number; notes: string | null; createdAt: string }

export function WaitlistPage() {
  const { can } = useAuth();
  const toast = useToast();
  const [status, setStatus] = useState('waiting,offered');
  const list = useGet<Page<Entry>>(`/api/v1/stay/waitlist${qs({ status })}`);
  const convert = useSend<{ id: string }, { stay: Stay }>('POST', (b) => `/api/v1/stay/waitlist/${b.id}:convert`, INV);
  const cancel = useSend<{ id: string; reason: string }>('POST', (b) => `/api/v1/stay/waitlist/${b.id}:cancel`, INV);
  const offer = useSend<Row>('POST', '/api/v1/stay/waitlist:offer', INV);
  const [open, setOpen] = useState<string | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Waitlist" help="Guests waiting for a full room type. When a bungalow frees up the entry is Offered and the guest notified; convert it into a reservation once confirmed."
        actions={can('stay.waitlist.manage') ? <button className="oc-btn oc-btn-neutral" disabled={offer.isPending} onClick={() => offer.mutate({}, { onSuccess: () => toast('Checked availability') })}>
          <Icon name="refresh" size={18} /> Check availability now</button> : undefined} />
      <Tabs value={status} onChange={setStatus} tabs={[{ value: 'waiting,offered', label: 'Open' }, { value: 'converted', label: 'Converted' }, { value: 'cancelled,expired', label: 'Closed' }]} />
      <ErrorAlert error={list.error ?? convert.error ?? cancel.error} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Row[]} loading={list.isLoading} rowKey={(e) => String(e.id)} inlineActions empty={<Empty title="Nobody waiting" icon="hourglass_empty" />}
          columns={[
            { key: 'waitlistNo', header: 'Entry', render: (e) => <><strong>{String(e.waitlistNo)}</strong><div className="oc-small oc-muted">{formatDateTime(String(e.createdAt))}</div></> },
            { key: 'guestName', header: 'Guest', render: (e) => <>{String(e.guestName)}<div className="oc-small oc-muted">{[e.guestPhone, e.guestEmail].filter(Boolean).join(' · ')}</div></> },
            { key: 'typeName', header: 'Room type' },
            { key: 'arrivalDate', header: 'Preferred date', render: (e) => `${formatDate(String(e.arrivalDate))} · ${e.nights} night(s) · ${e.adults}${Number(e.children) ? `+${e.children}` : ''} guests` },
            { key: 'priority', header: 'Priority', render: (e) => label(String(e.priority)) },
            { key: 'available', header: 'Free now', render: (e) => (e.available == null ? '—' : <strong className={Number(e.available) > 0 ? 'acc-avail' : 'acc-full'}>{String(e.available)}</strong>) },
            { key: 'status', header: 'Status', render: (e) => <>{<StatusPill status={String(e.status)} label={label(String(e.status))} tone={e.status === 'offered' ? 'success' : undefined} />}
              {e.stayNo ? <div className="oc-small">{String(e.stayNo)}</div> : null}</> }]}
          actions={(e) => ['waiting', 'offered'].includes(String(e.status)) && can('stay.waitlist.manage') ? (
            <div className="oc-row">
              <button className="oc-btn oc-btn-ink oc-btn-sm" disabled={convert.isPending || Number(e.available) === 0}
                onClick={() => convert.mutate({ id: String(e.id) }, { onSuccess: (r) => { toast(`Reservation ${r.stay.stayNo} made`); setOpen(r.stay.id); } })}>Convert to reservation</button>
              <button className="oc-btn oc-btn-text oc-btn-sm" disabled={cancel.isPending} onClick={() => cancel.mutate({ id: String(e.id), reason: 'removed' })}>Remove</button>
            </div>) : null} />
      </div>
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

