import React, { useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime, formatNumber } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { CRMOverviewPage } from './crm_overview';
import { CustomerTierBadge, CustomerTierCard, TierClassesPanel, TierOverridesPanel } from './tiers';

// PRD P5 — advanced segmentation, loyalty, journeys and CRM analytics (EP-17–20). Back Office routes, ops tiles and ops routes of the area
// (registered in p3/index.tsx and ops/p3.tsx). Naming Convention §7.6: CRM → Journeys, VIP Customers, Loyalty (Tiers, Rewards, Eligibility),
// CRM Analytics; ops Front Desk → VIP Lookup.

const CRM = ['/api/v1/crm'];
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const num = (v: unknown) => formatNumber(Number(v ?? 0));
const pct = (v: unknown) => `${(Number(v ?? 0) * 100).toFixed(1)}%`;
const day = (v: unknown) => (v ? formatDate(String(v)) : '—');
const when = (v: unknown) => (v ? formatDateTime(String(v)) : '—');
const rows = (v: unknown) => ((v as R[] | undefined) ?? []).map((x, i) => ({ ...x, id: String(x.id ?? x.key ?? i) })) as R[];
/** Rows keyed by a field (or the index). */
const keyed = (v: unknown, k: string) => ((v as R[] | undefined) ?? []).map((x, i) => ({ ...x, id: String(x[k] ?? i) })) as R[];
const CHANNELS = opts(['whatsapp', 'email', 'in_app'], { whatsapp: 'WhatsApp', in_app: 'In-app / Member App' });

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

