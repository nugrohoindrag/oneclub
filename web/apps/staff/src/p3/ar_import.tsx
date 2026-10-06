import React, { useState } from 'react';
import { useSend } from '@oneclub/api-client';
import { Card, DataTable, ErrorAlert, Modal, TextArea, TextField } from '@oneclub/shell';
import { KV, money, type R } from '../p1/common';

// Corporate AR migration (PRD P3 FR-MIG-P3-02 / FR-MIG-P3-05): the open
// corporate invoices of the legacy system from the Excel workbook or its CSV
// export — preview, import, and the AR reconciliation against the control
// total signed off by the club (docs/runbooks/corporate-ar-migration-p3.md).

const HEADER = 'legacyNumber,corporateCode,customerCode,issueDate,dueDate,outstanding,originalTotal,description\n';

function toBase64(buf: ArrayBuffer): string {
  let s = '';
  const bytes = new Uint8Array(buf);
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

export function ImportCorporateAR({ onClose }: { onClose: () => void }) {
  const [csv, setCsv] = useState(HEADER);
  const [xlsx, setXlsx] = useState<{ name: string; data: string } | null>(null);
  const [expected, setExpected] = useState('');
  const [result, setResult] = useState<R | null>(null);
  const send = useSend<R, R>('POST', '/api/v1/billing/invoices:import', ['/api/v1/billing']);
  const run = (mode: string) => {
    const body: Record<string, unknown> = { mode };
    if (xlsx) body.xlsx = xlsx.data;
    else body.csv = csv;
    if (expected.trim()) body.expectedTotal = expected.trim();
    send.mutate(body as unknown as R, { onSuccess: setResult });
  };
  const pick = (f: File | undefined) => {
    setResult(null);
    if (!f) { setXlsx(null); return; }
    void f.arrayBuffer().then((b) => setXlsx({ name: f.name, data: toBase64(b) }));
  };
  const errors = (result?.errors as R[] | undefined) ?? [];
  const companies = (result?.companies as R[] | undefined) ?? [];
  return (
    <Modal open onClose={onClose} title="Import Corporate AR" wide actions={<>
      <button className="oc-btn oc-btn-neutral" onClick={onClose}>Close</button>
      <button className="oc-btn oc-btn-neutral" disabled={send.isPending} onClick={() => run('preview')}>Preview</button>
      <button className="oc-btn oc-btn-primary" disabled={send.isPending || !result || result.mode !== 'preview'} onClick={() => run('commit')}>Import</button>
    </>}>
      <p className="oc-muted">Open invoices of the legacy system, one row per invoice. Rows are matched by <code>legacyNumber</code>
        (invoice RH-…), so importing the same file again changes nothing. Preview first, then import.</p>
      <label className="oc-field">
        <span className="oc-label">Excel workbook (.xlsx, first sheet)</span>
        <input type="file" accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" aria-label="Excel workbook"
          onChange={(e) => pick(e.target.files?.[0])} />
      </label>
      {!xlsx && <TextArea label="…or CSV" value={csv} onChange={(v) => { setCsv(v); setResult(null); }} rows={8} />}
      <TextField label="Control total of the legacy AR" value={expected} onChange={(v) => { setExpected(v); setResult(null); }} type="number" />
      <ErrorAlert error={send.error} />
      {result && (
        <Card title={result.mode === 'preview' ? 'Preview' : 'Imported'} icon="upload">
          <KV items={[['Rows', String(result.totalRows)], [result.mode === 'preview' ? 'To create' : 'Created', String(result.imported)],
            ['Already imported', String(result.existing)], ['Rejected', String(result.failed)], ['File total', money(result.fileTotal)],
            ['Control total', result.expectedTotal ? money(result.expectedTotal) : '—'], ['Migrated open AR', money(result.migratedOpen)],
            ['Difference', money(result.difference)], ['Reconciled', result.reconciled ? 'Yes' : 'No']]} />
          {companies.length > 0 && <DataTable rows={companies.map((c, i) => ({ ...c, id: String(i) })) as R[]} columns={[
            { key: 'code', header: 'Code' }, { key: 'name', header: 'Company / customer' }, { key: 'invoices', header: 'Invoices', align: 'right' },
            { key: 'outstanding', header: 'Outstanding', align: 'right', render: (r) => money(r.outstanding) }]} />}
          {errors.length > 0 && <DataTable rows={errors.map((e, i) => ({ ...e, id: String(i) })) as R[]} columns={[{ key: 'row', header: 'Row' },
            { key: 'field', header: 'Field' }, { key: 'message', header: 'Problem' }]} />}
        </Card>
      )}
    </Modal>
  );
}
