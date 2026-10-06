import React, { useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatusPill, TextArea, TextField,
  useAuth, useToast, type Option,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute } from './types';

// CRM Sales (PRD P3 EP-01–04, Naming Convention §12): Leads, Opportunities,
// Sales Pipeline, Quotations, Follow-ups, Sales Targets & Commission and the
// sales master data (pipelines, sales teams, targets, commission schemes).

const CRM = ['/api/v1/crm'];
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} />;
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const opts = (vals: string[], labels: Record<string, string> = {}): Option[] => vals.map((v) => ({ value: v, label: labels[v] ?? label(v) }));
const LINES = opts(['wedding', 'banquet', 'mice', 'event', 'tournament', 'stay', 'golf', 'package', 'membership', 'other'], { mice: 'MICE / Meeting' });
const SOURCES = opts(['whatsapp', 'instagram', 'facebook', 'tiktok', 'website_form', 'walk_in', 'member_referral', 'email', 'phone', 'event', 'other'],
  { whatsapp: 'WhatsApp', tiktok: 'TikTok', website_form: 'Website form' });
const EVENT_TYPES = opts(['wedding', 'meeting', 'conference', 'gathering', 'birthday', 'tournament', 'other']);
const ACTIVITY_TYPES = opts(['call', 'whatsapp', 'email', 'meeting', 'site_visit', 'food_tasting', 'note', 'task'], { whatsapp: 'WhatsApp' });
const DISQUALIFY = opts(['not_interested', 'budget', 'date_unavailable', 'duplicate', 'spam', 'no_response', 'competitor', 'other']);
const LOST = opts(['price', 'date_unavailable', 'competitor', 'cancelled', 'budget', 'no_response', 'other']);
const ITEM_TYPES = opts(['banquet_package', 'banquet_menu', 'venue', 'product', 'service', 'package', 'other']);
const num = (v: string) => (v === '' ? undefined : Number(v));
const opt = (v: string) => (v === '' ? undefined : v);
const toLocalInput = (d: Date) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

/** Sales staff of the property (owners of leads, opportunities and quotations). */
function useSalesUsers(): Option[] {
  const users = useGet<Page<R>>('/api/v1/crm/sales-users');
  return (users.data?.items ?? []).map((u) => ({ value: u.id, label: String(u.fullName) }));
}

/** Customer search + select. */
function CustomerPicker({ value, onChange, label: lbl = 'Customer', required }: { value: string; onChange: (v: string) => void; label?: string; required?: boolean }) {
  const [q, setQ] = useState('');
  const list = useGet<Page<R>>(`/api/v1/crm/customers${qs({ q, limit: 20, 'filter[status]': 'active' })}`);
  const items = list.data?.items ?? [];
  return (
    <>
      <TextField label={`Find ${lbl.toLowerCase()}`} value={q} onChange={setQ} placeholder="Name, phone, e-mail or code" />
      <SelectField label={lbl} value={value} onChange={onChange} required={required} placeholder="Select"
        options={items.map((c) => ({ value: c.id, label: `${String(c.name)}${c.code ? ` (${String(c.code)})` : ''}` }))} />
    </>
  );
}

function CorporatePicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const list = useGet<Page<R>>('/api/v1/crm/corporate-accounts?limit=200&filter[status]=active');
  return <SelectField label="Corporate account" value={value} onChange={onChange} placeholder="None"
    options={(list.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} (${String(c.code)})` }))} />;
}

function Actions({ children }: { children: React.ReactNode }) {
  return <div className="oc-row-wrap">{children}</div>;
}

// ── Leads (EP-01) ─────────────────────────────────────────────────────────

export function LeadsPage() {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const [modal, setModal] = useState<'' | 'new' | 'import' | 'transfer'>('');
  const [mine, setMine] = useState(false);
  const [line, setLine] = useState('');
  const [sla, setSla] = useState('');
  const open = params.get('id');
  return (
    <>
      <ListPage title="Leads" help="Inquiries from every channel with automatic assignment and the first-response SLA (Lead Management)."
        path="/api/v1/crm/leads" statuses={opts(['new', 'contacted', 'qualified', 'unqualified', 'converted'])}
        extraQuery={{ mine: mine ? 'true' : '', 'filter[line]': line, 'filter[slaStatus]': sla }}
        filters={<>
          <SelectField label="Business line" value={line} onChange={setLine} placeholder="All lines" options={LINES} />
          <SelectField label="SLA" value={sla} onChange={setSla} placeholder="Any" options={opts(['pending', 'overdue', 'met', 'breached'])} />
          <Checkbox label="My leads" checked={mine} onChange={setMine} />
        </>}
        actions={<Actions>
          {can('crm.lead.create') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('new')}>New Lead</button>}
          {can('crm.lead.import') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('import')}>Import</button>}
          {can('crm.lead.assign') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('transfer')}>Transfer</button>}
        </Actions>}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Lead' },
          { key: 'name', header: 'Name', render: (r) => <>{String(r.name)}{r.companyName ? <div className="oc-small oc-muted">{String(r.companyName)}</div> : null}</> },
          { key: 'line', header: 'Line', render: (r) => label(r.line) }, { key: 'source', header: 'Source', render: (r) => label(r.source) },
          { key: 'ownerName', header: 'Sales', render: (r) => String(r.ownerName ?? '—') },
          { key: 'slaStatus', header: 'First Response', render: (r) => (r.slaStatus ? <StatusPill status={String(r.slaStatus)} /> : '—') },
          { key: 'status', header: 'Status', render: pill('status') },
          { key: 'createdAt', header: 'Created', render: (r) => formatDateTime(String(r.createdAt)) }]} />
      {modal === 'new' && <LeadForm onClose={() => setModal('')} onDone={(id) => { setModal(''); setParams({ id }); }} />}
      {modal === 'import' && <ImportLeads onClose={() => setModal('')} />}
      {modal === 'transfer' && <TransferLeads onClose={() => setModal('')} />}
      {open && <LeadDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function LeadForm({ onClose, onDone, lead }: { onClose: () => void; onDone: (id: string) => void; lead?: R }) {
  const { can } = useAuth();
  const users = useSalesUsers();
  const [f, setF] = useState<Record<string, string>>({
    name: String(lead?.name ?? ''), companyName: String(lead?.companyName ?? ''), phone: String(lead?.phone ?? ''), email: String(lead?.email ?? ''),
    source: String(lead?.source ?? 'phone'), line: String(lead?.line ?? 'other'), eventType: String(lead?.eventType ?? ''),
    eventDate: String(lead?.eventDate ?? ''), pax: lead?.pax != null ? String(lead.pax) : '', budget: lead?.budget != null ? String(lead.budget) : '',
    notes: String(lead?.notes ?? ''), ownerUserId: '',
  });
  const [consent, setConsent] = useState(Boolean(lead?.marketingConsent));
  const [ack, setAck] = useState(false);
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>(lead ? 'PATCH' : 'POST', lead ? `/api/v1/crm/leads/${lead.id}` : '/api/v1/crm/leads', CRM);
  const duplicate = send.error?.code === 'duplicate_lead';
  const body: Record<string, unknown> = {
    name: f.name, companyName: f.companyName, phone: f.phone, email: f.email, line: f.line, eventType: opt(f.eventType), eventDate: opt(f.eventDate),
    pax: num(f.pax), budget: opt(f.budget), notes: f.notes, marketingConsent: consent,
  };
  if (!lead) Object.assign(body, { source: f.source, ownerUserId: opt(f.ownerUserId), duplicateAcknowledged: ack || undefined });
  const fe = send.error?.fieldErrors ?? {};
  return (
    <Modal open onClose={onClose} title={lead ? `Edit Lead ${String(lead.number)}` : 'New Lead'} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      {duplicate && !ack && <button className="oc-btn oc-btn-neutral" onClick={() => setAck(true)}>Save anyway</button>}
      <button className="oc-btn oc-btn-primary" disabled={!f.name || send.isPending} onClick={() => send.mutate(body as R, { onSuccess: (l) => onDone(l.id) })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextField label="Name" value={f.name} onChange={set('name')} required error={fe.name} />
        <TextField label="Company" value={f.companyName} onChange={set('companyName')} />
        <TextField label="Phone" value={f.phone} onChange={set('phone')} error={fe.phone} help="Phone or e-mail is required" />
        <TextField label="E-mail" type="email" value={f.email} onChange={set('email')} error={fe.email} />
        {!lead && <SelectField label="Source" value={f.source} onChange={set('source')} options={SOURCES} required />}
        <SelectField label="Business line" value={f.line} onChange={set('line')} options={LINES} />
        <SelectField label="Event type" value={f.eventType} onChange={set('eventType')} options={EVENT_TYPES} placeholder="—" />
        <TextField label="Event date" type="date" value={f.eventDate} onChange={set('eventDate')} />
        <TextField label="Pax" type="number" value={f.pax} onChange={set('pax')} error={fe.pax} />
        <TextField label="Budget" type="number" value={f.budget} onChange={set('budget')} error={fe.budget} />
        {!lead && can('crm.lead.assign') && <SelectField label="Assign to" value={f.ownerUserId} onChange={set('ownerUserId')} options={users}
          placeholder="Sales Policies (round robin)" />}
        <TextArea label="Notes" value={f.notes} onChange={set('notes')} span />
        <Checkbox label="Marketing consent given (UU PDP)" checked={consent} onChange={setConsent} />
      </div>
      {duplicate && !ack ? <p className="oc-alert oc-alert-warning" role="alert">{send.error?.message}. Open the existing lead, or save this one anyway.</p>
        : <ErrorAlert error={send.error} />}
    </Modal>
  );
}

function ImportLeads({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState('externalRef,name,phone,email,source,line,status,budget,ownerEmail,opportunityTitle,expectedValue,stage,expectedCloseDate,createdAt\n');
  const [result, setResult] = useState<R | null>(null);
  const send = useSend<R, R>('POST', '/api/v1/crm/leads:import', CRM);
  const run = (mode: string) => send.mutate({ mode, csv } as unknown as R, { onSuccess: setResult });
  const errors = (result?.errors as R[] | undefined) ?? [];
  return (
    <Modal open onClose={onClose} title="Import Leads" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Preview</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !result || result.mode !== 'preview'} onClick={() => run('commit')}>Import</button>
    </>}>
      <p className="oc-muted">Sales spreadsheet as CSV with a header row. Rows are matched by <code>externalRef</code>, so importing the same file again
        changes nothing. Preview first, then import.</p>
      <TextArea label="CSV" value={csv} onChange={(v) => { setCsv(v); setResult(null); }} rows={10} />
      <ErrorAlert error={send.error} />
      {result && (
        <Card title={result.mode === 'preview' ? 'Preview' : 'Imported'} icon="upload">
          <KV items={[['Rows', String(result.totalRows)], [result.mode === 'preview' ? 'To create' : 'Created', String(result.created)],
            ['Already imported', String(result.existing)], ['Opportunities', String(result.opportunities)], ['Rejected', String(result.failed)]]} />
          {errors.length > 0 && <DataTable rows={errors.map((e, i) => ({ ...e, id: String(i) })) as R[]} columns={[{ key: 'row', header: 'Row' },
            { key: 'field', header: 'Field' }, { key: 'message', header: 'Problem' }]} />}
        </Card>
      )}
    </Modal>
  );
}

function TransferLeads({ onClose }: { onClose: () => void }) {
  const users = useSalesUsers();
  const toast = useToast();
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [reason, setReason] = useState('');
  const [opps, setOpps] = useState(true);
  const send = useSend<R, R>('POST', '/api/v1/crm/leads:transfer', CRM);
  return (
    <Modal open onClose={onClose} title="Transfer Leads" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!from || !to || !reason || send.isPending}
        onClick={() => send.mutate({ fromUserId: from, toUserId: to, reason, opportunities: opps } as unknown as R, {
          onSuccess: (r) => { toast(`Moved ${String(r.leads)} leads, ${String(r.opportunities)} opportunities, ${String(r.followUps)} follow-ups`); onClose(); },
        })}>Transfer</button>
    </>}>
      <div className="oc-form">
        <SelectField label="From sales" value={from} onChange={setFrom} options={users} placeholder="Select" required />
        <SelectField label="To sales" value={to} onChange={setTo} options={users.filter((u) => u.value !== from)} placeholder="Select" required />
        <TextField label="Reason" value={reason} onChange={setReason} required span />
        <Checkbox label="Also move open opportunities and follow-ups" checked={opps} onChange={setOpps} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function LeadDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R & { duplicates: R[]; activities: R[]; assignments: R[] }>(`/api/v1/crm/leads/${id}`);
  const [modal, setModal] = useState<'' | 'edit' | 'assign' | 'activity' | 'disqualify' | 'convert'>('');
  const x = d.data;
  const inv = [...CRM, `/api/v1/crm/leads/${id}`];
  const openLead = x && ['new', 'contacted', 'qualified'].includes(String(x.status));
  return (
    <Drawer open onClose={onClose} title={x ? `Lead ${String(x.number)}` : 'Lead'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Name', String(x.name)], ['Company', String(x.companyName ?? '—')], ['Phone', String(x.phone ?? '—')], ['E-mail', String(x.email ?? '—')],
            ['Status', <StatusPill key="s" status={String(x.status)} />], ['Business line', label(x.line)],
            ['Source', `${label(x.source)}${x.channel ? ` · ${String(x.channel)}` : ''}`],
            ['Event', [label(x.eventType), x.eventDate ? formatDate(String(x.eventDate)) : '', x.pax ? `${String(x.pax)} pax` : ''].filter(Boolean).join(' · ') || '—'],
            ['Budget', money(x.budget)], ['Sales', String(x.ownerName ?? 'Unassigned')],
            ['First response due', x.firstResponseDueAt ? formatDateTime(String(x.firstResponseDueAt)) : '—'],
            ['First response', x.firstRespondedAt ? formatDateTime(String(x.firstRespondedAt)) : <StatusPill key="sla" status={String(x.slaStatus ?? 'pending')} />],
            ['Customer', x.customerId ? <Link key="c" to={`/crm/customer-360?customerId=${String(x.customerId)}`}>{String(x.customerName ?? 'Customer 360')}</Link> : '—'],
            ['Marketing consent', x.marketingConsent ? 'Yes' : 'No'],
            ['Opportunity', x.opportunityId ? <Link key="o" to={`/crm/opportunities?id=${String(x.opportunityId)}`}>Open</Link> : '—']]} />
          {x.message ? <Card title="Inquiry" icon="chat"><p style={{ whiteSpace: 'pre-wrap', margin: 0 }}>{String(x.message)}</p></Card> : null}
          <Actions>
            {openLead && can('crm.lead.update') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('activity')}>Log Activity</button>}
            {openLead && can('crm.lead.update') && x.status !== 'qualified' &&
              <ActionButton label="Qualify" path={`/api/v1/crm/leads/${id}:qualify`} invalidate={inv} />}
            {openLead && can('crm.lead.convert') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('convert')}>Convert</button>}
            {openLead && can('crm.lead.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('disqualify')}>Disqualify</button>}
            {x.status !== 'converted' && can('crm.lead.assign') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('assign')}>Assign</button>}
            {x.status !== 'converted' && can('crm.lead.update') && <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setModal('edit')}>Edit</button>}
          </Actions>
          {x.duplicates.length > 0 && <Card title="Possible duplicates" icon="content_copy">
            <DataTable rows={x.duplicates} columns={[{ key: 'kind', header: 'Kind', render: (r) => label(r.kind) }, { key: 'reference', header: 'Reference' },
              { key: 'name', header: 'Name' }, { key: 'matchedOn', header: 'Matched on' }, { key: 'status', header: 'Status', render: pill('status') }]} />
          </Card>}
          <Card title="Activities & follow-ups" icon="history">
            {x.activities.length === 0 ? <Empty title="No activity yet" /> : <ActivityTable rows={x.activities} />}
          </Card>
          {x.assignments.length > 0 && <Card title="Assignment history" icon="swap_horiz">
            <DataTable rows={x.assignments} columns={[{ key: 'assignedAt', header: 'When', render: (r) => formatDateTime(String(r.assignedAt)) },
              { key: 'fromName', header: 'From', render: (r) => String(r.fromName ?? '—') }, { key: 'toName', header: 'To', render: (r) => String(r.toName ?? '—') },
              { key: 'method', header: 'Method', render: (r) => label(r.method) }, { key: 'reason', header: 'Reason' }]} />
          </Card>}
          {modal === 'edit' && <LeadForm lead={x} onClose={() => setModal('')} onDone={() => setModal('')} />}
          {modal === 'assign' && <AssignLead id={id} onClose={() => setModal('')} />}
          {modal === 'activity' && <ActivityForm path={`/api/v1/crm/leads/${id}/activities`} invalidate={inv} onClose={() => setModal('')} />}
          {modal === 'disqualify' && <ReasonModal title="Disqualify Lead" path={`/api/v1/crm/leads/${id}:disqualify`} reasons={DISQUALIFY} invalidate={inv}
            onClose={() => setModal('')} />}
          {modal === 'convert' && <ConvertLead lead={x} onClose={() => setModal('')} />}
        </div>
      )}
    </Drawer>
  );
}

function ActivityTable({ rows, actions }: { rows: R[]; actions?: (r: R) => React.ReactNode }) {
  return <DataTable rows={rows} actions={actions} columns={[{ key: 'type', header: 'Type', render: (r) => label(r.type) }, { key: 'subject', header: 'Subject' },
    { key: 'when', header: 'When', render: (r) => (r.dueAt ? `Due ${formatDateTime(String(r.dueAt))}` : r.occurredAt ? formatDateTime(String(r.occurredAt)) : '—') },
    { key: 'assignedToName', header: 'Sales', render: (r) => String(r.assignedToName ?? '—') },
    { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.overdue ? 'overdue' : String(r.status)} /> }]} />;
}

function AssignLead({ id, onClose }: { id: string; onClose: () => void }) {
  const users = useSalesUsers();
  const [user, setUser] = useState('');
  const [reason, setReason] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/leads/${id}:assign`, CRM);
  return (
    <Modal open onClose={onClose} title="Assign Lead" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!user || send.isPending} onClick={() => send.mutate({ userId: user, reason } as unknown as R, { onSuccess: onClose })}>Assign</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Sales" value={user} onChange={setUser} options={users} placeholder="Select" required />
        <TextField label="Reason" value={reason} onChange={setReason} />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

