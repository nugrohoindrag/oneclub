import React, { useEffect, useState } from 'react';
import { request, useGet, type Page } from '@oneclub/api-client';
import {
  AutoResourcePage, Card, Checkbox, DataTable, ErrorAlert, Icon, MoneyField, PageHeader, SelectField, Skeleton, TextArea, TextField, useAuth, useToast,
} from '@oneclub/shell';
import { money, useCourtPolicy, type CourtPolicy, type R } from './shared';
import './sport.css';

// Pengaturan Sport Club (docs/requirement-booking-sportclub-mgcc.md FR-77,
// FR-78): sports and courts, opening hours and holidays, booking rules,
// rates like the brochure, online payment methods with the service fee
// charged to the guest, and the website content of every sport.

const WEBSITE = (typeof window !== 'undefined' && window.location.hostname.startsWith('dashboard.'))
  ? `${window.location.protocol}//${window.location.hostname.replace(/^dashboard\./, '')}` : 'http://localhost:3000';

function useSports() {
  return useGet<Page<R>>('/api/v1/sportclub/facilities?limit=50');
}

const sorted = (items: R[] | undefined) => (items ?? []).filter((f) => f.usageMode === 'slot_booking').sort((a, b) => Number(a.sortOrder ?? 0) - Number(b.sortOrder ?? 0));

/** Cabang Olahraga: name, icon, order, online booking, duration rules. */
export function SportsSettingsPage() {
  const toast = useToast();
  const { can } = useAuth();
  const f = useSports();
  const [edit, setEdit] = useState<R | null>(null);
  const [error, setError] = useState<unknown>(null);
  const save = async () => {
    if (!edit) return;
    setError(null);
    try {
      const content = { ...(edit.content as R), nameEn: (edit.content as R).nameEn, icon: (edit.content as R).icon, slug: (edit.content as R).slug };
      await request('PATCH', `/api/v1/sportclub/facilities/${String(edit.id)}`, { name: edit.name, sortOrder: Number(edit.sortOrder ?? 0), onlineBooking: !!edit.onlineBooking,
        status: edit.status, content, bookingRules: edit.bookingRules });
      toast('Cabor disimpan');
      setEdit(null);
      void f.refetch();
    } catch (e) {
      setError(e);
    }
  };
  const c = (edit?.content ?? {}) as R;
  const r = (edit?.bookingRules ?? {}) as R;
  return (
    <div className="oc-stack">
      <PageHeader title="Cabang Olahraga" help="Cabor tampil di website hanya bila aktif, bisa dibooking online dan punya lapangan aktif. Urutan mengikuti angka urutan." />
      <div className="oc-card">
        <DataTable rows={sorted(f.data?.items)} loading={f.isLoading} rowKey={(x) => String(x.id)} onRowClick={(x) => can('sportclub.facility.update') && setEdit({ ...x })}
          columns={[{ key: 'sortOrder', header: '#' }, { key: 'name', header: 'Cabor', render: (x) => <>{String(x.name)}<div className="oc-small oc-muted">{String((x.content as R)?.nameEn ?? '')}</div></> },
            { key: 'slug', header: 'URL website', render: (x) => `/book/sport-club/${String((x.content as R)?.slug ?? x.code)}` },
            { key: 'online', header: 'Online', render: (x) => (x.onlineBooking ? 'Ya' : 'Tidak') },
            { key: 'rules', header: 'Durasi', render: (x) => `${String((x.bookingRules as R)?.minHours ?? 1)}–${String((x.bookingRules as R)?.maxHours ?? '—')} jam` },
            { key: 'status', header: 'Status' }]} />
      </div>
      {edit && (
        <Card title={`Ubah ${String(edit.name)}`} icon="edit">
          <div className="oc-stack">
            <div className="oc-row-wrap">
              <TextField label="Nama (ID)" value={String(edit.name ?? '')} onChange={(v) => setEdit({ ...edit, name: v })} />
              <TextField label="Nama (EN)" value={String(c.nameEn ?? '')} onChange={(v) => setEdit({ ...edit, content: { ...c, nameEn: v } })} />
              <TextField label="Slug URL" value={String(c.slug ?? '')} onChange={(v) => setEdit({ ...edit, content: { ...c, slug: v.toLowerCase().replace(/[^a-z0-9-]/g, '-') } })} />
              <TextField label="Ikon (Material Symbols)" value={String(c.icon ?? '')} onChange={(v) => setEdit({ ...edit, content: { ...c, icon: v } })} />
              <TextField label="Urutan" type="number" value={String(edit.sortOrder ?? 0)} onChange={(v) => setEdit({ ...edit, sortOrder: Number(v) })} />
            </div>
            <div className="oc-row-wrap">
              <TextField label="Minimal jam" type="number" value={String(r.minHours ?? 1)} onChange={(v) => setEdit({ ...edit, bookingRules: { ...r, minHours: Number(v) } })} />
              <TextField label="Maksimal jam berurutan" type="number" value={String(r.maxHours ?? 4)} onChange={(v) => setEdit({ ...edit, bookingRules: { ...r, maxHours: Number(v) } })} />
              <TextField label="Jam malam mulai (paket)" type="time" value={String(r.eveningFrom ?? '')} onChange={(v) => setEdit({ ...edit, bookingRules: { ...r, eveningFrom: v } })} />
            </div>
            <Checkbox label="Bisa dibooking online (website, Member App)" checked={!!edit.onlineBooking} onChange={(v) => setEdit({ ...edit, onlineBooking: v })} />
            <SelectField label="Status" value={String(edit.status)} onChange={(v) => setEdit({ ...edit, status: v })} options={[{ value: 'active', label: 'Aktif' }, { value: 'inactive', label: 'Nonaktif' }]} />
            <div className="oc-row-wrap"><button className="oc-btn oc-btn-primary" onClick={() => void save()}>Simpan</button><button className="oc-btn oc-btn-neutral" onClick={() => setEdit(null)}>Batal</button></div>
            <ErrorAlert error={error} />
          </div>
        </Card>
      )}
    </div>
  );
}

