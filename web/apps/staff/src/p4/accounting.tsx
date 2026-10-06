import React, { useState } from 'react';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router';
import { qs, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDateTime } from '@oneclub/i18n';
import {
  AutoResourcePage, Card, Checkbox, DataTable, Drawer, Empty, ErrorAlert, Modal, PageHeader, SelectField, Skeleton, StatTile, StatusPill, TextArea, TextField, useAuth,
  useToast,
} from '@oneclub/shell';
import { ActionButton, KV, ListPage, Tabs, money, today, type R } from '../p1/common';
import type { AreaRoute, OpsRoute, OpsTile } from '../p3/types';
import { BudgetVsActualPage, FinanceDashboardPage, SoonPage } from './finance-dashboard';
import { ACC, API, AccountSelect, BankSelect, DateRange, addDays, dt, items, label, monthStart, pill, rowsOf, SidebarTabs, useTab, type SidebarTab } from './accounting-common';
import { CreditNoteModal, PayablesPage, ReceivablesPage } from './ar-ap';
import { InvoicesPage } from '../p3/billing';
import { BillingWorkspace, PrepareBillingPage } from '../p3/billing-workspace';

// Accounting (PRD P4 EP-16–23, EP-27 FR-POL-P4-03/04, EP-29): General
// Ledger, Accounts Receivable, Accounts Payable, Cash & Bank, Revenue & Tax,
// Financial Periods, Closing (posting exceptions & reconciliations),
// Financial Reports and the Accounting Transition (book, opening balances,
// sign-off). Every figure comes from /api/v1/accounting.

// ── Drill-down to source documents (FR-FIN-04) ──────────────────────────

const DOC_LABEL: Record<string, string> = {
  folio: 'Folio', invoice: 'Invoice', credit_note: 'Credit note', write_off: 'Write-off', payment: 'Payment', refund: 'Refund', deposit: 'Deposit',
  payout: 'Payout', cashier_shift: 'Cashier shift', purchase_order: 'PO', goods_receipt: 'GR', purchase_return: 'Purchase return',
  vendor_invoice: 'Vendor invoice', stock_movement: 'Stock movement', journal: 'Journal', manual_journal: 'Manual journal',
  recurring_journal: 'Recurring journal', bank_transaction: 'Bank transaction', cash_transaction: 'Cash document', vendor_bill: 'Vendor bill',
  vendor_payment: 'Vendor payment', payment_run: 'Payment run', opening_balance: 'Opening balances',
};

/** Screen (and its permission) showing a document. */
function docRoute(type: string, id: string): [string, string] | null {
  switch (type) {
    case 'folio': return [`/billing/folios?id=${id}`, 'billing.folio.view'];
    case 'invoice': return [`/billing/invoices?id=${id}`, 'billing.invoice.view'];
    case 'payment': return ['/billing/payments', 'billing.payment.view'];
    case 'purchase_order': return [`/procurement/purchase-orders?open=${id}`, 'procurement.purchase_order.view'];
    case 'goods_receipt': return [`/procurement/goods-receipts?open=${id}`, 'procurement.goods_receipt.view'];
    case 'purchase_return': return [`/procurement/purchase-returns?open=${id}`, 'procurement.purchase_return.view'];
    case 'vendor_invoice': return [`/procurement/vendor-invoices?open=${id}`, 'procurement.vendor_invoice.view'];
    case 'stock_movement': return [`/inventory/stock-movement?id=${id}`, 'inventory.stock_movement.view'];
    case 'journal': return [`/accounting/general-ledger?tab=journals&journal=${id}`, 'accounting.journal.view'];
    case 'manual_journal': return ['/accounting/general-ledger?tab=manual', 'accounting.journal.view'];
    case 'recurring_journal': return ['/accounting/general-ledger?tab=recurring', 'accounting.journal.view'];
    case 'vendor_bill': case 'vendor_payment': case 'payment_run': return ['/accounting/payables', 'accounting.payable.view'];
    case 'bank_transaction': case 'cash_transaction': return ['/accounting/cash-bank', 'accounting.bank_transaction.view'];
    case 'opening_balance': return ['/accounting/setup?tab=opening', 'accounting.opening_balance.view'];
  }
  return null;
}

/** "GR GR-2026-0012" linked to its screen when the user may open it. */
function DocRef({ type, id, number }: { type: string; id?: unknown; number?: unknown }) {
  const { can } = useAuth();
  const text = `${DOC_LABEL[type] ?? label(type)} ${String(number ?? '')}`.trim();
  const r = id ? docRoute(type, String(id)) : null;
  return r && can(r[1]) ? <Link to={r[0]} onClick={(e) => e.stopPropagation()}>{text}</Link> : <span>{text}</span>;
}

/** A source document with its related document (GR · PO, credit note · invoice). */
function SourceDoc({ d }: { d: R }) {
  if (d.documentType === 'other') return <span className="oc-muted">{label(d.sourceType)}</span>;
  return (
    <span>
      <DocRef type={String(d.documentType)} id={d.documentId} number={d.number} />
      {d.relatedType != null && <span className="oc-muted"> · <DocRef type={String(d.relatedType)} id={d.relatedId} number={d.relatedNumber} /></span>}
    </span>
  );
}

/** Opens the General Ledger of an account for a period (drill-down from TB and statements). */
function useLedgerLink() {
  const navigate = useNavigate();
  return (accountId: unknown, from: string, to: string, consolidated: boolean) => navigate(`/accounting/general-ledger${qs({ tab: 'ledger',
    accountId: String(accountId), from, to, consolidated: consolidated ? '1' : '' })}`);
}

// ── General Ledger (EP-16) ────────────────────────────────────────────────

function JournalDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const j = useGet<R>(`${API}/journals/${id}`);
  const docs = useGet<Page<R>>(`${API}/journals/${id}/documents`);
  const x = j.data;
  const amountOf = new Map(items(x?.sourceReferences).map((s) => [`${String(s.sourceType)}:${String(s.sourceId)}`, s.amount]));
  return (
    <Drawer open onClose={onClose} title={x ? `Journal ${String(x.number)}` : 'Journal'}>
      {j.isLoading && <Skeleton />}
      <ErrorAlert error={j.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Date', dt(x.journalDate)], ['Type', label(x.journalType)], ['Status', <StatusPill key="s" status={String(x.status)} />],
            ['Source', `${label(x.sourceType)} ${String(x.sourceRef ?? '')}`], ['Description', String(x.description)], ['Total', money(x.total)],
            ['Posted', `${formatDateTime(String(x.postedAt))} · ${String(x.postedByName ?? 'system')}`]]} />
          <DataTable rows={items(x.lines)} columns={[{ key: 'accountCode', header: 'Account', render: (l) => `${String(l.accountCode)} ${String(l.accountName)}` },
            { key: 'description', header: 'Description' }, { key: 'businessLine', header: 'Line', render: (l) => label(l.businessLine) },
            { key: 'costCenter', header: 'Cost center' }, { key: 'partnerName', header: 'Partner' },
            { key: 'debit', header: 'Debit', align: 'right', render: (l) => money(l.debit) }, { key: 'credit', header: 'Credit', align: 'right', render: (l) => money(l.credit) }]} />
          {rowsOf(docs.data).length > 0 && (
            <Card title="Source documents" icon="link">
              <DataTable rows={rowsOf(docs.data).map((d, i) => ({ ...d, id: String(i) }) as R)} columns={[
                { key: 'documentType', header: 'Document', render: (d) => <SourceDoc d={d} /> },
                { key: 'amount', header: 'Amount', align: 'right', render: (d) => {
                  const a = amountOf.get(`${String(d.sourceType)}:${String(d.sourceId)}`);
                  return a === undefined ? '—' : money(a);
                } }]} />
            </Card>
          )}
          {x.status === 'posted' && !x.reversedByJournalId && can('accounting.journal.reverse') &&
            <ActionButton label="Reverse" path={`${API}/journals/${id}:reverse`} invalidate={ACC} reason="required" danger />}
        </div>
      )}
    </Drawer>
  );
}

function JournalsTab() {
  // ?journal=<id> opens a journal (links from source documents and reversals)
  const [params, setParams] = useSearchParams();
  const open = params.get('journal') ?? '';
  const setOpen = (j: string) => setParams(j ? { tab: 'journals', journal: j } : { tab: 'journals' });
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  return (
    <>
      <ListPage title="Journals" help="Posted journals (append-only; corrections are reversals, FR-ACC-03)." path={`${API}/journals`} extraQuery={{ from, to }}
        filters={<DateRange from={from} to={to} setFrom={setFrom} setTo={setTo} />} onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'number', header: 'Journal' }, { key: 'journalDate', header: 'Date', render: (r) => dt(r.journalDate) },
          { key: 'journalType', header: 'Type', render: (r) => label(r.journalType) }, { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) },
          { key: 'description', header: 'Description' }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'status', header: 'Status', render: pill('status') }]} />
      {open && <JournalDrawer id={open} onClose={() => setOpen('')} />}
    </>
  );
}

type JLine = { accountId: string; debit: string; credit: string; description: string; costCenter: string; businessLine: string };
const emptyLine = (): JLine => ({ accountId: '', debit: '', credit: '', description: '', costCenter: '', businessLine: '' });

