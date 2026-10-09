import React, { useState } from 'react';
import { useParams } from 'react-router';
import { useGet, useSend } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import { Empty, ErrorAlert, Icon, Skeleton, TextArea, useToast } from '@oneclub/shell';
import { Head, Rows, StatusChip, dayLabel, money } from './ui';

// My Stay (accommodation requirements §26): the bungalow, dates, check-in
// and check-out times, the reservation, guest services (requests and paid
// services), the folio, stay information, house rules, amenities and
// property information. Access Control and Digital Key are out of scope.

interface Request { id: string; requestNo: string; requestType: string; quantity: number; description: string | null; status: string; requestedAt: string }
interface Addon { id: string; name: string; category: string; description: string | null; price: string; unit: string }
interface Experience {
  stay: { id: string; stayNo: string; reservationCode: string; unitName: string; typeName: string | null; start: string; end: string; status: string; nights: number;
    adults: number; children: number; ratePlan: string | null; specialRequests: string | null; lateCheckOutUntil: string | null };
  typeName: string; description: string | null; photos: string[]; amenities: string[]; bedConfiguration: string | null; view: string | null;
  checkInTime: string; checkOutTime: string; houseRules: string; propertyInfo: string; requests: Request[]; orderableAddons: Addon[];
  folio: { description: string; quantity: string; total: string; postedAt: string }[]; folioTotal: string; folioPaid: string; folioBalance: string; canRequest: boolean;
}

const SERVICES: [string, string, string][] = [['extra_towel', 'Extra towel', 'dry_cleaning'], ['room_cleaning', 'Room cleaning', 'cleaning_services'],
  ['extra_bed', 'Extra bed', 'bed'], ['laundry', 'Laundry', 'local_laundry_service'], ['food_beverage', 'Food & drinks', 'room_service'],
  ['transportation', 'Transport', 'airport_shuttle'], ['maintenance', 'Something broken', 'build'], ['other', 'Other', 'more_horiz']];
const label = (s: string) => s.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