/** Lapangan: name, sport, surface, indoor, photo, status, online. */
export function CourtsSettingsPage() {
  return <AutoResourcePage resourceKey="sportclub.court" />;
}

const DAYKINDS: [string, string][] = [['weekday', 'Hari kerja (Sen–Jum)'], ['weekend', 'Akhir pekan (Sab–Min)'], ['holiday', 'Hari libur']];

/** Jam Buka & Hari Libur: opening hours per sport and day type; public holidays and closed days. */
export function HoursSettingsPage() {
  const toast = useToast();
  const f = useSports();
  const [hours, setHours] = useState<Record<string, R>>({});
  useEffect(() => {
    const m: Record<string, R> = {};
    for (const x of sorted(f.data?.items)) m[String(x.id)] = (x.openingHours as R) ?? {};
    setHours(m);
  }, [f.data]);
  const save = async (x: R) => {
    try {
      await request('PATCH', `/api/v1/sportclub/facilities/${String(x.id)}`, { openingHours: hours[String(x.id)] });
      toast(`Jam buka ${String(x.name)} disimpan`);
      void f.refetch();
    } catch (e) {
      toast((e as Error).message, 'error');
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Jam Buka & Hari Libur" help="Jam buka per cabor per jenis hari. Selama mode demo 24 jam, semua lapangan buka 24 jam sampai tanggal di atribut 'allDayUntil', lalu kembali ke jam di bawah secara otomatis." />
      {f.isLoading ? <Skeleton rows={4} /> : sorted(f.data?.items).map((x) => {
        const h = hours[String(x.id)] ?? {};
        const until = String((x.attributes as R)?.allDayUntil ?? '');
        return (
          <Card key={String(x.id)} title={String(x.name)} icon="schedule">
            <div className="oc-stack">
              {until && <span className="oc-small" style={{ color: '#b45309' }}>Mode 24 jam aktif sampai {until} — booking yang sudah ada di luar jam normal tetap berlaku.</span>}
              {DAYKINDS.map(([k, l]) => {
                const v = (h[k] as string[] | undefined) ?? ['', ''];
                return (
                  <div key={k} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                    <span style={{ minWidth: 200 }}>{l}</span>
                    <TextField label="Buka" type="time" value={v[0] ?? ''} onChange={(nv) => setHours({ ...hours, [String(x.id)]: { ...h, [k]: [nv, v[1] ?? ''] } })} />
                    <TextField label="Tutup" type="time" value={v[1] === '24:00' ? '23:59' : v[1] ?? ''} onChange={(nv) => setHours({ ...hours, [String(x.id)]: { ...h, [k]: [v[0] ?? '', nv] } })} />
                  </div>
                );
              })}
              <div><button className="oc-btn oc-btn-primary" onClick={() => void save(x)}>Simpan</button></div>
            </div>
          </Card>
        );
      })}
      <Card title="Hari libur & tutup khusus" icon="event_busy">
        <AutoResourcePage resourceKey="platform.calendar_day" />
      </Card>
    </div>
  );
}

function PolicyForm({ children, render }: { children?: React.ReactNode; render: (p: CourtPolicy, set: (p: CourtPolicy) => void) => React.ReactNode }) {
  const toast = useToast();
  const { can } = useAuth();
  const pol = useCourtPolicy();
  const [p, setP] = useState<CourtPolicy | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => { if (pol.data) setP(pol.data); }, [pol.data]);
  if (!p) return <Skeleton rows={6} />;
  const save = async () => {
    setError(null);
    try {
      setP(await request<CourtPolicy>('PUT', '/api/v1/sportclub/court-policy', p));
      toast('Aturan disimpan (versi baru berlaku sekarang)');
    } catch (e) {
      setError(e);
    }
  };
  return (
    <div className="oc-stack">
      {children}
      {render(p, setP)}
      <ErrorAlert error={error} />
      {can('sportclub.setting.manage') && <div><button className="oc-btn oc-btn-primary" onClick={() => void save()}>Simpan</button></div>}
    </div>
  );
}

