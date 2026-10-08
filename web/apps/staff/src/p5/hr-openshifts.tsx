import React, { useState } from 'react';
import { Link } from 'react-router';
import { uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Card, DataTable, Empty, ErrorAlert, Icon, Modal, SelectField, StatusPill, TextArea, TextField, useToast, type Option } from '@oneclub/shell';
import type { R } from '../p1/common';
import { registerEssSection } from './hr';

// HRIS improvement phase C (spec §13 Open shift): the scheduler posts unassigned shifts of a schedule; employees of the unit claim them in
// Employee Self Service; an approved claim becomes the assignment through the roster rules and fills the shift.

const HR = '/api/v1/hris';
const ESS = '/api/v1/ess';
const INV = [HR, ESS];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');

function NoteAction({ title, path, required, danger, onClose }: { title: string; path: string; required?: boolean; danger?: boolean; onClose: () => void }) {
  const toast = useToast();
  const [note, setNote] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', path, INV);
  return (
    <Modal open onClose={onClose} title={title} actions={<>
      <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
      <button className={`oc-btn ${danger ? 'oc-btn-danger' : 'oc-btn-ink'}`} disabled={send.isPending || (!!required && !note.trim())}
        onClick={() => send.mutate(note ? { note } : {}, { onSuccess: () => { toast(`${title}: done`); onClose(); } })}>{title}</button>
    </>}>
      <ErrorAlert error={send.error} />
      <TextArea label={required ? 'Reason' : 'Note (optional)'} value={note} onChange={setNote} required={required} />
    </Modal>
  );
}

/** Open shifts of a schedule with the claims to decide (schedule detail). */
export function OpenShiftsPanel({ scheduleId, editable, days, templates }: { scheduleId: string; editable: boolean; days: string[]; templates: Option[] }) {
  const toast = useToast();
  const list = useGet<Page<R>>(`${HR}/open-shifts?scheduleId=${scheduleId}`);
  const positions = useGet<Page<R>>(editable ? `${HR}/positions?limit=500&filter[status]=active` : null);
  const [post, setPost] = useState(false);
  const [f, setF] = useState<Record<string, string>>({ slots: '1' });
  const [act, setAct] = useState<{ title: string; path: string; required?: boolean; danger?: boolean } | null>(null);
  const create = useSend<Record<string, unknown>, R>('POST', `${HR}/schedules/${scheduleId}/open-shifts`, INV, () => ({ 'Idempotency-Key': uuidv7() }));
  const items = list.data?.items ?? [];
  return (
    <Card title="Open shifts" icon="event_available" actions={editable && <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setPost(true)}><Icon name="add" size={18} /> Post open shift</button>}>
      {items.length === 0 ? <p className="oc-muted oc-small" style={{ margin: 0 }}>No open shift. Post one when a day needs extra people; employees of the department claim it in Employee Self Service once the schedule is published.</p> : (
        <div className="oc-stack">
          {items.map((o) => (
            <div key={String(o.id)} className="oc-stack" style={{ gap: 6 }}>
              <div className="oc-row-wrap">
                <strong>{date(o.workDate)} · {String(o.shiftName)} {String(o.startTime)}–{String(o.endTime)}</strong>
                {o.positionName ? <span className="oc-muted">· {String(o.positionName)}</span> : null}
                <StatusPill status={String(o.status)} label={`${label(o.status)} · ${String(o.filled)}/${String(o.slots)}`} />
                <span className="oc-spacer" />
                {editable && o.status === 'open' && <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setAct({ title: 'Withdraw open shift', path: `${HR}/open-shifts/${String(o.id)}:cancel` })}>Withdraw</button>}
              </div>
              {((o.claims as R[]) ?? []).length > 0 && (
                <DataTable rows={(o.claims as R[]) ?? []} columns={[
                  { key: 'employeeName', header: 'Claimed by' }, { key: 'note', header: 'Note', render: (c) => String(c.note ?? '—') },
                  { key: 'status', header: 'Status', render: (c) => <StatusPill status={String(c.status)} label={label(c.status)} /> },
                ]} actions={(c) => (editable && c.status === 'requested' && o.status === 'open' ? (
                  <div className="oc-row" style={{ gap: 4 }}>
                    <button className="oc-btn oc-btn-text oc-btn-sm" onClick={() => setAct({ title: 'Reject claim', path: `${HR}/open-shift-claims/${String(c.id)}:reject`, required: true, danger: true })}>Reject</button>
                    <button className="oc-btn oc-btn-ink oc-btn-sm" onClick={() => setAct({ title: 'Approve claim', path: `${HR}/open-shift-claims/${String(c.id)}:approve` })}>Approve</button>
                  </div>
                ) : null)} />
              )}
            </div>
          ))}
        </div>
      )}
      {post && (
        <Modal open onClose={() => setPost(false)} title="Post open shift" actions={<>
          <button className="oc-btn oc-btn-text" onClick={() => setPost(false)}>Cancel</button>
          <button className="oc-btn oc-btn-ink" disabled={create.isPending} onClick={() => create.mutate({ workDate: f.workDate, shiftTemplateId: f.shiftTemplateId,
            positionId: f.positionId || undefined, slots: Number(f.slots || 1), notes: f.notes || undefined }, { onSuccess: () => { toast('Open shift posted'); setPost(false); setF({ slots: '1' }); } })}>Post</button>
        </>}>
          <div className="oc-stack">
            <ErrorAlert error={create.error} />
            <div className="oc-form">
              <SelectField label="Date" value={f.workDate ?? ''} onChange={(v) => setF({ ...f, workDate: v })} options={days.map((d) => ({ value: d, label: date(d) }))} required />
              <SelectField label="Shift" value={f.shiftTemplateId ?? ''} onChange={(v) => setF({ ...f, shiftTemplateId: v })} options={templates} required />
              <SelectField label="Position (empty = anyone in the department)" value={f.positionId ?? ''} onChange={(v) => setF({ ...f, positionId: v })}
                options={(positions.data?.items ?? []).map((p) => ({ value: String(p.id), label: String(p.name) }))} placeholder="Any" />
              <TextField label="People needed" type="number" value={f.slots} onChange={(v) => setF({ ...f, slots: v })} required />
              <TextArea label="Notes" value={f.notes ?? ''} onChange={(v) => setF({ ...f, notes: v })} span />
            </div>
          </div>
        </Modal>
      )}
      {act && <NoteAction {...act} onClose={() => { setAct(null); void list.refetch(); }} />}
    </Card>
  );
}

