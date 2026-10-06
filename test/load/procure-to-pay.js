// Procure-to-pay throughput (PRD P4 FR-REL-P4-01 / FR-REL-P4-02 §9.1, NFR
// Performance): several buyers run the whole cycle in parallel — direct
// purchase order (submit), approval by the Procurement Manager, goods
// receipt at the warehouse (stock + GRNI journal), vendor invoice matched
// 3-way (AP journal) — on Staging with the Release 4 master data.
//
// Targets: p95 of each step < 1.5 s, every cycle ends with an approved
// (matched) vendor invoice, no 5xx. Afterwards the GRNI account and the
// AP control equal the sub-ledgers (Accounting → Closing → Reconciliations).
//
//   k6 run -e BASE_URL=... -e PROPERTY_ID=... -e BUYER_EMAIL=... -e BUYER_PASSWORD=... \
//          -e APPROVER_EMAIL=... -e APPROVER_PASSWORD=... -e SUPPLIER_ID=... -e WAREHOUSE_ID=... -e ITEM_ID=... \
//          [-e CYCLES=200] test/load/procure-to-pay.js
import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter, Trend } from 'k6/metrics';
import { BASE, headers, session, uuid } from './lib.js';

const stepTime = new Trend('p2p_step_ms', true);
const matched = new Counter('p2p_matched_invoices');

export const options = {
  setupTimeout: '2m',
  scenarios: {
    buyers: { executor: 'shared-iterations', iterations: Number(__ENV.CYCLES || 200), vus: 10, maxDuration: '20m' },
  },
  thresholds: {
    p2p_step_ms: ['p(95)<1500'],
    p2p_matched_invoices: [`count>=${Number(__ENV.CYCLES || 200)}`],
    http_req_failed: ['rate<0.01'],
  },
};

export function setup() {
  for (const k of ['SUPPLIER_ID', 'WAREHOUSE_ID', 'ITEM_ID']) if (!__ENV[k]) fail(`${k} is required`);
  return {
    buyer: session(__ENV.BUYER_EMAIL, __ENV.BUYER_PASSWORD),
    approver: session(__ENV.APPROVER_EMAIL || __ENV.BUYER_EMAIL, __ENV.APPROVER_PASSWORD || __ENV.BUYER_PASSWORD),
  };
}

function step(name, fn) {
  const t0 = Date.now();
  const r = fn();
  stepTime.add(Date.now() - t0, { step: name });
  return r;
}

export default function (data) {
  const p2p = `${BASE}/api/v1/procurement`;
  const qty = String(10 + Math.floor(Math.random() * 40));
  const po = step('purchase_order', () => http.post(`${p2p}/purchase-orders`, JSON.stringify({ supplierId: __ENV.SUPPLIER_ID, warehouseId: __ENV.WAREHOUSE_ID,
    submit: true, lines: [{ itemId: __ENV.ITEM_ID, quantity: qty, unitPrice: '12500' }] }), headers(data.buyer, { 'Idempotency-Key': uuid() })));
  if (!check(po, { 'PO created': (x) => x.status === 201 })) return;
  let order = JSON.parse(po.body);
  if (order.status === 'pending_approval') {
    const ap = step('approve', () => http.post(`${p2p}/purchase-orders/${order.id}:approve`, '{}', headers(data.approver)));
    if (!check(ap, { 'PO approved': (x) => x.status === 200 })) return;
    order = JSON.parse(ap.body);
  }
  const line = order.lines[0].id;
  const gr = step('goods_receipt', () => http.post(`${p2p}/goods-receipts`, JSON.stringify({ purchaseOrderId: order.id, deliveryNoteNo: `SJ-${uuid().slice(0, 8)}`,
    lines: [{ purchaseOrderLineId: line, acceptedQuantity: qty }] }), headers(data.buyer, { 'Idempotency-Key': uuid() })));
  if (!check(gr, { 'goods received': (x) => x.status === 201 })) return;
  const vi = step('vendor_invoice', () => http.post(`${p2p}/vendor-invoices`, JSON.stringify({ supplierId: __ENV.SUPPLIER_ID,
    supplierInvoiceNo: `LOAD-${uuid().slice(0, 12)}`, match: true, lines: [{ purchaseOrderLineId: line, quantity: qty, unitPrice: '12500' }] }),
  headers(data.buyer, { 'Idempotency-Key': uuid() })));
  if (check(vi, { 'vendor invoice matched': (x) => x.status === 201 && JSON.parse(x.body).status === 'approved' })) matched.add(1);
}
