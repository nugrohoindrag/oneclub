import React from 'react';
import { Navigate, useRoutes } from 'react-router';
import {
  ApiKeysPage, ApprovalDetailPage, ApprovalWorkflowsPage, ApprovalsPage, AuditLogsPage, BackgroundJobsPage, BrandingPage, BridgeAgentsPage,
  CustomerInstancePage, DepartmentsPage, DevicesPage, EmployeesPage, FeatureFlagsPage, FeaturesPage, IntegrationLogsPage, IntegrationsPage,
  LocalizationPage, MasterDataImportPage, NotFoundPage, NotificationHistoryPage, NotificationSettingsPage, NotificationsPage, OrganizationPage,
  PaymentMethodsPage, ProfilePage, PropertiesPage, RequirePermission, ResourcePage, RolesPage, RulesPage, SidebarLayout, SystemSettingsPage,
  TaxServicePage, UsersPage, VenuesPage,
} from '@oneclub/shell';
import { DashboardPage, ExportsPage, MODULE_PAGES, ModulePage, ReportRunPage, ReportsPage, SettingsHomePage } from '../pages';
import {
  AvailabilityPage, BookingHistoryPage, BookingNewPage, BookingsPage, CaddiesPage, CancellationsPage, CheckInPage, CoursePage, FlightsPage, GolfCartsPage,
  GolfSettingsPage, PlayersPage, RainChecksPage, StarterPage, TeeSheetPage,
} from '../p1/golf';
import {
  ApplicationsPage, CardsPage, CorporateAccountsPage, Customer360Page, CustomerAccountsPage, CustomersPage, DepositsPage, EffectiveDatesPage, FoliosPage,
  MemberChargesPage, MembersPage, MembershipHistoryPage, PaymentsPage, PricingRulesPage, RatePlansPage, ReconciliationPage, RefundsPage, RenewalsPage,
  packageCfg, programCfg, typeCfg,
} from '../p1/business';
import { HUBS, P2_ROUTES } from '../p2';
import { P3_ROUTES } from '../p3';
import { ACCOMMODATION_ROUTES } from '../accommodation/routes';
import { SPORT_ADMIN_ROUTES } from '../sport/routes';

// Back Office area: module paths at the root, so e-mail links keep working (Technical Doc §6.1).

const rp = (perm: string, el: React.ReactNode) => <RequirePermission perm={perm}>{el}</RequirePermission>;

