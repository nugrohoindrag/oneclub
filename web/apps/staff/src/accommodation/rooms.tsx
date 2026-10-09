import React, { useState } from 'react';
import { Link } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  DataTable, Empty, ErrorAlert, Icon, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { Tabs } from '../p1/common';
import {
  INV, OCCUPANCY, ROOM, RoomStatus, StayDrawer, addDays, label, todayISO, type Row, type RoomState,
} from './shared';
import './accommodation.css';

// Bungalows (requirements §6–§10): the room status board (housekeeping,
// operational and reservation status apart), the room rack (§7) with its
// quick actions, the room blocks (Blocked / Maintenance / Out of Order) and
// the inventory master data (room types and bungalows).

const HK_NEXT: Record<string, string[]> = {
  dirty: ['cleaning', 'cleaned', 'ready'], cleaning: ['dirty', 'cleaned', 'ready'], cleaned: ['dirty', 'cleaning', 'inspected', 'ready'],
  inspected: ['dirty', 'cleaning', 'ready'], ready: ['dirty', 'cleaning'],
};

/** Every bungalow as a tile: operational status, occupancy and the guest. */
export function RoomStatusBoard({ date, onOpenStay }: { date?: string; onOpenStay?: (id: string) => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const [typeId, setType] = useState('');
  const [filter, setFilter] = useState('');
  const [block, setBlock] = useState<RoomState | null>(null);
  const types = useGet<Page<Row>>('/api/v1/stay/bungalow-types?limit=100');
  const list = useGet<Page<RoomState>>(`/api/v1/stay/room-status${qs({ date, typeId })}`, { refetchInterval: 30_000 });
  const set = useSend<{ id: string; status: string }>('POST', (b) => `/api/v1/stay/bungalows/${b.id}:room-status`, INV);
  const rooms = (list.data?.items ?? []).filter((r) => !filter || r.operationalStatus === filter || r.reservationStatus === filter);
  const counts: Record<string, number> = {};
  for (const r of list.data?.items ?? []) counts[r.operationalStatus] = (counts[r.operationalStatus] ?? 0) + 1;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Room type" value={typeId} onChange={setType} placeholder="All" options={(types.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) }))} />
        <div className="acc-chips">
          <button type="button" className="oc-chip" aria-pressed={!filter} onClick={() => setFilter('')}>All {list.data?.items.length ?? 0}</button>
          {Object.keys(ROOM).filter((k) => counts[k]).map((k) => (
            <button key={k} type="button" className="oc-chip" aria-pressed={filter === k} onClick={() => setFilter(k)}>{ROOM[k][0]} {counts[k]}</button>
          ))}
          {['occupied', 'reserved', 'due_out'].map((k) => (
            <button key={k} type="button" className="oc-chip" aria-pressed={filter === k} onClick={() => setFilter(k)}>{OCCUPANCY[k][0]}</button>
          ))}
        </div>
      </div>
      <ErrorAlert error={list.error ?? set.error} />
      {list.isLoading && <Skeleton rows={6} />}
      <div className="acc-room-grid">
        {rooms.map((r) => (
          <article key={r.id} className="acc-room" data-status={r.operationalStatus}>
            <header>
              <strong>{r.code}</strong>
              <RoomStatus status={r.operationalStatus} />
            </header>
            <div className="oc-small oc-muted">{r.typeName}{r.location ? ` · ${r.location}` : ''}</div>
            <div className="acc-room-occ">
              <StatusPill status={r.reservationStatus} label={OCCUPANCY[r.reservationStatus]?.[0] ?? label(r.reservationStatus)} tone={OCCUPANCY[r.reservationStatus]?.[1]} />
            </div>
            {r.currentGuest && <button type="button" className="acc-room-guest" onClick={() => r.currentStayId && onOpenStay?.(r.currentStayId)}>
              <Icon name="person" size={14} /> {r.currentGuest}<span className="oc-small oc-muted"> until {r.currentDeparture ? formatDate(r.currentDeparture) : ''}</span></button>}
            {r.arrivalGuest && <button type="button" className="acc-room-guest" onClick={() => r.arrivalStayId && onOpenStay?.(r.arrivalStayId)}>
              <Icon name="flight_land" size={14} /> {r.arrivalGuest}</button>}
            {r.blockReason && <div className="oc-small">{r.blockReason}{r.blockUntil ? ` · until ${formatDateTime(r.blockUntil)}` : ''}</div>}
            {(r.openWorkOrders > 0 || r.openHkTasks > 0) && <div className="oc-small oc-muted">{r.openHkTasks ? `${r.openHkTasks} HK task(s) ` : ''}{r.openWorkOrders ? `${r.openWorkOrders} work order(s)` : ''}</div>}
            {can('stay.room_status.update') && !r.blockKind && (
              <div className="acc-room-actions">
                {(HK_NEXT[r.hkStatus] ?? []).map((s) => (
                  <button key={s} type="button" className="oc-btn oc-btn-text oc-btn-sm" disabled={set.isPending}
                    onClick={() => set.mutate({ id: r.id, status: s }, { onSuccess: () => toast(`${r.code}: ${ROOM[s]?.[0] ?? s}`) })}>{ROOM[s]?.[0] ?? s}</button>
                ))}
              </div>
            )}
            {can('stay.room_block.manage') && !r.blockKind && <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setBlock(r)}>Block…</button>}
          </article>
        ))}
      </div>
      {list.data?.items.length === 0 && <Empty title="No bungalows" help="Add room types and bungalows in Bungalows › Inventory." icon="cottage" />}
      {block && <BlockModal unit={block} onClose={() => setBlock(null)} />}
    </div>
  );
}