function ManualJournalModal({ onClose }: { onClose: () => void }) {
  const [date, setDate] = useState(today());
  const [type, setType] = useState('manual');
  const [desc, setDesc] = useState('');
  const [autoReverse, setAutoReverse] = useState(false);
  const [lines, setLines] = useState<JLine[]>([emptyLine(), emptyLine()]);
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/manual-journals`, ACC);
  const set = (i: number, k: keyof JLine, v: string) => setLines(lines.map((l, j) => (j === i ? { ...l, [k]: v } : l)));
  const sum = (k: 'debit' | 'credit') => lines.reduce((s, l) => s + (Number(l[k]) || 0), 0);
  const body = (submit: boolean) => ({
    journalDate: date, journalType: type, description: desc, autoReverse, submit,
    lines: lines.filter((l) => l.accountId).map((l) => ({ accountId: l.accountId, debit: l.debit || undefined, credit: l.credit || undefined,
      description: l.description || undefined, costCenter: l.costCenter || undefined, businessLine: l.businessLine || undefined })),
  });
  return (
    <Modal open onClose={onClose} title="New Manual Journal" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => send.mutate(body(false), { onSuccess: onClose })}>Save draft</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || sum('debit') !== sum('credit') || sum('debit') === 0}
        onClick={() => send.mutate(body(true), { onSuccess: onClose })}>Submit</button></>}>
      <div className="oc-form">
        <TextField label="Date" type="date" value={date} onChange={setDate} required />
        <SelectField label="Type" value={type} onChange={setType} options={[{ value: 'manual', label: 'Manual' }, { value: 'adjustment', label: 'Adjustment (allowed in a soft-closed period)' }]} />
        <TextField label="Description" value={desc} onChange={setDesc} required span />
        <Checkbox label="Reverse automatically on the first day of the next period (accrual)" checked={autoReverse} onChange={setAutoReverse} />
      </div>
      <div className="oc-stack">
        {lines.map((l, i) => (
          <div key={i} className="oc-row-wrap">
            <div style={{ minWidth: 260 }}><AccountSelect label={`Account ${i + 1}`} value={l.accountId} onChange={(v) => set(i, 'accountId', v)} /></div>
            <div style={{ width: 140 }}><TextField label="Debit" value={l.debit} onChange={(v) => set(i, 'debit', v)} inputMode="decimal" /></div>
            <div style={{ width: 140 }}><TextField label="Credit" value={l.credit} onChange={(v) => set(i, 'credit', v)} inputMode="decimal" /></div>
            <div style={{ width: 140 }}><TextField label="Cost center" value={l.costCenter} onChange={(v) => set(i, 'costCenter', v)} /></div>
            <div style={{ minWidth: 180 }}><TextField label="Line description" value={l.description} onChange={(v) => set(i, 'description', v)} /></div>
          </div>
        ))}
        <div className="oc-row">
          <button className="oc-btn oc-btn-sm oc-btn-text" onClick={() => setLines([...lines, emptyLine()])}>Add line</button>
          <span className="oc-spacer" />
          <span className="oc-small">Debit {money(sum('debit'))} · Credit {money(sum('credit'))}</span>
        </div>
        <div className="oc-small oc-muted">Journals of the approval threshold or more (Accounting Configuration) wait for the Finance Manager (FR-ACC-02).</div>
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ManualJournalsTab() {
  const { can } = useAuth();
  const [creating, setCreating] = useState(false);
  return (
    <>
      <ListPage title="Manual Journals" path={`${API}/manual-journals`}
        statuses={['draft', 'pending_approval', 'posted', 'rejected', 'cancelled'].map((s) => ({ value: s, label: label(s) }))}
        actions={can('accounting.journal.create') ? <button className="oc-btn oc-btn-primary" onClick={() => setCreating(true)}>New Manual Journal</button> : undefined}
        columns={[{ key: 'number', header: 'Request' }, { key: 'journalDate', header: 'Date', render: (r) => dt(r.journalDate) },
          { key: 'journalType', header: 'Type', render: (r) => label(r.journalType) }, { key: 'description', header: 'Description' },
          { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) }, { key: 'journalNumber', header: 'Journal' },
          { key: 'createdByName', header: 'Requested by' }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => can('accounting.journal.create') && ['draft', 'rejected', 'pending_approval'].includes(String(r.status)) ? (
          <div className="oc-row">
            {r.status !== 'pending_approval' && <ActionButton label="Submit" kind="primary" path={`${API}/manual-journals/${r.id}:submit`} invalidate={ACC} />}
            <ActionButton label="Cancel" path={`${API}/manual-journals/${r.id}:cancel`} invalidate={ACC} reason="optional" />
          </div>) : null} />
      {creating && <ManualJournalModal onClose={() => setCreating(false)} />}
    </>
  );
}

function TrialBalanceTab() {
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  const [consolidated, setConsolidated] = useState(false);
  const { propertyId } = useAuth();
  const ledger = useLedgerLink();
  const tb = useGet<R>(`${API}/trial-balance${qs({ from, to, propertyId: consolidated ? '' : propertyId })}`);
  const x = tb.data;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><DateRange from={from} to={to} setFrom={setFrom} setTo={setTo} />
        <Checkbox label="Consolidated MAIN + MDR" checked={consolidated} onChange={setConsolidated} /></div>
      <ErrorAlert error={tb.error} />
      {x && <KV items={[['Balanced', x.balanced ? 'Yes' : 'No'], ['Total debit', money(x.totalDebit)], ['Total credit', money(x.totalCredit)]]} />}
      <p className="oc-muted oc-small" style={{ margin: 0 }}>Select an account to open its General Ledger for the period.</p>
      <DataTable rows={items(x?.rows)} loading={tb.isLoading} rowKey={(r) => String(r.accountId)} onRowClick={(r) => ledger(r.accountId, from, to, consolidated)}
        columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Account' },
        { key: 'opening', header: 'Opening', align: 'right', render: (r) => money(r.opening) }, { key: 'debit', header: 'Debit', align: 'right', render: (r) => money(r.debit) },
        { key: 'credit', header: 'Credit', align: 'right', render: (r) => money(r.credit) }, { key: 'closing', header: 'Closing', align: 'right', render: (r) => money(r.closing) }]} />
    </div>
  );
}

function LedgerTab() {
  // ?accountId=&from=&to=&consolidated=1: drill-down from the Trial Balance and the financial statements
  const [params] = useSearchParams();
  const [account, setAccount] = useState(params.get('accountId') ?? '');
  const [from, setFrom] = useState(params.get('from') ?? monthStart());
  const [to, setTo] = useState(params.get('to') ?? today());
  const [consolidated, setConsolidated] = useState(params.get('consolidated') === '1');
  const [open, setOpen] = useState('');
  const { propertyId } = useAuth();
  const filter = qs({ accountId: account, from, to, propertyId: consolidated ? '' : propertyId });
  const gl = useGet<R>(account ? `${API}/general-ledger${filter}` : null);
  const docs = useGet<Page<R>>(account ? `${API}/general-ledger/documents${filter}` : null);
  const docsOf = new Map(rowsOf(docs.data).map((d) => [String(d.lineId), d]));
  const x = gl.data;
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><div style={{ minWidth: 300 }}><AccountSelect label="Account" value={account} onChange={setAccount} /></div>
        <DateRange from={from} to={to} setFrom={setFrom} setTo={setTo} />
        <Checkbox label="Consolidated MAIN + MDR" checked={consolidated} onChange={setConsolidated} /></div>
      <ErrorAlert error={gl.error} />
      {x && <KV items={[['Opening balance', money(x.openingBalance)], ['Debit', money(x.debit)], ['Credit', money(x.credit)], ['Closing balance', money(x.closingBalance)]]} />}
      {account && <DataTable rows={items(x?.lines)} loading={gl.isLoading} onRowClick={(l) => setOpen(String(l.journalId))} rowKey={(l) => String(l.lineId)} columns={[
        { key: 'journalDate', header: 'Date', render: (l) => dt(l.journalDate) }, { key: 'journalNumber', header: 'Journal' },
        { key: 'sourceType', header: 'Source document', render: (l) => {
          const d = docsOf.get(String(l.lineId));
          const list = items(d?.documents);
          if (list.length === 0) return `${label(l.sourceType)} ${String(l.sourceRef ?? '')}`;
          return (
            <span className="oc-stack" style={{ gap: 2 }}>
              {list.map((doc, i) => <SourceDoc key={i} d={doc} />)}
              {Number(d?.moreDocuments ?? 0) > 0 && <span className="oc-muted oc-small">+{String(d?.moreDocuments)} more (open the journal)</span>}
            </span>
          );
        } }, { key: 'description', header: 'Description' },
        { key: 'debit', header: 'Debit', align: 'right', render: (l) => money(l.debit) }, { key: 'credit', header: 'Credit', align: 'right', render: (l) => money(l.credit) },
        { key: 'balance', header: 'Balance', align: 'right', render: (l) => money(l.balance) }]} />}
      {open && <JournalDrawer id={open} onClose={() => setOpen('')} />}
    </div>
  );
}

function RecurringTab() {
  const { can } = useAuth();
  return (
    <div className="oc-stack">
      {can('accounting.journal.create') && <div><ActionButton label="Run due recurring journals" kind="primary" path={`${API}/recurring-journals:run`} body={{ asOf: today() }} invalidate={ACC} /></div>}
      <AutoResourcePage resourceKey="accounting.recurring_journal" />
    </div>
  );
}

export function GeneralLedgerPage() {
  const [tab, setTab] = useTab('journals');
  return (
    <div className="oc-stack">
      <PageHeader title="General Ledger" help="Chart of accounts, journals, manual journals with approval, trial balance and the ledger per account (EP-16)." />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'journals', label: 'Journals' }, { value: 'manual', label: 'Manual Journals' },
        { value: 'tb', label: 'Trial Balance' }, { value: 'ledger', label: 'Account Ledger' }, { value: 'accounts', label: 'Chart of Accounts' },
        { value: 'rules', label: 'Posting Rules' }, { value: 'recurring', label: 'Recurring Journals' }]} />
      {tab === 'journals' && <JournalsTab />}
      {tab === 'manual' && <ManualJournalsTab />}
      {tab === 'tb' && <TrialBalanceTab />}
      {tab === 'ledger' && <LedgerTab />}
      {tab === 'accounts' && <AutoResourcePage resourceKey="accounting.account" />}
      {tab === 'rules' && <PostingRulesTab />}
      {tab === 'recurring' && <RecurringTab />}
    </div>
  );
}

function PostingRulesTab() {
  const { can } = useAuth();
  const toast = useToast();
  const gen = useSend<Record<string, unknown>, R>('POST', `${API}/posting-rules:generate-defaults`, ACC);
  return (
    <div className="oc-stack">
      {can('accounting.posting.manage') && <div><button className="oc-btn oc-btn-neutral" disabled={gen.isPending}
        onClick={() => gen.mutate({}, { onSuccess: (r) => toast(`${String(r.created)} default rules created`) })}>Generate default rules</button></div>}
      <AutoResourcePage resourceKey="accounting.posting_rule" />
    </div>
  );
}

// ── Cash & Bank (EP-20) ───────────────────────────────────────────────────

function ImportStatementModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [bank, setBank] = useState('');
  const [format, setFormat] = useState('');
  const [filename, setFilename] = useState('');
  const [content, setContent] = useState('');
  const [preview, setPreview] = useState<R | null>(null);
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/bank-transactions:import`, ACC);
  const run = (pv: boolean) => send.mutate({ bankAccountId: bank, format: format || undefined, filename: filename || undefined, content, preview: pv }, {
    onSuccess: (r) => { if (pv) setPreview(r); else { toast(`${String(r.imported)} lines imported, ${String(r.duplicates)} duplicates skipped`); onClose(); } },
  });
  const onFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (!f) return;
    setFilename(f.name);
    void f.text().then(setContent);
  };
  return (
    <Modal open onClose={onClose} title="Import Bank Statement" wide actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={!bank || !content || send.isPending} onClick={() => run(true)}>Preview</button>
      <button className="oc-btn oc-btn-primary" disabled={!bank || !content || send.isPending} onClick={() => run(false)}>Import</button></>}>
      <div className="oc-form">
        <BankSelect label="Bank account" value={bank} onChange={setBank} kind="bank" />
        <SelectField label="Format" value={format} onChange={setFormat} placeholder="Format of the account"
          options={[{ value: 'generic_csv', label: 'CSV (generic)' }, { value: 'bca_csv', label: 'BCA CSV' }, { value: 'mandiri_csv', label: 'Mandiri CSV' }, { value: 'mt940', label: 'MT940' }]} />
        <label className="oc-field"><span>Statement file</span><input type="file" accept=".csv,.txt,.sta,.940" onChange={onFile} /></label>
        <TextArea label="Or paste the statement" value={content} onChange={setContent} rows={6} span />
      </div>
      {preview && <Card title={`Preview · ${String(preview.lines)} lines`} icon="preview">
        <DataTable rows={items(preview.preview).map((l, i) => ({ ...l, id: String(i) }) as R)} columns={[{ key: 'date', header: 'Date' }, { key: 'description', header: 'Description' },
          { key: 'reference', header: 'Reference' }, { key: 'amount', header: 'Amount', align: 'right', render: (l) => money(l.amount) }]} />
      </Card>}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function ReconciliationDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const r = useGet<R>(`${API}/bank-reconciliations/${id}`);
  const x = r.data;
  const inv = [...ACC, `${API}/bank-reconciliations/${id}`];
  const [selected, setSelected] = useState<string[]>([]);
  const [resolveFor, setResolveFor] = useState('');
  const [account, setAccount] = useState('');
  const edit = can('accounting.bank_transaction.reconcile') && x?.status === 'in_progress';
  return (
    <Drawer open onClose={onClose} title={x ? `Bank Reconciliation ${String(x.number)}` : 'Bank Reconciliation'}>
      {r.isLoading && <Skeleton />}
      <ErrorAlert error={r.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Account', String(x.bankAccountName)], ['Statement date', dt(x.statementDate)], ['Statement balance', money(x.statementBalance)],
            ['Book balance', money(x.bookBalance)], ['Outstanding book lines', money(x.outstandingBook)], ['Unmatched statement lines', money(x.unmatchedBank)],
            ['Difference', <strong key="d">{money(x.difference)}</strong>], ['Status', <StatusPill key="s" status={String(x.status)} />]]} />
          {edit && <div className="oc-row-wrap">
            <ActionButton label="Auto-match" kind="primary" path={`${API}/bank-reconciliations/${id}:auto-match`} invalidate={inv} />
            <ActionButton label="Complete" path={`${API}/bank-reconciliations/${id}:complete`} invalidate={inv} disabled={x.difference !== '0'} />
          </div>}
          <Card title="Exceptions: statement lines without a book entry" icon="error">
            <DataTable rows={items(x.unmatchedTransactions)} columns={[{ key: 'txDate', header: 'Date', render: (t) => dt(t.txDate) }, { key: 'description', header: 'Description' },
              { key: 'amount', header: 'Amount', align: 'right', render: (t) => money(t.amount) }]}
              actions={(t) => edit ? <div className="oc-row">
                {selected.length > 0 && <ActionButton label="Match selected" path={`${API}/bank-reconciliations/${id}:match`}
                  body={{ bankTransactionId: t.id, journalLineIds: selected }} invalidate={inv} onDone={() => setSelected([])} />}
                <button className="oc-btn oc-btn-sm oc-btn-neutral" onClick={() => setResolveFor(t.id)}>Journal</button>
                <ActionButton label="Ignore" path={`${API}/bank-reconciliations/${id}:ignore`} body={{ bankTransactionId: t.id }} invalidate={inv} reason="required" />
              </div> : null} />
          </Card>
          <Card title="Outstanding book lines" icon="pending">
            <DataTable rows={items(x.outstandingLines).map((l) => ({ ...l, id: String(l.lineId) }) as R)} columns={[
              { key: 'pick', header: '', render: (l) => edit ? <input type="checkbox" checked={selected.includes(l.id)}
                onChange={(e) => setSelected(e.target.checked ? [...selected, l.id] : selected.filter((s) => s !== l.id))} /> : null },
              { key: 'journalDate', header: 'Date', render: (l) => dt(l.journalDate) }, { key: 'journalNumber', header: 'Journal' },
              { key: 'description', header: 'Description' }, { key: 'amount', header: 'Amount', align: 'right', render: (l) => money(l.amount) }]} />
          </Card>
          <Card title="Matched" icon="done_all">
            <DataTable rows={items(x.matchedTransactions)} columns={[{ key: 'txDate', header: 'Date', render: (t) => dt(t.txDate) }, { key: 'description', header: 'Description' },
              { key: 'amount', header: 'Amount', align: 'right', render: (t) => money(t.amount) },
              { key: 'matchedJournals', header: 'Journals', render: (t) => (Array.isArray(t.matchedJournals) ? t.matchedJournals.join(', ') : '') }]}
              actions={(t) => edit ? <ActionButton label="Unmatch" path={`${API}/bank-reconciliations/${id}:unmatch`} body={{ bankTransactionId: t.id }} invalidate={inv} /> : null} />
          </Card>
          {resolveFor && (
            <Modal open onClose={() => setResolveFor('')} title="Settle with a journal" actions={<>
              <button className="oc-btn oc-btn-neutral" onClick={() => setResolveFor('')}>Cancel</button>
              <ActionButton label="Post" kind="primary" path={`${API}/bank-reconciliations/${id}:resolve`} body={{ bankTransactionId: resolveFor, accountId: account }}
                invalidate={inv} disabled={!account} onDone={() => setResolveFor('')} /></>}>
              <AccountSelect label="Counter account (bank charges, interest …)" value={account} onChange={setAccount} required />
            </Modal>
          )}
        </div>
      )}
    </Drawer>
  );
}

