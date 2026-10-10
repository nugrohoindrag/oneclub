import React, { useState } from 'react';
import { qs, useGet } from '@oneclub/api-client';
import { formatDate, formatNumber } from '@oneclub/i18n';
import {
  Amount, BreakdownList, ColumnChart, DashCard, DashGrid, DashHead, ErrorAlert, MiniCard, ProgressRow, Skeleton, SplitStats,
} from '@oneclub/shell';
import { moneyShort } from '../p1/common';
import { label, money, sourceLabel, todayISO } from './shared';
import { StayFinanceReports } from './mgcc';
import './accommodation.css';

// Accommodation reports & hotel KPI (requirements §32):
//   Occupancy = occupied room nights / available room nights × 100
//   ADR = room revenue / rooms sold · RevPAR = room revenue / available room nights
//   ALOS = total room nights / reservations

interface NightStat { date: string; available: number; occupied: number; outOfOrder: number; roomRevenue: string; occupancy: string; adr: string; revpar: string }
interface Breakdown { key: string; count: number; nights: number; revenue: string }
interface Report {
  from: string; to: string; availableRoomNights: number; occupiedRoomNights: number; outOfOrderRoomNights: number; occupancy: string; adr: string; revpar: string;
  roomRevenue: string; addonRevenue: string; otherRevenue: string; totalRevenue: string; bookingVolume: number; byRoomType: Breakdown[]; bySource: Breakdown[];
  cancellations: number; noShows: number; alos: string; newGuests: number; returningGuests: number; avgGuestSpending: string; roomTurnaroundMinutes: number | null;
  hkCompletion: string; hkTasks: number; maintenanceIssues: number; maintenanceOpen: number; maintenanceHours: string | null; maintenanceByCategory: Breakdown[];
  requestCompletion: string; requests: number; requestMinutes: number | null; nights: NightStat[];
}
const COLORS = ['var(--dash-blue)', 'var(--dash-ink)', 'var(--dash-lime)', 'var(--dash-sky)', 'var(--dash-amber)', 'var(--dash-green)', 'var(--dash-red)'];

