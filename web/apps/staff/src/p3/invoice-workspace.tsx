import React, { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { qs, request, useGet, useSend, type Page } from '@oneclub/api-client';
import { formatDate } from '@oneclub/i18n';
import {
  Card, Checkbox, ErrorAlert, Icon, PageHeader, SelectField, StatusPill, TextArea, TextField, useAuth, useDebounced, useToast,
} from '@oneclub/shell';
import { money, today, type R } from '../p1/common';
import { CorporatePicker, CustomerPicker } from './sales';

// Create Invoice workspace (Revenue & Billing, docs/Revenue_Billing_Invoice_Management.md
// §5–§13): the source (customer folio, one folio, corporate city ledger or a
// manual invoice for the exceptions), customer & billing, payment term,
// customer references, the lines with a live preview computed by the server
// (POST /billing/invoices:preview, nothing saved), notes and supporting
// documents. Saved as a draft or issued at once.

type Source = 'customerFolio' | 'folio' | 'account' | 'manual';

type Line = {
  description: string; businessLine: string; revenueComponent: string; quantity: string; unit: string; unitPrice: string;
  discount: string; discountPercent: string; taxCodes: string[];
};

type Doc = { id: string; filename: string };

const SOURCES: { value: Source; label: string; icon: string; help: string }[] = [
  { value: 'customerFolio', label: 'Customer folio', icon: 'folder_shared', help: 'Every uninvoiced charge of the customer folio, less what was received' },
  { value: 'folio', label: 'One folio', icon: 'receipt', help: 'The uninvoiced charges of a booking, stay or event folio' },
  { value: 'account', label: 'Corporate city ledger', icon: 'apartment', help: 'Charges on the company account in a period' },
  { value: 'manual', label: 'Manual invoice', icon: 'edit_note', help: 'Exceptions only: adjustment, special billing, non-system transaction, one-off charge' },
];

export const BUSINESS_LINES = [
  { value: 'golf', label: 'Golf' }, { value: 'stay', label: 'Resort' }, { value: 'banquet', label: 'Events & Banquet' }, { value: 'pos', label: 'F&B & Retail' },
  { value: 'sportclub', label: 'Sport Club' }, { value: 'membership', label: 'Membership' }, { value: 'package', label: 'Package' }, { value: 'other', label: 'Other' },
];

// Revenue components per business line; with the line they decide the
// revenue account through the posting rules (Accounting → Posting Rules).
export const COMPONENTS: Record<string, string[]> = {
  golf: ['green_fee', 'caddy_fee', 'buggy_fee', 'driving_range', 'tournament_fee', 'sponsorship', 'golf_other'],
  stay: ['bungalow', 'vip_suite', 'meeting', 'equipment', 'late_checkout_fee'],
  banquet: ['banquet_package', 'banquet_fnb', 'venue_rental', 'corkage', 'outdoor_venue', 'electricity', 'event_fee'],
  pos: ['fnb', 'pro_shop'],
  sportclub: ['sport_entry', 'court', 'class', 'registration_fee', 'locker'],
  membership: ['membership_fee', 'membership_annual_fee', 'card_replacement_fee', 'reactivation_fee', 'nominee_fee'],
  package: ['package'],
  other: ['other', 'cancellation_fee', 'damage_charge'],
};

export const TERMS = [
  { value: '', label: 'Default (Credit Policies)' }, { value: '0', label: 'Immediate' }, { value: '7', label: 'Net 7' }, { value: '14', label: 'Net 14' },
  { value: '30', label: 'Net 30' }, { value: '60', label: 'Net 60' }, { value: 'custom', label: 'Custom' },
];

const label = (v: unknown) => String(v ?? '').replace(/_/g, ' ');
const newLine = (taxCodes: string[] = []): Line => ({
  description: '', businessLine: 'other', revenueComponent: 'other', quantity: '1', unit: '', unitPrice: '', discount: '', discountPercent: '', taxCodes,
});
const addDays = (date: string, days: number) => {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
};
const opt = (v: string) => (v.trim() ? v.trim() : undefined);

export function InvoiceWorkspacePage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const toast = useToast();
  const { can } = useAuth();
  const manualAllowed = can('billing.invoice.manual');
  const preset = params.get('source') as Source | null;
  const [source, setSource] = useState<Source>(preset && (preset !== 'manual' || manualAllowed) ? preset : 'customerFolio');
  const [ref, setRef] = useState(params.get('ref') ?? '');
  const [corporate, setCorporate] = useState('');
  const [customer, setCustomer] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState(today());
  const [term, setTerm] = useState('');
  const [customDays, setCustomDays] = useState('');
  const [billTo, setBillTo] = useState({ name: '', address: '', npwp: '', email: '', phone: '' });
  const [refs, setRefs] = useState({ customerPo: '', contractRef: '', billingRef: '' });
  const [notes, setNotes] = useState('');
  const [internalNotes, setInternalNotes] = useState('');
  const [docs, setDocs] = useState<Doc[]>([]);
  const [lines, setLines] = useState<Line[]>([newLine()]);

  const termsDays = term === 'custom' ? (customDays ? Number(customDays) : undefined) : term ? Number(term) : undefined;
  const manualLines = lines.filter((l) => l.description.trim() && l.unitPrice !== '');
  const ready = source === 'manual' ? !!(customer || corporate) && manualLines.length > 0 : !!ref;

  // The request without notes and documents: what the preview depends on
  // (compared by its JSON, see usePreview).
  const core = (() => {
    const b: Record<string, unknown> = { termsDays };
    if (source === 'customerFolio') b.customerFolioId = ref;
    if (source === 'folio') b.folioId = ref;
    if (source === 'account') Object.assign(b, { corporateAccountId: ref, from: opt(from), to: opt(to) });
    if (source === 'manual') {
      Object.assign(b, { customerId: opt(customer), corporateAccountId: opt(corporate), lines: manualLines.map((l) => ({
        description: l.description.trim(), businessLine: l.businessLine, revenueComponent: l.revenueComponent, quantity: l.quantity || '1',
        unit: opt(l.unit), unitPrice: l.unitPrice, discountPercent: opt(l.discountPercent), discount: l.discountPercent ? undefined : opt(l.discount),
        taxCodes: l.taxCodes.length ? l.taxCodes : undefined,
      })) });
    } else if (source !== 'account' && corporate) b.corporateAccountId = corporate;
    const bt = Object.fromEntries(Object.entries(billTo).filter(([, v]) => v.trim()));
    if (Object.keys(bt).length) b.billTo = bt;
    return b;
  })();
  const preview = usePreview(ready ? core : null);
  const pv = preview.data;

  const send = useSend<Record<string, unknown>, R>('POST', '/api/v1/billing/invoices', ['/api/v1/billing'], () => ({ 'Idempotency-Key': crypto.randomUUID() }));
  const submit = (issue: boolean) => send.mutate({
    ...core, issue, notes: opt(notes), internalNotes: opt(internalNotes), ...Object.fromEntries(Object.entries(refs).map(([k, v]) => [k, opt(v)])),
    attachmentFileIds: docs.length ? docs.map((d) => d.id) : undefined,
  }, { onSuccess: (inv) => {
    toast(issue ? `Invoice ${String(inv.number ?? '')} issued` : 'Draft invoice saved');
    navigate(`/accounting/revenue?tab=invoices&id=${inv.id}`);
  } });

  const businessDay = useBusinessDate();
  const days = termsDays ?? (pv ? Number(pv.termsDays) : undefined);
  const due = days !== undefined && !Number.isNaN(days) ? addDays(businessDay, days) : '';
  const back = () => (window.history.length > 1 ? navigate(-1) : navigate('/accounting/revenue?tab=invoices'));

  return (
    <div className="oc-stack">
      <PageHeader title="Create Invoice" help="Most invoices are generated from what was billed in golf, resort and events. Use a manual invoice for exceptions only."
        actions={<button className="oc-btn oc-btn-text" onClick={back}><Icon name="arrow_back" size={18} /> Back to invoices</button>} />
      <div className="oc-workspace">
        <div className="oc-stack">
          <Card title="Source" icon="input">
            <div className="oc-choice-grid" role="radiogroup" aria-label="Invoice source">
              {SOURCES.filter((s) => s.value !== 'manual' || manualAllowed).map((s) => (
                <button key={s.value} type="button" role="radio" aria-checked={source === s.value} className="oc-choice"
                  onClick={() => { setSource(s.value); setRef(''); }}>
                  <Icon name={s.icon} size={22} />
                  <span><strong>{s.label}</strong><span className="oc-small oc-muted">{s.help}</span></span>
                </button>
              ))}
            </div>
            <div className="oc-form" style={{ marginTop: 16 }}>
              {(source === 'customerFolio' || source === 'folio') && <FolioSelect kind={source} value={ref} onChange={setRef} />}
              {source === 'account' && <>
                <CorporateSelect label="Corporate account" value={ref} onChange={setRef} required />
                <TextField label="Charges from" type="date" value={from} onChange={setFrom} help="Empty: every uninvoiced charge" />
                <TextField label="Charges to" type="date" value={to} onChange={setTo} />
              </>}
              {source === 'manual' && <>
                <CustomerPicker value={customer} onChange={setCustomer} label="Customer" />
                <CorporatePicker value={corporate} onChange={setCorporate} />
                <p className="oc-span oc-small oc-muted" style={{ margin: 0 }}>Choose the customer, the company that pays, or both (the company is billed).</p>
              </>}
            </div>
          </Card>

          <Card title="Customer & Billing" icon="badge">
            <div className="oc-form">
              {(source === 'customerFolio' || source === 'folio') &&
                <CorporateSelect label="Bill to company (optional)" value={corporate} onChange={setCorporate} placeholder="The customer of the folio" />}
              <TextField label="Bill to" value={billTo.name} onChange={(v) => setBillTo({ ...billTo, name: v })} placeholder={pv ? String(pv.billToName ?? '') : 'From the customer'} />
              <TextField label="NPWP" value={billTo.npwp} onChange={(v) => setBillTo({ ...billTo, npwp: v })} placeholder={str(pv?.billToNpwp)}
                help="Needed for a tax invoice (e-Faktur)" />
              <TextField label="Billing address" value={billTo.address} onChange={(v) => setBillTo({ ...billTo, address: v })} placeholder={str(pv?.billToAddress)} span />
              <TextField label="E-mail" type="email" value={billTo.email} onChange={(v) => setBillTo({ ...billTo, email: v })} placeholder={str(pv?.billToEmail)} />
              <TextField label="Phone" value={billTo.phone} onChange={(v) => setBillTo({ ...billTo, phone: v })} placeholder={str(pv?.billToPhone)} />
              <SelectField label="Payment term" value={term} onChange={setTerm} options={TERMS} />
              {term === 'custom' && <TextField label="Days" type="number" value={customDays} onChange={setCustomDays} />}
              <Readonly label="Invoice & posting date" value={`${formatDate(businessDay)} (business date at issue)`} />
              <Readonly label="Due date" value={due ? formatDate(due) : '—'} />
              <Readonly label="Currency" value={str(pv?.currency) || 'IDR'} />
            </div>
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Empty fields come from the customer or company master data.</p>
          </Card>

          <Card title={source === 'manual' ? 'Invoice Items' : 'Invoice Items (from the source)'} icon="list_alt">
            {source === 'manual'
              ? <ManualLines lines={lines} onChange={setLines} preview={pv?.lines as R[] | undefined} />
              : <SourceLines rows={pv?.lines as R[] | undefined} ready={ready} loading={preview.loading} />}
          </Card>

          <Card title="References" icon="tag">
            <div className="oc-form">
              <TextField label="Customer PO" value={refs.customerPo} onChange={(v) => setRefs({ ...refs, customerPo: v })} />
              <TextField label="Contract" value={refs.contractRef} onChange={(v) => setRefs({ ...refs, contractRef: v })} />
              <TextField label="Billing reference" value={refs.billingRef} onChange={(v) => setRefs({ ...refs, billingRef: v })} placeholder="Booking, event …" />
            </div>
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>Printed on the customer invoice PDF.</p>
          </Card>

          <Card title="Notes & Attachments" icon="attach_file">
            <div className="oc-form">
              <TextArea label="Customer notes" value={notes} onChange={setNotes} help="Printed on the invoice" />
              <TextArea label="Internal notes" value={internalNotes} onChange={setInternalNotes} help="Never shown to the customer" />
            </div>
            <InvoiceDocuments docs={docs} onChange={setDocs} />
          </Card>
        </div>

        <aside className="oc-workspace-aside">
          <Card title="Summary" icon="calculate">
            <InvoiceSummary pv={pv} />
            {!ready && <p className="oc-small oc-muted">{source === 'manual' ? 'Choose the customer or company and add a line with a price.' : 'Choose the source.'}</p>}
            {preview.loading && <p className="oc-small oc-muted">Calculating…</p>}
            <ErrorAlert error={preview.error} />
            <ErrorAlert error={send.error} />
            <div className="oc-stack" style={{ gap: 8, marginTop: 16 }}>
              {can('billing.invoice.issue') && <button className="oc-btn oc-btn-primary" disabled={!ready || !pv || send.isPending} onClick={() => submit(true)}>Generate & Issue</button>}
              <button className="oc-btn oc-btn-neutral" disabled={!ready || !pv || send.isPending} onClick={() => submit(false)}>Save as Draft</button>
              <button className="oc-btn oc-btn-text" onClick={back}>Cancel</button>
            </div>
            <p className="oc-small oc-muted" style={{ marginBottom: 0 }}>The invoice number is assigned at issue; the journal is posted automatically.</p>
          </Card>
        </aside>
      </div>
    </div>
  );
}