function NewReconciliation({ onClose, onDone }: { onClose: () => void; onDone: (id: string) => void }) {
  const [bank, setBank] = useState('');
  const [date, setDate] = useState(today());
  const [balance, setBalance] = useState('');
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/bank-reconciliations`, ACC);
  return (
    <Modal open onClose={onClose} title="Start Bank Reconciliation" actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={!bank || !balance || send.isPending}
        onClick={() => send.mutate({ bankAccountId: bank, statementDate: date, statementBalance: balance }, { onSuccess: (r) => onDone(r.id) })}>Start</button></>}>
      <div className="oc-form">
        <BankSelect label="Bank account" value={bank} onChange={setBank} kind="bank" />
        <TextField label="Statement date" type="date" value={date} onChange={setDate} />
        <TextField label="Statement closing balance" value={balance} onChange={setBalance} required />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function CashDocumentModal({ onClose, shift }: { onClose: () => void; shift?: R }) {
  const [kind, setKind] = useState(shift ? 'deposit' : 'expense');
  const [f, setF] = useState({ txDate: today(), fromAccountId: '', toAccountId: '', amount: shift ? String(shift.toDeposit) : '', fee: '', expenseAccountId: '',
    costCenter: '', reference: '', description: '' });
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/cash-transactions`, ACC);
  const upd = (k: keyof typeof f) => (v: string) => setF({ ...f, [k]: v });
  const body: Record<string, unknown> = { kind, ...Object.fromEntries(Object.entries(f).filter(([, v]) => v !== '')) };
  if (shift) body.shiftRefs = [shift.number];
  return (
    <Modal open onClose={onClose} title={shift ? `Deposit shift ${String(shift.number)}` : 'Cash Document'} actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending} onClick={() => send.mutate(body, { onSuccess: onClose })}>Post</button></>}>
      <div className="oc-form">
        <SelectField label="Kind" value={kind} onChange={setKind} options={['deposit', 'expense', 'replenishment', 'transfer', 'count'].map((k) => ({ value: k, label: label(k) }))} />
        <TextField label="Date" type="date" value={f.txDate} onChange={upd('txDate')} />
        <BankSelect label={kind === 'replenishment' ? 'From bank' : 'Cash account'} value={f.fromAccountId} onChange={upd('fromAccountId')} />
        {['deposit', 'replenishment', 'transfer'].includes(kind) && <BankSelect label="To account" value={f.toAccountId} onChange={upd('toAccountId')} />}
        <TextField label={kind === 'count' ? 'Counted cash' : 'Amount'} value={f.amount} onChange={upd('amount')} required />
        {['deposit', 'transfer'].includes(kind) && <TextField label="Bank fee" value={f.fee} onChange={upd('fee')} />}
        {kind === 'expense' && <AccountSelect label="Expense account" value={f.expenseAccountId} onChange={upd('expenseAccountId')} required />}
        {kind === 'expense' && <TextField label="Cost center" value={f.costCenter} onChange={upd('costCenter')} />}
        <TextField label="Reference" value={f.reference} onChange={upd('reference')} />
        <TextField label="Description" value={f.description} onChange={upd('description')} span />
      </div>
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

