import React, { useEffect, useRef, useState } from 'react';
import { download, request, uuidv7, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate, formatDateTime } from '@oneclub/i18n';
import {
  Card, Checkbox, DataTable, ErrorAlert, Modal, PageHeader, SelectField, StatusPill, TextArea, TextField, useToast, type Option,
} from '@oneclub/shell';
import { KV, Tabs, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';

// PRD P5 gap closure (docs/p5-gap-audit.md): device clock-in of partner caddies and instructors (FR-ATT-08, HRIS → Partner
// Clock-in and Caddy Master → Device Clock-ins in ops), the HR Migration Reconciliation with HR Manager and Finance Manager
// sign-off (FR-MIG-P5-05) and the push notification settings of the installed app (FR-INT-P5-05, ESS profile). Routes are
// spread into HR_ROUTES / HR_OPS_ROUTES by hr.tsx.

const HR = '/api/v1/hris';
const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const val = (v: unknown) => (v === null || v === undefined || v === '' ? '—' : String(v));
const date = (v: unknown) => (v ? formatDate(String(v).slice(0, 10)) : '—');
const opts = (vals: string[]): Option[] => vals.map((v) => ({ value: v, label: label(v) }));
const pill = (k: string) => (r: R) => <StatusPill status={String(r[k] ?? '')} label={label(r[k])} />;
const idem = () => ({ 'Idempotency-Key': uuidv7() });
const clean = (o: Record<string, unknown>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v !== '' && v !== undefined && v !== null));
const addDays = (day: string, n: number) => {
  const d = new Date(`${day}T00:00:00`);
  d.setDate(d.getDate() + n);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
};

function FormModal({ title, path, body, onClose, children, submit = 'Save', invalidate = [HR] }: {
  title: string; path: string; body: () => Record<string, unknown>; onClose: () => void; children: React.ReactNode; submit?: string; invalidate?: string[];
}) {
  const toast = useToast();
  const send = useSend<Record<string, unknown>, R>('POST', path, invalidate, idem);
  return (
    <Modal open onClose={onClose} title={title} actions={
      <>
        <button className="oc-btn oc-btn-text" onClick={onClose}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={send.isPending} onClick={() => send.mutate(body(), { onSuccess: () => { toast(`${title}: done`); onClose(); } })}>{submit}</button>
      </>
    }>
      <div className="oc-stack">
        <ErrorAlert error={send.error} />
        <div className="oc-form">{children}</div>
      </div>
    </Modal>
  );
}

// ── Partner clock-in (FR-ATT-08) ──────────────────────────────────────────

const PARTNER_TABS: Option[] = [{ value: 'profiles', label: 'Partners' }, { value: 'events', label: 'Clock Events' }];

/** HRIS → Partner Clock-in: caddies and instructors enrolled on the biometric devices, with consent and their clock events. */
export function PartnerAttendancePage() {
  const [tab, setTab] = useState('profiles');
  return (
    <div className="oc-stack">
      <PageHeader title="Partner Clock-in" help="Partner caddies and instructors may clock in on the face / fingerprint devices. Biometric templates stay on the devices; a caddy's first clock-in of the day marks the caddy present in Caddy Master." />
      <Tabs tabs={PARTNER_TABS} value={tab} onChange={setTab} />
      {tab === 'profiles' ? <PartnerProfiles /> : <PartnerEvents />}
    </div>
  );
}

