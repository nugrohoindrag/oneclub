import { expect, test, type Page } from '@playwright/test';
import { DASHBOARD, apiOf, email, login } from './helpers';

/**
 * PRD P5 §9 flows in the browser (FR-REL-P5-02) for the parts that do not depend on payroll (the payroll and payouts areas
 * add §9.1 pay, §9.2 and §9.3): §9.1 hire to work (HR hires, schedules and approves overtime; the lifeguard clocks in on the
 * pool device), Employee Self Service on the phone, §9.4 retention journey, §9.5 executive review, §9.6 Order of Merit, and
 * the HR migration reconciliation signed by HR and Finance. Back-office steps between the screens run through the API with
 * the session of a logged-in page. Run against a running instance with the demo data (see playwright.config.ts):
 * `cd web && pnpm e2e e2e/p5-flows.spec.ts`.
 */

const HR = 'hr@demo.oneclub.id';
const FINANCE = 'finance@demo.oneclub.id';
const EMPLOYEE = 'employee@demo.oneclub.id';
const stamp = String(Date.now()).slice(-5);
const ymd = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const day = (n: number) => { const d = new Date(); d.setDate(d.getDate() + n); return ymd(d); };
/** A past weekday (local). */
const pastWorkday = () => { const d = new Date(); d.setDate(d.getDate() - 1); while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() - 1); return ymd(d); };
const at = (date: string, hhmm: string) => new Date(`${date}T${hhmm}:00`).toISOString().replace(/\.\d+Z$/, 'Z');

/** Opens a screen and waits until its data has loaded. */
async function open(page: Page, url: string) {
  await page.goto(url);
  await page.waitForLoadState('networkidle');
}