/** Aturan Booking: window, hold, duration, no-show, reminder, terms; no cancellation and no refund always. */
export function RulesSettingsPage() {
  const num = (p: CourtPolicy, set: (p: CourtPolicy) => void, k: keyof CourtPolicy, label: string, help?: string) => (
    <TextField label={label} help={help} type="number" value={String(p[k] ?? '')} onChange={(v) => set({ ...p, [k]: Number(v) })} />
  );
  return (
    <div className="oc-stack">
      <PageHeader title="Aturan Booking" help="Berlaku untuk website, Member App dan front desk. Kebijakan tanpa pembatalan dan tanpa refund berlaku tetap." />
      <PolicyForm render={(p, set) => (
        <>
          <Card title="Waktu" icon="schedule">
            <div className="oc-row-wrap">
              {num(p, set, 'windowDays', 'Booking window website (hari)')}
              {num(p, set, 'deskWindowDays', 'Booking window front desk (hari)', 'Turnamen dan komunitas')}
              {num(p, set, 'holdMinutes', 'Batas waktu bayar online (menit)')}
              {num(p, set, 'checkInMinutes', 'Check-in dibuka (menit sebelum)')}
              {num(p, set, 'noShowMinutes', 'Belum Datang setelah (menit)')}
              {num(p, set, 'reminderMinutes', 'Pengingat petugas (menit sebelum)')}
            </div>
          </Card>
          <Card title="Durasi" icon="timelapse">
            <div className="oc-row-wrap">
              {num(p, set, 'minHours', 'Minimal jam')}
              {num(p, set, 'maxHours', 'Maksimal jam berurutan per lapangan')}
              {num(p, set, 'prospectVisits', 'Prospek membership: booking per 30 hari', '0 = nonaktif')}
            </div>
            <span className="oc-small oc-muted">Durasi slot 60 menit. Override per cabor ada di Cabang Olahraga (mis. Basket Indoor maks. 2 jam).</span>
          </Card>
          <Card title="Syarat & Ketentuan (tampil di checkout dan e-ticket)" icon="gavel">
            <TextArea label="Bahasa Indonesia" value={p.terms?.id ?? ''} onChange={(v) => set({ ...p, terms: { ...p.terms, id: v } })} rows={3} />
            <TextArea label="English" value={p.terms?.en ?? ''} onChange={(v) => set({ ...p, terms: { ...p.terms, en: v } })} rows={3} />
          </Card>
          <Card title="Tambahan saat main (sewa alat, minuman)" icon="add_shopping_cart">
            <div className="oc-stack">
              {p.extras.map((x, i) => (
                <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                  <TextField label="Kode" value={x.code} onChange={(v) => set({ ...p, extras: p.extras.map((y, j) => (j === i ? { ...y, code: v.toUpperCase() } : y)) })} />
                  <TextField label="Nama" value={x.name} onChange={(v) => set({ ...p, extras: p.extras.map((y, j) => (j === i ? { ...y, name: v } : y)) })} />
                  <MoneyField label="Harga (sebelum pajak)" value={x.price} onChange={(v) => set({ ...p, extras: p.extras.map((y, j) => (j === i ? { ...y, price: v } : y)) })} />
                  <SelectField label="Jenis" value={x.component} onChange={(v) => set({ ...p, extras: p.extras.map((y, j) => (j === i ? { ...y, component: v } : y)) })}
                    options={[{ value: 'sport_rental', label: 'Sewa alat' }, { value: 'fnb', label: 'Minuman' }]} />
                  <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => set({ ...p, extras: p.extras.filter((_, j) => j !== i) })}><Icon name="delete" size={16} /></button>
                </div>
              ))}
              <div><button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => set({ ...p, extras: [...p.extras, { code: '', name: '', price: '0', component: 'sport_rental' }] })}>Tambah item</button></div>
            </div>
          </Card>
        </>
      )} />
    </div>
  );
}

