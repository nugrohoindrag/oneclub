import React, { useState } from 'react';
import { useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatMoney, useTranslation } from '@oneclub/i18n';
import { useAuth, useBootstrap } from '../context';
import { Card, Checkbox, DataTable, ErrorAlert, Icon, Modal, SelectField, StatusPill, TextField, fieldErrors } from '../components/ui';
import { useToast } from '../components/toast';
import { ResourcePage, dateCol, statusCol, type ResourceConfig, type Row } from './resource';

const CODE_HELP = 'A–Z, 0–9, - or _ (max 20)';

export function PropertiesPage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Properties', singular: 'Property', help: t('help.properties'), path: '/api/v1/platform/properties', perm: 'platform.property',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'city', header: 'City' },
      { key: 'timezone', header: 'Timezone', render: (r) => (r.timezone as string) || <span className="oc-muted">Instance default</span> }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, help: CODE_HELP },
      { name: 'name', label: 'Name', required: true },
      { name: 'address', label: 'Address', type: 'textarea' },
      { name: 'city', label: 'City' },
      { name: 'phone', label: 'Phone' },
      { name: 'email', label: 'E-mail', type: 'email' },
      { name: 'timezone', label: 'Timezone', placeholder: 'Asia/Jakarta', help: 'Empty = instance timezone' },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return <ResourcePage cfg={cfg} />;
}

const venueTypes = ['golf', 'sport', 'stay', 'banquet', 'dining', 'other'].map((v) => ({ value: v, label: v.charAt(0).toUpperCase() + v.slice(1) }));

export function VenuesPage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Venues', singular: 'Venue', help: t('help.venues'), path: '/api/v1/platform/venues', perm: 'platform.venue',
    statusOptions: [{ value: 'active', label: 'Active' }, { value: 'pending', label: 'Pending' }, { value: 'inactive', label: 'Inactive' }, { value: 'rejected', label: 'Rejected' }],
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'venueType', header: 'Venue Type' }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, help: CODE_HELP },
      { name: 'name', label: 'Name', required: true },
      { name: 'venueType', label: 'Venue Type', type: 'select', options: venueTypes, default: 'golf' },
      { name: 'description', label: 'Description', type: 'textarea' },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }] },
    ],
    noEdit: (r) => (r.status === 'pending' ? 'This venue is waiting for Venue Activation approval. It can be edited after the decision.' : undefined),
  };
  return <ResourcePage cfg={cfg} />;
}

export function CoursesPage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Courses', singular: 'Course', help: t('help.courses'), path: '/api/v1/golf/courses', perm: 'golf.course',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'holes', header: 'Holes', align: 'right' }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, help: CODE_HELP },
      { name: 'name', label: 'Name', required: true },
      { name: 'venueId', label: 'Venue', type: 'reference', required: true, ref: { path: '/api/v1/platform/venues?filter[status]=active', label: (r) => `${r.name} (${r.code})` } },
      { name: 'holes', label: 'Holes', type: 'select', required: true, options: ['9', '18', '27', '36'].map((h) => ({ value: h, label: h })), default: '18' },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return <ResourcePage cfg={cfg} />;
}

export function DepartmentsPage() {
  const { t } = useTranslation();
  const list = useGet<Page<Row>>('/api/v1/platform/departments?limit=500');
  const names = new Map((list.data?.items ?? []).map((d) => [d.id, String(d.name)]));
  const cfg: ResourceConfig = {
    title: 'Departments', singular: 'Department', help: t('help.departments'), path: '/api/v1/platform/departments', perm: 'platform.department',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' },
      { key: 'parentId', header: 'Parent', render: (r) => names.get(String(r.parentId)) ?? <span className="oc-muted">—</span> }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, help: CODE_HELP },
      { name: 'name', label: 'Name', required: true },
      { name: 'parentId', label: 'Parent Department', type: 'reference', ref: { path: '/api/v1/platform/departments', label: (r) => String(r.name) } },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return <ResourcePage cfg={cfg} />;
}

export function EmployeesPage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Employees', singular: 'Employee', help: t('help.employees'), path: '/api/v1/platform/employees', perm: 'platform.employee',
    columns: [{ key: 'employeeNo', header: 'Employee No.' }, { key: 'fullName', header: 'Name' }, { key: 'jobTitle', header: 'Job Title' }, dateCol('joinDate', 'Join Date'), statusCol],
    fields: [
      { name: 'employeeNo', label: 'Employee No.', required: true },
      { name: 'fullName', label: 'Full Name', required: true },
      { name: 'departmentId', label: 'Department', type: 'reference', ref: { path: '/api/v1/platform/departments?filter[status]=active', label: (r) => String(r.name) } },
      { name: 'jobTitle', label: 'Job Title' },
      { name: 'email', label: 'E-mail', type: 'email' },
      { name: 'phone', label: 'Phone' },
      { name: 'joinDate', label: 'Join Date', type: 'date' },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return <ResourcePage cfg={cfg} />;
}