/** Logs an activity; a due date makes it a follow-up. */
function ActivityForm({ path, invalidate, onClose, followUp, target }: { path: string; invalidate: string[]; onClose: () => void; followUp?: boolean;
  target?: Record<string, string> }) {
  const users = useSalesUsers();
  const [type, setType] = useState('call');
  const [direction, setDirection] = useState('outbound');
  const [subject, setSubject] = useState('');
  const [notes, setNotes] = useState('');
  const [due, setDue] = useState(followUp ? toLocalInput(new Date(Date.now() + 86400000)) : '');
  const [assignee, setAssignee] = useState('');
  const send = useSend<R, R>('POST', path, invalidate);
  return (
    <Modal open onClose={onClose} title={followUp ? 'Schedule Follow-up' : 'Log Activity'} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!subject || (followUp && !due) || send.isPending}
        onClick={() => send.mutate({ ...target, type, direction, subject, notes, dueAt: due ? new Date(due).toISOString() : undefined,
          assignedTo: opt(assignee) } as unknown as R, { onSuccess: onClose })}>Save</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Type" value={type} onChange={setType} options={ACTIVITY_TYPES} />
        <SelectField label="Direction" value={direction} onChange={setDirection} options={opts(['outbound', 'inbound', 'internal'])} />
        <TextField label="Subject" value={subject} onChange={setSubject} required span />
        <TextArea label="Notes" value={notes} onChange={setNotes} span />
        <TextField label={followUp ? 'Due' : 'Follow-up due (optional)'} type="datetime-local" value={due} onChange={setDue} required={followUp} />
        {due && <SelectField label="Assigned to" value={assignee} onChange={setAssignee} options={users} placeholder="The owner" />}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ReasonModal({ title, path, reasons, invalidate, onClose }: { title: string; path: string; reasons: Option[]; invalidate: string[]; onClose: () => void }) {
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const send = useSend<R, R>('POST', path, invalidate);
  return (
    <Modal open onClose={onClose} title={title} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-danger" disabled={!reason || send.isPending} onClick={() => send.mutate({ reason, note } as unknown as R, { onSuccess: onClose })}>{title}</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Reason" value={reason} onChange={setReason} options={reasons} placeholder="Select" required />
        <TextField label="Note" value={note} onChange={setNote} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ConvertLead({ lead, onClose }: { lead: R; onClose: () => void }) {
  const toast = useToast();
  const [title, setTitle] = useState(`${label(lead.line)} · ${String(lead.companyName ?? lead.name)}`);
  const [value, setValue] = useState(lead.budget != null ? String(lead.budget) : '');
  const [close, setClose] = useState('');
  const [corporate, setCorporate] = useState(Boolean(lead.companyName));
  const [createOpp, setCreateOpp] = useState(true);
  const send = useSend<R, R>('POST', `/api/v1/crm/leads/${lead.id}:convert`, CRM);
  return (
    <Modal open onClose={onClose} title="Convert Lead" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ title, expectedValue: opt(value),
        expectedCloseDate: opt(close), createCorporateAccount: corporate, createOpportunity: createOpp } as unknown as R, {
        onSuccess: () => { toast('Lead converted'); onClose(); },
      })}>Convert</button>
    </>}>
      <p className="oc-muted">The lead becomes a customer (the matching customer when one exists){lead.companyName ? ' with its corporate account' : ''} and,
        optionally, an opportunity in the pipeline of its business line.</p>
      <div className="oc-form">
        <Checkbox label="Create the opportunity" checked={createOpp} onChange={setCreateOpp} />
        {Boolean(lead.companyName) && <Checkbox label={`Create corporate account "${String(lead.companyName)}"`} checked={corporate} onChange={setCorporate} />}
        {createOpp && <>
          <TextField label="Opportunity title" value={title} onChange={setTitle} span />
          <TextField label="Expected value" type="number" value={value} onChange={setValue} />
          <TextField label="Expected close" type="date" value={close} onChange={setClose} />
        </>}
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Opportunities & Sales Pipeline (EP-02) ────────────────────────────────

export function OpportunitiesPage() {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const [creating, setCreating] = useState(false);
  const [mine, setMine] = useState(false);
  const [line, setLine] = useState('');
  const open = params.get('id');
  return (
    <>
      <ListPage title="Opportunities" help="Deals in the pipelines of each business line: expected value, probability, event date and the sales owner."
        path="/api/v1/crm/opportunities" statuses={opts(['open', 'won', 'lost'])} extraQuery={{ mine: mine ? 'true' : '', 'filter[line]': line }}
        filters={<><SelectField label="Business line" value={line} onChange={setLine} placeholder="All lines" options={LINES} />
          <Checkbox label="My opportunities" checked={mine} onChange={setMine} /></>}
        actions={can('crm.opportunity.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Opportunity</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Opportunity' }, { key: 'title', header: 'Title' },
          { key: 'customer', header: 'Customer', render: (r) => String(r.corporateName ?? r.customerName ?? '—') }, { key: 'stageName', header: 'Stage' },
          { key: 'probability', header: 'Prob.', align: 'right', render: (r) => `${String(r.probability)}%` },
          { key: 'expectedValue', header: 'Value', align: 'right', render: (r) => money(r.expectedValue) },
          { key: 'expectedCloseDate', header: 'Close', render: (r) => (r.expectedCloseDate ? formatDate(String(r.expectedCloseDate)) : '—') },
          { key: 'ownerName', header: 'Sales', render: (r) => String(r.ownerName ?? '—') }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <OpportunityForm onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setParams({ id }); }} />}
      {open && <OpportunityDrawer id={open} onClose={() => setParams({})} />}
    </>
  );
}