function BlockModal({ unit, onClose, from }: { unit: { id: string; code: string }; onClose: () => void; from?: string }) {
  const toast = useToast();
  const [v, setV] = useState({ kind: 'blocked', startDate: from ?? todayISO(), endDate: addDays(from ?? todayISO(), 1), reason: '' });
  const send = useSend<Row>('POST', '/api/v1/stay/room-blocks', INV);
  return (
    <Modal open onClose={onClose} title={`Block ${unit.code}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-ink" disabled={!v.reason || send.isPending}
        onClick={() => send.mutate({ bungalowId: unit.id, ...v }, { onSuccess: () => { toast(`${unit.code} blocked`); onClose(); } })}>Block</button></>}>
      <p className="oc-muted">A blocked bungalow is not offered for reservation; the block is refused when a stay is booked in the period.</p>
      <div className="oc-form">
        <SelectField label="Kind" value={v.kind} onChange={(x) => setV({ ...v, kind: x })}
          options={[{ value: 'blocked', label: 'Blocked (owner, event, hold)' }, { value: 'maintenance', label: 'Maintenance' }, { value: 'out_of_order', label: 'Out of Order' }]} />
        <TextField label="From" type="date" value={v.startDate} onChange={(x) => setV({ ...v, startDate: x })} />
        <TextField label="Until" type="date" value={v.endDate} onChange={(x) => setV({ ...v, endDate: x })} />
        <TextArea label="Reason" span rows={2} value={v.reason} onChange={(x) => setV({ ...v, reason: x })} required />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── room rack (§7) ────────────────────────────────────────────────────────

interface Segment { kind: 'stay' | 'block'; id: string; label: string; start: string; end: string; status: string; guest?: string; stayNo?: string; vip: boolean }
interface RackUnit { id: string; code: string; name: string; typeId: string; typeName: string; hkStatus: string; operationalStatus: string; segments: Segment[] }
interface Rack { from: string; days: number; dates: string[]; units: RackUnit[]; unplaced: Segment[] }

export function RoomRackPage() {
  const { can } = useAuth();
  const [view, setView] = useState('14');
  const [from, setFrom] = useState(todayISO(-1));
  const [typeId, setType] = useState('');
  const [status, setStatus] = useState('');
  const [open, setOpen] = useState<string | null>(null);
  const [block, setBlock] = useState<{ unit: RackUnit; day: string } | null>(null);
  const days = Number(view);
  const types = useGet<Page<Row>>('/api/v1/stay/bungalow-types?limit=100');
  const rack = useGet<Rack>(`/api/v1/stay/room-rack${qs({ from, days, typeId, status })}`);
  const x = rack.data;
  const t0 = x ? new Date(`${x.from}T00:00:00`).getTime() : 0;
  const pos = (iso: string) => Math.max(0, Math.min(days, (new Date(iso).getTime() - t0) / 86_400_000));
  const release = useSend<{ id: string }>('POST', (b) => `/api/v1/stay/room-blocks/${b.id}:release`, INV);
  const today = todayISO();
  return (
    <div className="oc-stack">
      <PageHeader title="Room Rack" help="Reservation calendar per bungalow. Click a stay to view, reschedule, change bungalow or cancel it; click a free day to book or block it."
        actions={can('stay.stay.create') ? <Link className="oc-btn oc-btn-primary" to="/accommodation/reservations/new"><Icon name="add" size={18} /> New reservation</Link> : undefined} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <Tabs value={view} onChange={(v) => setView(v)} tabs={[{ value: '1', label: 'Day' }, { value: '7', label: 'Week' }, { value: '14', label: '2 weeks' }, { value: '31', label: 'Month' }]} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setFrom(addDays(from, -Math.max(days, 1)))}><Icon name="chevron_left" size={18} /></button>
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setFrom(addDays(from, Math.max(days, 1)))}><Icon name="chevron_right" size={18} /></button>
        <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setFrom(todayISO(-1))}>Today</button>
        <SelectField label="Room type" value={typeId} onChange={setType} placeholder="All" options={(types.data?.items ?? []).map((t) => ({ value: String(t.id), label: String(t.name) }))} />
        <SelectField label="Status" value={status} onChange={setStatus} placeholder="All"
          options={[{ value: 'reserved,requested', label: 'Reserved' }, { value: 'checked_in', label: 'In-house' }, { value: 'checked_out', label: 'Checked-out' }]} />
      </div>
      <ErrorAlert error={rack.error ?? release.error} />
      {rack.isLoading && <Skeleton rows={8} />}
      {x && (
        <div className="acc-rack" style={{ ['--days' as string]: days }}>
          <div className="acc-rack-head">
            <div className="acc-rack-unit">Bungalow</div>
            <div className="acc-rack-days">{x.dates.map((d) => <div key={d} data-today={d === today}>{formatDate(d).slice(0, 6)}</div>)}</div>
          </div>
          {x.units.map((u) => (
            <div key={u.id} className="acc-rack-row">
              <div className="acc-rack-unit"><strong>{u.code}</strong><span className="oc-small oc-muted">{u.typeName}</span><RoomStatus status={u.operationalStatus} /></div>
              <div className="acc-rack-days">
                {x.dates.map((d) => (
                  <button key={d} type="button" className="acc-rack-cell" data-today={d === today} title={`${u.code} · ${d}`}
                    onClick={() => can('stay.room_block.manage') && setBlock({ unit: u, day: d })} />
                ))}
                {u.segments.map((s) => {
                  const a = pos(s.start);
                  const b = pos(s.end);
                  if (b <= a) return null;
                  return (
                    <button key={s.kind + s.id} type="button" className="acc-rack-bar" data-kind={s.kind} data-status={s.status}
                      style={{ left: `calc(${(a / days) * 100}% + 2px)`, width: `calc(${((b - a) / days) * 100}% - 4px)` }}
                      title={s.kind === 'stay' ? `${s.stayNo} · ${s.guest} · ${formatDateTime(s.start)} → ${formatDateTime(s.end)}` : `${label(s.status)}: ${s.label}`}
                      onClick={() => (s.kind === 'stay' ? setOpen(s.id) : can('stay.room_block.manage') && window.confirm(`Release the block "${s.label}"?`) && release.mutate({ id: s.id }))}>
                      {s.vip ? '★ ' : ''}{s.kind === 'stay' ? s.guest : `${label(s.status)} · ${s.label}`}
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
          {x.unplaced.length > 0 && (
            <div className="acc-rack-unplaced">
              <strong>Booked by type, no bungalow yet:</strong>{' '}
              {x.unplaced.map((s) => <button key={s.id} type="button" className="oc-chip" onClick={() => setOpen(s.id)}>{s.stayNo} · {s.guest}</button>)}
            </div>
          )}
          <div className="acc-rack-legend oc-small">
            <span data-status="reserved">Reserved</span><span data-status="checked_in">In-house</span><span data-status="checked_out">Checked-out</span>
            <span data-kind="block">Blocked / Maintenance / Out of Order</span>
          </div>
        </div>
      )}
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
      {block && <BlockModal unit={block.unit} from={block.day} onClose={() => setBlock(null)} />}
    </div>
  );
}

// ── bungalows: status board, inventory, blocks ───────────────────────────

export function BungalowsPage() {
  const [tab, setTab] = useState('status');
  const [open, setOpen] = useState<string | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="Bungalows" help="Each bungalow is inventory of its room type. The housekeeping status (Ready, Dirty, Cleaning…) is kept apart from the reservation status."
        actions={<><Link className="oc-btn oc-btn-neutral" to="/accommodation/bungalows/types">Room types</Link><Link className="oc-btn oc-btn-neutral" to="/accommodation/bungalows/units">Inventory</Link></>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'status', label: 'Room Status' }, { value: 'blocks', label: 'Blocks & Out of Order' }]} />
      {tab === 'status' && <RoomStatusBoard onOpenStay={setOpen} />}
      {tab === 'blocks' && <BlocksList />}
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

interface Block { id: string; blockNo: string; bungalowCode: string; kind: string; start: string; end: string; reason: string; status: string; workOrderId: string | null }

function BlocksList() {
  const { can } = useAuth();
  const [status, setStatus] = useState('active');
  const list = useGet<Page<Block>>(`/api/v1/stay/room-blocks${qs({ status })}`);
  const release = useSend<{ id: string; reason: string }>('POST', (b) => `/api/v1/stay/room-blocks/${b.id}:release`, INV);
  return (
    <div className="oc-stack">
      <Tabs value={status} onChange={setStatus} tabs={[{ value: 'active', label: 'Active' }, { value: 'released', label: 'Released' }, { value: '', label: 'All' }]} />
      <ErrorAlert error={list.error ?? release.error} />
      <div className="oc-card">
        <DataTable rows={list.data?.items as unknown as Row[]} loading={list.isLoading} rowKey={(b) => String(b.id)} empty={<Empty title="No blocks" icon="block" />}
          columns={[{ key: 'blockNo', header: 'Block' }, { key: 'bungalowCode', header: 'Bungalow' },
            { key: 'kind', header: 'Kind', render: (b) => <RoomStatus status={String(b.kind)} /> },
            { key: 'start', header: 'From', render: (b) => formatDateTime(String(b.start)) }, { key: 'end', header: 'Until', render: (b) => formatDateTime(String(b.end)) },
            { key: 'reason', header: 'Reason', render: (b) => <>{String(b.reason)}{b.workOrderId ? <div className="oc-small oc-muted">work order</div> : null}</> },
            { key: 'status', header: 'Status', render: (b) => <StatusPill status={String(b.status)} /> }]}
          actions={(b) => b.status === 'active' && can('stay.room_block.manage') && !b.workOrderId ? (
            <button className="oc-btn oc-btn-text oc-btn-sm" disabled={release.isPending} onClick={() => release.mutate({ id: String(b.id), reason: 'released' })}>Release</button>) : null} />
      </div>
    </div>
  );
}
