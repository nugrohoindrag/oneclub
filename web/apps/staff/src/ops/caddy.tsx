import React, { useState } from 'react';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import { Card, DataTable, ErrorAlert, SelectField, StatusPill, TextField, useAuth, useToast } from '@oneclub/shell';
import { Btn, CaddyAssignmentPage, Head, money } from './golf';

// Caddy History (demo feedback 9 Oct 2026): per caddy the members they
// accompanied and how often, the assignments, the ratings, and the wage
// of a month — base salary plus the caddy fee per assignment, tips and
// deductions. The day view of assignments stays below.

type R = Record<string, unknown> & { id: string };
const withId = (xs: R[] | undefined, key: string): R[] => (xs ?? []).map((x) => ({ ...x, id: String(x[key]) }) as R);
const thisMonth = () => new Date().toISOString().slice(0, 7);

export function CaddyHistoryPage() {
  const caddies = useGet<Page<R>>('/api/v1/golf/caddies?filter[status]=active&limit=200');
  const [caddy, setCaddy] = useState('');
  return (
    <div className="oc-stack">
      <Head title="Caddy History" help="Members accompanied, assignments, ratings and the monthly wage of a caddy." />
      <div style={{ maxWidth: 360 }}>
        <SelectField label="Caddy" value={caddy} onChange={setCaddy} placeholder="Choose a caddy"
          options={(caddies.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.code)} · ${String(c.name)}` }))} />
      </div>
      {caddy && <CaddyDetail id={caddy} />}
      <CaddyAssignmentPage history />
    </div>
  );
}

function CaddyDetail({ id }: { id: string }) {
  const h = useGet<R & { customers: R[]; assignments: R[] }>(`/api/v1/golf/caddies/${id}/history`);
  const x = h.data;
  return (
    <div className="oc-stack">
      <ErrorAlert error={h.error} />
      {x && (
        <div className="oc-row-wrap">
          <Card title="Rounds"><strong style={{ fontSize: 24 }}>{String(x.rounds)}</strong></Card>
          <Card title="Average rating"><strong style={{ fontSize: 24 }}>{x.averageRating ? `★ ${String(x.averageRating)}` : '—'}</strong></Card>
          <Card title="Favourite of"><strong style={{ fontSize: 24 }}>{String(x.favoriteCount)}</strong> members</Card>
          <Card title="Repeat members"><strong style={{ fontSize: 24 }}>{String(x.repeatCustomers)}</strong></Card>
        </div>
      )}
      <Wage id={id} />
      <Card title="Members accompanied" icon="group">
        <DataTable rows={withId(x?.customers, 'customerId')} loading={h.isLoading} columns={[{ key: 'name', header: 'Member / player' },
          { key: 'rounds', header: 'Times together', align: 'right' }, { key: 'lastRound', header: 'Last round', render: (c) => formatDate(String(c.lastRound)) },
          { key: 'favorite', header: 'Favourite', render: (c) => (c.favorite ? '★ favourite' : '') }]} />
      </Card>
      <Card title="Assignment history" icon="history">
        <DataTable rows={(x?.assignments ?? []).slice(0, 50)} loading={h.isLoading} columns={[{ key: 'playDate', header: 'Date', render: (a) => formatDate(String(a.playDate)) },
          { key: 'teeTime', header: 'Tee Time' }, { key: 'bookingCode', header: 'Booking' },
          { key: 'playerNames', header: 'Player', render: (a) => ((a.playerNames as string[]) ?? []).join(', ') },
          { key: 'feeAmount', header: 'Caddy fee', align: 'right', render: (a) => money(a.feeAmount) },
          { key: 'status', header: 'Status', render: (a) => <StatusPill status={String(a.status).replace(/_/g, '-')} /> }]} />
      </Card>
    </div>
  );
}

/** The wage of a month: base salary + caddy fee per assignment (frequency) + tips − deductions. */
function Wage({ id }: { id: string }) {
  const { can } = useAuth();
  const toast = useToast();
  const [month, setMonth] = useState(thisMonth());
  const w = useGet<R & { feeBands: R[] }>(`/api/v1/golf/caddies/${id}/wage${qs({ month })}`);
  const profile = useGet<R>(`/api/v1/golf/caddies/${id}/profile`);
  const save = useSend<Record<string, unknown>>('PUT', `/api/v1/golf/caddies/${id}/profile`, ['/api/v1/golf/caddies']);
  const [base, setBase] = useState('');
  const x = w.data;
  return (
    <Card title="Wage" icon="payments">
      <div className="oc-row-wrap" style={{ alignItems: 'flex-end' }}>
        <TextField label="Month" type="month" value={month} onChange={setMonth} />
        {can('golf.caddy.update') && (
          <>
            <TextField label={`Base salary / month (now ${money(profile.data?.baseSalary ?? 0)})`} value={base} onChange={(v) => setBase(v.replace(/\D/g, ''))} inputMode="numeric" />
            <Btn label="Save base salary" disabled={!base || save.isPending}
              onClick={() => save.mutate({ baseSalary: base }, { onSuccess: () => { toast('Base salary saved'); setBase(''); void w.refetch(); void profile.refetch(); } })} />
          </>
        )}
      </div>
      <ErrorAlert error={w.error ?? save.error} />
      {x && (
        <div className="oc-stack" style={{ marginTop: 12 }}>
          <DataTable rows={[
            { id: 'base', label: 'Base salary (gaji pokok)', amount: x.baseSalary },
            ...((x.feeBands ?? []).map((b, i) => ({ id: `fee${i}`, label: `Caddy fee ${money(b.amount)} × ${String(b.assignments)} assignment${Number(b.assignments) === 1 ? '' : 's'}`, amount: b.total }))),
            { id: 'tips', label: 'Non-cash tips', amount: x.tipsNonCash },
            { id: 'ded', label: 'Deductions (Caddy Policies)', amount: `-${String(x.deductions)}` },
            { id: 'total', label: 'Total', amount: x.total },
          ] as R[]} columns={[{ key: 'label', header: 'Component' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => (r.id === 'total' ? <strong>{money(r.amount)}</strong> : money(r.amount)) }]} />
          <span className="oc-small oc-muted">
            {String(x.assignments)} assignments · {String(x.members)} members · cash tips received directly {money(x.tipsCash)} · already in settlements {money(x.settled)}
            {x.averageRating ? ` · rating this month ★ ${String(x.averageRating)}` : ''}
          </span>
        </div>
      )}
    </Card>
  );
}