const str = (v: unknown) => (v == null ? '' : String(v));

function Readonly({ label: l, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="oc-field">
      <span className="oc-label">{l}</span>
      <div className="oc-readonly">{value}</div>
    </div>
  );
}

/** The current business date (the invoice date at issue). */
function useBusinessDate() {
  const days = useGet<Page<R>>('/api/v1/billing/business-days?limit=5', { retry: false });
  return String(days.data?.items.find((d) => d.current)?.businessDate ?? today());
}

/** Server preview of the request, debounced; nothing is saved. */
function usePreview(body: Record<string, unknown> | null) {
  const key = useDebounced(body ? JSON.stringify(body) : '', 400);
  const [state, setState] = useState<{ data?: R; error?: unknown; loading: boolean }>({ loading: false });
  useEffect(() => {
    if (!key) {
      setState({ loading: false });
      return;
    }
    let live = true;
    setState((s) => ({ ...s, loading: true }));
    request<R>('POST', '/api/v1/billing/invoices:preview', JSON.parse(key))
      .then((data) => live && setState({ data, loading: false }))
      .catch((error) => live && setState({ error, loading: false }));
    return () => { live = false; };
  }, [key]);
  return state;
}

function FolioSelect({ kind, value, onChange }: { kind: 'customerFolio' | 'folio'; value: string; onChange: (v: string) => void }) {
  const base = kind === 'customerFolio' ? '/api/v1/billing/customer-folios' : '/api/v1/billing/folios';
  const list = useGet<Page<R>>(`${base}?limit=200&filter[status]=open`);
  // A preset folio may be closed (Billing: ready to invoice).
  const preset = useGet<R>(value ? `${base}/${value}` : null, { retry: false });
  const items = [...(list.data?.items ?? [])];
  if (preset.data && !items.some((f) => f.id === preset.data.id)) items.unshift(preset.data);
  return <SelectField label={kind === 'folio' ? 'Folio' : 'Customer folio'} value={value} onChange={onChange} required placeholder="Select" span
    options={items.map((f) => ({ value: f.id, label: `${String(f.number)} · ${String(f.holderName)}${f.balance != null ? ` · ${money(f.balance)}` : ''}` }))} />;
}

