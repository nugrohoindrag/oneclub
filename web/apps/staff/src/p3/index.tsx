import React from 'react';
import { RequirePermission } from '@oneclub/shell';
import { BANQUET_ROUTES } from './banquet';
import { BILLING_P3_ROUTES } from './billing';
import { COMMERCIAL_P3_ROUTES } from './commercial';
import { ENGAGEMENT_ROUTES } from './engagement';
import { SALES_ROUTES } from './sales';
import { TOURNAMENT_ROUTES } from './tournament';
import type { AreaRoute } from './types';
import { ACCOUNTING_ROUTES } from '../p4/accounting';
import { CMS_ROUTES } from '../p4/cms';
import { INVENTORY_ROUTES } from '../p4/inventory';
import { PROCUREMENT_ROUTES } from '../p4/procurement';
import { BI_ROUTES } from '../p5/bi';
import { CRM_P5_ROUTES } from '../p5/crm';
import { HR_ROUTES } from '../p5/hr';
import { HR_SERVICES_ROUTES } from '../p5/hr-services';
import { TIMESHEET_ROUTES } from '../p5/hr-timesheets';
import { HR_TIME_ROUTES } from '../p5/hr_time';
import { LEISURE_ROUTES } from '../p5/leisure';
import { PAYROLL_ROUTES } from '../p5/payroll';

// Back Office routes of PRD P3 (Commercial & Business Expansion) and PRD P4
// (Enterprise Back Office) and PRD P5 (People & Advanced Enterprise), one module per file at the paths of the server
// navigation (internal/platform/navigation).

const AREAS: AreaRoute[][] = [BILLING_P3_ROUTES, SALES_ROUTES, ENGAGEMENT_ROUTES, COMMERCIAL_P3_ROUTES, BANQUET_ROUTES, TOURNAMENT_ROUTES,
  INVENTORY_ROUTES, PROCUREMENT_ROUTES, ACCOUNTING_ROUTES, CMS_ROUTES, HR_ROUTES, HR_TIME_ROUTES, PAYROLL_ROUTES, CRM_P5_ROUTES, BI_ROUTES,
  LEISURE_ROUTES, HR_SERVICES_ROUTES, TIMESHEET_ROUTES];

export const P3_ROUTES = AREAS.flat().map((r) => ({ path: r.path, element: <RequirePermission perm={r.perm}>{r.element}</RequirePermission> }));
