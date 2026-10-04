import React from 'react';
import { Link } from 'react-router';
import { useTranslation } from '@oneclub/i18n';
import { Icon } from '../components/ui';
import { useAuth } from '../context';
import { areasOf, useArea } from '../areas';

/** Standard pages (FR-SH-04): 403, 404, error, maintenance. */
function StatusPage({ code, icon, title, help, action = true, children }: {
  code: string; icon: string; title: string; help: string; action?: boolean; children?: React.ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <div style={{ minHeight: '60vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <div className="oc-card" style={{ maxWidth: 520, textAlign: 'center' }}>
        <span className="oc-icon-circle" style={{ width: 64, height: 64, margin: '0 auto 12px' }}><Icon name={icon} size={32} /></span>
        <div className="oc-muted oc-num" style={{ fontWeight: 700, letterSpacing: '0.1em' }}>{code}</div>
        <h1 style={{ margin: '4px 0 8px', fontSize: 28 }}>{title}</h1>
        <p className="oc-muted">{help}</p>
        {children ?? (action && <Link to="/" className="oc-btn oc-btn-ink">{t('shell.goHome')}</Link>)}
      </div>
    </div>
  );
}

/** 403; in the Staff App it links to the areas the user may open. */
export function ForbiddenPage() {
  const { t } = useTranslation();
  const { me, logout } = useAuth();
  const staff = useArea() !== null;
  const areas = areasOf(me);
  return (
    <StatusPage code="403" icon="lock" title={t('shell.forbiddenTitle')} help={t('shell.forbiddenHelp')}>
      {staff ? (
        <nav className="oc-stack" aria-label={t('shell.yourAreas')}>
          {areas.length > 0 && <div className="oc-small oc-muted">{t('shell.yourAreas')}</div>}
          {areas.map((a) => (
            <Link key={a.code} to={a.path} className="oc-btn oc-btn-outline oc-btn-block"><Icon name={a.icon} size={20} /> {a.label}</Link>
          ))}
          {areas.length === 0 && <button className="oc-btn oc-btn-ink" onClick={() => void logout()}>{t('shell.logout')}</button>}
        </nav>
      ) : undefined}
    </StatusPage>
  );
}

export function NotFoundPage() {
  const { t } = useTranslation();
  return <StatusPage code="404" icon="travel_explore" title={t('shell.notFoundTitle')} help={t('shell.notFoundHelp')} />;
}

export function ErrorPage({ error }: { error?: unknown }) {
  const { t } = useTranslation();
  return (
    <>
      <StatusPage code="500" icon="error" title={t('shell.errorTitle')} help={t('shell.errorHelp')} />
      {error ? <pre className="oc-pre" style={{ maxWidth: 720, margin: '0 auto' }}>{String((error as Error)?.message ?? error)}</pre> : null}
    </>
  );
}

export function MaintenancePage() {
  const { t } = useTranslation();
  return <StatusPage code="503" icon="construction" title={t('shell.maintenanceTitle')} help={t('shell.maintenanceHelp')} action={false} />;
}

/** Placeholder for modules whose screens arrive in later phases. */
export function ComingSoonPage({ title, phase, features }: { title: string; phase: string; features?: string[] }) {
  const { t } = useTranslation();
  return (
    <div className="oc-stack">
      <div className="oc-page-head"><div><h1>{title}</h1><p>{t('common.comingSoonHelp', { phase })}</p></div></div>
      <div className="oc-card">
        <div className="oc-row" style={{ marginBottom: 12 }}>
          <span className="oc-icon-circle"><Icon name="rocket_launch" size={20} /></span>
          <strong>{t('common.comingSoon', { phase })}</strong>
        </div>
        {features && (
          <div className="oc-row-wrap">
            {features.map((f) => <span key={f} className="oc-chip" style={{ cursor: 'default' }}>{f}</span>)}
          </div>
        )}
      </div>
    </div>
  );
}

/** Error boundary that renders the standard error page. */
export class ErrorBoundary extends React.Component<{ children: React.ReactNode }, { error: unknown }> {
  override state = { error: null as unknown };
  static getDerivedStateFromError(error: unknown) {
    return { error };
  }
  override render() {
    return this.state.error ? <ErrorPage error={this.state.error} /> : this.props.children;
  }
}