/** Customer search + select. */
function CustomerPicker({ value, onChange, required, label: lbl = 'Customer' }: { value: string; onChange: (v: string) => void; required?: boolean; label?: string }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  return (
    <>
      <TextField label={`Find ${lbl.toLowerCase()}`} value={q} onChange={setQ} placeholder="Name, phone, e-mail or code" />
      <SelectField label={lbl} value={value} onChange={onChange} required={required} placeholder="Select"
        options={(list.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} (${String(c.code)})` }))} />
    </>
  );
}

// ── Journeys (EP-19) ──────────────────────────────────────────────────────

const JOURNEY_TABS: Option[] = [{ value: 'journeys', label: 'Journeys' }, { value: 'experiments', label: 'Experiments (A/B & control)' }];

export function JourneysPage() {
  const { can } = useAuth();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'journeys';
  const [tpl, setTpl] = useState(false);
  const [custom, setCustom] = useState(false);
  return (
    <div className="oc-stack">
      <PageHeader title="Journeys" help="Multi-step lifecycle automation (renewal, birthday, welcome, win-back, post-event). Consent, suppression, the frequency cap (2 per week, 6 per month) and quiet hours (21.00–08.00) apply at every message — Settings → Club Policies → Campaign Policies."
        actions={can('crm.journey.create') ? <Actions>
          <button className="oc-btn oc-btn-primary" onClick={() => setTpl(true)}>New from Template</button>
          <button className="oc-btn oc-btn-neutral" onClick={() => setCustom(true)}>New Journey</button>
          {can('crm.journey.run') && <ActionButton label="Run All Now" path="/api/v1/crm/journeys:run" invalidate={CRM} />}
        </Actions> : undefined} />
      <Tabs tabs={JOURNEY_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'journeys' && <ListPage title="Journey List" path="/api/v1/crm/journeys" statuses={opts(['draft', 'pending', 'active', 'paused', 'completed'])}
        onRowClick={(r) => navigate(`/crm/journeys/${r.id}`)}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'template', header: 'Template', render: (r) => label(r.template) },
          { key: 'triggerType', header: 'Trigger', render: (r) => `${label(r.triggerType)}${r.triggerEvent ? ` · ${String(r.triggerEvent)}` : ''}${r.triggerDate ? ` · ${label(r.triggerDate)}` : ''}` },
          { key: 'category', header: 'Category', render: (r) => label(r.category) }, { key: 'enrolled', header: 'Enrolled', align: 'right' },
          { key: 'activeEnrollments', header: 'In progress', align: 'right' }, { key: 'lastRunAt', header: 'Last run', render: (r) => when(r.lastRunAt) },
          { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'experiments' && <Experiments />}
      {tpl && <TemplateModal onClose={() => setTpl(false)} onDone={(id) => navigate(`/crm/journeys/${id}`)} />}
      {custom && <JourneyEditor onClose={() => setCustom(false)} onDone={(id) => navigate(`/crm/journeys/${id}`)} />}
    </div>
  );
}

function TemplateModal({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const t = useGet<Page<R>>('/api/v1/crm/journey-templates');
  const [f, setF] = useState({ template: '', code: '', name: '', voucherTypeRef: '' });
  const send = useSend<R, R>('POST', '/api/v1/crm/journeys:from-template', CRM);
  const chosen = (t.data?.items ?? []).find((x) => x.template === f.template);
  return (
    <Modal open onClose={onClose} title="New Journey from Template" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.template || send.isPending}
        onClick={() => send.mutate({ template: f.template, code: f.code || undefined, name: f.name || undefined, voucherTypeRef: f.voucherTypeRef || undefined } as unknown as R,
          { onSuccess: (j) => onDone(j.id) })}>Create Draft</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Template" value={f.template} onChange={(v) => setF({ ...f, template: v })} required placeholder="Select"
          options={(t.data?.items ?? []).map((x) => ({ value: String(x.template), label: `${Number(x.priority) > 0 ? `${String(x.priority)}. ` : ''}${String(x.name)}` }))} />
        <TextField label="Code (optional)" value={f.code} onChange={(v) => setF({ ...f, code: v })} placeholder={String(chosen?.code ?? '')} />
        <TextField label="Name (optional)" value={f.name} onChange={(v) => setF({ ...f, name: v })} span />
        {f.template === 'win_back' && <TextField label="F&B voucher type (Commercial code)" value={f.voucherTypeRef} onChange={(v) => setF({ ...f, voucherTypeRef: v })} placeholder="JRN-FNB100" />}
      </div>
      {chosen && <p className="oc-muted">{String(chosen.description)}</p>}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

type Step = Record<string, unknown> & { key: string; stepType: string };
const STEP_TYPES = opts(['message', 'wait', 'condition', 'voucher', 'points', 'reward', 'sales_task', 'tag', 'exit'], { sales_task: 'Sales task' });
const CONDITIONS = opts(['booked', 'paid', 'renewed', 'in_segment', 'tier', 'clicked', 'opted_in'], { in_segment: 'In segment', opted_in: 'Opted in (channel)' });
const TRIGGER_EVENTS = ['membership.activated', 'membership.renewed', 'banquet.event_completed', 'banquet.event_confirmed', 'billing.payment_settled',
  'golf.booking_confirmed', 'golf.booking_cancelled', 'golf.round_finished', 'crm.tier_changed', 'crm.ticket_resolved', 'commercial.package_booked'];
const EVENT_OPTS: Option[] = TRIGGER_EVENTS.map((e) => ({ value: e, label: e }));

/** Clean a step for the API (drop empty fields). */
function cleanStep(s: Step): Step {
  const out: Step = { key: s.key, stepType: s.stepType };
  for (const [k, v] of Object.entries(s)) {
    if (v === '' || v === undefined || v === null || k === 'id' || k === 'position') continue;
    if (['waitDays', 'waitHours', 'untilDaysBefore', 'splitPercent', 'offerValidDays', 'taskDueDays', 'points'].includes(k)) out[k] = Number(v);
    else out[k] = v;
  }
  return out;
}

function StepEditor({ step, onChange, onRemove }: { step: Step; onChange: (s: Step) => void; onRemove: () => void }) {
  const set = (k: string) => (v: string) => onChange({ ...step, [k]: v });
  const v = (k: string) => String(step[k] ?? '');
  return (
    <Card title={`${step.key || 'step'} · ${label(step.stepType)}`} icon="route" actions={<button className="oc-btn oc-btn-sm oc-btn-text" onClick={onRemove}>Remove</button>}>
      <div className="oc-form">
        <TextField label="Key" value={step.key} onChange={set('key')} required help="a-z, 0-9, _" />
        <SelectField label="Step type" value={step.stepType} onChange={set('stepType')} options={STEP_TYPES} />
        <TextField label="Name" value={v('name')} onChange={set('name')} />
        <TextField label="Next step (key, exit or end)" value={v('nextKey')} onChange={set('nextKey')} />
        {step.stepType === 'message' && <>
          <SelectField label="Channel" value={v('channel')} onChange={set('channel')} options={CHANNELS} placeholder="Select" />
          <TextField label="Subject" value={v('subject')} onChange={set('subject')} />
          <TextArea label="Message (variant A)" value={v('body')} onChange={set('body')} span rows={3} help="{{.name}}, {{.date}}, {{.detail}}, {{.promoCode}}, {{.offer}}, {{.link}}" />
          <TextField label="A/B: % to variant B" type="number" value={v('splitPercent')} onChange={set('splitPercent')} />
          <TextArea label="Message (variant B)" value={v('bodyB')} onChange={set('bodyB')} span rows={2} />
          <TextField label="Offer title (Member App)" value={v('offerTitle')} onChange={set('offerTitle')} />
          <TextField label="Promo code" value={v('promoCode')} onChange={set('promoCode')} />
          <TextField label="Offer valid (days)" type="number" value={v('offerValidDays')} onChange={set('offerValidDays')} />
        </>}
        {step.stepType === 'wait' && <>
          <TextField label="Wait days" type="number" value={v('waitDays')} onChange={set('waitDays')} />
          <TextField label="Wait hours" type="number" value={v('waitHours')} onChange={set('waitHours')} />
          <TextField label="Or until N days before the anchor date" type="number" value={v('untilDaysBefore')} onChange={set('untilDaysBefore')} />
        </>}
        {step.stepType === 'condition' && <>
          <SelectField label="Condition" value={v('conditionKind')} onChange={set('conditionKind')} options={CONDITIONS} placeholder="Select" />
          <TextField label="Value (segment id, tier codes, channel)" value={v('conditionValue')} onChange={set('conditionValue')} />
          <TextField label="If true: step" value={v('onTrue')} onChange={set('onTrue')} />
          <TextField label="If false: step" value={v('onFalse')} onChange={set('onFalse')} />
        </>}
        {step.stepType === 'points' && <TextField label="Bonus points" type="number" value={v('points')} onChange={set('points')} />}
        {step.stepType === 'voucher' && <TextField label="Voucher type (Commercial code)" value={v('voucherTypeRef')} onChange={set('voucherTypeRef')} />}
        {step.stepType === 'reward' && <RewardSelect value={v('rewardId')} onChange={set('rewardId')} />}
        {step.stepType === 'sales_task' && <>
          <TextField label="Task subject" value={v('taskSubject')} onChange={set('taskSubject')} span />
          <TextField label="Due in (days)" type="number" value={v('taskDueDays')} onChange={set('taskDueDays')} />
        </>}
        {step.stepType === 'tag' && <TextField label="Tag" value={v('tag')} onChange={set('tag')} />}
      </div>
    </Card>
  );
}

function RewardSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const rw = useGet<Page<R>>('/api/v1/crm/loyalty/rewards?limit=200&filter[status]=active');
  return <SelectField label="Reward" value={value} onChange={onChange} placeholder="Select"
    options={(rw.data?.items ?? []).map((r) => ({ value: r.id, label: `${String(r.name)} (${String(r.code)})` }))} />;
}

/** Create (no id) or edit a draft / paused journey with its steps. */
function JourneyEditor({ journey, onClose, onDone }: { journey?: R; onClose: () => void; onDone: (id: string) => void }) {
  const segs = useGet<Page<R>>('/api/v1/crm/segments?limit=200&filter[status]=active');
  const [f, setF] = useState<Record<string, string>>({
    code: String(journey?.code ?? ''), name: String(journey?.name ?? ''), description: String(journey?.description ?? ''),
    category: String(journey?.category ?? 'marketing'), triggerType: String(journey?.triggerType ?? 'manual'), triggerEvent: String(journey?.triggerEvent ?? ''),
    triggerSegmentId: String(journey?.triggerSegmentId ?? ''), triggerDate: String(journey?.triggerDate ?? ''), triggerDays: String(journey?.triggerDays ?? '0'),
    exitEvents: ((journey?.exitEvents as string[] | undefined) ?? []).join(', '), goalEvent: String(journey?.goalEvent ?? ''),
    goalDays: String(journey?.goalDays ?? '7'), reEntryDays: String(journey?.reEntryDays ?? '0'), controlPercent: String(journey?.controlPercent ?? '0'),
  });
  const [steps, setSteps] = useState<Step[]>(((journey?.steps as Step[] | undefined) ?? [{ key: 'message', stepType: 'message', channel: 'whatsapp', body: 'Halo {{.name}}, ' }]));
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>(journey ? 'PUT' : 'POST', journey ? `/api/v1/crm/journeys/${String(journey.id)}` : '/api/v1/crm/journeys', CRM);
  const fe = send.error?.fieldErrors ?? {};
  const body = {
    code: f.code, name: f.name, description: f.description || undefined, category: f.category, triggerType: f.triggerType,
    triggerEvent: f.triggerType === 'event' ? f.triggerEvent : undefined, triggerSegmentId: f.triggerType === 'segment_entry' ? f.triggerSegmentId || undefined : undefined,
    triggerDate: f.triggerType === 'date' ? f.triggerDate : undefined, triggerDays: f.triggerType === 'date' ? Number(f.triggerDays || 0) : undefined,
    exitEvents: f.exitEvents.split(',').map((x) => x.trim()).filter(Boolean), goalEvent: f.goalEvent || undefined, goalDays: Number(f.goalDays || 7),
    reEntryDays: Number(f.reEntryDays || 0), controlPercent: Number(f.controlPercent || 0), steps: steps.map(cleanStep),
  };
  return (
    <Modal open onClose={onClose} title={journey ? `Edit ${String(journey.code)}` : 'New Journey'} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.code || !f.name || send.isPending} onClick={() => send.mutate(body as unknown as R, { onSuccess: (j) => onDone(j.id) })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextField label="Code" value={f.code} onChange={set('code')} required error={fe.code} />
        <TextField label="Name" value={f.name} onChange={set('name')} required error={fe.name} />
        <TextArea label="Description" value={f.description} onChange={set('description')} span rows={2} />
        <SelectField label="Category" value={f.category} onChange={set('category')} options={opts(['marketing', 'transactional'])}
          help="Transactional messages are not counted in the frequency cap and not deferred by the quiet hours" />
        <SelectField label="Trigger" value={f.triggerType} onChange={set('triggerType')} options={opts(['manual', 'event', 'segment_entry', 'date'], { segment_entry: 'Segment entry' })} />
        {f.triggerType === 'event' && <SelectField label="Event" value={f.triggerEvent} onChange={set('triggerEvent')} options={EVENT_OPTS} placeholder="Select" error={fe.triggerEvent} />}
        {f.triggerType === 'segment_entry' && <SelectField label="Segment" value={f.triggerSegmentId} onChange={set('triggerSegmentId')} placeholder="Select"
          options={(segs.data?.items ?? []).map((s) => ({ value: s.id, label: String(s.name) }))} />}
        {f.triggerType === 'date' && <>
          <SelectField label="Date" value={f.triggerDate} onChange={set('triggerDate')} options={opts(['birthday', 'membership_expiry', 'last_visit'])} placeholder="Select" />
          <TextField label="Days (ahead; last visit: days ago)" type="number" value={f.triggerDays} onChange={set('triggerDays')} />
        </>}
        <TextField label="Exit on events (comma separated)" value={f.exitEvents} onChange={set('exitEvents')} span help={TRIGGER_EVENTS.join(', ')} />
        <SelectField label="Goal (conversion) event" value={f.goalEvent} onChange={set('goalEvent')} options={EVENT_OPTS} placeholder="None" />
        <TextField label="Goal window (days)" type="number" value={f.goalDays} onChange={set('goalDays')} />
        <TextField label="Re-entry after (days, 0 = once per occurrence)" type="number" value={f.reEntryDays} onChange={set('reEntryDays')} />
        <TextField label="Control group (%)" type="number" value={f.controlPercent} onChange={set('controlPercent')} help="No messages or incentives; measures the lift" />
      </div>
      <h3>Steps</h3>
      <div className="oc-stack">
        {steps.map((s, i) => (
          <StepEditor key={i} step={s} onChange={(n) => setSteps(steps.map((x, j) => (j === i ? n : x)))} onRemove={() => setSteps(steps.filter((_, j) => j !== i))} />
        ))}
        <button className="oc-btn oc-btn-neutral" onClick={() => setSteps([...steps, { key: `step_${steps.length + 1}`, stepType: 'message', channel: 'whatsapp' }])}>Add Step</button>
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

const DETAIL_TABS: Option[] = [{ value: 'steps', label: 'Steps' }, { value: 'performance', label: 'Performance' }, { value: 'enrollments', label: 'Enrollments' },
  { value: 'events', label: 'Journey Events' }];

export function JourneyDetailPage() {
  const { id = '' } = useParams();
  const { can } = useAuth();
  const navigate = useNavigate();
  const [tab, setTab] = useState('steps');
  const [edit, setEdit] = useState(false);
  const [enrol, setEnrol] = useState(false);
  const j = useGet<R>(`/api/v1/crm/journeys/${id}`);
  const x = j.data;
  if (j.isLoading) return <Skeleton rows={8} />;
  if (!x) return <ErrorAlert error={j.error} />;
  const st = String(x.status);
  return (
    <div className="oc-stack">
      <PageHeader title={`${String(x.code)} · ${String(x.name)}`} help={String(x.description ?? '')} actions={<Actions>
        <Link className="oc-btn oc-btn-neutral" to="/crm/journeys">Journeys</Link>
        {can('crm.journey.update') && ['draft', 'paused'].includes(st) && <button className="oc-btn oc-btn-neutral" onClick={() => setEdit(true)}>Edit</button>}
        {can('crm.journey.activate') && ['draft', 'paused'].includes(st) && <ActionButton label={st === 'paused' ? 'Resume' : 'Activate'} kind="primary" path={`/api/v1/crm/journeys/${id}:activate`} invalidate={CRM} />}
        {can('crm.journey.activate') && st === 'active' && <ActionButton label="Pause" path={`/api/v1/crm/journeys/${id}:pause`} invalidate={CRM} />}
        {can('crm.journey.run') && st === 'active' && <ActionButton label="Run Now" path={`/api/v1/crm/journeys/${id}:run`} invalidate={CRM} />}
        {can('crm.journey.enroll') && st === 'active' && <button className="oc-btn oc-btn-neutral" onClick={() => setEnrol(true)}>Enroll Customers</button>}
        {can('crm.journey.activate') && ['active', 'paused'].includes(st) && <ActionButton label="Complete" danger reason="required" path={`/api/v1/crm/journeys/${id}:complete`} invalidate={CRM} />}
        {can('crm.journey.delete') && ['draft', 'completed'].includes(st) && <ActionButton label="Archive" danger confirm="Archive this journey?" path={`/api/v1/crm/journeys/${id}:archive`} invalidate={CRM} onDone={() => navigate('/crm/journeys')} />}
      </Actions>} />
      <div className="oc-grid">
        <Card title="Journey" icon="route"><KV items={[['Status', <StatusPill key="s" status={st} />], ['Template', label(x.template)], ['Category', label(x.category)],
          ['Trigger', `${label(x.triggerType)} ${String(x.triggerEvent ?? '')}${x.triggerDate ? ` · ${label(x.triggerDate)} (${String(x.triggerDays)} days)` : ''}`],
          ['Exit on', ((x.exitEvents as string[]) ?? []).join(', ') || '—'], ['Goal', x.goalEvent ? `${String(x.goalEvent)} within ${String(x.goalDays)} days` : '—'],
          ['Control group', `${String(x.controlPercent)}%`], ['Enrolled', `${num(x.enrolled)} (${num(x.activeEnrollments)} in progress)`], ['Last run', when(x.lastRunAt)]]} /></Card>
      </div>
      <Tabs tabs={DETAIL_TABS} value={tab} onChange={setTab} />
      {tab === 'steps' && <DataTable rows={keyed(x.steps, 'key')} columns={[{ key: 'key', header: 'Key' }, { key: 'name', header: 'Name' },
        { key: 'stepType', header: 'Type', render: (r) => label(r.stepType) },
        { key: 'detail', header: 'Detail', render: (r) => stepDetail(r) }, { key: 'next', header: 'Next', render: (r) => String(r.nextKey ?? r.onTrue ?? '→') }]} />}
      {tab === 'performance' && <JourneyPerformance id={id} />}
      {tab === 'enrollments' && <Enrollments id={id} active={st === 'active'} />}
      {tab === 'events' && <ListPage title="Journey Events" path={`/api/v1/crm/journeys/${id}/events`} search={false}
        columns={[{ key: 'createdAt', header: 'Time', render: (r) => when(r.createdAt) }, { key: 'customerName', header: 'Customer' }, { key: 'stepKey', header: 'Step' },
          { key: 'outcome', header: 'Outcome', render: pill('outcome') }, { key: 'channel', header: 'Channel', render: (r) => label(r.channel ?? '—') },
          { key: 'variant', header: 'Variant', render: (r) => String(r.variant ?? '—') }, { key: 'readAt', header: 'Read', render: (r) => when(r.readAt) },
          { key: 'clickedAt', header: 'Clicked', render: (r) => when(r.clickedAt) }]} />}
      {edit && <JourneyEditor journey={x} onClose={() => setEdit(false)} onDone={() => { setEdit(false); void j.refetch(); }} />}
      {enrol && <EnrollModal id={id} onClose={() => setEnrol(false)} />}
    </div>
  );
}

function stepDetail(r: R): string {
  switch (r.stepType) {
    case 'message': return `${label(r.channel)}: ${String(r.subject ?? r.body ?? '').slice(0, 60)}${Number(r.splitPercent) > 0 ? ` · A/B ${String(r.splitPercent)}% B` : ''}`;
    case 'wait': return r.untilDaysBefore != null ? `until H-${String(r.untilDaysBefore)}` : `${String(r.waitDays ?? 0)} days ${String(r.waitHours ?? 0)} hours`;
    case 'condition': return `${label(r.conditionKind)} ${String(r.conditionValue ?? '')} → yes: ${String(r.onTrue ?? 'next')} / no: ${String(r.onFalse ?? 'next')}`;
    case 'points': return `${num(r.points)} points`;
    case 'voucher': return String(r.voucherTypeRef ?? '');
    case 'sales_task': return String(r.taskSubject ?? '');
    case 'tag': return String(r.tag ?? '');
    default: return '';
  }
}

function JourneyPerformance({ id }: { id: string }) {
  const p = useGet<R>(`/api/v1/crm/journeys/${id}/performance`);
  const x = p.data;
  if (!x) return <Skeleton />;
  const tr = (x.treatment ?? {}) as R;
  const ct = (x.control ?? {}) as R;
  return (
    <div className="oc-stack">
      <div className="oc-grid">
        <Card title="Journey" icon="insights"><KV items={[['Enrolled', num(x.enrolled)], ['In progress', num(x.active)], ['Completed', num(x.completed)],
          ['Exited', num(x.exited)], ['Converted', `${num(x.converted)} (${pct(x.conversionRate)})`], ['Attributed revenue', money(x.revenue)]]} /></Card>
        <Card title="Messages" icon="forum"><KV items={[['Sent', num(x.sent)], ['Read', num(x.read)], ['Clicked', num(x.clicked)]]} /></Card>
        <Card title="Control Group" icon="science"><KV items={[['Treatment', `${num(tr.enrolled)} · ${pct(tr.conversionRate)}`],
          ['Control', `${num(ct.enrolled)} · ${pct(ct.conversionRate)}`], ['Lift', pct(x.lift)]]} /></Card>
      </div>
      <DataTable rows={keyed(x.steps, 'key')} columns={[{ key: 'key', header: 'Step' }, { key: 'stepType', header: 'Type', render: (r) => label(r.stepType) },
        { key: 'executed', header: 'Executed', align: 'right' }, { key: 'sent', header: 'Sent', align: 'right' },
        { key: 'skipped', header: 'Skipped', render: (r) => Object.entries((r.skipped ?? {}) as Record<string, number>).map(([k, v]) => `${label(k)} ${v}`).join(', ') || '—' },
        { key: 'read', header: 'Read', align: 'right' }, { key: 'clicked', header: 'Clicked', align: 'right' }, { key: 'converted', header: 'Converted', align: 'right' },
        { key: 'revenue', header: 'Revenue', align: 'right', render: (r) => money(r.revenue) },
        { key: 'variants', header: 'A / B', render: (r) => rows(r.variants).map((v) => `${String(v.variant)}: ${num(v.sent)} sent, ${pct(v.conversionRate)}`).join(' · ') || '—' }]} />
    </div>
  );
}

function Enrollments({ id, active }: { id: string; active: boolean }) {
  const { can } = useAuth();
  return (
    <ListPage title="Enrollments" path={`/api/v1/crm/journeys/${id}/enrollments`} search={false} statuses={opts(['active', 'completed', 'exited'])}
      columns={[{ key: 'customerName', header: 'Customer' }, { key: 'occurrence', header: 'Occurrence' }, { key: 'cohort', header: 'Group', render: (r) => label(r.cohort) },
        { key: 'currentKey', header: 'Current step', render: (r) => String(r.currentKey ?? '—') }, { key: 'nextRunAt', header: 'Next run', render: (r) => when(r.nextRunAt) },
        { key: 'enteredAt', header: 'Entered', render: (r) => when(r.enteredAt) }, { key: 'convertedAt', header: 'Converted', render: (r) => when(r.convertedAt) },
        { key: 'exitReason', header: 'Exit', render: (r) => String(r.exitReason ?? '—') }, { key: 'status', header: 'Status', render: pill('status') }]}
      rowActions={(r) => (active && r.status === 'active' && can('crm.journey.enroll') ? (
        <ActionButton label="Exit" danger reason="required" path={`/api/v1/crm/journeys/${id}/enrollments/${r.id}:exit`} invalidate={CRM} />) : null)} />
  );
}

function EnrollModal({ id, onClose }: { id: string; onClose: () => void }) {
  const toast = useToast();
  const [cust, setCust] = useState('');
  const [list, setList] = useState<string[]>([]);
  const send = useSend<R, R>('POST', `/api/v1/crm/journeys/${id}/enrollments`, CRM);
  return (
    <Modal open onClose={onClose} title="Enroll Customers" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={list.length === 0 || send.isPending} onClick={() => send.mutate({ customerIds: list } as unknown as R, {
        onSuccess: (r) => { toast(`${String(r.enrolled)} enrolled, ${String(r.skipped)} skipped`); onClose(); },
      })}>Enroll {list.length}</button>
    </>}>
      <div className="oc-form"><CustomerPicker value={cust} onChange={(v) => { setCust(v); if (v && !list.includes(v)) setList([...list, v]); }} /></div>
      <p className="oc-muted">{list.length} customer(s) selected. Consent and the frequency cap are checked at every message.</p>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function Experiments() {
  const e = useGet<Page<R>>('/api/v1/crm/experiments');
  if (e.isLoading) return <Skeleton />;
  const items = e.data?.items ?? [];
  if (items.length === 0) return <Empty title="No experiments" help="Give a message step a B variant (A/B test) or a journey a control group." icon="science" />;
  return (
    <DataTable rows={items.map((x, i) => ({ ...x, id: `${String(x.journeyId)}-${String(x.stepKey ?? i)}` }) as R)} columns={[
      { key: 'journeyCode', header: 'Journey' }, { key: 'kind', header: 'Experiment', render: (r) => label(r.kind) },
      { key: 'stepKey', header: 'Step', render: (r) => String(r.stepKey ?? '—') }, { key: 'percent', header: '%', align: 'right' },
      { key: 'arms', header: 'Arms', render: (r) => rows(r.arms).map((a) => `${String(a.arm)}: ${num(a.size)} · ${pct(a.conversionRate)}`).join(' | ') },
      { key: 'leader', header: 'Leader', render: (r) => String(r.leader ?? '—') }]} />
  );
}

// ── Loyalty: Tiers, Rewards, Eligibility (EP-18) ──────────────────────────

export function LoyaltyTiersPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'tiers';
  const [kind, setKind] = useState('annual');
  const [open, setOpen] = useState<string | null>(null);
  const [cust, setCust] = useState('');
  return (
    <div className="oc-stack">
      <PageHeader title="Loyalty Tiers" help="Silver, Gold (≥ Rp25 jt / 12 months), Platinum (≥ Rp75 jt): points multiplier, booking window + days, F&B discount and VIP events. Annual evaluation with a 3-month grace before a downgrade — Settings → Club Policies → Loyalty Policies." />
      <Tabs tabs={[{ value: 'tiers', label: 'Tiers & Benefits' }, { value: 'overrides', label: 'Manual Classification' }, { value: 'evaluations', label: 'Tier Evaluations' },
        { value: 'lookup', label: 'Benefit Lookup' }]}
        value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'tiers' && <TierClassesPanel />}
      {tab === 'overrides' && <TierOverridesPanel />}
      {tab === 'evaluations' && <>
        {can('crm.loyalty_tier.update') && <Card title="Run Evaluation" icon="play_circle">
          <SelectField label="Kind" value={kind} onChange={setKind} options={opts(['annual', 'periodic', 'grace_review'], { annual: 'Annual (with grace)', periodic: 'Periodic (upgrades, ended grace)', grace_review: 'Grace review' })} />
          <ActionButton label="Run Now" kind="primary" confirm="Evaluate the tier of every active account now?" path="/api/v1/crm/loyalty/tier-evaluations" body={{ kind }} invalidate={CRM} />
        </Card>}
        <ListPage title="Tier Evaluations" path="/api/v1/crm/loyalty/tier-evaluations" search={false} onRowClick={(r) => setOpen(r.id)}
          columns={[{ key: 'number', header: 'Number' }, { key: 'kind', header: 'Kind', render: (r) => label(r.kind) }, { key: 'evaluatedOn', header: 'Date', render: (r) => day(r.evaluatedOn) },
            { key: 'windowFrom', header: 'Window', render: (r) => `${day(r.windowFrom)} – ${day(r.windowTo)}` }, { key: 'accounts', header: 'Accounts', align: 'right' },
            { key: 'upgraded', header: 'Upgraded', align: 'right' }, { key: 'graceStarted', header: 'Grace started', align: 'right' },
            { key: 'downgraded', header: 'Downgraded', align: 'right' }, { key: 'retained', header: 'Retained', align: 'right' }]} />
        {open && <EvaluationDrawer id={open} onClose={() => setOpen(null)} />}
      </>}
      {tab === 'lookup' && <>
        <Card title="Tier Benefits of a Customer" icon="workspace_premium"><div className="oc-form"><CustomerPicker value={cust} onChange={setCust} /></div></Card>
        {cust && <CustomerTierCard customerId={cust} />}
      </>}
    </div>
  );
}

function EvaluationDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const d = useGet<R>(`/api/v1/crm/loyalty/tier-evaluations/${id}`);
  return (
    <Drawer open onClose={onClose} title={d.data ? `Evaluation ${String(d.data.number)}` : 'Evaluation'}>
      {!d.data ? <Skeleton /> : <DataTable rows={rows(d.data.lines)} columns={[{ key: 'customerName', header: 'Customer' }, { key: 'fromTierName', header: 'From', render: (r) => String(r.fromTierName ?? '—') },
        { key: 'qualifiedTierName', header: 'Qualified', render: (r) => String(r.qualifiedTierName ?? '—') }, { key: 'toTierName', header: 'To', render: (r) => String(r.toTierName ?? '—') },
        { key: 'outcome', header: 'Outcome', render: pill('outcome') }, { key: 'spendBasis', header: 'Spend', align: 'right', render: (r) => money(r.spendBasis) },
        { key: 'graceUntil', header: 'Grace until', render: (r) => day(r.graceUntil) }]} />}
    </Drawer>
  );
}

export function LoyaltyRewardsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'catalogue';
  const [issue, setIssue] = useState(false);
  return (
    <div className="oc-stack">
      <PageHeader title="Rewards" help="Reward catalogue with stock, cost and validity; rewards given without points (staff, Top Spender programme, journeys) count toward the loyalty programme budget (2% of net revenue)." />
      <Tabs tabs={[{ value: 'catalogue', label: 'Reward Catalogue' }, { value: 'issued', label: 'Issued Rewards' }, { value: 'programs', label: 'Top Spender Programmes' },
        { value: 'budget', label: 'Programme Cost & Budget' }]} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'catalogue' && <AutoResourcePage resourceKey="crm.loyalty_reward" />}
      {tab === 'issued' && <>
        <ListPage title="Issued Rewards" path="/api/v1/crm/loyalty/reward-issues" search={false} statuses={opts(['issued', 'fulfilled', 'cancelled'])}
          actions={can('crm.loyalty_reward_issue.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setIssue(true)}>Issue Reward</button> : undefined}
          columns={[{ key: 'number', header: 'Number' }, { key: 'customerName', header: 'Customer' }, { key: 'rewardName', header: 'Reward' }, { key: 'source', header: 'Source', render: (r) => label(r.source) },
            { key: 'sourceRef', header: 'Reference', render: (r) => String(r.sourceRef ?? '—') }, { key: 'totalCost', header: 'Cost', align: 'right', render: (r) => money(r.totalCost) },
            { key: 'fulfilmentCode', header: 'Code', render: (r) => String(r.fulfilmentCode ?? '—') }, { key: 'status', header: 'Status', render: pill('status') }]}
          rowActions={(r) => (r.status === 'issued' && can('crm.loyalty_reward_issue.fulfil') ? <Actions>
            <ActionButton label="Handed Over" kind="primary" path={`/api/v1/crm/loyalty/reward-issues/${r.id}:fulfil`} invalidate={CRM} />
            <ActionButton label="Cancel" danger reason="required" path={`/api/v1/crm/loyalty/reward-issues/${r.id}:cancel`} invalidate={CRM} />
          </Actions> : null)} />
        {issue && <IssueModal onClose={() => setIssue(false)} />}
      </>}
      {tab === 'programs' && <TopSpenderPrograms />}
      {tab === 'budget' && <Budget />}
    </div>
  );
}

function IssueModal({ onClose }: { onClose: () => void }) {
  const [cust, setCust] = useState('');
  const [reward, setReward] = useState('');
  const [note, setNote] = useState('');
  const send = useSend<R, R>('POST', '/api/v1/crm/loyalty/reward-issues', CRM);
  return (
    <Modal open onClose={onClose} title="Issue Reward" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!cust || !reward || !note || send.isPending}
        onClick={() => send.mutate({ customerId: cust, rewardId: reward, note } as unknown as R, { onSuccess: onClose })}>Issue</button>
    </>}>
      <div className="oc-form"><CustomerPicker value={cust} onChange={setCust} required /><RewardSelect value={reward} onChange={setReward} />
        <TextField label="Reason" value={note} onChange={setNote} required span /></div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function TopSpenderPrograms() {
  const { can } = useAuth();
  const [period, setPeriod] = useState('');
  const progs = useGet<Page<R>>('/api/v1/crm/loyalty/top-spender-programs?limit=100&filter[status]=active');
  return (
    <div className="oc-stack">
      <AutoResourcePage resourceKey="crm.top_spender_program" />
      {can('crm.top_spender_program.update') && <Card title="Run a Programme" icon="emoji_events">
        <TextField label="Period (YYYY-MM, YYYY-Qn or YYYY; empty = last complete)" value={period} onChange={setPeriod} />
        <Actions>{(progs.data?.items ?? []).map((p) => (
          <ActionButton key={p.id} label={`Run ${String(p.code)}`} path={`/api/v1/crm/loyalty/top-spender-programs/${p.id}:run`} body={period ? { period } : {}} invalidate={CRM} />
        ))}</Actions>
      </Card>}
      <ListPage title="Programme Runs" path="/api/v1/crm/loyalty/top-spender-program-runs" search={false}
        columns={[{ key: 'programCode', header: 'Programme' }, { key: 'period', header: 'Period' }, { key: 'ranked', header: 'Ranked', align: 'right' },
          { key: 'issued', header: 'Rewards', align: 'right' }, { key: 'invited', header: 'Invited', align: 'right' }, { key: 'skippedBudget', header: 'Over budget', align: 'right' },
          { key: 'createdAt', header: 'Run at', render: (r) => when(r.createdAt) }]} />
    </div>
  );
}

function Budget() {
  const [period, setPeriod] = useState(today().slice(0, 7));
  const b = useGet<R>(`/api/v1/crm/loyalty/budget?period=${period}`);
  const x = b.data;
  return (
    <Card title="Loyalty Programme Cost" icon="savings">
      <TextField label="Month" type="month" value={period} onChange={setPeriod} />
      {!x ? <Skeleton /> : <KV items={[['Net revenue (loyalty lines)', money(x.netRevenue)], ['Budget', `${String(x.budgetPercent)}% = ${money(x.cap)}`],
        ['Points issued', `${num(x.pointsIssued)} × ${money(x.pointValue)} = ${money(x.pointsCost)}`], ['Rewards issued (cost)', money(x.rewardCost)],
        ['Rewards redeemed (cost, paid with points)', money(x.redeemedCost)], ['Programme cost', <strong key="t">{money(x.totalCost)}</strong>],
        ['Remaining', money(x.remaining)], ['Utilisation', <StatusPill key="u" status={x.withinBudget ? 'within budget' : 'over budget'} label={pct(x.utilisation)} />]]} />}
      <p className="oc-small oc-muted">Per month in the <Link to="/reports?module=crm&code=crm.loyalty_cost">Loyalty Programme Cost Report</Link>.</p>
    </Card>
  );
}

export function LoyaltyEligibilityPage() {
  const [acct, setAcct] = useState('');
  const accounts = useGet<Page<R>>('/api/v1/crm/loyalty/accounts?limit=200&filter[status]=active');
  const el = useGet<Page<R>>(acct ? `/api/v1/crm/loyalty/accounts/${acct}/eligible-rewards` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Reward Eligibility" help="A reward with eligibility rules is available to customers matching at least one rule (tier, segment, visits or spend in the lookback, validity) within the limit per customer." />
      <AutoResourcePage resourceKey="crm.loyalty_reward_rule" />
      <Card title="Check an Account" icon="fact_check">
        <SelectField label="Loyalty account" value={acct} onChange={setAcct} placeholder="Select"
          options={(accounts.data?.items ?? []).map((a) => ({ value: a.id, label: `${String(a.customerName)} · ${String(a.tierName ?? 'No tier')} · ${num(a.balance)} points` }))} />
        {el.data && <DataTable rows={el.data.items} columns={[{ key: 'name', header: 'Reward' }, { key: 'pointsCost', header: 'Points', align: 'right', render: (r) => num(r.pointsCost) },
          { key: 'eligible', header: 'Eligible', render: (r) => <StatusPill status={r.eligible ? 'eligible' : 'not eligible'} /> },
          { key: 'reason', header: 'Reason', render: (r) => label(r.reason ?? '—') }, { key: 'affordable', header: 'Enough points', render: (r) => (r.affordable ? 'Yes' : 'No') }]} />}
      </Card>
    </div>
  );
}

// ── VIP (EP-17 FR-SEG-03) ─────────────────────────────────────────────────

export function VIPPage() {
  const { can } = useAuth();
  const [add, setAdd] = useState(false);
  return (
    <div className="oc-stack">
      <ListPage title="VIP Customers" help="Automatic VIPs from the Top Spender rank and the loyalty tier (refreshed daily), manual VIPs by staff. The marker shows at check-in and POS."
        path="/api/v1/crm/vip" statuses={opts(['active', 'inactive'])}
        actions={can('crm.vip.manage') ? <Actions>
          <button className="oc-btn oc-btn-primary" onClick={() => setAdd(true)}>Add VIP</button>
          {can('crm.analytics.refresh') && <ActionButton label="Refresh Automatic VIPs" path="/api/v1/crm/analytics:refresh" invalidate={CRM} />}
        </Actions> : undefined}
        columns={[{ key: 'customerCode', header: 'Code' }, { key: 'customerName', header: 'Customer' }, { key: 'level', header: 'Level', render: (r) => String(r.level).toUpperCase() },
          { key: 'source', header: 'Source', render: (r) => label(r.source) }, { key: 'reason', header: 'Reason', render: (r) => String(r.reason ?? '—') },
          { key: 'handlingNote', header: 'Handling', render: (r) => String(r.handlingNote ?? '—') }, { key: 'validUntil', header: 'Valid until', render: (r) => day(r.validUntil) },
          { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => (r.status === 'active' && can('crm.vip.manage') ? <ActionButton label="End VIP" danger reason="required" path={`/api/v1/crm/vip/${r.id}:deactivate`} invalidate={CRM} /> : null)} />
      {add && <VIPModal onClose={() => setAdd(false)} />}
    </div>
  );
}

function VIPModal({ onClose }: { onClose: () => void }) {
  const [f, setF] = useState({ customerId: '', level: 'vip', reason: '', benefits: '', handlingNote: '', validUntil: '' });
  const send = useSend<R, R>('POST', '/api/v1/crm/vip', CRM);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  return (
    <Modal open onClose={onClose} title="Add VIP" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.customerId || !f.reason || send.isPending} onClick={() => send.mutate({ ...f, benefits: f.benefits || undefined,
        handlingNote: f.handlingNote || undefined, validUntil: f.validUntil || undefined } as unknown as R, { onSuccess: onClose })}>Save</button>
    </>}>
      <div className="oc-form">
        <CustomerPicker value={f.customerId} onChange={set('customerId')} required />
        <SelectField label="Level" value={f.level} onChange={set('level')} options={opts(['vip', 'vvip'], { vip: 'VIP', vvip: 'VVIP' })} />
        <TextField label="Reason" value={f.reason} onChange={set('reason')} required span />
        <TextField label="Benefits (default: Segmentation policy)" value={f.benefits} onChange={set('benefits')} span />
        <TextField label="Handling note" value={f.handlingNote} onChange={set('handlingNote')} span />
        <TextField label="Valid until" type="date" value={f.validUntil} onChange={set('validUntil')} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── CRM Analytics (EP-17 / EP-20) ─────────────────────────────────────────

const ANALYTICS_TABS: Option[] = [{ value: 'rfm', label: 'RFM & Segments' }, { value: 'clv', label: 'Lifetime Value & Cohorts' }, { value: 'nps', label: 'NPS' },
  { value: 'sla', label: 'Complaint SLA' }, { value: 'sales', label: 'Sales & Commission' }, { value: 'compare', label: 'Segment Comparison' }];

function PeriodBar({ from, to, onFrom, onTo }: { from: string; to: string; onFrom: (v: string) => void; onTo: (v: string) => void }) {
  return <div className="oc-row-wrap"><TextField label="From" type="date" value={from} onChange={onFrom} /><TextField label="To" type="date" value={to} onChange={onTo} /></div>;
}

function usePeriod(days: number) {
  const d = new Date();
  d.setDate(d.getDate() - days);
  const iso = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  const [from, setFrom] = useState(iso);
  const [to, setTo] = useState(today());
  return { from, to, setFrom, setTo, q: qs({ from, to }) };
}

export function CRMAnalyticsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? 'rfm';
  return (
    <div className="oc-stack">
      <PageHeader title="CRM Analytics" help="Scores and behaviour come from the analytics store, refreshed daily (01.05); reports are in CRM Reports."
        actions={can('crm.analytics.refresh') ? <ActionButton label="Refresh Now" path="/api/v1/crm/analytics:refresh" invalidate={CRM} /> : undefined} />
      <Tabs tabs={ANALYTICS_TABS} value={tab} onChange={(v) => setParams({ tab: v })} />
      {tab === 'rfm' && <RFMTab />}
      {tab === 'clv' && <CLVTab />}
      {tab === 'nps' && <NPSTab />}
      {tab === 'sla' && <SLATab />}
      {tab === 'sales' && <SalesTab />}
      {tab === 'compare' && <CompareTab />}
    </div>
  );
}

function RFMTab() {
  const [group, setGroup] = useState('champions');
  const [open, setOpen] = useState<string | null>(null);
  const s = useGet<R>('/api/v1/crm/analytics/rfm');
  const c = useGet<Page<R>>(`/api/v1/crm/analytics/rfm/customers?filter[group]=${group}&limit=100`);
  const mv = useGet<R>('/api/v1/crm/analytics/rfm/movement');
  const x = s.data;
  return (
    <div className="oc-stack">
      {!x ? <Skeleton /> : !x.asOf ? <Empty title="No scores yet" help="Refresh the analytics to score the customers." icon="query_stats" /> : <>
        <p className="oc-muted">Snapshot of {day(x.asOf)} · refreshed {when(x.refreshedAt)} · {num(x.customers)} customers · {num(x.multiLine)} cross-business</p>
        <DataTable rows={keyed(x.groups, 'group')} onRowClick={(r) => setGroup(String(r.group))} columns={[
          { key: 'group', header: 'Group', render: (r) => label(r.group) }, { key: 'customers', header: 'Customers', align: 'right' },
          { key: 'share', header: 'Share', align: 'right', render: (r) => pct(r.share) }, { key: 'monetary', header: 'Spend', align: 'right', render: (r) => money(r.monetary) },
          { key: 'avgRecencyDays', header: 'Avg recency (days)', align: 'right' }, { key: 'avgFrequency', header: 'Avg visit days', align: 'right' },
          { key: 'avgClv', header: 'Avg lifetime value', align: 'right', render: (r) => money(r.avgClv) }]} />
        <Card title="Cross-business behaviour" icon="hub"><DataTable rows={keyed(x.lines, 'businessLine')} columns={[
          { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) }, { key: 'customers', header: 'Active customers', align: 'right' },
          { key: 'monetary', header: 'Spend', align: 'right', render: (r) => money(r.monetary) }]} /></Card>
      </>}
      <Card title={`Customers · ${label(group)}`} icon="groups">
        <DataTable rows={keyed(c.data?.items, 'customerId')} onRowClick={(r) => setOpen(String(r.customerId))} columns={[
          { key: 'customerName', header: 'Customer' }, { key: 'rfm', header: 'R-F-M', render: (r) => `${String(r.rScore)}-${String(r.fScore)}-${String(r.mScore)}` },
          { key: 'recencyDays', header: 'Recency (days)', align: 'right' }, { key: 'frequency', header: 'Visit days', align: 'right' },
          { key: 'monetary', header: 'Spend', align: 'right', render: (r) => money(r.monetary) }, { key: 'clv', header: 'Lifetime value', align: 'right', render: (r) => money(r.clv) },
          { key: 'lines', header: 'Lines', render: (r) => ((r.lines as string[]) ?? []).map(label).join(', ') }, { key: 'vipLevel', header: 'VIP', render: (r) => String(r.vipLevel ?? '—').toUpperCase() }]} />
      </Card>
      {mv.data && <Card title={`Movement ${day(mv.data.from)} → ${day(mv.data.to)}`} icon="swap_horiz">
        <DataTable rows={keyed(mv.data.moves, '#')} columns={[{ key: 'fromGroup', header: 'From', render: (r) => label(r.fromGroup) },
          { key: 'toGroup', header: 'To', render: (r) => label(r.toGroup) }, { key: 'customers', header: 'Customers', align: 'right' }]} />
      </Card>}
      {open && <BehaviorDrawer id={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function BehaviorDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const b = useGet<R>(`/api/v1/crm/analytics/customers/${id}`);
  const x = b.data;
  const rfm = (x?.rfm ?? null) as R | null;
  const vip = (x?.vip ?? {}) as R;
  return (
    <Drawer open onClose={onClose} title="Customer Behaviour">
      {!x ? <Skeleton /> : <div className="oc-stack">
        <KV items={[['RFM group', label(rfm?.group ?? '—')], ['Scores', rfm ? `R${String(rfm.rScore)} F${String(rfm.fScore)} M${String(rfm.mScore)}` : '—'],
          ['Lifetime value', money(rfm?.clv)], ['VIP', vip.vip ? `${String(vip.level).toUpperCase()} · ${String(vip.handlingNote ?? '')}` : 'No'], ['Tier', String(vip.tierName ?? '—')]]} />
        <DataTable rows={keyed(x.lines, 'businessLine')} columns={[{ key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) },
          { key: 'lastAt', header: 'Last visit', render: (r) => day(r.lastAt) }, { key: 'frequency', header: 'Visit days', align: 'right' },
          { key: 'monetary', header: 'Spend', align: 'right', render: (r) => money(r.monetary) }]} />
        <p className="oc-small oc-muted">History: {rows(x.history).map((h) => `${day(h.asOf)} ${label(h.group)}`).join(' · ') || '—'}</p>
        <Link to={`/crm/customer-360?customerId=${id}`}>Customer 360</Link>
      </div>}
    </Drawer>
  );
}

function CLVTab() {
  const c = useGet<R>('/api/v1/crm/analytics/clv');
  const x = c.data;
  if (!x) return <Skeleton />;
  return (
    <div className="oc-stack">
      <div className="oc-grid">
        <Card title="Lifetime Value" icon="paid"><KV items={[['Customers scored', num(x.customers)], ['Total', money(x.totalClv)], ['Average', money(x.avgClv)]]} /></Card>
      </div>
      <Card title="Top Customers by Lifetime Value" icon="star"><DataTable rows={keyed(x.top, 'customerId')} columns={[
        { key: 'customerName', header: 'Customer' }, { key: 'group', header: 'Group', render: (r) => label(r.group) },
        { key: 'monetary', header: 'Spend (window)', align: 'right', render: (r) => money(r.monetary) }, { key: 'clv', header: 'Lifetime value', align: 'right', render: (r) => money(r.clv) }]} /></Card>
      <Card title="Retention per First-visit Cohort" icon="calendar_month"><DataTable rows={keyed(x.cohorts, 'cohort')} columns={[
        { key: 'cohort', header: 'Cohort' }, { key: 'customers', header: 'Customers', align: 'right' }, { key: 'rate1', header: 'Month 1', align: 'right', render: (r) => pct(r.rate1) },
        { key: 'rate3', header: 'Month 3', align: 'right', render: (r) => pct(r.rate3) }, { key: 'rate6', header: 'Month 6', align: 'right', render: (r) => pct(r.rate6) }]} /></Card>
    </div>
  );
}

function NPSTab() {
  const p = usePeriod(90);
  const [line, setLine] = useState('');
  const n = useGet<R>(`/api/v1/crm/analytics/nps${p.q}${line ? `&businessLine=${line}` : ''}`);
  const x = n.data;
  const o = (x?.overall ?? {}) as R;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><PeriodBar from={p.from} to={p.to} onFrom={p.setFrom} onTo={p.setTo} />
        <SelectField label="Business line" value={line} onChange={setLine} placeholder="All" options={opts(['golf', 'sportclub', 'stay', 'pos', 'membership', 'banquet', 'other'])} /></div>
      {!x ? <Skeleton /> : <>
        <div className="oc-grid">
          <Card title="NPS" icon="sentiment_satisfied" ink><div style={{ fontSize: 40, fontWeight: 700 }}>{String(o.nps)}</div>
            <p>{num(o.responses)} answers · {num(o.promoters)} promoters · {num(o.detractors)} detractors</p></Card>
          <Card title="Retention by NPS group" icon="autorenew"><DataTable rows={keyed(x.retention, 'group')} columns={[
            { key: 'group', header: 'Group', render: (r) => label(r.group) }, { key: 'customers', header: 'Customers', align: 'right' },
            { key: 'retentionRate', header: `Visited within ${String(x.retentionDays)} days`, align: 'right', render: (r) => pct(r.retentionRate) }]} /></Card>
        </div>
        <Card title="Drivers from comments" icon="psychology"><DataTable rows={keyed(x.drivers, 'driver')} columns={[
          { key: 'driver', header: 'Driver', render: (r) => label(r.driver) }, { key: 'mentions', header: 'Mentions', align: 'right' },
          { key: 'promoters', header: 'Promoters', align: 'right' }, { key: 'detractors', header: 'Detractors', align: 'right' }, { key: 'nps', header: 'NPS', align: 'right' }]} /></Card>
        <Card title="Trend" icon="trending_up"><DataTable rows={keyed(x.trend, '#')} columns={[{ key: 'month', header: 'Month' },
          { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) }, { key: 'responses', header: 'Answers', align: 'right' }, { key: 'nps', header: 'NPS', align: 'right' }]} /></Card>
        <Card title="Per context (outlet, venue, round …)" icon="storefront"><DataTable rows={keyed(x.byContext, 'key')} columns={[
          { key: 'key', header: 'Context', render: (r) => label(r.key) }, { key: 'responses', header: 'Answers', align: 'right' }, { key: 'nps', header: 'NPS', align: 'right' }]} /></Card>
      </>}
    </div>
  );
}

function SLATab() {
  const p = usePeriod(90);
  const s = useGet<R>(`/api/v1/crm/analytics/sla${p.q}`);
  const x = s.data;
  const groupCols = [{ key: 'key', header: 'Group', render: (r: R) => label(r.key) }, { key: 'tickets', header: 'Tickets', align: 'right' as const },
    { key: 'resolved', header: 'Resolved', align: 'right' as const }, { key: 'compliance', header: 'Within SLA', align: 'right' as const, render: (r: R) => pct(r.compliance) },
    { key: 'avgResolutionHours', header: 'Avg resolution (h)', align: 'right' as const }, { key: 'escalated', header: 'Escalated', align: 'right' as const }];
  return (
    <div className="oc-stack">
      <PeriodBar from={p.from} to={p.to} onFrom={p.setFrom} onTo={p.setTo} />
      {!x ? <Skeleton /> : <>
        <Card title="Complaint SLA" icon="timer"><KV items={[['Tickets', num(x.tickets)], ['Open', num(x.open)], ['Resolution within SLA', pct(x.resolutionCompliance)],
          ['First response within SLA', pct(x.firstResponseCompliance)], ['Avg resolution', `${String(x.avgResolutionHours)} h`], ['Escalated', num(x.escalated)], ['Reopened', num(x.reopened)]]} /></Card>
        <Card title="Per business line" icon="category"><DataTable rows={keyed(x.byLine, 'key')} columns={groupCols} /></Card>
        <Card title="Per priority" icon="priority_high"><DataTable rows={keyed(x.byPriority, 'key')} columns={groupCols} /></Card>
        <Card title="Recurring categories" icon="repeat"><DataTable rows={keyed(x.recurringCategories, 'category')} columns={[
          { key: 'category', header: 'Category' }, { key: 'tickets', header: 'Tickets', align: 'right' }, { key: 'customers', header: 'Customers', align: 'right' },
          { key: 'repeatCustomers', header: 'Repeat customers', align: 'right' }]} /></Card>
      </>}
    </div>
  );
}

function SalesTab() {
  const p = usePeriod(180);
  const s = useGet<R>(`/api/v1/crm/analytics/sales${p.q}`);
  const x = s.data;
  const groupCols = [{ key: 'key', header: 'Group', render: (r: R) => label(r.key) }, { key: 'opportunities', header: 'Opportunities', align: 'right' as const },
    { key: 'won', header: 'Won', align: 'right' as const }, { key: 'lost', header: 'Lost', align: 'right' as const }, { key: 'winRate', header: 'Win rate', align: 'right' as const, render: (r: R) => pct(r.winRate) },
    { key: 'wonValue', header: 'Won value', align: 'right' as const, render: (r: R) => money(r.wonValue) }, { key: 'avgCycleDays', header: 'Avg cycle (days)', align: 'right' as const }];
  const commCols = [{ key: 'key', header: 'Group' }, { key: 'deals', header: 'Deals', align: 'right' as const }, { key: 'revenue', header: 'Revenue', align: 'right' as const, render: (r: R) => money(r.revenue) },
    { key: 'commission', header: 'Commission', align: 'right' as const, render: (r: R) => money(r.commission) }, { key: 'rate', header: 'Rate', align: 'right' as const, render: (r: R) => pct(r.rate) }];
  return (
    <div className="oc-stack">
      <PeriodBar from={p.from} to={p.to} onFrom={p.setFrom} onTo={p.setTo} />
      {!x ? <Skeleton /> : <>
        <Card title="Sales Performance" icon="trending_up"><KV items={[['Opportunities', num(x.opportunities)], ['Won / lost / open', `${num(x.won)} / ${num(x.lost)} / ${num(x.open)}`],
          ['Win rate', pct(x.winRate)], ['Won value', money(x.wonValue)], ['Avg sales cycle', `${String(x.avgCycleDays)} days`],
          ['Commission', `${money(x.commissionTotal)} on ${money(x.commissionRevenue)} (${pct(x.commissionRate)})`]]} /></Card>
        <Card title="Funnel (conversion per stage)" icon="filter_alt"><DataTable rows={keyed(x.funnel, '#')} columns={[
          { key: 'pipeline', header: 'Pipeline' }, { key: 'stage', header: 'Stage' }, { key: 'reached', header: 'Reached', align: 'right' },
          { key: 'conversionToNext', header: 'To next stage', align: 'right', render: (r) => pct(r.conversionToNext) }]} /></Card>
        <Card title="Per sales person" icon="badge"><DataTable rows={keyed(x.byOwner, 'key')} columns={groupCols} /></Card>
        <Card title="Per line" icon="category"><DataTable rows={keyed(x.byLine, 'key')} columns={groupCols} /></Card>
        <Card title="Loss reasons" icon="thumb_down"><DataTable rows={keyed(x.lossReasons, 'reason')} columns={[
          { key: 'reason', header: 'Reason', render: (r) => label(r.reason) }, { key: 'count', header: 'Lost', align: 'right' }, { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) }]} /></Card>
        <Card title="Forecast vs actual" icon="query_stats"><DataTable rows={keyed(x.forecast, 'month')} columns={[
          { key: 'month', header: 'Month' }, { key: 'forecast', header: 'Forecast (weighted)', align: 'right', render: (r) => money(r.forecast) },
          { key: 'target', header: 'Target', align: 'right', render: (r) => money(r.target) }, { key: 'actual', header: 'Actual (won)', align: 'right', render: (r) => money(r.actual) }]} /></Card>
        <Card title="Commission per sales person" icon="payments"><DataTable rows={keyed(x.commissionByOwner, 'key')} columns={commCols} /></Card>
        <Card title="Commission scheme effectiveness" icon="rule"><DataTable rows={keyed(x.commissionByScheme, 'key')} columns={commCols} /></Card>
      </>}
    </div>
  );
}

function CompareTab() {
  const segs = useGet<Page<R>>('/api/v1/crm/segments?limit=200&filter[status]=active');
  const [picked, setPicked] = useState<string[]>([]);
  const cmp = useGet<Page<R>>(picked.length ? `/api/v1/crm/analytics/segments?segmentIds=${picked.join(',')}` : null);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">{(segs.data?.items ?? []).map((s) => (
        <Checkbox key={s.id} label={String(s.name)} checked={picked.includes(s.id)} onChange={(v) => setPicked(v ? [...picked, s.id] : picked.filter((x) => x !== s.id))} />
      ))}</div>
      {cmp.data && <DataTable rows={keyed(cmp.data.items, 'segmentId')} columns={[{ key: 'name', header: 'Segment' },
        { key: 'members', header: 'Members', align: 'right' }, { key: 'avgMonetary', header: 'Avg spend', align: 'right', render: (r) => money(r.avgMonetary) },
        { key: 'avgFrequency', header: 'Avg visit days', align: 'right' }, { key: 'avgClv', header: 'Avg lifetime value', align: 'right', render: (r) => money(r.avgClv) },
        { key: 'vip', header: 'VIP', align: 'right' },
        { key: 'groups', header: 'RFM groups', render: (r) => Object.entries((r.groups ?? {}) as Record<string, number>).map(([k, v]) => `${label(k)} ${v}`).join(', ') || '—' },
        { key: 'lines', header: 'Active lines', render: (r) => Object.entries((r.lines ?? {}) as Record<string, number>).map(([k, v]) => `${label(k)} ${v}`).join(', ') || '—' }]} />}
      {picked.length === 0 && <Empty title="Choose segments" help="Compare members, spend, RFM groups, business lines and VIPs of segments." icon="compare" />}
    </div>
  );
}

// ── ops: VIP Lookup (check-in / POS marker) ───────────────────────────────

export function VIPLookupPage() {
  const [cust, setCust] = useState('');
  const f = useGet<R>(cust ? `/api/v1/crm/vip-flags/${cust}` : null);
  const x = f.data;
  return (
    <div className="oc-stack">
      <PageHeader title="VIP Lookup" help="Check a guest at check-in or the POS: VIP level, benefits and handling." />
      <Card title="Guest" icon="person_search"><div className="oc-form"><CustomerPicker value={cust} onChange={setCust} label="Guest" /></div>
        {cust && <div className="oc-row-wrap" style={{ marginTop: 8 }}><CustomerTierBadge customerId={cust} /></div>}</Card>
      {cust && !x && <Skeleton />}
      {x && (x.vip ? (
        <Card title={`${String(x.level).toUpperCase()} guest`} icon="workspace_premium" ink>
          <KV items={[['Benefits', String(x.benefits ?? '—')], ['Handling', <strong key="h" style={{ fontSize: 20 }}>{String(x.handlingNote ?? '—')}</strong>],
            ['Loyalty tier', String(x.tierName ?? '—')], ['Source', label(x.source)]]} />
        </Card>
      ) : <Card title="Not a VIP" icon="person"><KV items={[['Loyalty tier', String(x.tierName ?? '—')], ['Segment', label(x.rfmGroup ?? '—')]]} /></Card>)}
    </div>
  );
}

export const CRM_P5_ROUTES: AreaRoute[] = [
  { path: 'crm/overview', perm: 'crm.analytics.view', element: <CRMOverviewPage /> },
  { path: 'crm/journeys', perm: 'crm.journey.view', element: <JourneysPage /> },
  { path: 'crm/journeys/:id', perm: 'crm.journey.view', element: <JourneyDetailPage /> },
  { path: 'crm/vip', perm: 'crm.vip.view', element: <VIPPage /> },
  { path: 'crm/loyalty-tiers', perm: 'crm.loyalty_tier.view', element: <LoyaltyTiersPage /> },
  { path: 'crm/loyalty-rewards', perm: 'crm.loyalty_reward.view', element: <LoyaltyRewardsPage /> },
  { path: 'crm/loyalty-eligibility', perm: 'crm.loyalty_reward_rule.view', element: <LoyaltyEligibilityPage /> },
  { path: 'crm/analytics', perm: 'crm.analytics.view', element: <CRMAnalyticsPage /> },
];
export const CRM_P5_OPS_TILES: OpsTile[] = [['workspace_premium', 'VIP Lookup', '/ops/front-desk/vip', 'crm.vip.view']];
export const CRM_P5_OPS_ROUTES: OpsRoute[] = [{ path: 'front-desk/vip', element: <VIPLookupPage /> }];