export function CashBankPage() {
  const { can } = useAuth();
  const [tab, setTab] = useTab('reconciliations');
  const [modal, setModal] = useState<'' | 'import' | 'rec' | 'cash'>('');
  const [open, setOpen] = useState('');
  const [deposit, setDeposit] = useState<R | null>(null);
  const shifts = useGet<Page<R>>(tab === 'shifts' ? `${API}/cash-shifts` : null);
  return (
    <div className="oc-stack">
      <PageHeader title="Cash & Bank" help="Cash and bank accounts, statements (CSV / MT940), reconciliation with auto-match, shift deposits and petty cash (EP-20)."
        actions={<>{can('accounting.bank_transaction.import') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('import')}>Import Statement</button>}
          {can('accounting.cash.manage') && <button className="oc-btn oc-btn-neutral" onClick={() => setModal('cash')}>Cash Document</button>}
          {can('accounting.bank_transaction.reconcile') && <button className="oc-btn oc-btn-primary" onClick={() => setModal('rec')}>Reconcile</button>}</>} />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'reconciliations', label: 'Reconciliations' }, { value: 'transactions', label: 'Bank Transactions' },
        { value: 'cash', label: 'Cash Documents' }, { value: 'shifts', label: 'Shift Deposits' }, { value: 'accounts', label: 'Cash & Bank Accounts' }]} />
      {tab === 'reconciliations' && <ListPage title="Bank Reconciliations" path={`${API}/bank-reconciliations`} search={false} onRowClick={(r) => setOpen(r.id)}
        columns={[{ key: 'number', header: 'Reconciliation' }, { key: 'bankAccountName', header: 'Account' }, { key: 'statementDate', header: 'Statement date', render: (r) => dt(r.statementDate) },
          { key: 'statementBalance', header: 'Statement', align: 'right', render: (r) => money(r.statementBalance) },
          { key: 'difference', header: 'Difference', align: 'right', render: (r) => money(r.difference) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'transactions' && <ListPage title="Bank Transactions" path={`${API}/bank-transactions`} search={false}
        statuses={['unmatched', 'matched', 'ignored'].map((s) => ({ value: s, label: label(s) }))}
        columns={[{ key: 'txDate', header: 'Date', render: (r) => dt(r.txDate) }, { key: 'bankAccountName', header: 'Account' }, { key: 'description', header: 'Description' },
          { key: 'reference', header: 'Reference' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'cash' && <ListPage title="Cash Documents" path={`${API}/cash-transactions`} search={false} columns={[{ key: 'number', header: 'Document' },
        { key: 'kind', header: 'Kind', render: (r) => label(r.kind) }, { key: 'txDate', header: 'Date', render: (r) => dt(r.txDate) },
        { key: 'fromAccountName', header: 'From' }, { key: 'toAccountName', header: 'To' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
        { key: 'variance', header: 'Variance', align: 'right', render: (r) => money(r.variance) }, { key: 'journalNumber', header: 'Journal' }]} />}
      {tab === 'shifts' && <DataTable rows={rowsOf(shifts.data)} loading={shifts.isLoading} columns={[{ key: 'number', header: 'Shift' }, { key: 'source', header: 'Kind', render: (r) => label(r.source) },
        { key: 'station', header: 'Station / outlet' }, { key: 'businessDate', header: 'Business date', render: (r) => dt(r.businessDate) },
        { key: 'countedCash', header: 'Counted', align: 'right', render: (r) => money(r.countedCash) }, { key: 'variance', header: 'Variance', align: 'right', render: (r) => money(r.variance) },
        { key: 'toDeposit', header: 'To deposit', align: 'right', render: (r) => money(r.toDeposit) },
        { key: 'deposited', header: 'Deposited', render: (r) => <StatusPill status={r.deposited ? 'completed' : 'pending'} label={r.deposited ? 'Deposited' : 'Not yet'} /> }]}
        actions={(r) => !r.deposited && can('accounting.cash.manage') ? <button className="oc-btn oc-btn-sm oc-btn-primary" onClick={() => setDeposit(r)}>Deposit</button> : null} />}
      {tab === 'accounts' && <AutoResourcePage resourceKey="accounting.bank_account" />}
      {modal === 'import' && <ImportStatementModal onClose={() => setModal('')} />}
      {modal === 'rec' && <NewReconciliation onClose={() => setModal('')} onDone={(id) => { setModal(''); setOpen(id); }} />}
      {modal === 'cash' && <CashDocumentModal onClose={() => setModal('')} />}
      {deposit && <CashDocumentModal shift={deposit} onClose={() => setDeposit(null)} />}
      {open && <ReconciliationDrawer id={open} onClose={() => setOpen('')} />}
    </div>
  );
}

// ── Revenue & Tax (EP-21) ─────────────────────────────────────────────────

function TaxInvoicesTab() {
  const { can } = useAuth();
  const toast = useToast();
  const [period, setPeriod] = useState(today().slice(0, 7));
  const exp = useSend<Record<string, unknown>, R>('POST', `${API}/tax-invoices:export`, ACC);
  const download = (r: R) => {
    const blob = new Blob([String(r.content ?? '')], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `efaktur-${period}.csv`;
    a.click();
    toast(`${String(r.invoices)} tax invoices exported`);
  };
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><div style={{ width: 170 }}><TextField label="Tax period" type="month" value={period} onChange={setPeriod} /></div>
        {can('accounting.tax_invoice.manage') && <button className="oc-btn oc-btn-neutral" disabled={exp.isPending}
          onClick={() => exp.mutate({ taxPeriod: period }, { onSuccess: download })}>Export e-Faktur CSV</button>}</div>
      <ErrorAlert error={exp.error} />
      <ListPage title="Tax Invoices" help="Output tax invoices of taxed invoices to customers with NPWP, input tax invoices of vendor invoices (FR-REV-07)."
        path={`${API}/tax-invoices`} statuses={['draft', 'uploaded', 'failed', 'cancelled'].map((s) => ({ value: s, label: label(s) }))}
        columns={[{ key: 'direction', header: 'Direction', render: (r) => label(r.direction) }, { key: 'sourceNumber', header: 'Document' },
          { key: 'partnerName', header: 'Partner' }, { key: 'partnerNpwp', header: 'NPWP' }, { key: 'invoiceDate', header: 'Date', render: (r) => dt(r.invoiceDate) },
          { key: 'dpp', header: 'DPP', align: 'right', render: (r) => money(r.dpp) }, { key: 'ppn', header: 'PPN', align: 'right', render: (r) => money(r.ppn) },
          { key: 'fakturNumber', header: 'Faktur no.' }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => can('accounting.tax_invoice.manage') && r.direction === 'output' ? <div className="oc-row">
          {['draft', 'failed'].includes(String(r.status)) && <ActionButton label="Upload" kind="primary" path={`${API}/tax-invoices/${r.id}:upload`} invalidate={ACC} />}
          {r.status !== 'cancelled' && <ActionButton label="Replace" path={`${API}/tax-invoices/${r.id}:cancel`} body={{ replace: true }} invalidate={ACC} reason="required" />}
          {r.status !== 'cancelled' && <ActionButton label="Cancel" path={`${API}/tax-invoices/${r.id}:cancel`} invalidate={ACC} reason="required" danger />}
        </div> : null} />
    </div>
  );
}

function PPNTab() {
  const [period, setPeriod] = useState(today().slice(0, 7));
  const r = useGet<R>(`${API}/ppn-report?period=${period}`);
  const x = r.data;
  return (
    <div className="oc-stack">
      <div style={{ width: 170 }}><TextField label="Tax period" type="month" value={period} onChange={setPeriod} /></div>
      <ErrorAlert error={r.error} />
      {x && <KV items={[['Output DPP', money(x.outputDpp)], ['Output PPN', money(x.outputPpn)], ['Output invoices', String(x.outputInvoices)],
        ['Uploaded', String(x.uploaded)], ['Input DPP', money(x.inputDpp)], ['Input PPN', money(x.inputPpn)], ['Net payable', <strong key="n">{money(x.netPayable)}</strong>],
        ['GL output PPN', money(x.glOutputPpn)], ['GL input PPN', money(x.glInputPpn)]]} />}
    </div>
  );
}

function DeferredTab() {
  const [to, setTo] = useState(today());
  const r = useGet<R>(`${API}/deferred-revenue?to=${to}`);
  return (
    <div className="oc-stack">
      <div style={{ width: 170 }}><TextField label="As of" type="date" value={to} onChange={setTo} /></div>
      <ErrorAlert error={r.error} />
      <DataTable rows={items(r.data?.rows).map((x) => ({ ...x, id: String(x.type) }) as R)} loading={r.isLoading} columns={[{ key: 'label', header: 'Liability' },
        { key: 'accountCode', header: 'Account' }, { key: 'deferred', header: 'Deferred', align: 'right', render: (x) => money(x.deferred) },
        { key: 'recognised', header: 'Recognised', align: 'right', render: (x) => money(x.recognised) }, { key: 'breakage', header: 'Breakage', align: 'right', render: (x) => money(x.breakage) },
        { key: 'glBalance', header: 'GL balance', align: 'right', render: (x) => money(x.glBalance) }, { key: 'subledger', header: 'Sub-ledger', align: 'right', render: (x) => money(x.subledger) },
        { key: 'difference', header: 'Difference', align: 'right', render: (x) => money(x.difference) }]} />
    </div>
  );
}

function ServiceChargeTab() {
  const { can } = useAuth();
  const [month, setMonth] = useState(today().slice(0, 7));
  return (
    <div className="oc-stack">
      {can('accounting.revenue.manage') && <div className="oc-row-wrap"><div style={{ width: 170 }}><TextField label="Month" type="month" value={month} onChange={setMonth} /></div>
        <ActionButton label="Compute pool" kind="primary" path={`${API}/service-charge-pools`} body={{ year: Number(month.slice(0, 4)), month: Number(month.slice(5, 7)) }} invalidate={ACC} /></div>}
      <ListPage title="Service Charge Pools" help="Service charge collected per month, reserve and distribution basis per department (FR-REV-06)." path={`${API}/service-charge-pools`}
        search={false} columns={[{ key: 'year', header: 'Month', render: (r) => `${String(r.year)}-${String(r.month).padStart(2, '0')}` },
          { key: 'collected', header: 'Collected', align: 'right', render: (r) => money(r.collected) }, { key: 'reserve', header: 'Reserve', align: 'right', render: (r) => money(r.reserve) },
          { key: 'distributable', header: 'Distributable', align: 'right', render: (r) => money(r.distributable) }, { key: 'status', header: 'Status', render: pill('status') }]}
        rowActions={(r) => r.status === 'draft' && can('accounting.revenue.manage') ? <ActionButton label="Approve" path={`${API}/service-charge-pools/${r.id}:approve`} invalidate={ACC} /> : null} />
    </div>
  );
}

// ── Revenue & Billing, Revenue Recognition, Tax ───────────────────────────
// Revenue & Billing answers "what must be billed and what was billed" (billing
// workspace, invoices, credit notes, adjustments, revenue reconciliation);
// Revenue Recognition "how revenue is recognised and allocated"; Tax "what
// is owed and how it is reported". Accounts Receivable takes over once an
// invoice is issued.

/** Journals of a business day against the Daily Revenue Report (FR-PST-07). */
function RevenueReconciliationTab() {
  // Yesterday: the latest business day the night audit has closed and posted.
  const [date, setDate] = useState(() => addDays(-1));
  const pr = useGet<R>(`${API}/posting-reconciliation?date=${date}`);
  return (
    <div className="oc-stack">
      <div className="oc-row-wrap"><input className="oc-input oc-filter" type="date" aria-label="Business date" value={date} onChange={(e) => e.target.value && setDate(e.target.value)} /></div>
      <ErrorAlert error={pr.error} />
      {pr.data && (
        <div className="oc-stat-grid">
          <StatTile label="Daily Revenue Report" icon="summarize" value={money(pr.data.dailyRevenue)} />
          <StatTile label="Posted to the ledger" icon="menu_book" value={money(pr.data.journal)} />
          <StatTile label="Result" icon="balance" value={pr.data.ok ? 'Reconciled' : 'Difference'}
            status={<StatusPill status={pr.data.ok ? 'approved' : 'rejected'} label={pr.data.dayClosed ? 'Day closed' : 'Day open'} />} />
        </div>
      )}
      <DataTable rows={items(pr.data?.rows).map((r, i) => ({ ...r, id: String(i) }) as R)} loading={pr.isLoading} columns={[
        { key: 'businessLine', header: 'Business line', render: (r) => label(r.businessLine) }, { key: 'revenueComponent', header: 'Component', render: (r) => label(r.revenueComponent) },
        { key: 'dailyRevenue', header: 'Daily Revenue', align: 'right', render: (r) => money(r.dailyRevenue) }, { key: 'journal', header: 'Journals', align: 'right', render: (r) => money(r.journal) },
        { key: 'difference', header: 'Difference', align: 'right', render: (r) => (Number(r.difference) === 0 ? <span className="oc-muted">0</span> : <strong style={{ color: 'var(--md-sys-color-error)' }}>{money(r.difference)}</strong>) }]} />
    </div>
  );
}

// Revenue & Billing: each part is a sidebar item (no tab bar).
const REV_TABS: SidebarTab[] = [
  { value: 'billing', label: 'Billing', help: 'Charges from golf, resort, events and F&B to bill: prepare, validate, resolve exceptions and generate invoices. Invoiced records continue in Invoices.' },
  { value: 'invoices', label: 'Invoices', own: true }, { value: 'notes', label: 'Credit / Debit Notes', own: true },
  { value: 'adjustments', label: 'Revenue Adjustments', help: 'Refunds of cancelled bookings and services, and revenue moved between accounts by an adjustment journal.' },
  { value: 'reconciliation', label: 'Revenue Reconciliation', help: 'The journals of a business day against its Daily Revenue Report.' },
];

/** Revenue & Billing: billing workspace, invoices, credit notes, adjustments and revenue reconciliation. */
export function RevenueBillingPage() {
  const { can } = useAuth();
  const [tab] = useTab('billing');
  const [credit, setCredit] = useState(false);
  return (
    <div className="oc-stack">
      <SidebarTabs tabs={REV_TABS} tab={tab} />
      {tab === 'billing' && <BillingWorkspace />}
      {tab === 'invoices' && <InvoicesPage />}
      {tab === 'notes' && <ListPage title="Credit / Debit Notes" help="Corrections that reduce an invoice: overbilling, cancelled service, billing adjustment. Customer debit notes are issued as a new invoice."
        actions={can('billing.invoice.credit') && <button className="oc-btn oc-btn-primary" onClick={() => setCredit(true)}>New Credit Note</button>}
        path="/api/v1/billing/credit-notes" search={false}
        columns={[{ key: 'number', header: 'Credit note' }, { key: 'createdAt', header: 'Date', render: (r) => formatDateTime(String(r.createdAt)) },
          { key: 'reason', header: 'Reason' }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
          { key: 'invoiceId', header: '', render: (r) => <Link className="oc-btn oc-btn-sm oc-btn-text" to={`/accounting/revenue?tab=invoices&id=${String(r.invoiceId)}`}>Invoice</Link> }]} />}
      {tab === 'adjustments' && (
        <>
          <ListPage title="Refunds" help="Money returned for cancelled bookings and services; the revenue is reversed by the refund."
            path="/api/v1/billing/refunds" search={false} statuses={['pending', 'approved', 'completed', 'rejected'].map((s) => ({ value: s, label: label(s) }))}
            columns={[{ key: 'number', header: 'Refund' }, { key: 'createdAt', header: 'Date', render: (r) => formatDateTime(String(r.createdAt)) },
              { key: 'reason', header: 'Reason', render: (r) => String(r.reason ?? '—') }, { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) },
              { key: 'status', header: 'Status', render: pill('status') }]} />
          <Card title="Reclassify revenue" icon="swap_horiz">
            <p className="oc-small oc-muted" style={{ marginTop: 0 }}>Wrong revenue account, pricing or discount correction, service charge correction: post an adjustment journal (approval) that moves the amount between revenue accounts.</p>
            <div className="oc-row-wrap">
              <Link className="oc-btn oc-btn-sm oc-btn-outline" to="/accounting/general-ledger?tab=manual">New adjustment journal</Link>
              <Link className="oc-btn oc-btn-sm oc-btn-text" to="/accounting/revenue-recognition?tab=allocations">Revenue allocations</Link>
            </div>
          </Card>
        </>
      )}
      {tab === 'reconciliation' && <RevenueReconciliationTab />}
      {credit && <CreditNoteModal onClose={() => setCredit(false)} />}
    </div>
  );
}

/** Revenue Recognition: allocation, deferred revenue, recognition schedule and service charge. */
export function RevenueRecognitionPage() {
  const [tab] = useTab('allocations');
  return (
    <div className="oc-stack">
      <SidebarTabs tab={tab} tabs={[{ value: 'allocations', label: 'Revenue Allocation', own: true },
        { value: 'deferred', label: 'Deferred Revenue', help: 'What was deferred (membership, prepaid, vouchers) and recognised per liability.' },
        { value: 'schedule', label: 'Recognition Schedule', help: 'When deferred revenue is recognised.' },
        { value: 'service-charge', label: 'Service Charge', help: 'The service charge pool per month, its reserve and distribution.' }]} />
      {tab === 'allocations' && <ListPage title="Revenue Allocation" help="Packages and promotions split over their revenue components by the allocation rules (Settings)."
        path={`${API}/revenue-allocations`} search={false} columns={[
          { key: 'sourceType', header: 'Source', render: (r) => label(r.sourceType) }, { key: 'reference', header: 'Reference' },
          { key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) }, { key: 'total', header: 'Total', align: 'right', render: (r) => money(r.total) },
          { key: 'discount', header: 'Discount', align: 'right', render: (r) => money(r.discount) }, { key: 'createdAt', header: 'Received', render: (r) => formatDateTime(String(r.createdAt)) }]} />}
      {tab === 'deferred' && <DeferredTab />}
      {tab === 'schedule' && <Card><Empty title="Recognition schedule per contract is not available yet" icon="event_upcoming"
        help="Deferred revenue is recognised by the posting rules (membership, prepaid, vouchers); Deferred Revenue shows what was deferred and recognised per liability." /></Card>}
      {tab === 'service-charge' && <ServiceChargeTab />}
    </div>
  );
}

/** Tax: tax transactions (e-Faktur), tax reports (PPN) and the tax configuration. */
export function TaxPage() {
  const [tab] = useTab('transactions');
  return (
    <div className="oc-stack">
      <SidebarTabs tab={tab} tabs={[{ value: 'transactions', label: 'Tax Transactions', help: 'Output and input tax invoices (e-Faktur / Coretax).' },
        { value: 'reports', label: 'Tax Reports', help: 'The PPN report per tax period.' },
        { value: 'configuration', label: 'Tax Configuration', help: 'The tax codes used by sales, purchases and POS.' }]} />
      {tab === 'transactions' && <TaxInvoicesTab />}
      {tab === 'reports' && <PPNTab />}
      {tab === 'configuration' && <AutoResourcePage resourceKey="accounting.tax_code" />}
    </div>
  );
}

const REVENUE_TAX_LEGACY: Record<string, string> = {
  'tax-invoices': '/accounting/tax?tab=transactions', ppn: '/accounting/tax?tab=reports', 'tax-codes': '/accounting/tax?tab=configuration',
  deferred: '/accounting/revenue-recognition?tab=deferred', allocations: '/accounting/revenue-recognition?tab=allocations',
  'service-charge': '/accounting/revenue-recognition?tab=service-charge', 'allocation-rules': '/accounting/setup?tab=allocation-rules',
};

/** The former Revenue & Tax page: its tabs now live in Revenue Recognition, Tax and Settings. */
export function RevenueTaxPage() {
  const [tab] = useTab('allocations');
  return <Navigate to={REVENUE_TAX_LEGACY[tab] ?? '/accounting/revenue-recognition'} replace />;
}

// ── Financial Periods (FR-ACC-06/07) ──────────────────────────────────────

function PeriodDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const p = useGet<R>(`${API}/periods/${id}`);
  const x = p.data;
  const inv = [...ACC, `${API}/periods/${id}`];
  return (
    <Drawer open onClose={onClose} title={x ? `Period ${String(x.year)}-${String(x.month).padStart(2, '0')}` : 'Period'}>
      {p.isLoading && <Skeleton />}
      <ErrorAlert error={p.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Status', <StatusPill key="s" status={String(x.status)} />], ['From', dt(x.startDate)], ['To', dt(x.endDate)], ['Journals', String(x.journals)],
            ['Closed by', String(x.closedByName ?? '—')], ['Reopen', x.reopenStatus ? `${label(x.reopenStatus)} · ${String(x.reopenReason ?? '')}` : '—']]} />
          <Card title="Closing checklist" icon="checklist">
            <DataTable rows={items(x.checklist).map((c) => ({ ...c, id: String(c.code) }) as R)} columns={[{ key: 'label', header: 'Check' },
              { key: 'ok', header: 'Result', render: (c) => <StatusPill status={c.ok ? 'completed' : c.blocking ? 'failed' : 'pending'} label={c.ok ? 'OK' : c.blocking ? 'Blocking' : 'Warning'} /> },
              { key: 'detail', header: 'Detail' }]} />
          </Card>
          <div className="oc-row-wrap">
            {x.status === 'open' && can('accounting.period.close') && <ActionButton label="Soft close" path={`${API}/periods/${id}:soft-close`} invalidate={inv} />}
            {x.status !== 'closed' && can('accounting.period.close') && <ActionButton label="Close" kind="primary" path={`${API}/periods/${id}:close`} invalidate={inv}
              disabled={!x.canClose} confirm="No journal of any source can be dated in a closed period. Close it?" />}
            {x.status !== 'open' && can('accounting.period.reopen') && <ActionButton label="Reopen" path={`${API}/periods/${id}:reopen`} invalidate={inv} reason="required" danger />}
          </div>
        </div>
      )}
    </Drawer>
  );
}