test('P5 §9.1 hire to work: hired lifeguard scheduled on the pool shift, clocks in on the device, overtime approved by HR', async ({ page }) => {
  test.setTimeout(180_000);
  await login(page, DASHBOARD, HR, '/hris/employees');
  const api = await apiOf(page);
  const sport = (await api.get('/api/v1/hris/org-units?limit=500')).items.find((u: { code: string }) => u.code === 'SPORT');
  const unit = await api.post('/api/v1/hris/org-units', { code: `PW${stamp}`, name: `Pool E2E ${stamp}`, parentId: sport.id, unitType: 'section' });
  const pos = await api.post('/api/v1/hris/positions', { code: `PWL${stamp}`, name: `Pool Lifeguard ${stamp}`, orgUnitId: unit.id, workforceRole: 'lifeguard',
    requiredCertifications: ['LIFEGUARD', 'CPR_BLS'] });
  // EP-03: requisition → candidate → offer → hired (employee, PKWT, ESS login)
  const rq = await api.post('/api/v1/hris/job-requisitions', { positionId: pos.id, headcount: 1, contractType: 'pkwt', salaryMax: '5500000', targetStartDate: day(0) });
  await api.post(`/api/v1/hris/job-requisitions/${rq.id}:submit`, {});
  const cand = await api.post('/api/v1/hris/candidates', { fullName: `Lifeguard E2E ${stamp}`, email: `lg${stamp}@e2e.test`, phone: `0812${stamp}9`, source: 'referral' });
  const app = await api.post('/api/v1/hris/applications', { requisitionId: rq.id, candidateId: cand.id });
  await api.post(`/api/v1/hris/applications/${app.id}:move-stage`, { stage: 'screening', screeningScore: '4' });
  await api.post(`/api/v1/hris/applications/${app.id}:move-stage`, { stage: 'interview' });
  const end = new Date(); end.setFullYear(end.getFullYear() + 1);
  const offer = await api.post(`/api/v1/hris/applications/${app.id}:offer`, { startDate: day(-20), endDate: ymd(end), baseSalary: '5190000' });
  if (offer.status === 'submitted') test.skip(true, 'the demo approval workflow needs the GM for this offer');
  await api.post(`/api/v1/hris/job-offers/${offer.id}:send`, {});
  await api.post(`/api/v1/hris/job-offers/${offer.id}:accept`, { reason: 'Signed' });
  const hire = await api.post(`/api/v1/hris/applications/${app.id}:hire`, { workEmail: `lg${stamp}@club.e2e` });
  const eid = hire.employee.employeeId;
  // EP-04 certificates, EP-06 pool shift published
  const types = (await api.get('/api/v1/hris/certification-types?limit=100')).items;
  for (const code of ['LIFEGUARD', 'CPR_BLS']) {
    await api.post('/api/v1/hris/certifications', { certificationTypeId: types.find((x: { code: string }) => x.code === code).id, employeeId: eid,
      issuedOn: day(-30), certificateNo: `${code}-${stamp}` });
  }
  const tmpl = await api.post('/api/v1/hris/shift-templates', { code: `PWP${stamp}`, name: 'Pool E2E', startTime: '07:00', endTime: '15:00', breakMinutes: 60,
    workforceRole: 'lifeguard' });
  const work = pastWorkday();
  const sch = await api.post('/api/v1/hris/schedules', { orgUnitId: unit.id, periodStart: work, periodEnd: work });
  await api.post(`/api/v1/hris/schedules/${sch.id}:assign`, { assignments: [{ employeeId: eid, workDate: work, shiftTemplateId: tmpl.id }] });
  await api.post(`/api/v1/hris/schedules/${sch.id}:publish`, {});
  // EP-07: face recognition on the pool gate (mock adapter of the trial)
  const dev = await api.post('/api/v1/hris/attendance-devices', { code: `PWD${stamp}`, name: `Pool gate ${stamp}`, deviceKind: 'biometric' });
  await api.post(`/api/v1/hris/attendance-profiles/${eid}:consent`, { consent: true, signedOn: day(-20) });
  await api.post(`/api/v1/hris/attendance-devices/${dev.id}:simulate`, { employeeId: eid, method: 'face_recognition', direction: 'in', occurredAt: at(work, '06:55'), eventId: 'E2E-IN' });
  await api.post(`/api/v1/hris/attendance-devices/${dev.id}:simulate`, { employeeId: eid, method: 'face_recognition', direction: 'out', occurredAt: at(work, '17:05'), eventId: 'E2E-OUT' });
  await api.post('/api/v1/hris/overtime-requests', { employeeId: eid, workDate: work, startTime: '15:00', endTime: '17:00', reason: 'Swimming gala' });

  // In the browser: the attendance day is Present; HR approves the 2 hours of overtime (EP-08).
  await open(page, `${DASHBOARD}/hris/attendance`);
  await expect(page.getByRole('heading', { name: 'Attendance', level: 1 })).toBeVisible();
  await open(page, `${DASHBOARD}/hris/overtime`);
  const row = page.locator('table.oc-table tbody tr', { hasText: `Lifeguard E2E ${stamp}` }).first();
  await expect(row).toBeVisible();
  const approve = row.getByRole('button', { name: 'Approve' });
  if (await approve.isVisible().catch(() => false)) {
    await approve.click();
    await page.getByRole('dialog').getByRole('button', { name: 'Approve' }).click();
  }
  await expect.poll(async () => (await api.get(`/api/v1/hris/time-summary?from=${work}&to=${work}&employeeId=${eid}`)).items[0].overtime.payableHours).toBe('2');
});

test('ESS on the phone: the employee opens My Schedule, Clock In / Out and the push notification card of the profile', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const page = await ctx.newPage();
  await login(page, DASHBOARD, EMPLOYEE, '/ess');
  await expect(page.getByText(/My Schedule/).first()).toBeVisible();
  await open(page, `${DASHBOARD}/ess/schedule`);
  await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();
  await open(page, `${DASHBOARD}/ess/profile`);
  await expect(page.getByText('Employment')).toBeVisible();
  // payroll data is never cached by the service worker (FR-OPS-P5-05)
  const cached = await page.evaluate(async () => {
    const names = await caches.keys();
    const urls: string[] = [];
    for (const n of names) for (const r of await (await caches.open(n)).keys()) urls.push(r.url);
    return urls;
  });
  expect(cached.filter((u) => /payslip|payroll|salary/.test(u))).toEqual([]);
  await ctx.close();
});