// ── Employee Self Service → Open Shifts ───────────────────────────────────

function EssOpenShifts({ base }: { base: string }) {
  const toast = useToast();
  const list = useGet<Page<R>>(`${ESS}/open-shifts`);
  const [claim, setClaim] = useState<R | null>(null);
  const items = list.data?.items ?? [];
  const send = useSend<Record<string, unknown>, R>('POST', () => `${ESS}/open-shifts/${String(claim?.id)}:claim`, INV);
  const [note, setNote] = useState('');
  return (
    <div className="oc-stack">
      <div className="oc-row" style={{ gap: 8 }}>
        <Link className="oc-btn oc-btn-text" to={base} aria-label="Back to Employee Self Service"><Icon name="arrow_back" size={22} /></Link>
        <h1 style={{ margin: 0, fontSize: 24 }}>Open Shifts</h1>
      </div>
      <ErrorAlert error={list.error} />
      {!list.isLoading && !list.error && items.length === 0 && <Empty title="No open shift" help="Extra shifts of your department appear here; claim one and your manager confirms it." icon="event_available" />}
      {items.map((o) => (
        <div key={String(o.id)} className="oc-card">
          <div className="oc-card-head"><span className="oc-icon-circle"><Icon name="event_available" size={22} /></span>
            <h3>{date(o.workDate)} · {String(o.shiftName)}</h3><span className="oc-spacer" />
            {o.myClaim ? <StatusPill status={String(o.myClaim)} label={label(o.myClaim)} /> : null}</div>
          <p style={{ margin: '0 0 8px' }}>{String(o.startTime)}–{String(o.endTime)} · {String(o.orgUnitName)}{o.positionName ? ` · ${String(o.positionName)}` : ''} · {Number(o.slots) - Number(o.filled)} place(s) left</p>
          {o.notes ? <p className="oc-small oc-muted" style={{ marginTop: 0 }}>{String(o.notes)}</p> : null}
          {(!o.myClaim || o.myClaim === 'withdrawn' || o.myClaim === 'rejected') && (
            <button className="oc-btn oc-btn-ink" style={{ minHeight: 44 }} onClick={() => { setNote(''); setClaim(o); }}>Claim this shift</button>
          )}
        </div>
      ))}
      {claim && (
        <Modal open onClose={() => setClaim(null)} title="Claim open shift" actions={<>
          <button className="oc-btn oc-btn-text" onClick={() => setClaim(null)}>Cancel</button>
          <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(note ? { note } : {}, {
            onSuccess: () => { toast('Claim sent to your manager'); setClaim(null); } })}>Claim</button>
        </>}>
          <ErrorAlert error={send.error} />
          <p style={{ marginTop: 0 }}>{date(claim.workDate)} · {String(claim.shiftName)} {String(claim.startTime)}–{String(claim.endTime)}</p>
          <TextArea label="Note to your manager (optional)" value={note} onChange={setNote} />
        </Modal>
      )}
    </div>
  );
}

registerEssSection('open-shifts', ({ base }) => <EssOpenShifts base={base} />);