export function PeriodsPage() {
  const { can } = useAuth();
  const [year, setYear] = useState(today().slice(0, 4));
  const [open, setOpen] = useState('');
  const periods = useGet<Page<R>>(`${API}/periods?year=${year}`);
  const years = useGet<Page<R>>(`${API}/fiscal-years`);
  return (
    <div className="oc-stack">
      <PageHeader title="Financial Periods" help="Open → Soft Closed (finance adjustments only) → Closed, with the closing checklist; year-end closing to retained earnings (FR-ACC-06/07)."
        actions={can('accounting.period.close') ? <>
          <ActionButton label={`Create periods ${year}`} path={`${API}/periods:generate`} body={{ year: Number(year) }} invalidate={ACC} />
          <ActionButton label={`Close fiscal year ${year}`} path={`${API}/fiscal-years:close`} body={{ year: Number(year) }} invalidate={ACC}
            confirm="Post the closing journal of the year to retained earnings and close every period of the year?" /></> : undefined} />
      <div style={{ width: 120 }}><TextField label="Year" type="number" value={year} onChange={setYear} /></div>
      <DataTable rows={rowsOf(periods.data)} loading={periods.isLoading} error={periods.error} onRowClick={(p) => setOpen(p.id)} columns={[
        { key: 'month', header: 'Period', render: (p) => `${String(p.year)}-${String(p.month).padStart(2, '0')}` }, { key: 'startDate', header: 'From', render: (p) => dt(p.startDate) },
        { key: 'endDate', header: 'To', render: (p) => dt(p.endDate) }, { key: 'journals', header: 'Journals', align: 'right' },
        { key: 'status', header: 'Status', render: pill('status') }, { key: 'reopenStatus', header: 'Reopen', render: (p) => label(p.reopenStatus) }]} />
      <Card title="Closed fiscal years" icon="event_available">
        <DataTable rows={rowsOf(years.data).map((y) => ({ ...y, id: String(y.year) }) as R)} columns={[{ key: 'year', header: 'Year' },
          { key: 'netIncome', header: 'Net income', align: 'right', render: (y) => money(y.netIncome) }, { key: 'closedAt', header: 'Closed', render: (y) => formatDateTime(String(y.closedAt)) }]} />
      </Card>
      {open && <PeriodDrawer id={open} onClose={() => setOpen('')} />}
    </div>
  );
}