/** P1 Golf Core MVP pages (PRD P1 §6.1). */
const p1 = [
  { path: 'golf', element: rp('golf.tee_sheet.view', <TeeSheetPage />) },
  { path: 'golf/tee-sheet', element: rp('golf.tee_sheet.view', <TeeSheetPage />) },
  { path: 'golf/bookings', element: rp('golf.booking.view', <BookingsPage />) },
  { path: 'golf/bookings/new', element: rp('golf.booking.create', <BookingNewPage />) },
  { path: 'golf/flights', element: rp('golf.flight.view', <FlightsPage />) },
  { path: 'golf/players', element: rp('golf.booking.view', <PlayersPage />) },
  { path: 'golf/caddies', element: rp('golf.caddy.view', <CaddiesPage />) },
  { path: 'golf/golf-carts', element: rp('golf.golf_cart.view', <GolfCartsPage />) },
  { path: 'golf/course', element: rp('golf.course.view', <CoursePage />) },
  { path: 'golf/check-in', element: rp('golf.check_in.perform', <CheckInPage />) },
  { path: 'golf/starter', element: rp('golf.starter.view', <StarterPage />) },
  { path: 'golf/rain-checks', element: rp('golf.rain_check.view', <RainChecksPage />) },
  { path: 'golf/settings', element: rp('golf.tee_sheet.view', <GolfSettingsPage />) },
  { path: 'membership', element: rp('membership.member.view', <MembersPage />) },
  { path: 'membership/members', element: rp('membership.member.view', <MembersPage />) },
  { path: 'membership/programs', element: rp('membership.program.view', <ResourcePage cfg={programCfg} />) },
  { path: 'membership/types', element: rp('membership.program.view', <ResourcePage cfg={typeCfg} />) },
  { path: 'membership/packages', element: rp('membership.program.view', <ResourcePage cfg={packageCfg} />) },
  { path: 'membership/applications', element: rp('membership.application.view', <ApplicationsPage />) },
  { path: 'membership/cards', element: rp('membership.card.view', <CardsPage />) },
  { path: 'membership/renewals', element: rp('membership.membership.view', <RenewalsPage />) },
  { path: 'membership/history', element: rp('membership.membership.view', <MembershipHistoryPage />) },
  { path: 'booking', element: rp('golf.booking.view', <BookingsPage title="All Bookings" noDate />) },
  { path: 'booking/all', element: rp('golf.booking.view', <BookingsPage title="All Bookings" noDate />) },
  { path: 'booking/availability', element: rp('golf.tee_sheet.view', <AvailabilityPage />) },
  { path: 'booking/history', element: rp('golf.booking.view', <BookingHistoryPage />) },
  { path: 'booking/cancellations', element: rp('golf.booking.view', <CancellationsPage />) },
  { path: 'crm', element: rp('crm.customer.view', <CustomersPage />) },
  { path: 'crm/customers', element: rp('crm.customer.view', <CustomersPage />) },
  { path: 'crm/customer-360', element: rp('crm.customer_overview.view', <Customer360Page />) },
  { path: 'crm/corporate-accounts', element: rp('crm.corporate_account.view', <CorporateAccountsPage />) },
  { path: 'commercial', element: rp('commercial.pricing.view', <RatePlansPage />) },
  { path: 'commercial/pricing/rate-plans', element: rp('commercial.pricing.view', <RatePlansPage />) },
  { path: 'commercial/pricing/rules', element: rp('commercial.pricing.view', <PricingRulesPage />) },
  { path: 'commercial/pricing/effective-dates', element: rp('commercial.pricing.view', <EffectiveDatesPage />) },
  { path: 'billing', element: rp('billing.folio.view', <FoliosPage />) },
  { path: 'billing/folios', element: rp('billing.folio.view', <FoliosPage />) },
  { path: 'billing/customer-accounts', element: rp('billing.customer_account.view', <CustomerAccountsPage />) },
  { path: 'billing/payments', element: rp('billing.payment.view', <PaymentsPage />) },
  { path: 'billing/deposits', element: rp('billing.payment.view', <DepositsPage />) },
  { path: 'billing/refunds', element: rp('billing.refund.view', <RefundsPage />) },
  { path: 'billing/member-charges', element: rp('billing.customer_account.view', <MemberChargesPage />) },
  { path: 'billing/reconciliation', element: rp('billing.reconciliation.view', <ReconciliationPage />) },
];
const P1_MODULES = new Set(['golf', 'membership', 'booking', 'crm', 'commercial']);
HUBS.forEach((h) => P1_MODULES.add(h.path)); // P2 hubs replace the module placeholders
P1_MODULES.add('sport-club'); // Dashboard Admin Sport Club (sport/routes.tsx)