const methodTypes = ['cash', 'bank_transfer', 'virtual_account', 'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid'];
const methodLabel: Record<string, string> = {
  cash: 'Cash', bank_transfer: 'Bank Transfer', virtual_account: 'Virtual Account', qris: 'QRIS', card: 'Card', payment_gateway: 'Payment Gateway',
  member_account: 'Member Account', voucher_prepaid: 'Voucher & Prepaid',
};

type Setting = Schemas['Setting'];

/** Effective-dated availability of payment methods at the active property. */
function Availability() {
  const { can } = useAuth();
  const toast = useToast();
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const methods = useGet<Page<Row>>('/api/v1/billing/payment-methods?filter[status]=active&limit=100');
  const history = useGet<Page<Setting>>('/api/v1/billing/payment-method-settings?history=true');
  const current = useGet<Page<Setting>>('/api/v1/billing/payment-method-settings');
  const [pm, setPm] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [surcharge, setSurcharge] = useState('0');
  const [from, setFrom] = useState('');
  const add = useSend('POST', '/api/v1/billing/payment-method-settings', ['/api/v1/billing/payment-method-settings']);
  const inForce = new Set((current.data?.items ?? []).map((s) => s.id));
  return (
    <Card title="Availability at this property" icon="event_available" actions={can('billing.payment_method_setting.manage') &&
      <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={() => setOpen(true)}><Icon name="add" size={18} /> Add version</button>}>
      <DataTable rows={history.data?.items as unknown as Row[]} loading={history.isLoading}
        columns={[
          { key: 'paymentMethod', header: 'Payment Method' },
          { key: 'enabled', header: 'Enabled', render: (s) => <StatusPill status={s.enabled ? 'active' : 'inactive'} label={s.enabled ? 'Enabled' : 'Disabled'} /> },
          { key: 'surchargePercent', header: 'Surcharge %', align: 'right' },
          { key: 'effectiveFrom', header: 'Effective From', render: (s) => formatDateTime(s.effectiveFrom as string) },
          { key: 'inForce', header: '', render: (s) => (inForce.has(String(s.id)) ? <StatusPill status="active" label="In force" /> :
            new Date(String(s.effectiveFrom)) > new Date() ? <StatusPill status="scheduled" /> : <span className="oc-small oc-muted">Superseded</span>) },
        ]} />
      <Modal open={open} onClose={() => setOpen(false)} title="Add availability version" actions={<>
        <button className="oc-btn oc-btn-neutral" onClick={() => setOpen(false)}>Cancel</button>
        <button className="oc-btn oc-btn-ink" disabled={!pm || add.isPending} onClick={() => add.mutate({
          paymentMethodId: pm, enabled, surchargePercent: surcharge || '0', ...(from ? { effectiveFrom: new Date(from).toISOString() } : {}),
        }, { onSuccess: () => { setOpen(false); toast(t('common.saved')); } })}>Save</button>
      </>}>
        <div className="oc-form">
          <SelectField label="Payment Method" value={pm} onChange={setPm} required placeholder="Select…"
            options={(methods.data?.items ?? []).map((m) => ({ value: m.id, label: String(m.name) }))} />
          <TextField label="Surcharge %" inputMode="decimal" value={surcharge} onChange={setSurcharge} error={fieldErrors(add.error).surchargePercent} />
          <TextField label="Effective From" type="datetime-local" value={from} onChange={setFrom} help="Empty = now. Existing transactions keep the old version."
            error={fieldErrors(add.error).effectiveFrom} />
          <div className="oc-field"><span className="oc-label">&nbsp;</span><Checkbox label="Enabled at this property" checked={enabled} onChange={setEnabled} /></div>
        </div>
        <ErrorAlert error={Object.keys(fieldErrors(add.error)).length ? null : add.error} />
      </Modal>
    </Card>
  );
}

export function PaymentMethodsPage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Payment Methods', singular: 'Payment Method', help: t('help.paymentMethods'), path: '/api/v1/billing/payment-methods', perm: 'billing.payment_method',
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' },
      { key: 'methodType', header: 'Method Type', render: (r) => methodLabel[String(r.methodType)] }, { key: 'sortOrder', header: 'Order', align: 'right' }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, help: CODE_HELP },
      { name: 'name', label: 'Name', required: true },
      { name: 'methodType', label: 'Method Type', type: 'select', required: true, options: methodTypes.map((m) => ({ value: m, label: methodLabel[m] })), default: 'cash' },
      { name: 'sortOrder', label: 'Sort Order', type: 'number', default: 0 },
      { name: 'description', label: 'Description', type: 'textarea' },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return (
    <div className="oc-stack">
      <ResourcePage cfg={cfg} />
      <Availability />
    </div>
  );
}