function OpportunityForm({ onClose, onDone, opp }: { onClose: () => void; onDone: (id: string) => void; opp?: R }) {
  const { can } = useAuth();
  const users = useSalesUsers();
  const [f, setF] = useState<Record<string, string>>({
    title: String(opp?.title ?? ''), line: String(opp?.line ?? 'other'), customerId: String(opp?.customerId ?? ''),
    corporateAccountId: String(opp?.corporateAccountId ?? ''), expectedValue: opp ? String(opp.expectedValue ?? '') : '',
    expectedCloseDate: String(opp?.expectedCloseDate ?? ''), eventType: String(opp?.eventType ?? ''), eventDate: String(opp?.eventDate ?? ''),
    endDate: String(opp?.endDate ?? ''), pax: opp?.pax != null ? String(opp.pax) : '', notes: String(opp?.notes ?? ''), ownerUserId: String(opp?.ownerUserId ?? ''),
  });
  const set = (k: string) => (v: string) => setF((x) => ({ ...x, [k]: v }));
  const send = useSend<R, R>(opp ? 'PATCH' : 'POST', opp ? `/api/v1/crm/opportunities/${opp.id}` : '/api/v1/crm/opportunities', CRM);
  const body: Record<string, unknown> = { title: f.title, customerId: opt(f.customerId), corporateAccountId: opt(f.corporateAccountId),
    expectedValue: opt(f.expectedValue), expectedCloseDate: opt(f.expectedCloseDate), eventType: opt(f.eventType), eventDate: opt(f.eventDate),
    endDate: opt(f.endDate), pax: num(f.pax), notes: f.notes, ownerUserId: opt(f.ownerUserId) };
  if (!opp) body.line = f.line;
  return (
    <Modal open onClose={onClose} title={opp ? `Edit ${String(opp.number)}` : 'New Opportunity'} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!f.title || send.isPending} onClick={() => send.mutate(body as R, { onSuccess: (o) => onDone(o.id) })}>Save</button>
    </>}>
      <div className="oc-form">
        <TextField label="Title" value={f.title} onChange={set('title')} required span />
        {!opp && <SelectField label="Business line" value={f.line} onChange={set('line')} options={LINES} help="Opens in the pipeline of the line" />}
        <CustomerPicker value={f.customerId} onChange={set('customerId')} />
        <CorporatePicker value={f.corporateAccountId} onChange={set('corporateAccountId')} />
        <TextField label="Expected value" type="number" value={f.expectedValue} onChange={set('expectedValue')} />
        <TextField label="Expected close" type="date" value={f.expectedCloseDate} onChange={set('expectedCloseDate')} />
        <SelectField label="Event type" value={f.eventType} onChange={set('eventType')} options={EVENT_TYPES} placeholder="—" />
        <TextField label="Event date" type="date" value={f.eventDate} onChange={set('eventDate')} />
        <TextField label="Until" type="date" value={f.endDate} onChange={set('endDate')} />
        <TextField label="Pax" type="number" value={f.pax} onChange={set('pax')} />
        {can('crm.lead.assign') && <SelectField label="Sales" value={f.ownerUserId} onChange={set('ownerUserId')} options={users} placeholder="Me" />}
        <TextArea label="Notes" value={f.notes} onChange={set('notes')} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function OpportunityDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const d = useGet<R & { stageHistory: R[]; activities: R[]; quotations: R[] }>(`/api/v1/crm/opportunities/${id}`);
  const x = d.data;
  const stages = useGet<Page<R>>(x ? `/api/v1/crm/pipeline-stages?filter[pipelineId]=${String(x.pipelineId)}&limit=100` : null);
  const venues = useGet<Page<R>>(x && (x.venueResourceId || x.pax) && x.status === 'open' ? `/api/v1/crm/opportunities/${id}/venue-availability` : null);
  const [modal, setModal] = useState<'' | 'edit' | 'activity' | 'lose' | 'quote'>('');
  const [stage, setStage] = useState('');
  const move = useSend<R, R>('POST', `/api/v1/crm/opportunities/${id}:move-stage`, CRM);
  const inv = [...CRM, `/api/v1/crm/opportunities/${id}`];
  const openStages = (stages.data?.items ?? []).filter((s) => s.kind === 'open' && s.status === 'active' && s.id !== x?.stageId);
  return (
    <Drawer open onClose={onClose} title={x ? `Opportunity ${String(x.number)}` : 'Opportunity'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Title', String(x.title)], ['Customer', String(x.corporateName ?? x.customerName ?? '—')], ['Status', <StatusPill key="s" status={String(x.status)} />],
            ['Pipeline', `${String(x.pipelineName)} · ${String(x.stageName)}`], ['Probability', `${String(x.probability)}%`],
            ['Expected value', money(x.expectedValue)], ['Weighted value', money(x.weightedValue)],
            ['Expected close', x.expectedCloseDate ? formatDate(String(x.expectedCloseDate)) : '—'],
            ['Event', [label(x.eventType), x.eventDate ? formatDate(String(x.eventDate)) : '', x.pax ? `${String(x.pax)} pax` : ''].filter(Boolean).join(' · ') || '—'],
            ['Sales', String(x.ownerName ?? '—')], ['Lost reason', x.lostReason ? `${label(x.lostReason)}${x.lostNote ? ` — ${String(x.lostNote)}` : ''}` : '—']]} />
          {x.status === 'open' && can('crm.opportunity.update') && openStages.length > 0 && (
            <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
              <SelectField label="Move to stage" value={stage} onChange={setStage} placeholder="Select" options={openStages.map((s) => ({ value: s.id,
                label: `${String(s.name)} (${String(s.probability)}%)` }))} />
              <button className="oc-btn oc-btn-sm oc-btn-neutral" disabled={!stage || move.isPending}
                onClick={() => move.mutate({ stageId: stage } as unknown as R, { onSuccess: () => setStage('') })}>Move</button>
            </div>
          )}
          <ErrorAlert error={move.error} />
          <Actions>
            {x.status === 'open' && can('crm.quotation.create') && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('quote')}>New Quotation</button>}
            {x.status === 'open' && can('crm.opportunity.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('activity')}>Log Activity</button>}
            {x.status === 'open' && can('crm.opportunity.close') && <ActionButton label="Mark Won" path={`/api/v1/crm/opportunities/${id}:win`} invalidate={inv}
              confirm="Won needs an accepted quotation of this opportunity (or one accepted directly for the customer)." />}
            {x.status === 'open' && can('crm.opportunity.close') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('lose')}>Mark Lost</button>}
            {x.status === 'lost' && can('crm.opportunity.close') && <ActionButton label="Reopen" path={`/api/v1/crm/opportunities/${id}:reopen`} invalidate={inv} />}
            {x.status === 'open' && can('crm.opportunity.update') && <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setModal('edit')}>Edit</button>}
          </Actions>
          {venues.data && <Card title="Venue availability" icon="event_available">
            <DataTable rows={venues.data.items} rowKey={(v) => String(v.resourceId)} columns={[{ key: 'name', header: 'Venue' },
              { key: 'capacity', header: 'Capacity', align: 'right', render: (v) => String(v.capacity ?? '—') },
              { key: 'available', header: 'On the event date', render: (v) => (v.available ? <StatusPill status="available" /> : <StatusPill status="booked" />) },
              { key: 'busy', header: 'Booked', render: (v) => ((v.busy as R[] | undefined) ?? []).map((b) => `${formatDateTime(String(b.start))} – ${formatDateTime(String(b.end))}`).join(', ') || '—' }]} />
          </Card>}
          <Card title="Quotations" icon="request_quote">
            {x.quotations.length === 0 ? <Empty title="No quotation yet" /> : <DataTable rows={x.quotations} columns={[
              { key: 'number', header: 'Quotation', render: (q) => <Link to={`/crm/quotations?id=${q.id}`}>{`${String(q.number)} v${String(q.version)}`}</Link> },
              { key: 'total', header: 'Total', align: 'right', render: (q) => money(q.total) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
          </Card>
          <Card title="Stage history" icon="timeline">
            <DataTable rows={x.stageHistory} columns={[{ key: 'changedAt', header: 'When', render: (s) => formatDateTime(String(s.changedAt)) },
              { key: 'fromStage', header: 'From', render: (s) => String(s.fromStage ?? '—') }, { key: 'toStage', header: 'To' },
              { key: 'probability', header: 'Prob.', align: 'right', render: (s) => `${String(s.probability)}%` },
              { key: 'changedByName', header: 'By', render: (s) => String(s.changedByName ?? '—') }, { key: 'note', header: 'Note' }]} />
          </Card>
          <Card title="Activities & follow-ups" icon="history">
            {x.activities.length === 0 ? <Empty title="No activity yet" /> : <ActivityTable rows={x.activities} />}
          </Card>
          {modal === 'edit' && <OpportunityForm opp={x} onClose={() => setModal('')} onDone={() => setModal('')} />}
          {modal === 'activity' && <ActivityForm path={`/api/v1/crm/opportunities/${id}/activities`} invalidate={inv} onClose={() => setModal('')} />}
          {modal === 'lose' && <ReasonModal title="Mark Lost" path={`/api/v1/crm/opportunities/${id}:lose`} reasons={LOST} invalidate={inv} onClose={() => setModal('')} />}
          {modal === 'quote' && <QuotationForm preset={{ opportunityId: id }} onClose={() => setModal('')} onDone={() => setModal('')} />}
        </div>
      )}
    </Drawer>
  );
}

export function SalesPipelinePage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const pipelines = useGet<Page<R>>('/api/v1/crm/pipelines?filter[status]=active&limit=100');
  const list = pipelines.data?.items ?? [];
  const pipeline = params.get('pipelineId') ?? (list[0]?.id as string | undefined) ?? '';
  const [mine, setMine] = useState(false);
  const [tab, setTab] = useState('board');
  const board = useGet<R & { stages: (R & { opportunities: R[] })[] }>(pipeline && tab === 'board'
    ? `/api/v1/crm/pipelines/${pipeline}/board${qs({ mine: mine ? 'true' : '' })}` : null);
  const forecast = useGet<R & { rows: R[] }>(tab === 'forecast' ? '/api/v1/crm/sales-forecast' : null);
  const open = params.get('id');
  const set = (k: string, v: string) => { const n = new URLSearchParams(params); if (v) n.set(k, v); else n.delete(k); setParams(n, { replace: true }); };
  const b = board.data;
  return (
    <div className="oc-stack">
      <PageHeader title="Sales Pipeline" help="Opportunities per stage with probability-weighted value; move a card to record the stage change."
        actions={<Link className="oc-btn oc-btn-neutral" to="/crm/sales-settings">Pipeline settings</Link>} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <Tabs tabs={[{ value: 'board', label: 'Board' }, { value: 'forecast', label: 'Forecast' }]} value={tab} onChange={setTab} />
        {tab === 'board' && <>
          <SelectField label="Pipeline" value={pipeline} onChange={(v) => set('pipelineId', v)} options={list.map((p) => ({ value: p.id, label: String(p.name) }))} />
          <Checkbox label="My opportunities" checked={mine} onChange={setMine} />
        </>}
      </div>
      <ErrorAlert error={board.error ?? forecast.error} />
      {tab === 'board' && board.isLoading && <Skeleton rows={6} />}
      {tab === 'board' && b && (
        <>
          <div className="oc-row-wrap"><span className="oc-chip">Open {String(b.openCount)}</span><span className="oc-chip">Value {money(b.openValue)}</span>
            <span className="oc-chip">Weighted {money(b.weightedValue)}</span></div>
          <div style={{ display: 'grid', gridAutoFlow: 'column', gridAutoColumns: 'minmax(240px, 1fr)', gap: 12, overflowX: 'auto', paddingBottom: 8 }}
            role="list" aria-label="Pipeline stages">
            {b.stages.map((s) => (
              <section key={String(s.stageId)} className="oc-card" role="listitem" aria-label={String(s.name)} style={{ padding: 12, minHeight: 200 }}>
                <header style={{ marginBottom: 8 }}>
                  <strong>{String(s.name)}</strong> <span className="oc-muted oc-small">{String(s.probability)}%</span>
                  <div className="oc-small oc-muted">{String(s.count)} · {money(s.value)}</div>
                </header>
                <div className="oc-stack" style={{ gap: 8 }}>
                  {s.opportunities.map((o) => (
                    <article key={o.id} className="oc-card" style={{ padding: 10 }}>
                      <button className="oc-btn oc-btn-text oc-btn-sm" style={{ padding: 0, textAlign: 'left' }} onClick={() => set('id', o.id)}>
                        {String(o.number)} · {String(o.title)}
                      </button>
                      <div className="oc-small oc-muted">{String(o.corporateName ?? o.customerName ?? '—')}</div>
                      <div className="oc-small">{money(o.expectedValue)}{o.expectedCloseDate ? ` · close ${formatDate(String(o.expectedCloseDate))}` : ''}</div>
                      <div className="oc-small oc-muted">{String(o.ownerName ?? '')}</div>
                      {s.kind === 'open' && can('crm.opportunity.update') && (
                        <MoveSelect opp={o} stages={b.stages.filter((t) => t.kind === 'open' && t.stageId !== s.stageId)} />
                      )}
                    </article>
                  ))}
                  {s.opportunities.length === 0 && <span className="oc-small oc-muted">No opportunity</span>}
                </div>
              </section>
            ))}
          </div>
        </>
      )}
      {tab === 'forecast' && (
        <Card title="Weighted forecast by expected close month" icon="trending_up">
          <DataTable rows={(forecast.data?.rows ?? []).map((r, i) => ({ ...r, id: String(i) })) as R[]} loading={forecast.isLoading} columns={[
            { key: 'month', header: 'Month' }, { key: 'line', header: 'Line', render: (r) => label(r.line) }, { key: 'count', header: 'Opportunities', align: 'right' },
            { key: 'value', header: 'Value', align: 'right', render: (r) => money(r.value) }, { key: 'weightedValue', header: 'Weighted', align: 'right', render: (r) => money(r.weightedValue) }]} />
          {forecast.data && <p className="oc-small">Total {money(forecast.data.value)} · weighted {money(forecast.data.weightedValue)}</p>}
        </Card>
      )}
      {open && <OpportunityDrawer id={open} onClose={() => set('id', '')} />}
    </div>
  );
}