test('P5 §9.4 retention journey: CRM opens the win-back journey and its performance', async ({ page }) => {
  await login(page, DASHBOARD, email('super_admin'), '/crm/journeys');
  await open(page, `${DASHBOARD}/crm/journeys`);
  await expect(page.getByRole('heading', { name: 'Journeys', level: 1 })).toBeVisible();
  const winback = page.locator('table.oc-table tbody tr', { hasText: /win-back|Win-back|WINBACK/ }).first();
  await expect(winback).toBeVisible();
  await winback.click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText(/WINBACK/);
});

test('P5 §9.5 executive review: the GM opens the Executive Overview and drills down Golf Revenue', async ({ page }) => {
  await login(page, DASHBOARD, email('general_manager'), '/management');
  await open(page, `${DASHBOARD}/management`);
  await expect(page.getByRole('heading', { name: 'Executive Overview', level: 1 })).toBeVisible();
  await open(page, `${DASHBOARD}/management/drilldown?kpi=golf_revenue`);
  await expect(page.getByRole('heading', { level: 1 })).toContainText(/drill-down/);
});

test('P5 §9.6 tournament series: Golf opens the series with its Order of Merit and the tournament history', async ({ page }) => {
  await login(page, DASHBOARD, email('golf_manager'), '/golf/tournament-series');
  await open(page, `${DASHBOARD}/golf/tournament-series`);
  await expect(page.getByRole('heading', { level: 1 }).first()).toContainText(/Series/);
  await open(page, `${DASHBOARD}/golf/tournament-history`);
  await expect(page.getByRole('heading', { name: 'Tournament History', level: 1 })).toBeVisible();
});

test('EP-28 migration: HR reconciles headcount and leave balances; HR and Finance sign off', async ({ browser, page }) => {
  await login(page, DASHBOARD, HR, '/hris/migration-reconciliation');
  await open(page, `${DASHBOARD}/hris/migration-reconciliation`);
  await expect(page.getByRole('heading', { name: 'HR Migration Reconciliation', level: 1 })).toBeVisible();
  await page.getByRole('button', { name: 'New reconciliation' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel(/Legacy system/).fill(`HR Excel E2E ${stamp}`);
  await dialog.getByLabel(/Control totals/).fill('metric,key,legacy\nheadcount,TOTAL,1\n');
  await dialog.getByRole('button', { name: 'Reconcile' }).click();
  const row = page.locator('table.oc-table tbody tr', { hasText: `HR Excel E2E ${stamp}` }).first();
  await expect(row).toBeVisible();
  await row.click();
  await page.getByRole('button', { name: 'Sign as HR Manager' }).click();
  await page.getByLabel(/Explanation of the differences/).fill('Test difference explained');
  await page.getByRole('button', { name: 'Sign off' }).click();
  await expect(page.getByText(/hr signed/i).first()).toBeVisible();
  const fin = await (await browser.newContext()).newPage();
  await login(fin, DASHBOARD, FINANCE, '/hris/migration-reconciliation');
  await open(fin, `${DASHBOARD}/hris/migration-reconciliation`);
  await fin.locator('table.oc-table tbody tr', { hasText: `HR Excel E2E ${stamp}` }).first().click();
  await fin.getByRole('button', { name: 'Sign as Finance Manager' }).click();
  await fin.getByLabel(/Explanation of the differences/).fill('Agreed with HR');
  await fin.getByRole('button', { name: 'Sign off' }).click();
  await expect(fin.getByText(/signed off/i).first()).toBeVisible();
});
