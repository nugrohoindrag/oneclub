import React, { useState } from 'react';
import { qs, useGet } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import {
  Amount, BreakdownList, ColumnChart, DashButton, DashCard, DashGrid, DashHead, DashTable, ErrorAlert, Gauge, Icon, MiniCard, Skeleton, useAuth,
  type DashColumn,
} from '@oneclub/shell';
import { moneyShort } from '../p1/common';
import {
  PaymentStatus, ROOM, RoomStatus, StayDrawer, StayStatus, guestOf, label, money, todayISO, type GuestRequest, type HKTask, type Stay, type WorkOrder,
} from './shared';
import './accommodation.css';

// Accommodation Overview (requirements §3): the KPI of the day — occupancy,
// available and occupied bungalows, arrivals, departures, in-house guests,
// revenue, ADR, RevPAR, cancellations, no-shows and the room status — and
// today's operation: arrivals, departures, pending payment, pending room
// preparation, VIP guests, guest requests, housekeeping and maintenance.

interface NightStat { date: string; units: number; outOfOrder: number; available: number; occupied: number; roomRevenue: string; occupancy: string; adr: string; revpar: string }
interface Dash {
  date: string; night: NightStat; occupancy: string; availableBungalows: number; occupiedBungalows: number; todaysArrivals: number; todaysDepartures: number;
  inHouseGuests: number; revenue: string; adr: string; revpar: string; cancelled: number; noShows: number; roomStatus: Record<string, number>;
  arrivals: Stay[]; departures: Stay[]; expectedCheckIns: number; expectedCheckOuts: number; pendingPayment: Stay[]; pendingPreparation: Stay[]; vip: Stay[];
  requests: GuestRequest[]; housekeepingTasks: HKTask[]; maintenanceIssues: WorkOrder[]; week: NightStat[];
  soldTonight: number; inHouseNow: number; outOfOrder: number; alerts: { kind: string; message: string; stayId: string | null; stayNo: string | null }[];
}