/** Moves a board card to another open stage. */
function MoveSelect({ opp, stages }: { opp: R; stages: R[] }) {
  const toast = useToast();
  const move = useSend<R, R>('POST', `/api/v1/crm/opportunities/${opp.id}:move-stage`, CRM);
  return (
    <select className="oc-select" aria-label={`Move ${String(opp.number)} to`} value="" disabled={move.isPending} style={{ marginTop: 6 }}
      onChange={(e) => e.target.value && move.mutate({ stageId: e.target.value } as unknown as R, { onError: (err) => toast(err.message, 'error') })}>
      <option value="">Move to…</option>
      {stages.map((t) => <option key={String(t.stageId)} value={String(t.stageId)}>{String(t.name)}</option>)}
    </select>
  );
}

// ── Quotations (EP-03) ────────────────────────────────────────────────────

export function QuotationsPage() {
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
  const [creating, setCreating] = useState(false);
  const [all, setAll] = useState(false);
  const open = params.get('id');
  return (
    <>
      <ListPage title="Quotations" help="Priced offers with tax & service, payment terms, discount approval, versions and the secure acceptance link."
        path="/api/v1/crm/quotations" statuses={opts(['draft', 'pending_approval', 'sent', 'accepted', 'rejected', 'expired'])}
        extraQuery={{ allVersions: all ? 'true' : '' }} filters={<Checkbox label="All versions" checked={all} onChange={setAll} />}
        actions={can('crm.quotation.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Quotation</button> : undefined}
        onRowClick={(r) => setParams({ id: r.id })}
        columns={[{ key: 'number', header: 'Quotation', render: (r) => `${String(r.number)} v${String(r.version)}` }, { key: 'title', header: 'Title' },
          { key: 'customer', header: 'Customer', render: (r) => String(r.corporateName ?? r.customerName ?? '—') }, { key: 'line', header: 'Line', render: (r) => label(r.line) },
          { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'validUntil', header: 'Valid until', render: (r) => (r.validUntil ? formatDate(String(r.validUntil)) : '—') },
          { key: 'ownerName', header: 'Sales', render: (r) => String(r.ownerName ?? '—') }, { key: 'status', header: 'Status', render: pill('status') }]} />
      {creating && <QuotationForm onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); setParams({ id }); }} />}
      {open && <QuotationDrawer id={open} onClose={() => setParams({})} onOpen={(qid) => setParams({ id: qid })} />}
    </>
  );
}

type Line = { itemType: string; itemRef: string; serviceType: string; description: string; quantity: string; unitPrice: string; discountPercent: string;
  discount: string };
type Term = { label: string; percent: string; dueDays: string; daysBeforeEvent: string };
const newLine = (): Line => ({ itemType: 'service', itemRef: '', serviceType: '', description: '', quantity: '1', unitPrice: '', discountPercent: '',
  discount: '' });