const settings = [
  { path: 'settings', element: <SettingsHomePage /> },
  { path: 'settings/organization', element: <RequirePermission perm="platform.organization.view"><OrganizationPage /></RequirePermission> },
  { path: 'settings/organization/properties', element: <RequirePermission perm="platform.property.view"><PropertiesPage /></RequirePermission> },
  { path: 'settings/organization/departments', element: <RequirePermission perm="platform.department.view"><DepartmentsPage /></RequirePermission> },
  { path: 'settings/organization/employees', element: <RequirePermission perm="platform.employee.view"><EmployeesPage /></RequirePermission> },
  { path: 'settings/customer-instance', element: <RequirePermission perm="platform.instance.view"><CustomerInstancePage /></RequirePermission> },
  { path: 'settings/venues', element: <RequirePermission perm="platform.venue.view"><VenuesPage /></RequirePermission> },
  { path: 'settings/courses', element: <Navigate to="/golf/course" replace /> },
  { path: 'settings/users', element: <RequirePermission perm="platform.user.view"><UsersPage /></RequirePermission> },
  { path: 'settings/roles', element: <RequirePermission perm="platform.role.view"><RolesPage /></RequirePermission> },
  { path: 'settings/features', element: <RequirePermission perm="platform.module.view"><FeaturesPage /></RequirePermission> },
  { path: 'settings/feature-configuration', element: <RequirePermission perm="platform.feature_flag.view"><FeatureFlagsPage /></RequirePermission> },
  { path: 'settings/business-rules', element: <RequirePermission perm="platform.business_rule.view"><RulesPage kind="business_rule" /></RequirePermission> },
  { path: 'settings/club-policies', element: <RequirePermission perm="platform.club_policy.view"><RulesPage kind="club_policy" /></RequirePermission> },
  { path: 'settings/notifications', element: <RequirePermission perm="platform.notification_template.view"><NotificationSettingsPage /></RequirePermission> },
  { path: 'settings/integrations', element: <RequirePermission perm="platform.integration.view"><IntegrationsPage /></RequirePermission> },
  { path: 'settings/payment-methods', element: <RequirePermission perm="billing.payment_method.view"><PaymentMethodsPage /></RequirePermission> },
  { path: 'settings/tax-service', element: <RequirePermission perm="commercial.tax_service.view"><TaxServicePage /></RequirePermission> },
  { path: 'settings/approval-workflows', element: <RequirePermission perm="platform.approval_workflow.view"><ApprovalWorkflowsPage /></RequirePermission> },
  { path: 'settings/audit-logs', element: <RequirePermission perm="audit.log.view"><AuditLogsPage /></RequirePermission> },
  { path: 'settings/localization', element: <RequirePermission perm="platform.localization.update"><LocalizationPage /></RequirePermission> },
  { path: 'settings/branding', element: <RequirePermission perm="platform.branding.update"><BrandingPage /></RequirePermission> },
  { path: 'settings/system', element: <RequirePermission perm="platform.system_settings.view"><SystemSettingsPage /></RequirePermission> },
  { path: 'settings/system/background-jobs', element: <RequirePermission perm="platform.job.view"><BackgroundJobsPage /></RequirePermission> },
  { path: 'settings/system/devices', element: <RequirePermission perm="platform.device.view"><DevicesPage /></RequirePermission> },
  { path: 'settings/system/master-data-import', element: <RequirePermission perm="platform.import.view"><MasterDataImportPage /></RequirePermission> },
  { path: 'settings/system/api-keys', element: <RequirePermission perm="platform.api_key.view"><ApiKeysPage /></RequirePermission> },
  { path: 'settings/system/bridge-agents', element: <RequirePermission perm="platform.bridge_agent.view"><BridgeAgentsPage /></RequirePermission> },
  { path: 'settings/system/integration-logs', element: <RequirePermission perm="platform.integration_log.view"><IntegrationLogsPage /></RequirePermission> },
  { path: 'settings/system/notification-history', element: <RequirePermission perm="platform.notification_delivery.view"><NotificationHistoryPage /></RequirePermission> },
];

const routes = [
  {
    element: <SidebarLayout />,
    children: [
      { index: true, element: <DashboardPage /> },
      { path: 'approvals', element: <ApprovalsPage /> },
      { path: 'approvals/:id', element: <ApprovalDetailPage /> },
      ...p1,
      ...P2_ROUTES,
      ...P3_ROUTES,
      ...ACCOMMODATION_ROUTES,
      ...SPORT_ADMIN_ROUTES,
      ...MODULE_PAGES.filter((m) => !P1_MODULES.has(m.path)).map((m) => ({ path: m.path, element: <ModulePage path={m.path} /> })),
      { path: 'reports', element: <RequirePermission perm="reporting.report.view"><ReportsPage /></RequirePermission> },
      { path: 'reports/exports', element: <RequirePermission perm="reporting.export.create"><ExportsPage /></RequirePermission> },
      { path: 'reports/:code', element: <RequirePermission perm="reporting.report.view"><ReportRunPage /></RequirePermission> },
      ...settings,
      { path: 'profile', element: <ProfilePage /> },
      { path: 'notifications', element: <NotificationsPage /> },
      { path: '*', element: <NotFoundPage /> },
    ],
  },
];

export default function BackOfficeArea() {
  return useRoutes(routes);
}