// ── Closing: posting exceptions and reconciliations (FR-PST-04/07, FR-AR-06) ──

function ExceptionDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const e = useGet<R>(`${API}/posting-exceptions/${id}`);
  const x = e.data;
  const inv = [...ACC, `${API}/posting-exceptions/${id}`];
  return (
    <Drawer open onClose={onClose} title="Posting Exception">
      <ErrorAlert error={e.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Event', String(x.eventType)], ['Reason', label(x.reason)], ['Message', String(x.message)], ['Amount', money(x.amount)],
            ['Suspense journal', String(x.journalNumber ?? '—')], ['Status', <StatusPill key="s" status={String(x.status)} />], ['Attempts', String(x.attempts)]]} />
          <pre className="oc-code" style={{ whiteSpace: 'pre-wrap', maxHeight: 260, overflow: 'auto' }}>{JSON.stringify(x.eventPayload ?? x.details, null, 2)}</pre>
          {x.status === 'open' && can('accounting.posting.manage') && <div className="oc-row-wrap">
            <ActionButton label="Repost" kind="primary" path={`${API}/posting-exceptions/${id}:repost`} invalidate={inv} />
            <ActionButton label="Ignore" path={`${API}/posting-exceptions/${id}:ignore`} invalidate={inv} reason="required" />
          </div>}
        </div>
      )}
    </Drawer>
  );
}

function ReconciliationChecks() {
  const [date, setDate] = useState(today());
  const pr = useGet<R>(`${API}/posting-reconciliation?date=${date}`);
  const cr = useGet<R>(`${API}/reconciliation?asOf=${date}`);
  return (
    <div className="oc-stack">
      <div style={{ width: 170 }}><TextField label="Business date" type="date" value={date} onChange={setDate} /></div>
      <Card title="Posting reconciliation: journals = Daily Revenue Report (FR-PST-07)" icon="balance">
        <ErrorAlert error={pr.error} />
        {pr.data && <KV items={[['Day closed', pr.data.dayClosed ? 'Yes' : 'No'], ['Daily Revenue Report', money(pr.data.dailyRevenue)], ['Journals', money(pr.data.journal)],
          ['Result', <StatusPill key="r" status={pr.data.ok ? 'matched' : 'exceptions'} />]]} />}
        <DataTable rows={items(pr.data?.rows).map((r, i) => ({ ...r, id: String(i) }) as R)} columns={[{ key: 'businessLine', header: 'Line', render: (r) => label(r.businessLine) },
          { key: 'revenueComponent', header: 'Component', render: (r) => label(r.revenueComponent) }, { key: 'dailyRevenue', header: 'Daily Revenue', align: 'right', render: (r) => money(r.dailyRevenue) },
          { key: 'journal', header: 'Journals', align: 'right', render: (r) => money(r.journal) }, { key: 'difference', header: 'Difference', align: 'right', render: (r) => money(r.difference) }]} />
      </Card>
      <Card title="Control accounts = sub-ledgers (AR, AP, deposits, deferred revenue, caddy fees)" icon="fact_check">
        <ErrorAlert error={cr.error} />
        <DataTable rows={items(cr.data?.checks).map((c) => ({ ...c, id: String(c.code) }) as R)} columns={[{ key: 'label', header: 'Control' },
          { key: 'gl', header: 'GL', align: 'right', render: (c) => money(c.gl) }, { key: 'subledger', header: 'Sub-ledger', align: 'right', render: (c) => money(c.subledger) },
          { key: 'difference', header: 'Difference', align: 'right', render: (c) => money(c.difference) },
          { key: 'ok', header: 'Result', render: (c) => <StatusPill status={c.ok ? 'matched' : 'exceptions'} /> }]} />
      </Card>
    </div>
  );
}