function QuotationForm({ onClose, onDone, preset, quotation }: { onClose: () => void; onDone: (id: string) => void; preset?: { opportunityId?: string };
  quotation?: R & { lines: R[] } }) {
  const q = quotation;
  const [customer, setCustomer] = useState(String(q?.customerId ?? ''));
  const [corporate, setCorporate] = useState(String(q?.corporateAccountId ?? ''));
  const [h, setH] = useState<Record<string, string>>({
    title: String(q?.title ?? ''), line: String(q?.line ?? ''), eventType: String(q?.eventType ?? ''), eventDate: String(q?.eventDate ?? ''),
    pax: q?.pax != null ? String(q.pax) : '', pricingMode: String(q?.pricingMode ?? ''), validUntil: String(q?.validUntil ?? ''),
    discountPercent: q?.headerDiscountPercent != null ? String(q.headerDiscountPercent) : '', packageRef: String(q?.packageRef ?? ''), notes: String(q?.notes ?? ''),
  });
  const [lines, setLines] = useState<Line[]>(q ? q.lines.map((l) => ({ itemType: String(l.itemType), itemRef: String(l.itemRef ?? ''),
    serviceType: String(l.serviceType ?? ''), description: String(l.description), quantity: String(l.quantity), unitPrice: l.priceSource === 'manual' ? String(l.unitPrice) : '',
    discountPercent: '', discount: Number(l.lineDiscount ?? 0) > 0 ? String(l.lineDiscount) : '' })) : [newLine()]);
  const [terms, setTerms] = useState<Term[]>((q?.paymentTerms as R[] | undefined)?.map((t) => ({ label: String(t.label), percent: String(t.percent ?? ''),
    dueDays: t.dueDays != null ? String(t.dueDays) : '', daysBeforeEvent: t.daysBeforeEvent != null ? String(t.daysBeforeEvent) : '' })) ?? []);
  const setHead = (k: string) => (v: string) => setH((x) => ({ ...x, [k]: v }));
  const setLine = (i: number, k: keyof Line, v: string) => setLines((ls) => ls.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  const setTerm = (i: number, k: keyof Term, v: string) => setTerms((ts) => ts.map((t, j) => (j === i ? { ...t, [k]: v } : t)));
  const send = useSend<R, R>(q ? 'PUT' : 'POST', q ? `/api/v1/crm/quotations/${q.id}` : '/api/v1/crm/quotations', CRM,
    q ? undefined : () => ({ 'Idempotency-Key': crypto.randomUUID() }));
  const body = {
    opportunityId: q ? undefined : preset?.opportunityId, customerId: preset?.opportunityId ? undefined : opt(customer),
    corporateAccountId: preset?.opportunityId ? undefined : opt(corporate), title: opt(h.title), line: opt(h.line), eventType: opt(h.eventType),
    eventDate: opt(h.eventDate), pax: num(h.pax), pricingMode: opt(h.pricingMode), validUntil: opt(h.validUntil), discountPercent: opt(h.discountPercent),
    packageRef: opt(h.packageRef), notes: h.notes,
    lines: lines.filter((l) => l.description).map((l) => ({ itemType: l.itemType, itemRef: opt(l.itemRef), serviceType: opt(l.serviceType),
      description: l.description, quantity: l.quantity || '1', unitPrice: opt(l.unitPrice), discountPercent: opt(l.discountPercent),
      discount: l.discountPercent ? undefined : opt(l.discount) })),
    paymentTerms: terms.length ? terms.filter((t) => t.label).map((t) => ({ label: t.label, percent: opt(t.percent), dueDays: num(t.dueDays),
      daysBeforeEvent: num(t.daysBeforeEvent) })) : undefined,
  };
  const fe = send.error?.fieldErrors ?? {};
  return (
    <Modal open onClose={onClose} title={q ? `Edit ${String(q.number)} v${String(q.version)}` : 'New Quotation'} wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !body.lines.length}
        onClick={() => send.mutate(body as unknown as R, { onSuccess: (r) => onDone(r.id) })}>{q ? 'Save & re-price' : 'Create draft'}</button>
    </>}>
      <div className="oc-form">
        {!preset?.opportunityId && !q && <><CustomerPicker value={customer} onChange={setCustomer} required /><CorporatePicker value={corporate} onChange={setCorporate} /></>}
        {preset?.opportunityId && <p className="oc-muted oc-span">Customer, line, event and sales come from the opportunity.</p>}
        <TextField label="Title" value={h.title} onChange={setHead('title')} span error={fe.title} />
        {!preset?.opportunityId && <SelectField label="Business line" value={h.line} onChange={setHead('line')} options={LINES} placeholder="—" />}
        <SelectField label="Event type" value={h.eventType} onChange={setHead('eventType')} options={EVENT_TYPES} placeholder="—" />
        <TextField label="Event date" type="date" value={h.eventDate} onChange={setHead('eventDate')} />
        <TextField label="Pax" type="number" value={h.pax} onChange={setHead('pax')} />
        <TextField label="Package code" value={h.packageRef} onChange={setHead('packageRef')} help="Banquet, package or membership package" />
        <SelectField label="Manual prices are" value={h.pricingMode} onChange={setHead('pricingMode')} placeholder="Sales Policies"
          options={[{ value: 'nett', label: 'Nett (tax & service included)' }, { value: 'plus_plus', label: '++ (tax & service added)' }]} />
        <TextField label="Valid until" type="date" value={h.validUntil} onChange={setHead('validUntil')} help="Default: Sales Policies validity" />
        <TextField label="Discount on total (%)" type="number" value={h.discountPercent} onChange={setHead('discountPercent')}
          help="Above the discount limit of your role (Commercial → Pricing → Discount Limits) the discount needs approval" error={fe.discountPercent} />
      </div>
      <h3>Lines</h3>
      {lines.map((l, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <SelectField label="Item" value={l.itemType} onChange={(v) => setLine(i, 'itemType', v)} options={ITEM_TYPES} />
          <TextField label="Description" value={l.description} onChange={(v) => setLine(i, 'description', v)} />
          <TextField label="Qty" type="number" value={l.quantity} onChange={(v) => setLine(i, 'quantity', v)} style={{ width: 80 }} />
          <TextField label="Unit price" type="number" value={l.unitPrice} onChange={(v) => setLine(i, 'unitPrice', v)} placeholder="From pricing" />
          <TextField label="Service type" value={l.serviceType} onChange={(v) => setLine(i, 'serviceType', v)} placeholder="e.g. meeting_package" />
          <TextField label="Ref" value={l.itemRef} onChange={(v) => setLine(i, 'itemRef', v)} placeholder="Code / id" />
          <TextField label="Disc %" type="number" value={l.discountPercent} onChange={(v) => setLine(i, 'discountPercent', v)} style={{ width: 80 }}
            placeholder={l.discount ? `${l.discount} off` : undefined} />
          <button className="oc-btn oc-btn-sm oc-btn-text" aria-label={`Remove line ${i + 1}`} onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>Remove</button>
        </div>
      ))}
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setLines((ls) => [...ls, newLine()])}>Add line</button>
      <h3>Payment terms</h3>
      <p className="oc-small oc-muted">Leave empty for the Sales Policies terms. Due dates are fixed when the customer accepts.</p>
      {terms.map((t, i) => (
        <div key={i} className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
          <TextField label="Label" value={t.label} onChange={(v) => setTerm(i, 'label', v)} />
          <TextField label="%" type="number" value={t.percent} onChange={(v) => setTerm(i, 'percent', v)} style={{ width: 80 }} />
          <TextField label="Days after acceptance" type="number" value={t.dueDays} onChange={(v) => setTerm(i, 'dueDays', v)} />
          <TextField label="or days before event" type="number" value={t.daysBeforeEvent} onChange={(v) => setTerm(i, 'daysBeforeEvent', v)} />
          <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setTerms((ts) => ts.filter((_, j) => j !== i))}>Remove</button>
        </div>
      ))}
      <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setTerms((ts) => [...ts, { label: ts.length ? 'Final Payment' : 'Down Payment', percent: '', dueDays: '', daysBeforeEvent: '' }])}>
        Add term</button>
      <TextArea label="Notes" value={h.notes} onChange={setHead('notes')} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function QuotationDrawer({ id, onClose, onOpen }: { id: string; onClose: () => void; onOpen: (id: string) => void }) {
  const { can } = useAuth();
  const toast = useToast();
  const d = useGet<R & { lines: R[]; versions: R[]; paymentTerms: R[]; decision: R | null; acceptanceEvidence: R | null; eMeterai: R }>(`/api/v1/crm/quotations/${id}`);
  const [modal, setModal] = useState<'' | 'edit' | 'send' | 'accept' | 'reject'>('');
  const x = d.data;
  const inv = [...CRM, `/api/v1/crm/quotations/${id}`];
  const st = String(x?.status ?? '');
  const needsApproval = x && (x.approvalStatus === 'required' || x.approvalStatus === 'rejected');
  return (
    <Drawer open onClose={onClose} title={x ? `Quotation ${String(x.number)} v${String(x.version)}` : 'Quotation'}>
      {d.isLoading && <Skeleton />}
      <ErrorAlert error={d.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Title', String(x.title)], ['Customer', String(x.corporateName ?? x.customerName ?? '—')], ['Status', <StatusPill key="s" status={st} />],
            ['Discount approval', <StatusPill key="a" status={String(x.approvalStatus)} />], ['Business line', label(x.line)],
            ['Event', [label(x.eventType), x.eventDate ? formatDate(String(x.eventDate)) : '', x.pax ? `${String(x.pax)} pax` : ''].filter(Boolean).join(' · ') || '—'],
            ['Subtotal', money(x.subtotal)], ['Discount', `${money(x.discount)} (${String(x.discountPercent)}%)`], ['Service', money(x.serviceAmount)],
            ['Tax', money(x.taxAmount)], ['Total', <strong key="t">{money(x.total)}</strong>], ['Valid until', formatDate(String(x.validUntil))],
            ['Sales', String(x.ownerName ?? '—')], ['Paid towards the deal', money(x.paidAmount)]]} />
          <Actions>
            {st === 'draft' && can('crm.quotation.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('edit')}>Edit</button>}
            {st === 'draft' && needsApproval && can('crm.quotation.update') &&
              <ActionButton label="Submit for Approval" kind="primary" path={`/api/v1/crm/quotations/${id}:submit-approval`} invalidate={inv} />}
            {(st === 'draft' || st === 'sent') && !needsApproval && can('crm.quotation.send') &&
              <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setModal('send')}>{st === 'sent' ? 'Send again' : 'Send'}</button>}
            {(st === 'sent' || st === 'draft') && !needsApproval && can('crm.quotation.accept') &&
              <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('accept')}>Accept for Customer</button>}
            {st === 'sent' && can('crm.quotation.update') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setModal('reject')}>Record Rejection</button>}
            {['sent', 'rejected', 'expired'].includes(st) && can('crm.quotation.update') &&
              <ActionButton label="Revise" path={`/api/v1/crm/quotations/${id}:revise`} invalidate={inv} onDone={(r) => onOpen((r as R).id)} />}
            <a className="oc-btn oc-btn-sm oc-btn-text" href={`/api/v1/crm/quotations/${id}/pdf`} target="_blank" rel="noreferrer">PDF</a>
            {typeof x.publicLink === 'string' && <button className="oc-btn oc-btn-sm oc-btn-text"
              onClick={() => { void navigator.clipboard?.writeText(String(x.publicLink)); toast('Acceptance link copied'); }}>Copy link</button>}
          </Actions>
          {st === 'pending_approval' && <p className="oc-alert oc-alert-info">The discount waits for approval (Approvals inbox of the approver).</p>}
          <Card title="Lines" icon="list_alt">
            <DataTable rows={x.lines} columns={[{ key: 'description', header: 'Description' }, { key: 'quantity', header: 'Qty', align: 'right' },
              { key: 'unitPrice', header: 'Unit price', align: 'right', render: (l) => money(l.unitPrice) },
              { key: 'discount', header: 'Discount', align: 'right', render: (l) => money(l.discount) },
              { key: 'total', header: 'Total', align: 'right', render: (l) => money(l.total) }, { key: 'priceSource', header: 'Priced by', render: (l) => label(l.priceSource) }]} />
          </Card>
          <Card title="Payment terms" icon="event">
            <DataTable rows={x.paymentTerms.map((t, i) => ({ ...t, id: String(i) })) as R[]} columns={[{ key: 'label', header: 'Term' },
              { key: 'percent', header: '%', align: 'right' }, { key: 'amount', header: 'Amount', align: 'right', render: (t) => money(t.amount) },
              { key: 'dueDate', header: 'Due', render: (t) => (t.dueDate ? formatDate(String(t.dueDate)) : '—') }]} />
          </Card>
          {x.decision && <Card title="Decision" icon="gavel">
            <KV items={[['Decision', <StatusPill key="d" status={String(x.decision.decision)} />], ['Via', label(x.decision.via)], ['Name', String(x.decision.name ?? '—')],
              ['IP address', String(x.decision.ip ?? '—')], ['Terms accepted', x.decision.termsAccepted ? 'Yes' : 'No'], ['Note', String(x.decision.note ?? '—')],
              ['At', formatDateTime(String(x.decision.decidedAt))]]} />
          </Card>}
          {x.acceptanceEvidence && <AcceptanceEvidence ev={x.acceptanceEvidence} />}
          {x.eMeteraiRequired === true && <EMeteraiCard id={id} status={st} e={x.eMeterai} invalidate={inv} />}
          {x.versions.length > 1 && <Card title="Versions" icon="history">
            <DataTable rows={x.versions} onRowClick={(v) => onOpen(v.id)} columns={[{ key: 'version', header: 'Version' },
              { key: 'total', header: 'Total', align: 'right', render: (v) => money(v.total) }, { key: 'status', header: 'Status', render: pill('status') },
              { key: 'createdAt', header: 'Created', render: (v) => formatDateTime(String(v.createdAt)) }]} />
          </Card>}
          {modal === 'edit' && <QuotationForm quotation={x} onClose={() => setModal('')} onDone={() => setModal('')} />}
          {modal === 'send' && <SendQuotation id={id} invalidate={inv} onClose={() => setModal('')} />}
          {modal === 'accept' && <AcceptQuotation id={id} invalidate={inv} onClose={() => setModal('')} />}
          {modal === 'reject' && <RejectQuotation id={id} invalidate={inv} onClose={() => setModal('')} />}
        </div>
      )}
    </Drawer>
  );
}

// Acceptance evidence of the public link: IP, device and the verified one-time code (PRD P3 §16 #18).
function AcceptanceEvidence({ ev }: { ev: R }) {
  const otp = ev.otpChannel ? `${ev.otpChannel === 'whatsapp' ? 'WhatsApp' : 'E-mail'} · ${String(ev.otpDestination ?? '')}` : 'Not used';
  return (
    <Card title="Acceptance evidence" icon="verified_user">
      <KV items={[['IP address', String(ev.ip ?? '—')], ['Device', <span key="ua" className="oc-small">{String(ev.userAgent ?? '—')}</span>],
        ['Verification code', otp], ['Code verified', ev.otpVerifiedAt ? formatDateTime(String(ev.otpVerifiedAt)) : '—']]} />
    </Card>
  );
}