export function AccommodationOverviewPage() {
  const { can } = useAuth();
  const [date, setDate] = useState(todayISO());
  const [open, setOpen] = useState<string | null>(null);
  const d = useGet<Dash>(`/api/v1/stay/dashboard${qs({ date })}`, { refetchInterval: 60_000 });
  const controls = <input type="date" className="oc-input oc-filter" aria-label="Date" value={date} onChange={(e) => e.target.value && setDate(e.target.value)} />;
  if (d.error) return <div className="oc-dash-page"><DashHead title="Accommodation Overview" controls={controls} /><ErrorAlert error={d.error} /></div>;
  if (!d.data) return <div className="oc-dash-page"><DashHead title="Accommodation Overview" controls={controls} /><Skeleton rows={10} /></div>;
  const x = d.data;
  const rooms = Object.entries(x.roomStatus);
  const totalRooms = rooms.reduce((s, [, n]) => s + n, 0);
  const stayCols: DashColumn<Stay>[] = [
    { key: 'guest', header: 'Guest', render: (s) => <>{s.vip ? <span className="acc-vip">VIP</span> : null}{guestOf(s)}<div className="oc-small oc-muted">{s.stayNo} · {s.adults + s.children} guest(s)</div></> },
    { key: 'unit', header: 'Bungalow', render: (s) => <>{s.unitAssigned ? s.unitCode ?? s.unitName : '—'}<div className="oc-small oc-muted">{s.typeName}</div></> },
    { key: 'pay', header: 'Payment', render: (s) => <PaymentStatus status={s.paymentStatus} /> },
    { key: 'status', header: 'Status', render: (s) => <StayStatus s={s} /> },
  ];
  return (
    <div className="oc-dash-page">
      <DashHead title="Accommodation Overview" sub={`Bungalows · ${formatDate(x.date)}`} controls={<>{controls}
        {can('stay.stay.create') && <DashButton icon="add" tone="blue" to="/accommodation/reservations/new">New reservation</DashButton>}</>} />

      <DashGrid>
        <MiniCard span={3} label="Occupancy tonight" value={<Amount text={`${x.occupancy}%`} size="md" />} delta={<span className="oc-dash-chip-suffix">{x.night.occupied} of {x.night.available}</span>} />
        <MiniCard span={3} label="Sold tonight" value={<Amount text={formatNumber(x.soldTonight)} size="md" />} delta={<span className="oc-dash-chip-suffix">checked in or not</span>} to="/accommodation/reservations?tab=confirmed" />
        <MiniCard span={3} label="In-house now" value={<Amount text={formatNumber(x.inHouseNow)} size="md" />} to="/accommodation/front-office?tab=in_house" />
        <MiniCard span={3} label="Available tonight" value={<Amount text={formatNumber(x.availableBungalows)} size="md" />}
          delta={<span className="oc-dash-chip-suffix">{x.night.units} active − {x.outOfOrder} out of order − {x.soldTonight} sold</span>} to="/accommodation/room-rack" />
        <MiniCard span={3} label="In-house guests" value={<Amount text={formatNumber(x.inHouseGuests)} size="md" />} />
        <MiniCard span={3} label="Today's arrivals" value={<Amount text={formatNumber(x.todaysArrivals)} size="md" />} to="/accommodation/front-office" />
        <MiniCard span={3} label="Today's departures" value={<Amount text={formatNumber(x.todaysDepartures)} size="md" />} to="/accommodation/front-office?tab=departures" />
        <MiniCard span={3} label="Revenue today" value={<Amount text={moneyShort(x.revenue)} size="md" />} />
        <MiniCard span={3} label="ADR / RevPAR" value={<Amount text={moneyShort(x.adr)} size="md" />} delta={<span className="oc-dash-chip-suffix">RevPAR {moneyShort(x.revpar)}</span>} />
        <MiniCard span={3} label="Cancelled today" value={<Amount text={formatNumber(x.cancelled)} size="md" />} to="/accommodation/reservations?tab=cancelled" />
        <MiniCard span={3} label="No-show" value={<Amount text={formatNumber(x.noShows)} size="md" />} to="/accommodation/reservations?tab=no_show" />
        <MiniCard span={3} label="Bungalows ready" value={<Amount text={formatNumber(x.roomStatus.ready ?? 0)} size="md" />} to="/accommodation/housekeeping" />
        <MiniCard span={3} label="Dirty · cleaning · maintenance" value={<Amount text={`${x.roomStatus.dirty ?? 0} · ${(x.roomStatus.cleaning ?? 0) + (x.roomStatus.cleaned ?? 0)} · ${(x.roomStatus.maintenance ?? 0) + (x.roomStatus.out_of_order ?? 0)}`} size="md" />} to="/accommodation/housekeeping" />
      </DashGrid>

      {x.alerts.length > 0 && (
        <DashGrid>
          <DashCard span={12} icon="warning" tone="red" title={`Attention (${x.alerts.length})`}>
            <ul className="acc-alerts">{x.alerts.map((a, i) => (
              <li key={i}><Icon name={a.kind === 'late_departure' ? 'schedule' : a.kind === 'ooo_reserved' ? 'build' : 'payments'} size={18} />
                {a.stayId ? <button type="button" className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setOpen(a.stayId)}>{a.message}</button> : a.message}</li>))}
            </ul>
          </DashCard>
        </DashGrid>
      )}

      <DashGrid>
        <DashCard span={8} icon="bar_chart" tone="blue" title="Next 7 nights" action={<DashButton to="/accommodation/room-rack" icon="calendar_view_week">Room rack</DashButton>}>
          <ColumnChart aLabel="Occupied" bLabel="Available" format={(v) => formatNumber(v)}
            points={x.week.map((n, i) => ({ label: formatDate(n.date).slice(0, 6), a: n.occupied, b: n.available, state: i === 0 ? 'current' as const : 'future' as const,
              title: `${formatDate(n.date)} · ${n.occupancy}% · ADR ${money(n.adr)}` }))} />
        </DashCard>
        <DashCard span={4} icon="bed" tone="green" title="Room status">
          <Gauge title="Ready to sell" ratio={totalRooms ? (x.roomStatus.ready ?? 0) / totalRooms : null} value={`${x.roomStatus.ready ?? 0}/${totalRooms}`} caption="bungalows Ready" />
          <BreakdownList rows={rooms.map(([k, n]) => ({ label: ROOM[k]?.[0] ?? label(k), value: String(n), share: totalRooms ? n / totalRooms : null, color: ROOM[k]?.[2] }))} />
        </DashCard>
      </DashGrid>

      <DashGrid>
        <DashTable span={6} title={`Arrivals (${x.arrivals.length})`} icon="flight_land" rows={x.arrivals} rowKey={(s) => s.id} columns={stayCols} onRow={(s) => setOpen(s.id)}
          empty="No arrivals" />
        <DashTable span={6} title={`Departures (${x.departures.length})`} icon="flight_takeoff" rows={x.departures} rowKey={(s) => s.id} columns={stayCols} onRow={(s) => setOpen(s.id)}
          empty="No departures" />
      </DashGrid>

      <DashGrid>
        <DashTable span={4} title={`Pending payment (${x.pendingPayment.length})`} icon="payments" rows={x.pendingPayment} rowKey={(s) => s.id} onRow={(s) => setOpen(s.id)}
          columns={[{ key: 'g', header: 'Guest', render: (s) => <>{guestOf(s)}<div className="oc-small oc-muted">{s.stayNo}</div></> },
            { key: 'p', header: 'Open', align: 'right', render: (s) => money(Math.max(Number(s.totalDue) - Number(s.paid), 0)) }]} empty="All paid" />
        <DashTable span={4} title={`Room preparation (${x.pendingPreparation.length})`} icon="cleaning_services" rows={x.pendingPreparation} rowKey={(s) => s.id} onRow={(s) => setOpen(s.id)}
          columns={[{ key: 'g', header: 'Arrival', render: (s) => <>{guestOf(s)}<div className="oc-small oc-muted">{s.unitAssigned ? s.unitCode : 'no bungalow assigned'}</div></> }]}
          empty="All bungalows ready" />
        <DashTable span={4} title={`VIP / priority (${x.vip.length})`} icon="star" rows={x.vip} rowKey={(s) => s.id} onRow={(s) => setOpen(s.id)}
          columns={[{ key: 'g', header: 'Guest', render: (s) => <>{guestOf(s)}<div className="oc-small oc-muted">{s.unitCode ?? s.typeName} · {label(s.status)}</div></> }]} empty="No VIP today" />
      </DashGrid>

      <DashGrid>
        <DashTable span={4} title={`Guest requests (${x.requests.length})`} icon="support_agent" rows={x.requests} rowKey={(r) => r.id}
          columns={[{ key: 'r', header: 'Request', render: (r) => <>{label(r.requestType)} × {r.quantity}<div className="oc-small oc-muted">{r.bungalowCode ?? r.stayNo} · {label(r.status)}</div></> }]}
          empty="No open requests" action={<DashButton to="/accommodation/requests">All</DashButton>} />
        <DashTable span={4} title={`Housekeeping (${x.housekeepingTasks.length})`} icon="mop" rows={x.housekeepingTasks} rowKey={(t) => t.id}
          columns={[{ key: 't', header: 'Task', render: (t) => <>{t.bungalowCode} · {label(t.taskType)}<div className="oc-small oc-muted">{t.assignedTo ?? 'unassigned'} · {label(t.status)}</div></> },
            { key: 's', header: 'Room', render: (t) => <RoomStatus status={t.hkStatus} /> }]} empty="Nothing to clean" action={<DashButton to="/accommodation/housekeeping">Board</DashButton>} />
        <DashTable span={4} title={`Maintenance (${x.maintenanceIssues.length})`} icon="build" rows={x.maintenanceIssues} rowKey={(w) => w.id}
          columns={[{ key: 'w', header: 'Issue', render: (w) => <>{w.bungalowCode ?? 'General'} · {w.title}<div className="oc-small oc-muted">{w.woNo} · {label(w.priority)} · {label(w.status)}</div></> }]}
          empty="No open issues" action={<DashButton to="/accommodation/maintenance">Work orders</DashButton>} />
      </DashGrid>
      {open && <StayDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}