function PartnerProfiles() {
  const [kind, setKind] = useState('');
  const l = useGet<Page<R>>(`${HR}/partner-attendance-profiles${kind ? `?holderKind=${kind}` : ''}`);
  const devices = useGet<Page<R>>(`${HR}/attendance-devices?limit=200`);
  const mocks = (devices.data?.items ?? []).filter((d) => d.vendor === 'mock' && d.deviceKind === 'biometric' && d.status === 'active');
  const [enrol, setEnrol] = useState(false);
  const [consent, setConsent] = useState<R | null>(null);
  const [sim, setSim] = useState<R | null>(null);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <SelectField label="Partner type" value={kind} onChange={setKind} options={opts(['caddy', 'instructor'])} placeholder="All" />
        <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'end' }} onClick={() => setEnrol(true)}>Enrol partner</button>
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'partnerName', header: 'Partner' }, { key: 'holderKind', header: 'Type', render: (r) => label(r.holderKind) },
        { key: 'deviceUserNo', header: 'Device No.' },
        { key: 'biometricConsent', header: 'Biometric consent', render: (r) => (r.biometricConsent ? `Signed ${date(r.consentSignedOn)}` : r.consentWithdrawnAt ? 'Withdrawn' : '—') },
        { key: 'enrolledAt', header: 'On devices', render: (r) => (r.enrolledAt ? formatDateTime(String(r.enrolledAt)) : '—') },
        { key: 'lastEventAt', header: 'Last clock', render: (r) => (r.lastEventAt ? formatDateTime(String(r.lastEventAt)) : '—') },
      ]} actions={(r) => (
        <div className="oc-row" style={{ gap: 6 }}>
          <button className="oc-btn oc-btn-text" onClick={() => setConsent(r)}>Consent</button>
          {mocks.length > 0 && r.biometricConsent ? <button className="oc-btn oc-btn-text" onClick={() => setSim(r)}>Simulate scan</button> : null}
        </div>
      )} empty={<p className="oc-muted">No partner enrolled for device clock-in.</p>} />
      {enrol && <EnrolModal onClose={() => setEnrol(false)} />}
      {consent && <PartnerConsentModal r={consent} onClose={() => setConsent(null)} />}
      {sim && <PartnerSimModal r={sim} devices={mocks} onClose={() => setSim(null)} />}
    </div>
  );
}

