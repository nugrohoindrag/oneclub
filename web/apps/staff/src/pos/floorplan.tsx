import React, { useRef, useState } from 'react';
import { qs, request, useGet, type Page } from '@oneclub/api-client';
import { Card, Empty, ErrorAlert, Icon, PageHeader, SelectField, TextField, useAuth, useToast } from '@oneclub/shell';
import { chairs } from './tables';
import type { Row } from './shared';
import './pos.css';

// Back Office › Commercial › Floor Plan: the dining tables of an outlet on
// the plan the POS Table View shows. Drag a table to move it; the panel
// edits code, area, shape and seats.

type Table = { id: string; code: string; area: string | null; seats: number; shape: 'square' | 'round' | 'rect' | 'seat'; posX: string; posY: string; status: string };
const SHAPES = [{ value: 'square', label: 'Square' }, { value: 'round', label: 'Round' }, { value: 'rect', label: 'Long table' }, { value: 'seat', label: 'Bar seat' }];

export function FloorPlanPage() {
  const { can } = useAuth();
  const toast = useToast();
  const outlets = useGet<Page<Row>>('/api/v1/commercial/outlets?filter[status]=active&limit=100');
  const [outletId, setOutletId] = useState('');
  const outlet = outletId || String(outlets.data?.items[0]?.id ?? '');
  const list = useGet<Page<Table>>(outlet ? `/api/v1/commercial/dining-tables${qs({ 'filter[outletId]': outlet, limit: 200 })}` : null);
  const [sel, setSel] = useState('');
  const [drag, setDrag] = useState<{ id: string; x: number; y: number } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const plan = useRef<HTMLDivElement>(null);
  const edit = can('commercial.dining_table.update');
  const tables = list.data?.items ?? [];
  const current = tables.find((t) => t.id === sel);
  const save = async (id: string, body: Partial<Table>) => {
    setError(null);
    try {
      await request('PATCH', `/api/v1/commercial/dining-tables/${id}`, body);
      await list.refetch();
    } catch (e) { setError(e); }
  };
  const add = async () => {
    setError(null);
    const n = tables.filter((t) => /^T-\d+$/.test(t.code)).map((t) => Number(t.code.slice(2)));
    try {
      const t = await request<Table>('POST', '/api/v1/commercial/dining-tables', { outletId: outlet, code: `T-${Math.max(0, ...n) + 1}`, seats: 4, shape: 'square', posX: '50', posY: '50' });
      await list.refetch();
      setSel(t.id);
    } catch (e) { setError(e); }
  };
  const pos = (e: React.PointerEvent) => {
    const r = plan.current!.getBoundingClientRect();
    const clamp = (v: number) => Math.min(97, Math.max(3, v));
    return { x: clamp(((e.clientX - r.left) / r.width) * 100), y: clamp(((e.clientY - r.top) / r.height) * 100) };
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Floor Plan" help="The tables of each outlet as the POS Table View shows them. Drag a table to place it."
        actions={can('commercial.dining_table.create') && outlet ? <button className="oc-btn oc-btn-primary" onClick={() => void add()}><Icon name="add" size={18} />Add table</button> : undefined} />
      <div style={{ width: 300 }}>
        <SelectField label="Outlet" value={outlet} onChange={(v) => { setOutletId(v); setSel(''); }}
          options={(outlets.data?.items ?? []).map((o) => ({ value: String(o.id), label: String(o.name) }))} />
      </div>
      <ErrorAlert error={list.error ?? error} />
      {!outlet ? <Empty title="No outlets" icon="storefront" /> : (
        <div className="oc-row" style={{ alignItems: 'flex-start', gap: 16, flexWrap: 'wrap' }}>
          <div className="pos-vars pos-floor-wrap" style={{ margin: 0, flex: '1 1 640px', minHeight: 560 }}>
            <div ref={plan} className="pos-floor" style={{ touchAction: 'none' }} onPointerMove={(e) => { if (drag) setDrag({ id: drag.id, ...pos(e) }); }}
              onPointerUp={() => {
                if (!drag) return;
                void save(drag.id, { posX: drag.x.toFixed(2), posY: drag.y.toFixed(2) });
                setDrag(null);
              }}>
              {tables.map((t) => {
                const at = drag?.id === t.id ? drag : { x: Number(t.posX), y: Number(t.posY) };
                return (
                  <button key={t.id} className="pos-table" data-shape={t.shape} data-state={t.status === 'active' ? 'available' : 'occupied'} aria-pressed={sel === t.id}
                    style={{ left: `${at.x}%`, top: `${at.y}%`, cursor: edit ? 'grab' : 'pointer' }} aria-label={`${t.code}, ${t.seats} seats`}
                    onPointerDown={(e) => { setSel(t.id); if (edit) { (e.currentTarget.parentElement as HTMLElement).setPointerCapture(e.pointerId); setDrag({ id: t.id, ...pos(e) }); } }}>
                    <span className="pos-table-top">
                      {chairs(t).map((s, i) => <span key={i} className="pos-chair" style={s} />)}
                      <span className="pos-table-code">{t.code}</span>
                    </span>
                  </button>
                );
              })}
              {tables.length === 0 && <div className="pos-empty">No tables yet — add the first one.</div>}
            </div>
          </div>
          <div style={{ flex: '0 1 320px' }}>
            <Card title={current ? `Table ${current.code}` : 'Table'} icon="table_restaurant">
              {!current ? <p className="oc-muted">Choose a table on the plan.</p> : <TableForm key={current.id} t={current} edit={edit} onSave={(b) => save(current.id, b)}
                onRemove={can('commercial.dining_table.delete') ? async () => {
                  setError(null);
                  try { await request('DELETE', `/api/v1/commercial/dining-tables/${current.id}`); setSel(''); await list.refetch(); toast('Table removed'); } catch (e) { setError(e); }
                } : undefined} />}
            </Card>
          </div>
        </div>
      )}
    </div>
  );
}

function TableForm({ t, edit, onSave, onRemove }: { t: Table; edit: boolean; onSave: (b: Partial<Table>) => Promise<void>; onRemove?: () => void }) {
  const toast = useToast();
  const [v, setV] = useState({ code: t.code, area: t.area ?? '', seats: String(t.seats), shape: t.shape, status: t.status });
  return (
    <form className="oc-stack" onSubmit={(e) => {
      e.preventDefault();
      void onSave({ code: v.code, area: v.area || null, seats: Number(v.seats), shape: v.shape, status: v.status }).then(() => toast('Saved'));
    }}>
      <TextField label="Table" value={v.code} onChange={(x) => setV({ ...v, code: x })} disabled={!edit} required />
      <TextField label="Area" value={v.area} onChange={(x) => setV({ ...v, area: x })} disabled={!edit} placeholder="Dining Room, Terrace, Bar…" />
      <SelectField label="Shape" value={v.shape} onChange={(x) => setV({ ...v, shape: x as Table['shape'] })} options={SHAPES} />
      <TextField label="Seats" type="number" min={1} max={30} value={v.seats} onChange={(x) => setV({ ...v, seats: x })} disabled={!edit} />
      <SelectField label="Status" value={v.status} onChange={(x) => setV({ ...v, status: x })} options={[{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }]} />
      {edit && <div className="oc-row">
        <button className="oc-btn oc-btn-ink">Save</button>
        <span className="oc-spacer" />
        {onRemove && <button type="button" className="oc-btn oc-btn-text" onClick={onRemove}>Remove</button>}
      </div>}
    </form>
  );
}