// e-Meterai of a quotation above the Sales Policies threshold, with the staff retry of a pending / failed stamp.
function EMeteraiCard({ id, status, e, invalidate }: { id: string; status: string; e: R; invalidate: string[] }) {
  const { can } = useAuth();
  const items: [string, React.ReactNode][] = [['Status', e.status === 'not_required' ? 'Applied on acceptance' : <StatusPill key="s" status={String(e.status)} />],
    ['Serial no.', String(e.serialNumber ?? '—')], ['Stamped', e.stampedAt ? formatDateTime(String(e.stampedAt)) : '—'],
    ['Provider', e.provider ? `${String(e.provider)}${e.sandbox ? ' (mock / trial, no legal value)' : ''}` : '—']];
  if (e.error) items.push(['Last error', String(e.error)]);
  return (
    <Card title="e-Meterai" icon="approval">
      {status !== 'accepted' && <p className="oc-small oc-muted">The total is above {money(e.threshold)}: an e-Meterai is applied on acceptance.</p>}
      <KV items={items} />
      {status === 'accepted' && (e.status === 'pending' || e.status === 'failed') && can('crm.quotation.stamp') &&
        <Actions><ActionButton label="Retry e-Meterai" kind="primary" path={`/api/v1/crm/quotations/${id}:stamp-e-meterai`} invalidate={invalidate} /></Actions>}
    </Card>
  );
}

function SendQuotation({ id, invalidate, onClose }: { id: string; invalidate: string[]; onClose: () => void }) {
  const [email, setEmail] = useState(true);
  const [wa, setWa] = useState(false);
  const [to, setTo] = useState('');
  const [phone, setPhone] = useState('');
  const [message, setMessage] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/quotations/${id}:send`, invalidate);
  const channels = [email ? 'email' : '', wa ? 'whatsapp' : ''].filter(Boolean);
  return (
    <Modal open onClose={onClose} title="Send Quotation" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!channels.length || send.isPending}
        onClick={() => send.mutate({ channels, email: opt(to), phone: opt(phone), message } as unknown as R, { onSuccess: onClose })}>Send</button>
    </>}>
      <p className="oc-muted">The customer receives the PDF and a secure link to accept or reject online.</p>
      <div className="oc-form">
        <Checkbox label="E-mail" checked={email} onChange={setEmail} />
        <Checkbox label="WhatsApp" checked={wa} onChange={setWa} />
        <TextField label="E-mail (default: the customer's)" type="email" value={to} onChange={setTo} />
        <TextField label="Phone (default: the customer's)" value={phone} onChange={setPhone} />
        <TextArea label="Message" value={message} onChange={setMessage} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function AcceptQuotation({ id, invalidate, onClose }: { id: string; invalidate: string[]; onClose: () => void }) {
  const [name, setName] = useState('');
  const [note, setNote] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/quotations/${id}:accept`, invalidate);
  return (
    <Modal open onClose={onClose} title="Accept for Customer" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ acceptedByName: opt(name), note } as unknown as R, { onSuccess: onClose })}>Accept</button>
    </>}>
      <p className="oc-muted">Record a signed copy or a confirmed acceptance. The opportunity is won and the booking, event or payment schedule is created.</p>
      <div className="oc-form">
        <TextField label="Signatory" value={name} onChange={setName} placeholder="The customer" />
        <TextField label="Note" value={note} onChange={setNote} placeholder="e.g. signed copy received" span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function RejectQuotation({ id, invalidate, onClose }: { id: string; invalidate: string[]; onClose: () => void }) {
  const [reason, setReason] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/quotations/${id}:reject`, invalidate);
  return (
    <Modal open onClose={onClose} title="Record Rejection" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-danger" disabled={!reason || send.isPending} onClick={() => send.mutate({ reason } as unknown as R, { onSuccess: onClose })}>Record</button>
    </>}>
      <TextField label="Reason" value={reason} onChange={setReason} required />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Follow-ups ────────────────────────────────────────────────────────────

export function FollowUpsPage() {
  const { can } = useAuth();
  const users = useSalesUsers();
  const [status, setStatus] = useState('open');
  const [overdue, setOverdue] = useState(false);
  const [who, setWho] = useState('');
  const [modal, setModal] = useState<'' | 'new'>('');
  const [done, setDone] = useState<R | null>(null);
  const list = useGet<Page<R>>(`/api/v1/crm/follow-ups${qs({ 'filter[status]': status, overdue: overdue ? 'true' : '', 'filter[assignedTo]': who, limit: 200 })}`);
  return (
    <div className="oc-stack">
      <PageHeader title="Follow-ups" help="Calls, meetings, site visits and food tastings due on leads, opportunities and customers."
        actions={can('crm.follow_up.manage') ? <button className="oc-btn oc-btn-primary" onClick={() => setModal('new')}>New Follow-up</button> : undefined} />
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <Tabs tabs={opts(['open', 'completed', 'cancelled', 'all'])} value={status} onChange={setStatus} />
        <Checkbox label="Overdue only" checked={overdue} onChange={setOverdue} />
        {can('crm.lead.assign') && <SelectField label="Sales" value={who} onChange={setWho} options={users} placeholder="Everyone" />}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} columns={[
        { key: 'dueAt', header: 'Due', render: (r) => (r.dueAt ? formatDateTime(String(r.dueAt)) : '—') }, { key: 'type', header: 'Type', render: (r) => label(r.type) },
        { key: 'subject', header: 'Subject' }, { key: 'contactName', header: 'Contact', render: (r) => String(r.contactName ?? '—') },
        { key: 'ref', header: 'For', render: (r) => (r.leadId ? <Link to={`/crm/leads?id=${String(r.leadId)}`}>{String(r.leadNumber)}</Link>
          : r.opportunityId ? <Link to={`/crm/opportunities?id=${String(r.opportunityId)}`}>{String(r.opportunityNumber)}</Link> : '—') },
        { key: 'assignedToName', header: 'Sales', render: (r) => String(r.assignedToName ?? '—') },
        { key: 'status', header: 'Status', render: (r) => <StatusPill status={r.overdue ? 'overdue' : String(r.status)} /> }]}
        actions={(r) => r.status === 'open' && can('crm.follow_up.manage') ? <Actions>
          <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setDone(r)}>Complete</button>
          <ActionButton label="Cancel" path={`/api/v1/crm/follow-ups/${r.id}:cancel`} invalidate={CRM} reason="required" />
        </Actions> : null} />
      {modal === 'new' && <NewFollowUp onClose={() => setModal('')} />}
      {done && <CompleteFollowUp item={done} onClose={() => setDone(null)} />}
    </div>
  );
}

function NewFollowUp({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState('lead');
  const [ref, setRef] = useState('');
  const leads = useGet<Page<R>>(kind === 'lead' ? '/api/v1/crm/leads?filter[status]=new,contacted,qualified&limit=200' : null);
  const opps = useGet<Page<R>>(kind === 'opportunity' ? '/api/v1/crm/opportunities?filter[status]=open&limit=200' : null);
  const items = (kind === 'lead' ? leads.data?.items : opps.data?.items) ?? [];
  return (
    <>
      {!ref ? (
        <Modal open onClose={onClose} title="New Follow-up" actions={<button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>}>
          <div className="oc-form">
            <SelectField label="For" value={kind} onChange={(v) => { setKind(v); setRef(''); }} options={opts(['lead', 'opportunity'])} />
            <SelectField label={kind === 'lead' ? 'Lead' : 'Opportunity'} value={ref} onChange={setRef} placeholder="Select"
              options={items.map((i) => ({ value: i.id, label: `${String(i.number)} · ${String(i.name ?? i.title)}` }))} />
          </div>
        </Modal>
      ) : (
        <ActivityForm followUp path="/api/v1/crm/follow-ups" invalidate={CRM} onClose={onClose}
          target={kind === 'lead' ? { leadId: ref } : { opportunityId: ref }} />
      )}
    </>
  );
}

function CompleteFollowUp({ item, onClose }: { item: R; onClose: () => void }) {
  const [outcome, setOutcome] = useState('');
  const [notes, setNotes] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/follow-ups/${item.id}:complete`, CRM);
  return (
    <Modal open onClose={onClose} title={`Complete: ${String(item.subject)}`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ outcome, notes } as unknown as R, { onSuccess: onClose })}>Complete</button>
    </>}>
      <div className="oc-form">
        <TextField label="Outcome" value={outcome} onChange={setOutcome} span />
        <TextArea label="Notes" value={notes} onChange={setNotes} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

// ── Sales Targets & Commission (EP-04) ────────────────────────────────────

const thisMonth = () => today().slice(0, 7);

export function SalesCommissionPage() {
  const { can } = useAuth();
  const [tab, setTab] = useState(can('crm.commission.view') ? 'statements' : 'targets');
  const tabs = [...(can('crm.commission.view') ? [{ value: 'statements', label: 'Commission Statements' }, { value: 'lines', label: 'Commission Lines' }] : []),
    { value: 'targets', label: 'Sales Targets' }];
  return (
    <div className="oc-stack">
      <PageHeader title="Sales Targets & Commission" help="Commission is earned when a deal is paid in full; statements are approved by Finance, paid and exported for payroll." />
      <Tabs tabs={tabs} value={tab} onChange={setTab} />
      {tab === 'statements' && <Statements />}
      {tab === 'lines' && <CommissionLines />}
      {tab === 'targets' && <Targets />}
    </div>
  );
}