function EnrolModal({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState('caddy');
  const [partner, setPartner] = useState('');
  const [consent, setConsent] = useState(true);
  const [signed, setSigned] = useState(today());
  const list = useGet<Page<R>>(kind === 'caddy' ? '/api/v1/golf/caddies?limit=500' : '/api/v1/sportclub/instructors?limit=500');
  const partners: Option[] = (list.data?.items ?? []).map((p) => ({ value: p.id, label: `${String(p.name)} (${String(p.code ?? '')})` }));
  return (
    <FormModal title="Enrol Partner" path={`${HR}/partner-attendance-profiles`} submit="Enrol" onClose={onClose}
      body={() => (consent ? { holderKind: kind, partnerId: partner, consent, signedOn: signed } : { holderKind: kind, partnerId: partner })}>
      <SelectField label="Type" value={kind} onChange={(v) => { setKind(v); setPartner(''); }} options={opts(['caddy', 'instructor'])} />
      <SelectField label={kind === 'caddy' ? 'Caddy' : 'Instructor'} value={partner} onChange={setPartner} options={partners} required />
      <div className="oc-span"><Checkbox label="Written biometric consent signed" checked={consent} onChange={setConsent} /></div>
      {consent && <TextField label="Signed on" type="date" value={signed} onChange={setSigned} required />}
    </FormModal>
  );
}

function PartnerConsentModal({ r, onClose }: { r: R; onClose: () => void }) {
  const [consent, setConsent] = useState(!r.biometricConsent);
  const [signed, setSigned] = useState(today());
  return (
    <FormModal title={`Biometric Consent · ${String(r.partnerName)}`} path={`${HR}/partner-attendance-profiles/${r.id}:consent`} onClose={onClose}
      body={() => (consent ? { consent, signedOn: signed } : { consent })}>
      <div className="oc-span"><Checkbox label="Written consent signed (face / fingerprint enrolment allowed)" checked={consent} onChange={setConsent} /></div>
      {consent ? <TextField label="Signed on" type="date" value={signed} onChange={setSigned} required />
        : <p className="oc-span oc-muted">Withdrawing removes the partner from the biometric devices.</p>}
    </FormModal>
  );
}

function PartnerSimModal({ r, devices, onClose }: { r: R; devices: R[]; onClose: () => void }) {
  const [device, setDevice] = useState(String(devices[0]?.id ?? ''));
  const [method, setMethod] = useState('face_recognition');
  const [direction, setDirection] = useState('');
  return (
    <FormModal title={`Simulate scan · ${String(r.partnerName)}`} path={`${HR}/attendance-devices/${device}:simulate-partner`} submit="Scan" onClose={onClose}
      body={() => clean({ profileId: r.id, method, direction })}>
      <SelectField label="Mock device" value={device} onChange={setDevice} options={devices.map((d) => ({ value: d.id, label: `${String(d.code)} · ${String(d.name)}` }))} />
      <SelectField label="Method" value={method} onChange={setMethod} options={opts(['face_recognition', 'fingerprint'])} />
      <SelectField label="Direction" value={direction} onChange={setDirection} options={opts(['in', 'out'])} placeholder="Automatic" />
    </FormModal>
  );
}

function PartnerEvents({ kind = '' }: { kind?: string }) {
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const l = useGet<Page<R>>(`${HR}/partner-attendance-events?from=${from}&to=${to}${kind ? `&holderKind=${kind}` : ''}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap">
        <TextField label="From" type="date" value={from} onChange={setFrom} />
        <TextField label="To" type="date" value={to} onChange={setTo} />
      </div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} columns={[
        { key: 'occurredAt', header: 'Time', render: (r) => formatDateTime(String(r.occurredAt)) }, { key: 'partnerName', header: 'Partner' },
        { key: 'holderKind', header: 'Type', render: (r) => label(r.holderKind) }, { key: 'direction', header: 'In / Out', render: pill('direction') },
        { key: 'method', header: 'Method', render: (r) => label(r.method) }, { key: 'deviceCode', header: 'Device' },
        { key: 'offline', header: 'Offline', render: (r) => (r.offline ? 'Synced later' : '') },
      ]} empty={<p className="oc-muted">No clock events in the period.</p>} />
    </div>
  );
}

/** Caddy Master (ops): today's device clock-ins of the caddies. */
function OpsCaddyClockIns() {
  return (
    <div className="oc-stack">
      <PageHeader title="Device Clock-ins" help="Caddies who clocked in on the caddy house device join the Caddy Queue automatically." />
      <PartnerEvents kind="caddy" />
    </div>
  );
}

// ── HR migration reconciliation (FR-MIG-P5-05) ───────────────────────────

/** HRIS → HR Migration → Reconciliation: legacy control totals vs OneClub, signed off by the HR Manager and the Finance Manager. */
export function ReconciliationPage() {
  const l = useGet<Page<R>>(`${HR}/migration-reconciliations`);
  const metrics = useGet<Page<R>>(`${HR}/migration-reconciliation-metrics`);
  const [open, setOpen] = useState(false);
  const [sel, setSel] = useState<string | null>(null);
  return (
    <div className="oc-stack">
      <PageHeader title="HR Migration Reconciliation" help="Compare the headcount, leave balances and payroll totals (gross and net of the last legacy period against the parallel run) of the legacy HR system at cutover with OneClub. The HR Manager and the Finance Manager sign off; differences must be explained. Also available as `oneclub import hris --reconcile`." />
      <div><button className="oc-btn oc-btn-ink" onClick={() => setOpen(true)}>New reconciliation</button></div>
      <DataTable rows={l.data?.items} loading={l.isLoading} error={l.error} onRowClick={(r) => setSel(r.id)} columns={[
        { key: 'number', header: 'Number' }, { key: 'cutoverDate', header: 'Cutover', render: (r) => date(r.cutoverDate) },
        { key: 'legacySystem', header: 'Legacy system' }, { key: 'checks', header: 'Checks' }, { key: 'mismatches', header: 'Mismatches' },
        { key: 'status', header: 'Status', render: pill('status') },
      ]} empty={<p className="oc-muted">No reconciliation yet.</p>} />
      {sel && <ReconciliationDetail id={sel} onClose={() => setSel(null)} />}
      {open && <NewReconciliation metrics={metrics.data?.items ?? []} onClose={() => setOpen(false)} />}
    </div>
  );
}

function NewReconciliation({ metrics, onClose }: { metrics: R[]; onClose: () => void }) {
  const [cutover, setCutover] = useState(today());
  const [legacy, setLegacy] = useState('');
  const [csv, setCsv] = useState('metric,key,legacy\nheadcount,TOTAL,\nleave_balance,ANNUAL,\npayroll_gross,TOTAL,\npayroll_net,TOTAL,\n');
  const lines = () => csv.split('\n').map((l) => l.split(',').map((x) => x.trim())).filter((c) => c.length >= 3 && c[0] && c[0] !== 'metric')
    .map(([metric, key, value]) => ({ metric, key, legacy: value }));
  return (
    <FormModal title="New Reconciliation" path={`${HR}/migration-reconciliations`} submit="Reconcile" onClose={onClose}
      body={() => ({ cutoverDate: cutover, legacySystem: legacy, lines: lines() })}>
      <TextField label="Cutover date" type="date" value={cutover} onChange={setCutover} required />
      <TextField label="Legacy system" value={legacy} onChange={setLegacy} required placeholder="HR Excel 2026" />
      <TextArea label="Control totals (CSV metric,key,legacy)" value={csv} onChange={setCsv} rows={8} span
        help={metrics.map((m) => `${String(m.code)}: ${String(m.keyHelp)}`).join(' · ')} />
    </FormModal>
  );
}

function ReconciliationDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const r = useGet<R>(`${HR}/migration-reconciliations/${id}`);
  const [sign, setSign] = useState('');
  const [note, setNote] = useState('');
  const toast = useToast();
  // the action goes into the path only: the request body is exactly the API input (unknown fields are rejected)
  const action = useRef('');
  const send = useSend<Record<string, unknown>, R>('POST', () => `${HR}/migration-reconciliations/${id}:${action.current}`, [HR]);
  const x = r.data;
  const act = (a: string, body: Record<string, unknown> = {}) => { action.current = a; send.mutate(body, {
    onSuccess: () => { toast(`${label(a)}: done`); setSign(''); setNote(''); }, onError: (e) => toast(e.message, 'error'),
  }); };
  const lines = ((x?.lines as R[] | undefined) ?? []).map((l, i) => ({ ...l, id: String(i) } as R));
  return (
    <Modal open wide onClose={onClose} title={`Reconciliation ${String(x?.number ?? '')}`} actions={<button className="oc-btn oc-btn-text" onClick={onClose}>Close</button>}>
      {x && (
        <div className="oc-stack">
          <ErrorAlert error={send.error} />
          <KV items={[['Cutover', date(x.cutoverDate)], ['Legacy system', String(x.legacySystem)], ['Status', label(x.status)],
            ['HR Manager', x.hrSignedAt ? `${val(x.hrSignedName)} · ${formatDateTime(String(x.hrSignedAt))}${x.hrNote ? ` — ${String(x.hrNote)}` : ''}` : 'Pending'],
            ['Finance Manager', x.financeSignedAt ? `${val(x.financeSignedName)} · ${formatDateTime(String(x.financeSignedAt))}${x.financeNote ? ` — ${String(x.financeNote)}` : ''}` : 'Pending']]} />
          <DataTable rows={lines} columns={[
            { key: 'metricLabel', header: 'Metric' }, { key: 'key', header: 'Key' }, { key: 'legacy', header: 'Legacy', render: (l) => (l.legacy === null ? 'not in legacy' : String(l.legacy)) },
            { key: 'oneclub', header: 'OneClub' }, { key: 'difference', header: 'Difference' },
            { key: 'match', header: 'Result', render: (l) => <StatusPill status={l.match ? 'completed' : 'failed'} label={l.match ? 'Match' : 'Mismatch'} /> },
          ]} />
          <div className="oc-row-wrap">
            <button className="oc-btn oc-btn-neutral" onClick={() => void download('GET', `${HR}/migration-reconciliations/${id}/csv`, undefined, `${String(x.number)}.csv`)}>Download CSV</button>
            {x.status === 'draft' && <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => act('recalculate')}>Recalculate</button>}
            {x.status !== 'signed_off' && x.status !== 'cancelled' && (
              <>
                {!x.hrSignedAt && <button className="oc-btn oc-btn-ink" onClick={() => setSign('hr')}>Sign as HR Manager</button>}
                {!x.financeSignedAt && <button className="oc-btn oc-btn-ink" onClick={() => setSign('finance')}>Sign as Finance Manager</button>}
                <button className="oc-btn oc-btn-text" disabled={send.isPending} onClick={() => act('cancel')}>Cancel reconciliation</button>
              </>
            )}
          </div>
          {sign && (
            <Card title={sign === 'hr' ? 'HR Manager sign-off' : 'Finance Manager sign-off'} icon="verified">
              <TextArea label={Number(x.mismatches) > 0 ? 'Explanation of the differences' : 'Note (optional)'} value={note} onChange={setNote}
                required={Number(x.mismatches) > 0} />
              <div className="oc-row" style={{ gap: 8, marginTop: 8 }}>
                <button className="oc-btn oc-btn-ink" disabled={send.isPending || (Number(x.mismatches) > 0 && !note.trim())}
                  onClick={() => act('sign-off', clean({ role: sign, note }))}>Sign off</button>
                <button className="oc-btn oc-btn-text" onClick={() => setSign('')}>Cancel</button>
              </div>
            </Card>
          )}
        </div>
      )}
    </Modal>
  );
}

// ── Push notifications (FR-INT-P5-05) ─────────────────────────────────────

const b64ToBytes = (s: string) => {
  const raw = atob((s + '='.repeat((4 - (s.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
};
const bytesToB64 = (b: ArrayBuffer | null) => (b ? btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : '');

/** Push notifications on this device (schedules, approvals, payslips): subscribes the installed app with the Push API. */
export function PushSettings({ surface }: { surface: 'staff' | 'member' }) {
  const cfg = useGet<{ enabled: boolean; publicKey: string }>('/api/v1/platform/push-config');
  const subs = useGet<Page<R>>('/api/v1/platform/push-subscriptions');
  const [endpoint, setEndpoint] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const supported = typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  useEffect(() => {
    if (!supported) return;
    void navigator.serviceWorker.getRegistration().then((reg) => reg?.pushManager.getSubscription()).then((s) => setEndpoint(s?.endpoint ?? null));
  }, [supported]);
  if (!cfg.data?.enabled) return null;
  const on = async () => {
    setBusy(true);
    setErr(null);
    try {
      if ((await Notification.requestPermission()) !== 'granted') throw new Error('Notifications are blocked for this app in the browser settings.');
      const reg = await navigator.serviceWorker.ready;
      const s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64ToBytes(cfg.data!.publicKey) });
      await request('POST', '/api/v1/platform/push-subscriptions', { endpoint: s.endpoint, surface, userAgent: navigator.userAgent.slice(0, 300),
        keys: { p256dh: bytesToB64(s.getKey('p256dh')), auth: bytesToB64(s.getKey('auth')) } });
      setEndpoint(s.endpoint);
      await subs.refetch();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  const off = async () => {
    setBusy(true);
    setErr(null);
    try {
      const reg = await navigator.serviceWorker.getRegistration();
      const s = await reg?.pushManager.getSubscription();
      const row = (subs.data?.items ?? []).find((x) => s && s.endpoint.includes(String(x.service)));
      if (row) await request('DELETE', `/api/v1/platform/push-subscriptions/${row.id}`);
      await s?.unsubscribe();
      setEndpoint(null);
      await subs.refetch();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card title="Push notifications" icon="notifications_active">
      <ErrorAlert error={err} />
      {!supported ? <p className="oc-muted">Install the app on your phone (Add to Home Screen) to receive push notifications.</p> : (
        <div className="oc-stack">
          <p className="oc-muted">{endpoint ? 'This device receives your notifications (schedules, approvals, payslips) as push messages.'
            : 'Receive schedules, approvals and payslip notices on this device.'}</p>
          <div>
            {endpoint ? <button className="oc-btn oc-btn-neutral" style={{ minHeight: 44 }} disabled={busy} onClick={() => void off()}>Turn off on this device</button>
              : <button className="oc-btn oc-btn-ink" style={{ minHeight: 44 }} disabled={busy} onClick={() => void on()}>Turn on push notifications</button>}
          </div>
          {(subs.data?.items.length ?? 0) > 0 && <p className="oc-small oc-muted">{subs.data?.items.length} device(s) subscribed to your account.</p>}
        </div>
      )}
    </Card>
  );
}

// ── registrations ─────────────────────────────────────────────────────────

export const GAPS_ROUTES: AreaRoute[] = [
  { path: 'hris/partner-attendance', perm: 'hris.partner_attendance.view', element: <PartnerAttendancePage /> },
  { path: 'hris/migration-reconciliation', perm: 'hris.migration_reconciliation.view', element: <ReconciliationPage /> },
];

export const GAPS_OPS_TILES: OpsTile[] = [['fingerprint', 'Device Clock-ins', '/ops/caddy/clock-ins', 'hris.partner_attendance.view']];

export const GAPS_OPS_ROUTES: OpsRoute[] = [{ path: 'caddy/clock-ins', element: <OpsCaddyClockIns /> }];