/** FR-TRS-03 parallel run: the Excel Finance trial balance of the month against OneClub's, per mapped account. */
function ExcelTBComparison() {
  const { propertyId } = useAuth();
  const [from, setFrom] = useState(monthStart());
  const [to, setTo] = useState(today());
  const [consolidated, setConsolidated] = useState(false);
  const [content, setContent] = useState('');
  const [res, setRes] = useState<R | null>(null);
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/reconciliations:excel-trial-balance`);
  const unmapped = items(res?.unmapped);
  return (
    <div className="oc-stack">
      <Card title="Parallel run: OneClub Trial Balance vs Excel Finance (FR-TRS-03)" icon="compare_arrows">
        <div className="oc-stack">
          <p className="oc-muted oc-small" style={{ margin: 0 }}>Upload the Excel Finance trial balance of the month as CSV with a header
            (account,debit,credit or account,balance). Items match OneClub accounts through the Excel Finance Mapping, else by account code.</p>
          <div className="oc-row-wrap"><DateRange from={from} to={to} setFrom={setFrom} setTo={setTo} />
            <Checkbox label="Consolidated MAIN + MDR" checked={consolidated} onChange={setConsolidated} /></div>
          <label className="oc-field"><span>CSV file</span><input type="file" accept=".csv,.txt" aria-label="Excel Finance trial balance (CSV)"
            onChange={(e) => { const f = e.target.files?.[0]; if (f) void f.text().then(setContent); }} /></label>
          <TextArea label="account,debit,credit" value={content} onChange={setContent} rows={6} />
          <div><button className="oc-btn oc-btn-primary" disabled={!content || send.isPending}
            onClick={() => send.mutate({ from, to, content, propertyId: consolidated ? undefined : propertyId }, { onSuccess: setRes })}>Compare</button></div>
          <ErrorAlert error={send.error} />
        </div>
      </Card>
      {res && (
        <>
          <KV items={[['Period', `${dt(res.from)} – ${dt(res.to)}`],
            ['Result', <StatusPill key="r" status={res.matched ? 'matched' : 'exceptions'} label={res.matched ? 'Trial balances agree' : 'Differences to explain'} />],
            ['Accounts with a difference', String(res.differences)], ['Total difference', money(res.totalDifference)], ['Unmapped Excel items', String(unmapped.length)]]} />
          <DataTable rows={items(res.rows)} rowKey={(r) => String(r.accountId ?? r.code)} columns={[{ key: 'code', header: 'Code' }, { key: 'name', header: 'Account' },
            { key: 'excelItems', header: 'Excel items', render: (r) => (Array.isArray(r.excelItems) ? r.excelItems.join(', ') : '') || '—' },
            { key: 'excel', header: 'Excel Finance', align: 'right', render: (r) => money(r.excel) }, { key: 'oneClub', header: 'OneClub', align: 'right', render: (r) => money(r.oneClub) },
            { key: 'difference', header: 'Difference', align: 'right', render: (r) => money(r.difference) },
            { key: 'matched', header: 'Result', render: (r) => <StatusPill status={r.matched ? 'matched' : 'exceptions'} /> }]} />
          {unmapped.length > 0 && <Card title="Excel items without an account" icon="help" actions={<Link className="oc-btn oc-btn-sm oc-btn-neutral" to="/accounting/setup?tab=mappings">Excel Finance Mapping</Link>}>
            <DataTable rows={unmapped.map((u) => ({ ...u, id: String(u.row) }) as R)} columns={[{ key: 'row', header: 'Row', align: 'right' }, { key: 'account', header: 'Excel item' },
              { key: 'balance', header: 'Balance', align: 'right', render: (u) => money(u.balance) }]} />
          </Card>}
        </>
      )}
    </div>
  );
}

export function ClosingPage() {
  const { can } = useAuth();
  const [tab, setTab] = useTab('exceptions');
  const [open, setOpen] = useState('');
  return (
    <div className="oc-stack">
      <PageHeader title="Closing" help="Posting exceptions (no event is lost: unmapped postings go to suspense), processed events and the reconciliations before closing."
        actions={can('accounting.posting.manage') ? <ActionButton label="Run postings up to today" kind="primary" path={`${API}/postings:run`} body={{ upTo: today() }} invalidate={ACC} /> : undefined} />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'exceptions', label: 'Posting Exceptions' }, { value: 'events', label: 'Processed Events' },
        { value: 'reconciliation', label: 'Reconciliations' }, { value: 'excel-tb', label: 'Excel TB Comparison' }]} />
      {tab === 'exceptions' && <ListPage title="Posting Exceptions" path={`${API}/posting-exceptions`} search={false} onRowClick={(r) => setOpen(r.id)}
        statuses={['open', 'resolved', 'ignored'].map((s) => ({ value: s, label: label(s) }))}
        columns={[{ key: 'createdAt', header: 'When', render: (r) => formatDateTime(String(r.createdAt)) }, { key: 'eventType', header: 'Event' },
          { key: 'reason', header: 'Reason', render: (r) => label(r.reason) }, { key: 'message', header: 'Message' },
          { key: 'amount', header: 'Amount', align: 'right', render: (r) => money(r.amount) }, { key: 'status', header: 'Status', render: pill('status') }]} />}
      {tab === 'events' && <ListPage title="Processed Events" path={`${API}/processed-events`} search={false} columns={[
        { key: 'processedAt', header: 'Processed', render: (r) => formatDateTime(String(r.processedAt)) }, { key: 'eventType', header: 'Event' },
        { key: 'status', header: 'Result', render: pill('status') }, { key: 'note', header: 'Note' }, { key: 'attempts', header: 'Deliveries', align: 'right' }]} />}
      {tab === 'reconciliation' && <ReconciliationChecks />}
      {tab === 'excel-tb' && <ExcelTBComparison />}
      {open && <ExceptionDrawer id={open} onClose={() => setOpen('')} />}
    </div>
  );
}

// ── Financial Reports (EP-22) ─────────────────────────────────────────────

const REPORTS = [{ value: 'profit-loss', label: 'Profit & Loss' }, { value: 'balance-sheet', label: 'Balance Sheet' }, { value: 'cash-flow', label: 'Cash Flow (indirect)' },
  { value: 'revenue-by-business-line', label: 'Revenue by Business Line' }];

export function FinancialReportsPage() {
  const { propertyId } = useAuth();
  const [kind, setKind] = useState('profit-loss');
  const [from, setFrom] = useState(`${today().slice(0, 4)}-01-01`);
  const [to, setTo] = useState(today());
  const [compare, setCompare] = useState('');
  const [consolidated, setConsolidated] = useState(false);
  const r = useGet<R>(`${API}/reports/${kind}${qs({ from, to, compare, propertyId: consolidated ? '' : propertyId })}`);
  const ledger = useLedgerLink();
  const x = r.data;
  const totals = (x?.totals ?? {}) as Record<string, string>;
  return (
    <div className="oc-stack">
      <PageHeader title="Financial Reports" help="P&L, Balance Sheet, Cash Flow and Revenue by Business Line per property or consolidated, with comparison (EP-22)." />
      <div className="oc-row-wrap">
        <SelectField label="Report" value={kind} onChange={setKind} options={REPORTS} />
        <DateRange from={from} to={to} setFrom={setFrom} setTo={setTo} />
        <SelectField label="Compare with" value={compare} onChange={setCompare} placeholder="No comparison"
          options={[{ value: 'previous_period', label: 'Previous period' }, { value: 'last_year', label: 'Last year' }]} />
        <Checkbox label="Consolidated MAIN + MDR" checked={consolidated} onChange={setConsolidated} />
      </div>
      <ErrorAlert error={r.error} />
      {x?.balanced !== undefined && x?.balanced !== null && <StatusPill status={x.balanced ? 'matched' : 'exceptions'} label={x.balanced ? 'Balanced' : 'Not balanced'} />}
      <p className="oc-muted oc-small" style={{ margin: 0 }}>Select an account row to open its General Ledger, then a line for its journal and source documents.</p>
      <DataTable rows={items(x?.rows).map((row, i) => ({ ...row, id: String(i) }) as R)} loading={r.isLoading}
        onRowClick={(s) => { if (s.accountId) ledger(s.accountId, kind === 'balance-sheet' ? `${to.slice(0, 4)}-01-01` : from, to, consolidated); }}
        columns={[{ key: 'section', header: 'Section', render: (s) => label(s.section) },
        { key: 'code', header: 'Account' }, { key: 'name', header: 'Name', render: (s) => (s.total ? <strong>{String(s.name)}</strong> : String(s.name)) },
        { key: 'amount', header: 'Amount', align: 'right', render: (s) => (s.total ? <strong>{money(s.amount)}</strong> : money(s.amount)) },
        ...(compare ? [{ key: 'compare', header: 'Comparison', align: 'right' as const, render: (s: R) => money(s.compare) }] : [])]} />
      {Object.keys(totals).length > 0 && <KV items={Object.entries(totals).map(([k, v]) => [label(k), money(v)])} />}
      <Card title="All accounting reports" icon="monitoring">
        <div className="oc-row-wrap">
          <Link className="oc-btn oc-btn-neutral oc-btn-sm" to="/accounting/general-ledger?tab=tb">Trial Balance</Link>
          <Link className="oc-btn oc-btn-neutral oc-btn-sm" to="/accounting/receivables?tab=aging">AR Aging</Link>
          <Link className="oc-btn oc-btn-neutral oc-btn-sm" to="/accounting/payables?tab=aging">AP Aging</Link>
          <Link className="oc-btn oc-btn-neutral oc-btn-sm" to="/reports?module=accounting">Report library (XLSX / PDF export)</Link>
        </div>
      </Card>
    </div>
  );
}

// ── Accounting Transition (EP-23, EP-29) ──────────────────────────────────

function OpeningImportModal({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [content, setContent] = useState('');
  const [description, setDescription] = useState('Excel Finance opening balances');
  const [balanceDate, setBalanceDate] = useState('');
  const [result, setResult] = useState<R | null>(null);
  const send = useSend<Record<string, unknown>, R>('POST', `${API}/opening-balances:import`, ACC);
  const run = (preview: boolean) => send.mutate({ content, description, balanceDate: balanceDate || undefined, preview }, {
    onSuccess: (r) => { if (preview) setResult(r); else { toast('Opening balance batch drafted'); onClose(); } },
  });
  return (
    <Modal open onClose={onClose} title="Import Opening Balances (CSV)" wide actions={<><button className="oc-btn oc-btn-neutral" onClick={onClose}>Cancel</button>
      <button className="oc-btn oc-btn-neutral" disabled={!content || send.isPending} onClick={() => run(true)}>Validate</button>
      <button className="oc-btn oc-btn-primary" disabled={!content || send.isPending} onClick={() => run(false)}>Import</button></>}>
      <div className="oc-form">
        <TextField label="Description" value={description} onChange={setDescription} required span />
        <TextField label="Balance date" type="date" value={balanceDate} onChange={setBalanceDate} help="Default: the day before the cut-over date" />
        <label className="oc-field"><span>CSV file</span><input type="file" accept=".csv,.txt" onChange={(e) => { const f = e.target.files?.[0]; if (f) void f.text().then(setContent); }} /></label>
        <TextArea label="account,debit,credit,partner_type,partner,document_no,document_date,due_date,description" value={content} onChange={setContent} rows={8} span />
      </div>
      {result && <KV items={[['Rows', String(result.rows)], ['Total debit', money(result.totalDebit)], ['Total credit', money(result.totalCredit)],
        ['Balanced', result.balanced ? 'Yes' : 'No'], ['Issues', items(result.issues).map((i) => `row ${String(i.row)}: ${String(i.message)}`).join('; ') || 'None']]} />}
      <ErrorAlert error={send.error} />
    </Modal>
  );
}

function OpeningDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { can } = useAuth();
  const b = useGet<R>(`${API}/opening-balances/${id}`);
  const rec = useGet<R>(b.data?.status === 'posted' ? `${API}/opening-balances/${id}/reconciliation` : null);
  const x = b.data;
  const inv = [...ACC, `${API}/opening-balances/${id}`];
  return (
    <Drawer open onClose={onClose} title={x ? `Opening balances ${String(x.number)}` : 'Opening balances'}>
      <ErrorAlert error={b.error} />
      {x && (
        <div className="oc-stack">
          <KV items={[['Balance date', dt(x.balanceDate)], ['Description', String(x.description)], ['Debit', money(x.totalDebit)], ['Credit', money(x.totalCredit)],
            ['Status', <StatusPill key="s" status={String(x.status)} />]]} />
          {x.status === 'draft' && <div className="oc-row-wrap">
            {can('accounting.opening_balance.post') && <ActionButton label="Approve & post" kind="primary" path={`${API}/opening-balances/${id}:post`} invalidate={inv}
              confirm="Post the opening journal and the AR / AP open items?" />}
            {can('accounting.opening_balance.manage') && <ActionButton label="Delete" method="DELETE" path={`${API}/opening-balances/${id}`} invalidate={ACC} danger
              confirm="Delete this draft batch?" onDone={onClose} />}
          </div>}
          <DataTable rows={items(x.lines)} columns={[{ key: 'accountCode', header: 'Account', render: (l) => `${String(l.accountCode)} ${String(l.accountName)}` },
            { key: 'partnerName', header: 'Partner' }, { key: 'documentNo', header: 'Open item' }, { key: 'dueDate', header: 'Due', render: (l) => dt(l.dueDate) },
            { key: 'debit', header: 'Debit', align: 'right', render: (l) => money(l.debit) }, { key: 'credit', header: 'Credit', align: 'right', render: (l) => money(l.credit) }]} />
          {rec.data && <Card title="Reconciliation with the sub-ledgers" icon="fact_check">
            <DataTable rows={items(rec.data.checks).map((c) => ({ ...c, id: String(c.code) }) as R)} columns={[{ key: 'label', header: 'Check' },
              { key: 'gl', header: 'GL', align: 'right', render: (c) => money(c.gl) }, { key: 'subledger', header: 'Sub-ledger', align: 'right', render: (c) => money(c.subledger) },
              { key: 'ok', header: 'Result', render: (c) => <StatusPill status={c.ok ? 'matched' : 'exceptions'} /> }]} />
          </Card>}
        </div>
      )}
    </Drawer>
  );
}

export function TransitionPage() {
  const { can } = useAuth();
  const [tab, setTab] = useTab('book');
  const [cutOver, setCutOver] = useState(monthStart());
  const [note, setNote] = useState('');
  const [modal, setModal] = useState('');
  const [open, setOpen] = useState('');
  const st = useGet<R>(`${API}/book`);
  const book = st.data?.book as R | null | undefined;
  const cfg = (st.data?.configuration ?? {}) as R;
  return (
    <div className="oc-stack">
      <PageHeader title="Accounting Transition" help="Chart of accounts template, cut-over date, opening balances from Excel Finance with open items, reconciliation and sign-off (EP-23, EP-29)." />
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'book', label: 'Book & Sign-off' }, { value: 'opening', label: 'Opening Balances' },
        { value: 'mappings', label: 'Excel Finance Mapping' }, { value: 'configuration', label: 'Accounting Configuration' },
        { value: 'allocation-rules', label: 'Allocation Rules' }]} />
      {tab === 'allocation-rules' && <AutoResourcePage resourceKey="accounting.revenue_allocation_rule" />}
      <ErrorAlert error={st.error} />
      {tab === 'book' && st.data && (
        <div className="oc-grid-2">
          <Card title="Book" icon="menu_book">
            {book ? <KV items={[['Template', String(book.template)], ['Cut-over date', dt(book.cutOverDate)], ['Status', <StatusPill key="s" status={String(book.status)} />],
              ['Currency', String(book.currency)], ['Accounting Export', book.exportStoppedAt ? `Stopped ${formatDateTime(String(book.exportStoppedAt))}` : 'Running (parallel run)'],
              ['Accounts', String(st.data.accounts)], ['Posting rules', String(st.data.postingRules)], ['Tax codes', String(st.data.taxCodes)],
              ['Open posting exceptions', String(st.data.openExceptions)]]} />
              : <div className="oc-stack"><div className="oc-muted">No book yet: load the OneClub club & hospitality chart of accounts.</div>
                {can('accounting.setup.manage') && <div className="oc-row-wrap"><div style={{ width: 170 }}><TextField label="Cut-over date" type="date" value={cutOver} onChange={setCutOver} /></div>
                  <ActionButton label="Load template" kind="primary" path={`${API}/book:load-template`} body={{ cutOverDate: cutOver }} invalidate={ACC} /></div>}</div>}
          </Card>
          {book && book.status !== 'live' && can('accounting.setup.sign_off') && <Card title="Sign-off" icon="verified">
            <div className="oc-stack">
              <div className="oc-small oc-muted">After the reconciliation month (TB and P&L agreed with Excel Finance and the Accounting Export), the Finance Manager signs off:
                the Accounting Export of this property stops; earlier exports stay downloadable (FR-TRS-03/04).</div>
              <TextArea label="Sign-off note" value={note} onChange={setNote} rows={3} required />
              <div><ActionButton label="Sign off" kind="primary" path={`${API}/book:sign-off`} body={{ note }} invalidate={ACC} disabled={!note}
                confirm="Stop the Accounting Export of this property?" /></div>
            </div>
          </Card>}
        </div>
      )}
      {tab === 'opening' && <>
        <ListPage title="Opening Balance Batches" path={`${API}/opening-balances`} search={false} onRowClick={(r) => setOpen(r.id)}
          actions={can('accounting.opening_balance.manage') ? <>
            <button className="oc-btn oc-btn-neutral" onClick={() => setModal('import')}>Import CSV</button>
            <ActionButton label="From sub-ledgers" path={`${API}/opening-balances`} body={{ description: 'Operational sub-ledgers at cut-over', includeSubledgers: true }} invalidate={ACC} />
          </> : undefined}
          columns={[{ key: 'number', header: 'Batch' }, { key: 'balanceDate', header: 'Balance date', render: (r) => dt(r.balanceDate) }, { key: 'description', header: 'Description' },
            { key: 'totalDebit', header: 'Debit', align: 'right', render: (r) => money(r.totalDebit) }, { key: 'totalCredit', header: 'Credit', align: 'right', render: (r) => money(r.totalCredit) },
            { key: 'status', header: 'Status', render: pill('status') }]} />
        {modal === 'import' && <OpeningImportModal onClose={() => setModal('')} />}
        {open && <OpeningDrawer id={open} onClose={() => setOpen('')} />}
      </>}
      {tab === 'mappings' && <AutoResourcePage resourceKey="accounting.account_mapping" />}
      {tab === 'configuration' && st.data && (
        <Card title="Accounting Configuration in force" icon="tune" actions={<Link className="oc-btn oc-btn-sm oc-btn-neutral" to="/settings/club-policies">Edit club policy</Link>}>
          <KV items={[['Posting modes', Object.entries((cfg.postingModes ?? {}) as Record<string, string>).map(([k, v]) => `${k}: ${label(v)}`).join(', ')],
            ['Unmapped postings', label(cfg.unmappedPosting)], ['Late postings into closed periods', label(cfg.closedPeriodPosting)],
            ['Manual journal approval from', money(cfg.manualJournalApprovalThreshold)], ['Loyalty point value', money(cfg.loyaltyPointValue)],
            ['Bank auto-match tolerance', `${String(cfg.autoMatchDays)} days`]]} />
          <DataTable rows={Object.entries((cfg.accounts ?? {}) as Record<string, string>).map(([role, code]) => ({ id: role, role, code }))}
            columns={[{ key: 'role', header: 'Default account (role)', render: (r) => label(r.role) }, { key: 'code', header: 'Account code' }]} />
        </Card>
      )}
    </div>
  );
}

// ── routes ────────────────────────────────────────────────────────────────

/** Back Office routes of the area. */
export const ACCOUNTING_ROUTES: AreaRoute[] = [
  { path: 'accounting/dashboard', perm: 'accounting.dashboard.view', element: <FinanceDashboardPage /> },
  { path: 'accounting/budget-vs-actual', perm: 'accounting.dashboard.view', element: <BudgetVsActualPage /> },
  { path: 'soon/:key', perm: 'accounting.dashboard.view', element: <SoonPage /> },
  { path: 'accounting', perm: 'accounting.journal.view', element: <GeneralLedgerPage /> },
  { path: 'accounting/general-ledger', perm: 'accounting.journal.view', element: <GeneralLedgerPage /> },
  { path: 'accounting/receivables', perm: 'accounting.receivable.view', element: <ReceivablesPage /> },
  { path: 'accounting/receivables/reports', perm: 'accounting.receivable.view', element: <Navigate to="/accounting/receivables?tab=aging" replace /> },
  { path: 'accounting/receivables/adjustments', perm: 'accounting.receivable.view', element: <Navigate to="/accounting/receivables?tab=allowance" replace /> },
  { path: 'accounting/payables', perm: 'accounting.payable.view', element: <PayablesPage /> },
  { path: 'accounting/payables/reports', perm: 'accounting.payable.view', element: <Navigate to="/accounting/payables?tab=aging" replace /> },
  { path: 'accounting/revenue', perm: 'billing.invoice.view', element: <RevenueBillingPage /> },
  { path: 'accounting/revenue/billing/:id', perm: 'billing.folio.view', element: <PrepareBillingPage /> },
  { path: 'accounting/revenue-recognition', perm: 'accounting.revenue.view', element: <RevenueRecognitionPage /> },
  { path: 'accounting/tax', perm: 'accounting.tax_invoice.view', element: <TaxPage /> },
  { path: 'accounting/cash-bank', perm: 'accounting.bank_transaction.view', element: <CashBankPage /> },
  { path: 'accounting/revenue-tax', perm: 'accounting.revenue.view', element: <RevenueTaxPage /> },
  { path: 'accounting/periods', perm: 'accounting.period.view', element: <PeriodsPage /> },
  { path: 'accounting/closing', perm: 'accounting.posting.view', element: <ClosingPage /> },
  { path: 'accounting/reports', perm: 'accounting.report.view', element: <FinancialReportsPage /> },
  { path: 'accounting/setup', perm: 'accounting.setup.view', element: <TransitionPage /> },
];

/** Ops workstation tiles and routes of the area (finance works in the Back Office). */
export const ACCOUNTING_OPS_TILES: OpsTile[] = [];
export const ACCOUNTING_OPS_ROUTES: OpsRoute[] = [];