/** Metode Bayar & Biaya Layanan: online methods; the service fee is charged to the guest, online only (FR-30). */
export function PaymentSettingsPage() {
  return (
    <div className="oc-stack">
      <PageHeader title="Metode Bayar & Biaya Layanan" help="Metode pembayaran online website & Member App. Biaya layanan payment gateway dibebankan ke tamu dan hanya untuk pembayaran online. Selama integrasi pihak ketiga ditahan, semua diproses lewat mock gateway." />
      <PolicyForm render={(p, set) => (
        <Card title="Metode online" icon="payments">
          <div className="oc-stack">
            {p.methods.map((m, i) => (
              <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
                <Checkbox label="Aktif" checked={m.active} onChange={(v) => set({ ...p, methods: p.methods.map((y, j) => (j === i ? { ...y, active: v } : y)) })} />
                <TextField label="Label" value={m.label} onChange={(v) => set({ ...p, methods: p.methods.map((y, j) => (j === i ? { ...y, label: v } : y)) })} />
                <SelectField label="Jenis gateway" value={m.methodType} onChange={(v) => set({ ...p, methods: p.methods.map((y, j) => (j === i ? { ...y, methodType: v } : y)) })}
                  options={[['qris', 'QRIS'], ['virtual_account', 'Virtual Account'], ['payment_gateway', 'E-wallet / gateway'], ['card', 'Kartu']].map(([v, l]) => ({ value: v, label: l }))} />
                <MoneyField label="Biaya tetap" value={m.fee} onChange={(v) => set({ ...p, methods: p.methods.map((y, j) => (j === i ? { ...y, fee: v || '0' } : y)) })} />
                <TextField label="Biaya %" value={m.percent} onChange={(v) => set({ ...p, methods: p.methods.map((y, j) => (j === i ? { ...y, percent: v || '0' } : y)) })} />
                <span className="oc-small oc-muted">Contoh Rp500.000: {money(Number(m.fee) + 500000 * Number(m.percent) / 100)}</span>
              </div>
            ))}
            <span className="oc-small oc-muted">Tidak ada bayar di tempat untuk booking online (cashless). Pembayaran di front desk tanpa biaya layanan.</span>
          </div>
        </Card>
      )} />
    </div>
  );
}

interface RateCard { rates: { item: string; name: string; dayType: string | null; band: string | null; from: string | null; to: string | null; price: string; minHours: number }[];
  packages: { code: string; name: string; uses: string; price: string; items: string[] }[] }

/** Tarif: the rate card like the brochure (FR-09), from the pricing rules. */
export function RatesSettingsPage() {
  const rc = useGet<RateCard>('/api/v1/sportclub/rate-card');
  const items = Array.from(new Set((rc.data?.rates ?? []).map((r) => r.item)));
  return (
    <div className="oc-stack">
      <PageHeader title="Tarif" help="Tarif per jenis lapangan × jenis hari × jam, sesuai brosur Rates 2025, belum termasuk pajak. Ubah lewat Pricing Rules (versi dengan masa berlaku); website dan front desk membaca angka yang sama."
        actions={<a className="oc-btn oc-btn-neutral" href="/commercial/pricing/rules?filter[serviceType]=sport_court">Pricing Rules</a>} />
      {rc.isLoading ? <Skeleton rows={6} /> : items.map((it) => (
        <Card key={it} title={it} icon="sell">
          <table className="sc-rate-table">
            <thead><tr><th>Hari</th><th>Jam</th><th>Harga / jam (belum termasuk pajak)</th></tr></thead>
            <tbody>
              {(rc.data?.rates ?? []).filter((r) => r.item === it).map((r, i) => (
                <tr key={i}><td>{r.dayType ?? 'Senin–Minggu'}</td>
                  <td>{r.band ? r.band.replace(/^(Day|Evening) /, '') : r.minHours > 1 ? `${r.minHours} jam berurutan (harga per jam)` : 'Semua jam'}</td>
                  <td>{money(r.price)}</td></tr>
              ))}
            </tbody>
          </table>
        </Card>
      ))}
      <Card title="Semua paket lapangan" icon="card_membership">
        <DataTable rows={(rc.data?.packages ?? []) as unknown as R[]} rowKey={(r) => String(r.code)} columns={[{ key: 'code', header: 'Kode' }, { key: 'name', header: 'Paket' },
          { key: 'uses', header: 'Kuota', render: (r) => `${String(r.uses)}x` }, { key: 'price', header: 'Harga', align: 'right', render: (r) => money(r.price) }]} />
      </Card>
    </div>
  );
}