function Statements() {
  const { can } = useAuth();
  const [period, setPeriod] = useState(thisMonth());
  const [open, setOpen] = useState<string | null>(null);
  const [paying, setPaying] = useState<R | null>(null);
  const list = useGet<Page<R>>(`/api/v1/crm/commission-statements${qs({ 'filter[period]': period, limit: 200 })}`);
  return (
    <>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Period" type="month" value={period} onChange={setPeriod} />
        {can('crm.commission.manage') && <ActionButton label="Generate statements" kind="primary" path="/api/v1/crm/commission-statements:generate" body={{ period }} invalidate={CRM} />}
        {can('crm.commission.export') && <a className="oc-btn oc-btn-sm oc-btn-neutral" href={`/api/v1/crm/commission-statements:export${qs({ period })}`}>Export for payroll (CSV)</a>}
      </div>
      <DataTable rows={list.data?.items} loading={list.isLoading} error={list.error} onRowClick={(r) => setOpen(r.id)} columns={[
        { key: 'number', header: 'Statement' }, { key: 'userName', header: 'Sales' }, { key: 'earned', header: 'Earned', align: 'right', render: (r) => money(r.earned) },
        { key: 'clawback', header: 'Clawback', align: 'right', render: (r) => money(r.clawback) },
        { key: 'adjustments', header: 'Adjustments', align: 'right', render: (r) => money(r.adjustments) },
        { key: 'total', header: 'Total', align: 'right', render: (r) => <strong>{money(r.total)}</strong> }, { key: 'status', header: 'Status', render: pill('status') }]}
        actions={(r) => can('crm.commission.manage') ? <Actions>
          {(r.status === 'draft' || r.status === 'rejected') && <ActionButton label="Submit" path={`/api/v1/crm/commission-statements/${r.id}:submit`} invalidate={CRM} />}
          {r.status === 'approved' && <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setPaying(r)}>Mark Paid</button>}
        </Actions> : null} />
      {open && <StatementDrawer id={open} onClose={() => setOpen(null)} />}
      {paying && <MarkPaid statement={paying} onClose={() => setPaying(null)} />}
    </>
  );
}

function StatementDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const d = useGet<R & { lines: R[] }>(`/api/v1/crm/commission-statements/${id}`);
  const x = d.data;
  return (
    <Drawer open onClose={onClose} title={x ? `Statement ${String(x.number)}` : 'Statement'}>
      {d.isLoading && <Skeleton />}
      {x && <div className="oc-stack">
        <KV items={[['Sales', String(x.userName)], ['Period', String(x.period)], ['Status', <StatusPill key="s" status={String(x.status)} />],
          ['Earned', money(x.earned)], ['Clawback', money(x.clawback)], ['Adjustments', money(x.adjustments)], ['Total', <strong key="t">{money(x.total)}</strong>],
          ['Paid', x.paidAt ? `${formatDateTime(String(x.paidAt))} · ${String(x.paidReference ?? '')}` : '—']]} />
        <CommissionTable rows={x.lines} />
      </div>}
    </Drawer>
  );
}

function CommissionTable({ rows, loading }: { rows?: R[]; loading?: boolean }) {
  return <DataTable rows={rows} loading={loading} columns={[{ key: 'recognizedOn', header: 'Date', render: (r) => formatDate(String(r.recognizedOn)) },
    { key: 'userName', header: 'Sales' }, { key: 'kind', header: 'Kind', render: pill('kind') },
    { key: 'quotationNumber', header: 'Deal', render: (r) => String(r.quotationNumber ?? '—') }, { key: 'line', header: 'Line', render: (r) => label(r.line) },
    { key: 'basisAmount', header: 'Basis', align: 'right', render: (r) => money(r.basisAmount) },
    { key: 'ratePercent', header: 'Rate', align: 'right', render: (r) => (r.ratePercent != null ? `${String(r.ratePercent)}%` : '—') },
    { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'reason', header: 'Reason' }]} />;
}

function MarkPaid({ statement, onClose }: { statement: R; onClose: () => void }) {
  const [ref, setRef] = useState('');
  const send = useSend<R, R>('POST', `/api/v1/crm/commission-statements/${statement.id}:mark-paid`, CRM);
  return (
    <Modal open onClose={onClose} title={`Mark ${String(statement.number)} Paid`} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate({ reference: ref } as unknown as R, { onSuccess: onClose })}>Mark Paid</button>
    </>}>
      <TextField label="Payroll batch / transfer reference" value={ref} onChange={setRef} />
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function CommissionLines() {
  const { can } = useAuth();
  const users = useSalesUsers();
  const [who, setWho] = useState('');
  const [adjust, setAdjust] = useState(false);
  const list = useGet<Page<R>>(`/api/v1/crm/commission-lines${qs({ 'filter[userId]': who, limit: 200 })}`);
  return (
    <>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <SelectField label="Sales" value={who} onChange={setWho} options={users} placeholder="Everyone" />
        {can('crm.commission.manage') && <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setAdjust(true)}>Manual adjustment</button>}
      </div>
      <ErrorAlert error={list.error} />
      <CommissionTable rows={list.data?.items} loading={list.isLoading} />
      {adjust && <Adjustment users={users} onClose={() => setAdjust(false)} />}
    </>
  );
}

function Adjustment({ users, onClose }: { users: Option[]; onClose: () => void }) {
  const [user, setUser] = useState('');
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [period, setPeriod] = useState(thisMonth());
  const send = useSend<R, R>('POST', '/api/v1/crm/commission-adjustments', CRM);
  return (
    <Modal open onClose={onClose} title="Commission Adjustment" actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!user || !amount || !reason || send.isPending}
        onClick={() => send.mutate({ userId: user, amount, reason, period } as unknown as R, { onSuccess: onClose })}>Book</button>
    </>}>
      <div className="oc-form">
        <SelectField label="Sales" value={user} onChange={setUser} options={users} placeholder="Select" required />
        <TextField label="Amount" type="number" value={amount} onChange={setAmount} help="Negative for a deduction" required />
        <TextField label="Period" type="month" value={period} onChange={setPeriod} />
        <TextField label="Reason" value={reason} onChange={setReason} required span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function Targets() {
  const first = `${thisMonth()}-01`;
  const [from, setFrom] = useState(first);
  const [to, setTo] = useState(today());
  const list = useGet<Page<R>>(`/api/v1/crm/sales-target-achievements${qs({ from, to })}`);
  return (
    <>
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <TextField label="To" type="date" value={to} onChange={setTo} />
        <Link className="oc-btn oc-btn-sm oc-btn-neutral" to="/crm/sales-settings?tab=crm.sales_target">Manage targets</Link>
      </div>
      <DataTable rows={(list.data?.items ?? []).map((r) => ({ ...r, id: String(r.targetId) })) as R[]} loading={list.isLoading} error={list.error} columns={[
        { key: 'who', header: 'Sales / Team', render: (r) => String(r.userName ?? r.teamName ?? 'Club') }, { key: 'line', header: 'Line', render: (r) => label(r.line ?? 'all') },
        { key: 'period', header: 'Period', render: (r) => `${formatDate(String(r.periodStart))} – ${formatDate(String(r.periodEnd))}` },
        { key: 'targetRevenue', header: 'Target', align: 'right', render: (r) => money(r.targetRevenue) },
        { key: 'achievedRevenue', header: 'Achieved (paid)', align: 'right', render: (r) => money(r.achievedRevenue) },
        { key: 'achievementPercent', header: '%', align: 'right', render: (r) => `${String(r.achievementPercent)}%` },
        { key: 'deals', header: 'Deals', align: 'right', render: (r) => `${String(r.achievedDeals)} / ${String(r.targetDeals)}` },
        { key: 'bookedRevenue', header: 'Booked (accepted)', align: 'right', render: (r) => money(r.bookedRevenue) }]} />
    </>
  );
}

// ── Sales settings (master data) ──────────────────────────────────────────

const SALES_MASTER: Option[] = [
  { value: 'crm.sales_pipeline', label: 'Pipelines' }, { value: 'crm.sales_pipeline_stage', label: 'Pipeline Stages' },
  { value: 'crm.sales_team', label: 'Sales Teams' }, { value: 'crm.sales_team_member', label: 'Team Members' },
  { value: 'crm.sales_target', label: 'Sales Targets' }, { value: 'crm.commission_scheme', label: 'Commission Schemes' },
];

export function SalesSettingsPage() {
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const tab = params.get('tab') ?? SALES_MASTER[0].value;
  return (
    <div className="oc-stack">
      <PageHeader title="Sales Settings" help="Pipelines and stages per business line, sales teams for round-robin assignment, targets and commission schemes. Lead SLA and default commission rates are in Settings → Club Policies → Sales Policies; discount limits per role in Commercial → Pricing → Discount Limits."
        actions={can('crm.pipeline.create') ? <ActionButton label="Add default pipelines" path="/api/v1/crm/pipelines:seed-defaults" invalidate={CRM} /> : undefined} />
      <Tabs tabs={SALES_MASTER} value={tab} onChange={(v) => setParams({ tab: v })} />
      <AutoResourcePage key={tab} resourceKey={tab} />
    </div>
  );
}

/** Back Office routes of CRM Sales. */
export const SALES_ROUTES: AreaRoute[] = [
  { path: 'crm/leads', perm: 'crm.lead.view', element: <LeadsPage /> },
  { path: 'crm/opportunities', perm: 'crm.opportunity.view', element: <OpportunitiesPage /> },
  { path: 'crm/pipeline', perm: 'crm.opportunity.view', element: <SalesPipelinePage /> },
  { path: 'crm/quotations', perm: 'crm.quotation.view', element: <QuotationsPage /> },
  { path: 'crm/follow-ups', perm: 'crm.follow_up.view', element: <FollowUpsPage /> },
  { path: 'crm/sales-commission', perm: 'crm.sales_target.view', element: <SalesCommissionPage /> },
  { path: 'crm/sales-settings', perm: 'crm.pipeline.view', element: <SalesSettingsPage /> },
];
