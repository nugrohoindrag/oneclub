import { expect, test, type Page } from '@playwright/test';
import { TOTP } from 'otpauth';
import { CASHIER, DASHBOARD, PASSWORD, apiOf, email, login } from './helpers';

/**
 * PRD P5 §9 flows in the browser (FR-REL-P5-02): §9.1 Hire to First Pay (recruitment → certificates → pool shift → device
 * clock-in → overtime approved → payroll calculated, approved and posted → the payslip in the new employee's Employee Self
 * Service), Employee Self Service on the phone, §9.4 retention journey, §9.5 executive review, §9.6 Order of Merit, and the HR
 * migration reconciliation signed by HR and Finance. Back-office steps between the screens run through the API with the session
 * of a logged-in page. Run against a running instance with the demo data (see playwright.config.ts):
 * `cd web && pnpm e2e e2e/p5-flows.spec.ts`.
 */

const HR = 'hr@demo.oneclub.id';
const FINANCE = email('finance_manager'); // MFA enrolled afresh every run (e2e/setup.sql)
const EMPLOYEE = 'employee@demo.oneclub.id';
const SA = email('super_admin');
const stamp = String(Date.now()).slice(-5);
/** Time zone of the demo club: attendance and payroll work on club dates. */
const CLUB_TZ = process.env.E2E_CLUB_TZ ?? 'Asia/Jakarta';
const CLUB_OFFSET = process.env.E2E_CLUB_OFFSET ?? '+07:00';
/** The club date n days from today (YYYY-MM-DD). */
const day = (n: number) =>
  new Intl.DateTimeFormat('en-CA', { timeZone: CLUB_TZ, year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date(Date.now() + n * 86_400_000));
/** The instant of a club-local time on a date. */
const at = (date: string, hhmm: string) => new Date(`${date}T${hhmm}:00${CLUB_OFFSET}`).toISOString().replace(/\.\d+Z$/, 'Z');

/** Opens a screen and waits until its data has loaded. */
async function open(page: Page, url: string) {
  await page.goto(url);
  await page.waitForLoadState('networkidle');
}

/** API calls of a logged-in page at a given property (apiOf works at MAIN). */
async function apiAt(page: Page, property: string) {
  const call = async (method: string, path: string, body?: unknown) => {
    const r = await page.evaluate(async ([method, path, body, property]) => {
      const res = await fetch(path, { method, headers: { 'Content-Type': 'application/json', 'X-Property-Id': property as string },
        body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store' });
      return { status: res.status, body: await res.text() };
    }, [method, path, body, property] as const);
    expect(r.status < 300, `${method} ${path}: ${r.status} ${r.body}`).toBeTruthy();
    return r.body ? JSON.parse(r.body) : null;
  };
  return { get: (p: string) => call('GET', p), post: (p: string, b: unknown = {}) => call('POST', p, b) };
}

/**
 * The first login of a new employee: the initial password given by HR is replaced, and two-step verification is enrolled
 * when the role asks for it, in the order the login page asks.
 */
async function firstLogin(page: Page, base: string, user: string, initial: string, chosen: string, next: string) {
  await page.goto(`${base}/login?next=${encodeURIComponent(next)}`);
  await page.getByLabel(/^Email Address/).fill(user);
  await page.getByLabel(/^Password/).fill(initial);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  const code = page.getByLabel(/Authentication code|Kode autentikasi/);
  const fresh = page.getByLabel(/^New password|^Kata sandi baru/);
  const done = () => !new URL(page.url()).pathname.startsWith('/login');
  let secret = '';
  for (let step = 0; step < 4 && !done(); step++) {
    await Promise.race([
      page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15_000 }).catch(() => undefined),
      code.waitFor({ timeout: 15_000 }).catch(() => undefined),
      fresh.waitFor({ timeout: 15_000 }).catch(() => undefined),
    ]);
    if (done()) break;
    if (await code.isVisible().catch(() => false)) {
      if (!secret) secret = (await page.locator('code.oc-code').first().textContent())!.trim();
      await code.fill(new TOTP({ secret, digits: 6, period: 30 }).generate());
      await page.getByRole('button', { name: /^(Verify|Verifikasi)/ }).click();
      await code.waitFor({ state: 'detached', timeout: 15_000 }).catch(() => undefined);
    } else if (await fresh.isVisible().catch(() => false)) {
      const current = page.getByLabel(/^Current password|^Kata sandi saat ini/);
      if (await current.isVisible().catch(() => false)) await current.fill(initial);
      await fresh.fill(chosen);
      await page.locator('form').getByRole('button').last().click();
      await fresh.waitFor({ state: 'detached', timeout: 15_000 }).catch(() => undefined);
    }
  }
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

test('P5 §9.1 hire to first pay: the hired lifeguard works the pool shift, overtime approved, payroll posted, payslip in ESS', async ({ browser, page }) => {
  test.setTimeout(300_000);
  // A property of its own: its payroll period has no run yet, so the flow repeats on the same instance (Staging).
  await login(page, DASHBOARD, SA, '/');
  const main = await apiOf(page);
  const prop = await main.post('/api/v1/platform/properties', { code: `E2EH${stamp}`, name: `E2E Hire ${stamp}`, timezone: CLUB_TZ });
  const api = await apiAt(page, prop.id);
  const work = day(-1); // the shift worked yesterday (club date): devices deliver events up to 72 hours late
  const period = work.slice(0, 7);
  await api.post('/api/v1/accounting/book:load-template', { cutOverDate: `${period}-01` });
  const unit = await api.post('/api/v1/hris/org-units', { code: `PW${stamp}`, name: `Pool E2E ${stamp}`, unitType: 'section', costCenter: `CC${stamp}` });
  const pos = await api.post('/api/v1/hris/positions', { code: `PWL${stamp}`, name: `Pool Lifeguard ${stamp}`, orgUnitId: unit.id, workforceRole: 'lifeguard',
    requiredCertifications: ['LIFEGUARD', 'CPR_BLS'] });
  // EP-03: requisition → candidate → offer → hired (employee with a PKWT contract)
  const rq = await api.post('/api/v1/hris/job-requisitions', { positionId: pos.id, headcount: 1, contractType: 'pkwt', salaryMax: '5500000', targetStartDate: day(0) });
  await api.post(`/api/v1/hris/job-requisitions/${rq.id}:submit`, {});
  const name = `Lifeguard E2E ${stamp}`;
  const cand = await api.post('/api/v1/hris/candidates', { fullName: name, email: `lg${stamp}@e2e.test`, phone: `0812${stamp}9`, source: 'referral' });
  const app = await api.post('/api/v1/hris/applications', { requisitionId: rq.id, candidateId: cand.id });
  await api.post(`/api/v1/hris/applications/${app.id}:move-stage`, { stage: 'screening', screeningScore: '4' });
  await api.post(`/api/v1/hris/applications/${app.id}:move-stage`, { stage: 'interview' });
  const end = new Date(); end.setFullYear(end.getFullYear() + 1);
  const offer = await api.post(`/api/v1/hris/applications/${app.id}:offer`, { startDate: day(-40), endDate: end.toISOString().slice(0, 10), baseSalary: '5190000' });
  expect(offer.status).toBe('approved'); // no approval workflow at the new property
  await api.post(`/api/v1/hris/job-offers/${offer.id}:send`, {});
  await api.post(`/api/v1/hris/job-offers/${offer.id}:accept`, { reason: 'Signed' });
  const workEmail = `lg${stamp}@club.e2e`;
  const hire = await api.post(`/api/v1/hris/applications/${app.id}:hire`, { workEmail, createLogin: false });
  const eid = hire.employee.employeeId;
  // FR-HR-04: HR gives the new employee a login with an initial password (changed at the first login)
  const roles = await api.get('/api/v1/platform/roles?limit=500');
  const essRole = roles.items.find((r: { code: string }) => r.code === 'employee_self_service');
  const user = await api.post('/api/v1/platform/users', { email: workEmail, fullName: name, password: PASSWORD, locale: 'en',
    assignments: [{ roleId: essRole.id, propertyId: prop.id }] });
  await api.post(`/api/v1/hris/employees/${eid}:create-account`, { userId: user.id });
  // EP-04 certificates, EP-06 pool shift published
  const types = (await api.get('/api/v1/hris/certification-types?limit=100')).items;
  for (const code of ['LIFEGUARD', 'CPR_BLS']) {
    await api.post('/api/v1/hris/certifications', { certificationTypeId: types.find((x: { code: string }) => x.code === code).id, employeeId: eid,
      issuedOn: day(-30), certificateNo: `${code}-${stamp}` });
  }
  const tmpl = await api.post('/api/v1/hris/shift-templates', { code: `PWP${stamp}`, name: 'Pool E2E', startTime: '07:00', endTime: '15:00', breakMinutes: 60,
    workforceRole: 'lifeguard' });
  const sch = await api.post('/api/v1/hris/schedules', { orgUnitId: unit.id, periodStart: work, periodEnd: work });
  await api.post(`/api/v1/hris/schedules/${sch.id}:assign`, { assignments: [{ employeeId: eid, workDate: work, shiftTemplateId: tmpl.id }] });
  await api.post(`/api/v1/hris/schedules/${sch.id}:publish`, {});
  // EP-07: face recognition on the pool gate (mock adapter)
  const dev = await api.post('/api/v1/hris/attendance-devices', { code: `PWD${stamp}`, name: `Pool gate ${stamp}`, deviceKind: 'biometric' });
  await api.post(`/api/v1/hris/attendance-profiles/${eid}:consent`, { consent: true, signedOn: day(-20) });
  await api.post(`/api/v1/hris/attendance-devices/${dev.id}:simulate`, { employeeId: eid, method: 'face_recognition', direction: 'in', occurredAt: at(work, '06:55'), eventId: 'E2E-IN' });
  await api.post(`/api/v1/hris/attendance-devices/${dev.id}:simulate`, { employeeId: eid, method: 'face_recognition', direction: 'out', occurredAt: at(work, '17:05'), eventId: 'E2E-OUT' });
  const ot = await api.post('/api/v1/hris/overtime-requests', { employeeId: eid, workDate: work, startTime: '15:00', endTime: '17:00', reason: 'Swimming gala' });
  expect(ot.status).toBe('approved'); // approval engine: no workflow at the new property ⇒ approved at once

  // In the browser at the new property: the attendance day and the approved 2 hours of overtime (EP-08).
  await open(page, `${DASHBOARD}/hris/attendance`); // a fresh page knows the new property
  await page.getByRole('combobox', { name: 'Property' }).selectOption({ label: prop.name });
  await open(page, `${DASHBOARD}/hris/attendance`);
  await expect(page.getByRole('heading', { name: 'Attendance', level: 1 })).toBeVisible();
  await open(page, `${DASHBOARD}/hris/overtime`);
  await page.getByRole('group', { name: 'Filter' }).getByRole('button', { name: 'approved', exact: true }).click();
  await expect(page.locator('table.oc-table tbody tr', { hasText: name }).first()).toBeVisible();
  await expect.poll(async () => (await api.get(`/api/v1/hris/time-summary?from=${work}&to=${work}&employeeId=${eid}`)).items[0].overtime.payableHours).toBe('2');

  // EP-09/10/15: the payroll of the period is calculated, approved and posted on the Payroll screen.
  const run = await api.post('/api/v1/hris/payroll-runs', { runType: 'regular', periodCode: period });
  await open(page, `${DASHBOARD}/hris/payroll/runs/${run.id}`);
  await page.getByRole('button', { name: 'Calculate', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Submit for approval' })).toBeVisible();
  const slips = (await api.get(`/api/v1/hris/payroll-runs/${run.id}/payslips`)).items;
  expect(slips.map((x: { employeeId: string }) => x.employeeId)).toEqual([eid]);
  const slip = await api.get(`/api/v1/hris/payslips/${slips[0].id}`);
  expect(slip.lines.map((l: { code: string }) => l.code)).toEqual(expect.arrayContaining(['BASIC', 'OVERTIME', 'BPJS_JHT_EE']));
  expect(Number(slip.net)).toBeGreaterThan(0);
  await expect(page.locator('table.oc-table tbody tr', { hasText: name }).first()).toBeVisible();
  await page.getByRole('button', { name: 'Submit for approval' }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Submit for approval' }).click();
  // approved at once (no approval workflow at the new property), then posted: journal and payslips released
  await page.getByRole('button', { name: 'Post', exact: true }).click();
  await expect(page.getByRole('button', { name: /Mark paid/ })).toBeVisible();
  expect((await api.get(`/api/v1/hris/payroll-runs/${run.id}`)).status).toBe('posted');

  // FR-ESS-04: the new employee logs in for the first time on the phone and opens the payslip of the period.
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const phone = await ctx.newPage();
  await firstLogin(phone, CASHIER, workEmail, PASSWORD, `Pay#First${stamp}x`, '/ops/ess/payslip');
  await open(phone, `${CASHIER}/ops/ess/payslip`);
  await expect(phone.getByRole('heading', { name: 'Payslip', level: 1 })).toBeVisible();
  const mine = phone.locator('table.oc-table tbody tr', { hasText: period }).first();
  await expect(mine).toBeVisible();
  await mine.click();
  const sheet = phone.getByRole('dialog');
  await expect(sheet.getByRole('heading', { name: `Payslip ${period}` })).toBeVisible();
  await expect(sheet.getByText(/^Overtime/).first()).toBeVisible();
  // payslips are never kept in the offline cache (FR-OPS-P5-05)
  const cached = await phone.evaluate(async () => {
    const urls: string[] = [];
    for (const n of await caches.keys()) for (const r of await (await caches.open(n)).keys()) urls.push(r.url);
    return urls;
  });
  expect(cached.filter((u) => /\/api\/.*(payslip|payroll|salary)/.test(u))).toEqual([]);
  await ctx.close();
});

test('ESS on the phone: the employee opens My Schedule, Clock In / Out and the push notification card of the profile', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
  const page = await ctx.newPage();
  // Employee Self Service is part of the Operational app (§16 #6), on the cashier / ops domain
  await login(page, CASHIER, EMPLOYEE, '/ops/ess');
  await open(page, `${CASHIER}/ops/ess`);
  await expect(page.getByRole('heading', { name: /My Schedule|Jadwal Saya/ }).first()).toBeVisible();
  await open(page, `${CASHIER}/ops/ess/schedule`);
  await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();
  await open(page, `${CASHIER}/ops/ess/clock`);
  await expect(page.getByRole('heading', { level: 1 }).first()).toBeVisible();
  await open(page, `${CASHIER}/ops/ess/profile`);
  await expect(page.getByText(/Employment|Kepegawaian/).first()).toBeVisible();
  await expect(page.getByText(/Push notifications|Notifikasi push/).first()).toBeVisible();
  // payroll data is never cached by the service worker (FR-OPS-P5-05)
  const cached = await page.evaluate(async () => {
    const urls: string[] = [];
    for (const n of await caches.keys()) for (const r of await (await caches.open(n)).keys()) urls.push(r.url);
    return urls;
  });
  expect(cached.filter((u) => /\/api\/.*(payslip|payroll|salary)/.test(u))).toEqual([]);
  await ctx.close();
});

test('P5 §9.4 retention journey: CRM opens the win-back journey and its performance', async ({ page }) => {
  await login(page, DASHBOARD, SA, '/crm/journeys');
  await page.getByRole('combobox', { name: 'Property' }).selectOption({ label: 'Modern Golf & Country Club' }); // the demo journeys
  await open(page, `${DASHBOARD}/crm/journeys`);
  await expect(page.getByRole('heading', { name: 'Journeys', level: 1 })).toBeVisible();
  const winback = page.locator('table.oc-table tbody tr', { hasText: /win-back|Win-back|WINBACK/ }).first();
  await expect(winback).toBeVisible();
  await winback.click();
  await expect(page.getByRole('heading', { name: /JRN-WINBACK/, level: 1 })).toBeVisible();
  await page.getByRole('tab', { name: 'Performance' }).click();
  await expect(page.getByRole('tab', { name: 'Performance', selected: true })).toBeVisible();
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
  await expect(page.getByRole('button', { name: 'Sign as HR Manager' })).toHaveCount(0);
  await expect(page.getByText('Test difference explained').first()).toBeVisible();
  const fin = await (await browser.newContext()).newPage();
  await login(fin, DASHBOARD, FINANCE, '/hris/migration-reconciliation');
  await open(fin, `${DASHBOARD}/hris/migration-reconciliation`);
  await fin.locator('table.oc-table tbody tr', { hasText: `HR Excel E2E ${stamp}` }).first().click();
  await fin.getByRole('button', { name: 'Sign as Finance Manager' }).click();
  await fin.getByLabel(/Explanation of the differences/).fill('Agreed with HR');
  await fin.getByRole('button', { name: 'Sign off' }).click();
  await expect(fin.getByRole('button', { name: 'Sign as Finance Manager' })).toHaveCount(0);
  await expect(fin.getByText('Agreed with HR').first()).toBeVisible();
  await expect(fin.getByRole('button', { name: 'Cancel reconciliation' })).toHaveCount(0); // signed off: nothing left to do
});