/** Konten Website: text, photos, rules, amenities and FAQ per sport, with a preview. */
export function ContentSettingsPage() {
  const toast = useToast();
  const f = useSports();
  const list = sorted(f.data?.items);
  const [sel, setSel] = useState('');
  const cur = list.find((x) => x.id === sel) ?? list[0];
  const [c, setC] = useState<R>({});
  useEffect(() => { if (cur) setC({ ...((cur.content as R) ?? {}) }); }, [cur?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const desc = (c.description ?? {}) as Record<string, string>;
  const rules = (c.rules ?? {}) as Record<string, string[]>;
  const save = async () => {
    if (!cur) return;
    try {
      await request('PATCH', `/api/v1/sportclub/facilities/${String(cur.id)}`, { content: c });
      toast('Konten website disimpan');
      void f.refetch();
    } catch (e) {
      toast((e as Error).message, 'error');
    }
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Konten Website" help="Teks, foto, aturan, fasilitas pendukung dan FAQ per cabor di halaman booking website (Bahasa Indonesia dan Inggris)." />
      <div className="oc-row-wrap">{list.map((x) => <button key={String(x.id)} className="oc-chip" aria-pressed={cur?.id === x.id} onClick={() => setSel(String(x.id))}>{String(x.name)}</button>)}</div>
      {cur && (
        <Card title={String(cur.name)} icon="web" actions={<a className="oc-btn oc-btn-neutral oc-btn-sm" target="_blank" rel="noreferrer"
          href={`${WEBSITE}/id/book/sport-club/${String(c.slug ?? cur.code)}`}><Icon name="open_in_new" size={16} /> Pratinjau</a>}>
          <div className="oc-stack">
            <TextArea label="Deskripsi (ID)" value={desc.id ?? ''} onChange={(v) => setC({ ...c, description: { ...desc, id: v } })} rows={2} />
            <TextArea label="Description (EN)" value={desc.en ?? ''} onChange={(v) => setC({ ...c, description: { ...desc, en: v } })} rows={2} />
            <TextArea label="Aturan (ID, satu per baris)" value={(rules.id ?? []).join('\n')} onChange={(v) => setC({ ...c, rules: { ...rules, id: v.split('\n').filter(Boolean) } })} rows={4} />
            <TextArea label="Rules (EN, one per line)" value={(rules.en ?? []).join('\n')} onChange={(v) => setC({ ...c, rules: { ...rules, en: v.split('\n').filter(Boolean) } })} rows={4} />
            <TextField label="Fasilitas pendukung (pisahkan koma)" value={((c.amenities as string[]) ?? []).join(', ')}
              onChange={(v) => setC({ ...c, amenities: v.split(',').map((x) => x.trim()).filter(Boolean) })} />
            <TextArea label="Foto (URL, satu per baris)" value={((c.photos as string[]) ?? []).join('\n')} onChange={(v) => setC({ ...c, photos: v.split('\n').filter(Boolean) })} rows={3} />
            <div className="oc-row-wrap">{((c.photos as string[]) ?? []).map((p) => <img key={p} src={p.startsWith('http') ? p : `${WEBSITE}${p}`} alt="" style={{ width: 160, height: 100, objectFit: 'cover', borderRadius: 8 }} />)}</div>
            <TextArea label="FAQ (format: Pertanyaan? | Jawaban, satu per baris)" rows={4}
              value={((c.faq as R[]) ?? []).map((q) => `${String(q.q)} | ${String(q.a)}`).join('\n')}
              onChange={(v) => setC({ ...c, faq: v.split('\n').filter((l) => l.includes('|')).map((l) => ({ q: l.split('|')[0].trim(), a: l.split('|').slice(1).join('|').trim() })) })} />
            <TextField label="Judul SEO (ID)" value={String((c.seoTitle as R)?.id ?? '')} onChange={(v) => setC({ ...c, seoTitle: { ...((c.seoTitle as R) ?? {}), id: v } })} />
            <TextField label="SEO title (EN)" value={String((c.seoTitle as R)?.en ?? '')} onChange={(v) => setC({ ...c, seoTitle: { ...((c.seoTitle as R) ?? {}), en: v } })} />
            <div><button className="oc-btn oc-btn-primary" onClick={() => void save()}>Simpan</button></div>
          </div>
        </Card>
      )}
    </div>
  );
}