export function MyStayPage() {
  const { id } = useParams();
  const toast = useToast();
  const x = useGet<Experience>(`/api/v1/member/stays/${id}/experience`);
  const [type, setType] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const send = useSend<Record<string, unknown>>('POST', `/api/v1/member/stays/${id}/requests`, [`/api/v1/member/stays/${id}`]);
  if (x.isLoading) return <div className="mj-page"><Skeleton rows={8} /></div>;
  if (!x.data) return <div className="mj-page"><ErrorAlert error={x.error} /></div>;
  const e = x.data;
  const s = e.stay;
  const ask = (body: Record<string, unknown>) => send.mutate(body, { onSuccess: () => { toast('Request sent to the front office'); setType(null); setNote(''); } });
  return (
    <div className="mj-page">
      <Head title="My Stay" back={['/activity/stays', 'Stays']} help={`${s.stayNo} · ${s.reservationCode}`} actions={<StatusChip status={s.status} />} />
      <section className="mj-card">
        {e.photos[0] && <img src={e.photos[0]} alt="" style={{ width: '100%', maxHeight: 220, objectFit: 'cover', borderRadius: 16, marginBottom: 12 }} />}
        <h2 style={{ margin: 0 }}>{s.unitName}</h2>
        <div className="mj-muted">{e.typeName}</div>
        <div style={{ margin: '8px 0', fontSize: 18 }}>{dayLabel(s.start, { day: 'numeric', month: 'long' })} – {dayLabel(s.end, { day: 'numeric', month: 'long', year: 'numeric' })}</div>
        <Rows rows={[['Check-in', e.checkInTime], ['Check-out', s.lateCheckOutUntil ? `late, until ${new Date(s.lateCheckOutUntil).toLocaleTimeString('en-GB', { timeStyle: 'short' })}` : e.checkOutTime],
          ['Nights', String(s.nights)], ['Guests', `${s.adults} adult(s)${s.children ? `, ${s.children} child(ren)` : ''}`], ['Rate', s.ratePlan ?? '—'],
          ...(s.specialRequests ? [['Your request', s.specialRequests] as [string, string]] : [])]} />
      </section>

      {e.canRequest && (
        <section className="mj-card">
          <h3 style={{ marginTop: 0 }}>Guest Services</h3>
          <div className="mj-grid-tiles" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(120px, 1fr))', gap: 8 }}>
            {SERVICES.map(([k, l, icon]) => (
              <button key={k} type="button" className="oc-btn oc-btn-neutral" aria-pressed={type === k} style={{ height: 64, flexDirection: 'column' }} onClick={() => setType(k)}>
                <Icon name={icon} size={22} /> {l}
              </button>
            ))}
          </div>
          {type && (
            <div style={{ marginTop: 10 }}>
              <TextArea label={`${label(type)} — details`} rows={2} value={note} onChange={setNote} />
              <div className="mj-actions"><button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => ask({ requestType: type, description: note || undefined })}>Send request</button></div>
            </div>
          )}
          {e.orderableAddons.length > 0 && (
            <>
              <h4>Order</h4>
              <div className="mj-list">
                {e.orderableAddons.map((a) => (
                  <div key={a.id} className="mj-item">
                    <div className="mj-item-body"><strong>{a.name}</strong><span className="mj-small mj-muted">{money(a.price)} · charged to your folio</span></div>
                    <button className="oc-btn oc-btn-outline oc-btn-sm" disabled={send.isPending} onClick={() => ask({ requestType: a.category === 'laundry' ? 'laundry' : a.category.includes('towel') ? 'extra_towel'
                      : a.category === 'extra_bed' ? 'extra_bed' : ['breakfast', 'lunch', 'dinner', 'bbq'].includes(a.category) ? 'food_beverage' : a.category === 'transportation' ? 'transportation' : 'other',
                      addonId: a.id, description: a.name })}>Order</button>
                  </div>
                ))}
              </div>
            </>
          )}
          <ErrorAlert error={send.error} />
          {e.requests.length > 0 && (
            <div className="mj-list" style={{ marginTop: 10 }}>
              {e.requests.map((r) => (
                <div key={r.id} className="mj-item">
                  <div className="mj-item-body"><strong>{label(r.requestType)}{r.quantity > 1 ? ` × ${r.quantity}` : ''}</strong>
                    <span className="mj-small mj-muted">{formatDateTime(r.requestedAt)}{r.description ? ` · ${r.description}` : ''}</span></div>
                  <StatusChip status={r.status} />
                </div>
              ))}
            </div>
          )}
        </section>
      )}

      <section className="mj-card">
        <h3 style={{ marginTop: 0 }}>Folio</h3>
        {e.folio.length === 0 ? <Empty title="No charges yet" help="Room nights are posted each night." icon="receipt_long" /> : (
          <div className="mj-list">{e.folio.map((l, i) => <div key={i} className="mj-item"><div className="mj-item-body"><span>{l.description}</span>
            <span className="mj-small mj-muted">{formatDateTime(l.postedAt)}</span></div><strong>{money(l.total)}</strong></div>)}</div>
        )}
        <Rows rows={[['Total stay', money(e.folioTotal)], ['Paid', money(e.folioPaid)], ['Balance', <strong key="b">{money(Math.max(Number(e.folioBalance), 0))}</strong>]]} />
      </section>

      <section className="mj-card">
        <h3 style={{ marginTop: 0 }}>Stay Information</h3>
        {e.description && <p>{e.description}</p>}
        <Rows rows={[['Bed', e.bedConfiguration ?? '—'], ['View', e.view ?? '—']]} />
        {e.amenities.length > 0 && <><h4>Amenities</h4><div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>{e.amenities.map((a) => <span key={a} className="oc-chip">{label(a)}</span>)}</div></>}
        <h4>House Rules</h4><p style={{ whiteSpace: 'pre-wrap' }}>{e.houseRules}</p>
        <h4>Property Information</h4><p style={{ whiteSpace: 'pre-wrap' }}>{e.propertyInfo}</p>
      </section>
    </div>
  );
}