function Calculator() {
  const boot = useBootstrap();
  const [amount, setAmount] = useState('100000');
  const [at, setAt] = useState('');
  const calc = useSend<Record<string, unknown>, Schemas['Breakdown']>('POST', '/api/v1/commercial/tax-service-rules:calculate');
  const b = calc.data;
  return (
    <Card title="Calculator" icon="calculate">
      <form className="oc-row-wrap" onSubmit={(e) => { e.preventDefault(); calc.mutate({ amount, ...(at ? { at: new Date(at).toISOString() } : {}) }); }}>
        <div style={{ width: 200 }}><TextField label="Amount" inputMode="decimal" value={amount} onChange={setAmount} /></div>
        <div style={{ width: 240 }}><TextField label="At (optional)" type="datetime-local" value={at} onChange={setAt} /></div>
        <button className="oc-btn oc-btn-ink" style={{ alignSelf: 'flex-end' }}>Calculate</button>
      </form>
      <ErrorAlert error={calc.error} />
      {b && (
        <div className="oc-stack" style={{ marginTop: 16 }}>
          <div className="oc-small oc-muted">Pricing mode: {b.pricingMode === 'nett' ? 'Nett (price includes tax & service)' : 'Plus-Plus (added on top)'}</div>
          <table className="oc-table">
            <tbody>
              <tr><td>Net amount</td><td className="oc-num" style={{ textAlign: 'right' }}>{formatMoney(b.netAmount, boot.currency)}</td></tr>
              {b.lines.map((l) => (
                <tr key={l.code}><td>{l.name} <span className="oc-muted oc-small">{l.ratePercent}% of {l.basis === 'net_amount' ? 'net' : 'net + service'}</span></td>
                  <td className="oc-num" style={{ textAlign: 'right' }}>{formatMoney(l.amount, boot.currency)}</td></tr>
              ))}
              <tr><td><strong>Total</strong></td><td className="oc-num" style={{ textAlign: 'right' }}><strong>{formatMoney(b.total, boot.currency)}</strong></td></tr>
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

export function TaxServicePage() {
  const { t } = useTranslation();
  const cfg: ResourceConfig = {
    title: 'Tax & Service', singular: 'Rule Version', help: t('help.taxService'), path: '/api/v1/commercial/tax-service-rules', perm: 'commercial.tax_service',
    noDelete: true,
    columns: [{ key: 'code', header: 'Code' }, { key: 'name', header: 'Name' }, { key: 'kind', header: 'Kind' },
      { key: 'ratePercent', header: 'Rate %', align: 'right' }, { key: 'pricingMode', header: 'Mode', render: (r) => (r.pricingMode === 'nett' ? 'Nett' : 'Plus-Plus') },
      { key: 'effectiveFrom', header: 'Effective From', render: (r) => formatDateTime(String(r.effectiveFrom)) }, statusCol],
    fields: [
      { name: 'code', label: 'Code', required: true, createOnly: true, help: 'Same code = new version of the same rule' },
      { name: 'name', label: 'Name', required: true },
      { name: 'kind', label: 'Kind', type: 'select', required: true, createOnly: true, options: [{ value: 'service', label: 'Service Charge' }, { value: 'tax', label: 'Tax' }], default: 'service' },
      { name: 'ratePercent', label: 'Rate (%)', type: 'decimal', required: true },
      { name: 'basis', label: 'Calculation Basis', type: 'select', required: true, options: [{ value: 'net_amount', label: 'Net amount' }, { value: 'net_plus_service', label: 'Net + service charge' }], default: 'net_amount' },
      { name: 'pricingMode', label: 'Pricing Mode', type: 'select', required: true, options: [{ value: 'plus_plus', label: 'Plus-Plus' }, { value: 'nett', label: 'Nett' }], default: 'plus_plus' },
      { name: 'sequence', label: 'Sequence', type: 'number', default: 1 },
      { name: 'effectiveFrom', label: 'Effective From', type: 'datetime', required: true, createOnly: true },
      { name: 'status', label: 'Status', type: 'select', options: [{ value: 'active', label: 'Active' }, { value: 'inactive', label: 'Inactive' }], default: 'active' },
    ],
  };
  return (
    <div className="oc-stack">
      <ResourcePage cfg={cfg} />
      <Calculator />
    </div>
  );
}

