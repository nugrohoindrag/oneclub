import React from 'react';
import { Link } from 'react-router';
import { DashLinkContext, type DashLinkProps } from '@oneclub/ui';

// The dashboard kit lives in the design system (@oneclub/ui, reference
// docs/product/dashboard-ui.webp); the shell passes it on to the apps and
// gives it the router's link, so its cards and buttons navigate in place.

export {
  Amount, BreakdownList, CircleButton, ColumnChart, DASH_COLORS, DASH_OTHER, DashButton, DashCard, DashGrid, DashHead, DashIcon, DashName, DashStatusPill,
  DashTable, Delta, Gauge, HeatBars, MiniCard, Note, PillSelect, ProgressRow, PromoCard, ReportCard, SegmentBar, SplitStats, monthOptions,
} from '@oneclub/ui';
export type { ColumnPoint, DashColumn, DashOption, DashStatus, DashTone } from '@oneclub/ui';

function RouterLink({ to, ...rest }: DashLinkProps) {
  return <Link to={to} {...rest} />;
}

/** Makes the kit's links router links (set by the shell layouts). */
export function DashRouterLinks({ children }: { children: React.ReactNode }) {
  return <DashLinkContext.Provider value={RouterLink}>{children}</DashLinkContext.Provider>;
}