export function AccommodationReportsPage() {
  const [from, setFrom] = useState(todayISO(-29));
  const [to, setTo] = useState(todayISO());
  const r = useGet<Report>(`/api/v1/stay/accommodation-report${qs({ from, to })}`);
  const controls = <>
    <input type="date" className="oc-input oc-filter" aria-label="From" value={from} onChange={(e) => e.target.value && setFrom(e.target.value)} />
    <input type="date" className="oc-input oc-filter" aria-label="To" value={to} onChange={(e) => e.target.value && setTo(e.target.value)} />
  </>;
  if (r.error) return <div className="oc-dash-page"><DashHead title="Accommodation Reports" controls={controls} /><ErrorAlert error={r.error} /></div>;
  if (!r.data) return <div className="oc-dash-page"><DashHead title="Accommodation Reports" controls={controls} /><Skeleton rows={10} /></div>;
  const x = r.data;
  const typeTotal = x.byRoomType.reduce((s, b) => s + b.count, 0);
  const srcTotal = x.bySource.reduce((s, b) => s + b.count, 0);
  const guests = x.newGuests + x.returningGuests;
  return (
    <div className="oc-dash-page">
      <DashHead title="Accommodation Reports" sub={`${formatDate(x.from)} – ${formatDate(x.to)}`} controls={controls} />
      <DashGrid>
        <MiniCard span={3} label="Occupancy" value={<Amount text={`${x.occupancy}%`} size="md" />} delta={<span className="oc-dash-chip-suffix">{x.occupiedRoomNights} / {x.availableRoomNights} room nights</span>} />
        <MiniCard span={3} label="ADR" value={<Amount text={moneyShort(x.adr)} size="md" />} />
        <MiniCard span={3} label="RevPAR" value={<Amount text={moneyShort(x.revpar)} size="md" />} />
        <MiniCard span={3} label="Average length of stay" value={<Amount text={`${x.alos} nights`} size="md" />} />
        <MiniCard span={3} label="Room revenue" value={<Amount text={moneyShort(x.roomRevenue)} size="md" />} />
        <MiniCard span={3} label="Add-on revenue" value={<Amount text={moneyShort(x.addonRevenue)} size="md" />} />
        <MiniCard span={3} label="Total accommodation revenue" value={<Amount text={moneyShort(x.totalRevenue)} size="md" />} delta={<span className="oc-dash-chip-suffix">other {moneyShort(x.otherRevenue)}</span>} />
        <MiniCard span={3} label="Bookings · cancelled · no-show" value={<Amount text={`${x.bookingVolume} · ${x.cancellations} · ${x.noShows}`} size="md" />} />
      </DashGrid>
      <DashGrid>
        <DashCard span={8} icon="show_chart" tone="blue" title="Occupancy per night">
          <ColumnChart aLabel="Occupied" bLabel="Available" format={(v) => formatNumber(v)}
            points={x.nights.map((n) => ({ label: formatDate(n.date).slice(0, 6), a: n.occupied, b: n.available, title: `${formatDate(n.date)} · ${n.occupancy}% · ADR ${money(n.adr)} · RevPAR ${money(n.revpar)}` }))} />
        </DashCard>
        <DashCard span={4} icon="group" tone="green" title="Guests">
          <SplitStats items={[{ label: 'New', value: formatNumber(x.newGuests), color: COLORS[0] }, { label: 'Returning', value: formatNumber(x.returningGuests), color: COLORS[2] }]} />
          <ProgressRow label="Returning guests" ratio={guests ? x.returningGuests / guests : null} />
          <BreakdownList rows={[{ label: 'Average spending per stay', value: money(x.avgGuestSpending) }, { label: 'Out of order room nights', value: String(x.outOfOrderRoomNights) }]} />
        </DashCard>
      </DashGrid>
      <DashGrid>
        <DashCard span={6} icon="cottage" title="Bookings by room type">
          <BreakdownList rows={x.byRoomType.map((b, i) => ({ label: b.key, value: `${b.count} · ${b.nights} nights · ${moneyShort(b.revenue)}`, share: typeTotal ? b.count / typeTotal : null, color: COLORS[i % 7] }))} />
        </DashCard>
        <DashCard span={6} icon="call_split" title="Bookings by source">
          <BreakdownList rows={x.bySource.map((b, i) => ({ label: sourceLabel(b.key), value: `${b.count} · ${moneyShort(b.revenue)}`, share: srcTotal ? b.count / srcTotal : null, color: COLORS[i % 7] }))} />
        </DashCard>
      </DashGrid>
      <DashGrid>
        <DashCard span={4} icon="mop" title="Housekeeping">
          <ProgressRow label={`Tasks completed (${x.hkTasks})`} ratio={Number(x.hkCompletion) / 100} />
          <BreakdownList rows={[{ label: 'Room turnaround (check-out → ready)', value: x.roomTurnaroundMinutes != null ? `${x.roomTurnaroundMinutes} min` : '—' }]} />
        </DashCard>
        <DashCard span={4} icon="build" title="Maintenance">
          <BreakdownList rows={[{ label: 'Issues', value: String(x.maintenanceIssues) }, { label: 'Still open', value: String(x.maintenanceOpen) },
            { label: 'Average hours to resolve', value: x.maintenanceHours ?? '—' },
            ...x.maintenanceByCategory.map((b) => ({ label: label(b.key), value: `${b.count} · cost ${moneyShort(b.revenue)}` }))]} />
        </DashCard>
        <DashCard span={4} icon="support_agent" title="Guest requests">
          <ProgressRow label={`Completed (${x.requests})`} ratio={Number(x.requestCompletion) / 100} />
          <BreakdownList rows={[{ label: 'Average time to complete', value: x.requestMinutes != null ? `${x.requestMinutes} min` : '—' }]} />
        </DashCard>
      </DashGrid>
      <StayFinanceReports from={from} to={to} />
    </div>
  );
}