function CorporateSelect({ label: l, value, onChange, required, placeholder = 'Select' }: {
  label: string; value: string; onChange: (v: string) => void; required?: boolean; placeholder?: string;
}) {
  const list = useGet<Page<R>>('/api/v1/crm/corporate-accounts?limit=200&filter[status]=active');
  return <SelectField label={l} value={value} onChange={onChange} required={required} placeholder={placeholder}
    options={(list.data?.items ?? []).map((c) => ({ value: c.id, label: `${String(c.name)} (${String(c.code)})` }))} />;
}

function SourceLines({ rows, ready, loading }: { rows?: R[]; ready: boolean; loading: boolean }) {
  if (!ready) return <p className="oc-muted" style={{ margin: 0 }}>The uninvoiced charges appear here once the source is chosen.</p>;
  if (!rows) return <p className="oc-muted" style={{ margin: 0 }}>{loading ? 'Loading the charges…' : 'No charges to invoice.'}</p>;
  return (
    <div className="oc-table-wrap">
      <table className="oc-table">
        <thead><tr><th>Product / Service</th><th>Line</th><th style={{ textAlign: 'right' }}>Qty</th><th style={{ textAlign: 'right' }}>Unit price</th>
          <th style={{ textAlign: 'right' }}>Service</th><th style={{ textAlign: 'right' }}>Tax</th><th style={{ textAlign: 'right' }}>Total</th></tr></thead>
        <tbody>
          {rows.map((l, i) => (
            <tr key={i}>
              <td>{String(l.description)}</td>
              <td className="oc-muted">{label(l.businessLine)}{l.revenueComponent ? ` · ${label(l.revenueComponent)}` : ''}</td>
              <td style={{ textAlign: 'right' }}>{String(l.quantity)}{l.unit ? ` ${String(l.unit)}` : ''}</td>
              <td style={{ textAlign: 'right' }}>{money(l.unitPrice)}</td>
              <td style={{ textAlign: 'right' }}>{money(l.serviceAmount)}</td>
              <td style={{ textAlign: 'right' }}>{money(l.taxAmount)}</td>
              <td style={{ textAlign: 'right' }}><strong>{money(l.total)}</strong></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ManualLines({ lines, onChange, preview }: { lines: Line[]; onChange: (l: Line[]) => void; preview?: R[] }) {
  const codes = useGet<Page<R>>('/api/v1/billing/invoice-tax-codes');
  const set = (i: number, patch: Partial<Line>) => onChange(lines.map((l, j) => (j === i ? { ...l, ...patch } : l)));
  // Preview lines follow the priced lines (description and price filled) in order.
  let p = 0;
  return (
    <div className="oc-stack" style={{ gap: 12 }}>
      {lines.map((l, i) => {
        const priced = l.description.trim() && l.unitPrice !== '' ? preview?.[p++] : undefined;
        return (
          <div key={i} className="oc-line-item">
            <div className="oc-row" style={{ marginBottom: 8 }}>
              <strong>Item {i + 1}</strong><span className="oc-spacer" />
              {priced && <span>Total <strong>{money(priced.total)}</strong></span>}
              {lines.length > 1 && <button type="button" className="oc-btn oc-btn-text oc-btn-sm" aria-label={`Remove item ${i + 1}`}
                onClick={() => onChange(lines.filter((_, j) => j !== i))}><Icon name="delete" size={18} /></button>}
            </div>
            <div className="oc-form">
              <TextField label="Product / service" value={l.description} onChange={(v) => set(i, { description: v })} span required />
              <SelectField label="Business line" value={l.businessLine} options={BUSINESS_LINES}
                onChange={(v) => set(i, { businessLine: v, revenueComponent: COMPONENTS[v]?.[0] ?? 'other' })} />
              <SelectField label="Revenue" value={l.revenueComponent} onChange={(v) => set(i, { revenueComponent: v })}
                options={(COMPONENTS[l.businessLine] ?? ['other']).map((c) => ({ value: c, label: label(c) }))} help="Decides the revenue account" />
              <TextField label="Quantity" type="number" value={l.quantity} onChange={(v) => set(i, { quantity: v })} />
              <TextField label="Unit" value={l.unit} onChange={(v) => set(i, { unit: v })} placeholder="pax, night, hour …" />
              <TextField label="Unit price" type="number" value={l.unitPrice} onChange={(v) => set(i, { unitPrice: v })} required help="Before discount, tax & service" />
              <TextField label="Discount" type="number" value={l.discount} onChange={(v) => set(i, { discount: v, discountPercent: '' })} placeholder="Amount" />
              <TextField label="or discount %" type="number" value={l.discountPercent} onChange={(v) => set(i, { discountPercent: v, discount: '' })} />
            </div>
            {(codes.data?.items.length ?? 0) > 0 && (
              <div className="oc-row-wrap" style={{ marginTop: 8 }} aria-label={`Tax & service of item ${i + 1}`}>
                <span className="oc-small oc-muted">Tax & service:</span>
                {codes.data!.items.map((c) => (
                  <Checkbox key={String(c.code)} label={`${String(c.name)} ${String(c.ratePercent)}%`} checked={l.taxCodes.includes(String(c.code))}
                    onChange={(on) => set(i, { taxCodes: on ? [...l.taxCodes, String(c.code)] : l.taxCodes.filter((x) => x !== c.code) })} />
                ))}
              </div>
            )}
          </div>
        );
      })}
      <div><button type="button" className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => onChange([...lines, newLine(lines[lines.length - 1]?.taxCodes)])}>
        <Icon name="add" size={18} /> Add item</button></div>
      <p className="oc-small oc-muted" style={{ margin: 0 }}>Tax & service rates come from Settings → Tax & Service; revenue allocation follows the Allocation Rules.</p>
    </div>
  );
}

function InvoiceSummary({ pv }: { pv?: R }) {
  const lines = (pv?.lines as R[] | undefined) ?? [];
  const n = (v: unknown) => Number(v ?? 0);
  const received = lines.filter((l) => n(l.total) < 0).reduce((a, l) => a + n(l.total), 0);
  const discount = lines.reduce((a, l) => a + n(l.discountAmount), 0);
  const gross = lines.filter((l) => n(l.total) >= 0).reduce((a, l) => a + n(l.netAmount) + n(l.discountAmount), 0);
  const rows: [string, React.ReactNode][] = [
    ['Subtotal', money(gross)], ['Discount', discount ? `− ${money(discount)}` : money(0)], ['Service charge', money(pv?.serviceAmount ?? 0)],
    ['Tax', money(pv?.taxAmount ?? 0)],
  ];
  if (received) rows.push(['Less: received', money(received)]);
  return (
    <dl className="oc-summary">
      {rows.map(([k, v]) => <React.Fragment key={k}><dt>{k}</dt><dd>{v}</dd></React.Fragment>)}
      <dt className="oc-summary-total">Grand total</dt><dd className="oc-summary-total">{money(pv?.total ?? 0)}</dd>
      {pv && <><dt>Bill to</dt><dd>{String(pv.billToName)}</dd><dt>Kind</dt><dd><StatusPill status="draft" label={label(pv.kind)} /></dd></>}
    </dl>
  );
}

/** Supporting documents: uploaded at once, attached when the invoice is saved. */
export function InvoiceDocuments({ docs, onChange }: { docs: Doc[]; onChange: (d: Doc[]) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const upload = async (f: File) => {
    setBusy(true);
    setErr(null);
    const fd = new FormData();
    fd.append('file', f);
    try {
      const r = await request<R>('POST', '/api/v1/billing/invoice-files', fd);
      onChange([...docs, { id: String(r.id), filename: String(r.filename ?? f.name) }]);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="oc-stack" style={{ gap: 8, marginTop: 12 }}>
      <ErrorAlert error={err} />
      <div className="oc-row-wrap" style={{ alignItems: 'center' }} aria-label="Supporting documents">
        <label className="oc-btn oc-btn-neutral oc-btn-sm" style={{ cursor: busy ? 'not-allowed' : 'pointer' }} aria-disabled={busy}>
          <Icon name="attach_file" size={18} /> {busy ? 'Uploading…' : 'Attach document'}
          <input type="file" accept="application/pdf,image/jpeg,image/png,image/webp" className="oc-sr" disabled={busy}
            onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void upload(f); }} />
        </label>
        {docs.map((d) => (
          <span key={d.id} className="oc-chip">
            <Icon name="description" size={16} /> {d.filename}
            <button type="button" className="oc-btn oc-btn-text oc-btn-sm" aria-label={`Remove ${d.filename}`} onClick={() => onChange(docs.filter((x) => x.id !== d.id))}>
              <Icon name="close" size={16} /></button>
          </span>
        ))}
      </div>
      <span className="oc-small oc-muted">PO, contract, booking or event confirmation, service evidence (PDF or photo, up to 10 MB). Staff only.</span>
    </div>
  );
}

/** Link target of the Create Invoice workspace with a preset source. */
export const invoiceWorkspaceUrl = (source?: Source, ref?: string) => `/billing/invoices/new${qs({ source, ref })}`;
