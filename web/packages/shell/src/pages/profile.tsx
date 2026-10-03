import React, { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { request, useGet, useSend, type Page, type Schemas } from '@oneclub/api-client';
import { formatDateTime, formatRelative, useTranslation } from '@oneclub/i18n';
import { useAuth } from '../context';
import { Card, Checkbox, DataTable, ErrorAlert, PageHeader, PasswordField, StatusPill, TextField, fieldErrors } from '../components/ui';
import { useToast } from '../components/toast';

/** Profile: language & theme, password, PIN, sessions, notification preferences. */
export function ProfilePage({ showPin = false }: { showPin?: boolean }) {
  const { me, locale, changeLocale, theme, changeTheme } = useAuth();
  const { t } = useTranslation();
  const toast = useToast();
  const qc = useQueryClient();
  const [cur, setCur] = useState('');
  const [next, setNext] = useState('');
  const [pin, setPin] = useState('');
  const pw = useSend('POST', '/api/v1/auth/password/change');
  const pinM = useSend('PUT', '/api/v1/auth/me/pin');
  const sessions = useGet<Page<Schemas['Session']>>('/api/v1/platform/sessions');
  const prefs = useGet<Page<Schemas['Preference']>>('/api/v1/platform/notification-preferences');
  const savePrefs = useSend<{ preferences: Schemas['Preference'][] }>('PUT', '/api/v1/platform/notification-preferences', ['/api/v1/platform/notification-preferences']);
  if (!me) return null;
  const setPref = (p: Schemas['Preference'], k: 'inApp' | 'email' | 'whatsapp', v: boolean) => {
    const all = (prefs.data?.items ?? []).map((x) => (x.category === p.category ? { ...x, [k]: v } : x));
    savePrefs.mutate({ preferences: all }, { onSuccess: () => toast(t('common.saved')) });
  };
  return (
    <div className="oc-stack">
      <PageHeader title="Profile" help={me.email} />
      <div className="oc-grid-2">
        <Card title="Preferences" icon="tune">
          <div className="oc-stack">
            <div className="oc-row-wrap">
              <span className="oc-label" style={{ minWidth: 100 }}>{t('shell.language')}</span>
              <button className="oc-chip" aria-pressed={locale === 'id'} onClick={() => changeLocale('id')}>Bahasa Indonesia</button>
              <button className="oc-chip" aria-pressed={locale === 'en'} onClick={() => changeLocale('en')}>English</button>
            </div>
            <div className="oc-row-wrap">
              <span className="oc-label" style={{ minWidth: 100 }}>{t('shell.theme')}</span>
              {(['light', 'dark', 'system'] as const).map((m) => (
                <button key={m} className="oc-chip" aria-pressed={theme === m} onClick={() => changeTheme(m)}>{m === 'light' ? 'Light' : m === 'dark' ? 'Dark' : 'System'}</button>
              ))}
            </div>
            <div className="oc-small oc-muted">Roles: {me.roles.map((r) => r.name).filter((v, i, a) => a.indexOf(v) === i).join(', ')} · MFA {me.mfaEnabled ? 'on' : 'off'}</div>
          </div>
        </Card>
        <Card title="Change Password" icon="password">
          <form className="oc-stack" onSubmit={(e) => {
            e.preventDefault();
            pw.mutate({ currentPassword: cur, newPassword: next } as never, { onSuccess: () => { setCur(''); setNext(''); toast(t('common.saved')); } });
          }}>
            <PasswordField label={t('auth.currentPassword')} value={cur} onChange={setCur} required error={fieldErrors(pw.error).currentPassword} />
            <PasswordField label={t('auth.newPassword')} value={next} onChange={setNext} required help={t('auth.passwordRules')} error={fieldErrors(pw.error).password ?? fieldErrors(pw.error).newPassword} />
            <div><button className="oc-btn oc-btn-ink" disabled={pw.isPending || !cur || next.length < 10}>Change Password</button></div>
          </form>
        </Card>
        {showPin && (
          <Card title="Staff PIN" icon="pin">
            <form className="oc-stack" onSubmit={(e) => {
              e.preventDefault();
              pinM.mutate({ pin } as never, { onSuccess: () => { setPin(''); toast(t('common.saved')); } });
            }}>
              <TextField label={t('auth.pin')} inputMode="numeric" maxLength={6} value={pin} onChange={setPin} error={fieldErrors(pinM.error).pin} />
              <ErrorAlert error={Object.keys(fieldErrors(pinM.error)).length ? null : pinM.error} />
              <div><button className="oc-btn oc-btn-ink" disabled={pin.length !== 6}>Set PIN</button></div>
            </form>
          </Card>
        )}
        <Card title="Notifications" icon="notifications">
          <DataTable rows={prefs.data?.items} loading={prefs.isLoading} rowKey={(r) => r.category}
            columns={[
              { key: 'label', header: 'Category', render: (p) => <>{p.label}{p.mandatory && <span className="oc-small oc-muted"> · mandatory</span>}</> },
              { key: 'inApp', header: 'In-app', render: (p) => <Checkbox label="" checked={p.inApp} disabled={p.mandatory} onChange={(v) => setPref(p, 'inApp', v)} /> },
              { key: 'email', header: 'E-mail', render: (p) => <Checkbox label="" checked={p.email} disabled={p.mandatory} onChange={(v) => setPref(p, 'email', v)} /> },
              { key: 'whatsapp', header: 'WhatsApp', render: (p) => <Checkbox label="" checked={p.whatsapp} disabled={p.mandatory} onChange={(v) => setPref(p, 'whatsapp', v)} /> },
            ]} />
        </Card>
      </div>
      <Card title={t('shell.sessions')} icon="devices">
        <DataTable rows={sessions.data?.items} loading={sessions.isLoading}
          columns={[
            { key: 'userAgent', header: 'Device', render: (s) => <span className="oc-small">{s.kind === 'device' ? 'Operational device' : (s.userAgent ?? '—').slice(0, 80)}</span> },
            { key: 'ip', header: 'IP' },
            { key: 'lastSeenAt', header: 'Last active', render: (s) => formatRelative(s.lastSeenAt) },
            { key: 'expiresAt', header: 'Expires', render: (s) => formatDateTime(s.expiresAt) },
            { key: 'current', header: '', render: (s) => (s.current ? <StatusPill status="active" label="This session" /> : null) },
          ]}
          actions={(s) => !s.current && (
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={async () => {
              await request('DELETE', `/api/v1/platform/sessions/${s.id}`);
              await qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith('/api/v1/platform/sessions') });
            }}>Revoke</button>
          )} />
      </Card>
    </div>
  );
}

/** Full notification list. */
export function NotificationsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [unread, setUnread] = useState(false);
  const list = useGet<Page<Schemas['Notification']>>(`/api/v1/platform/notifications?limit=100${unread ? '&filter[unread]=true' : ''}`);
  const refresh = () => qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith('/api/v1/platform/notifications') });
  return (
    <div className="oc-stack">
      <PageHeader title={t('shell.notifications')} actions={<>
        <button className="oc-chip" aria-pressed={unread} onClick={() => setUnread((u) => !u)}>Unread</button>
        <button className="oc-btn oc-btn-neutral oc-btn-sm" onClick={async () => { await request('POST', '/api/v1/platform/notifications:read-all'); void refresh(); }}>{t('shell.markAllRead')}</button>
      </>} />
      <div className="oc-card">
        <DataTable rows={list.data?.items} loading={list.isLoading}
          columns={[
            { key: 'title', header: 'Notification', render: (n) => (
              <div style={{ fontWeight: n.readAt ? 400 : 700 }}>{n.title}<div className="oc-small oc-muted" style={{ whiteSpace: 'pre-line' }}>{n.body}</div></div>
            ) },
            { key: 'category', header: 'Category' },
            { key: 'createdAt', header: 'Received', render: (n) => formatDateTime(n.createdAt) },
          ]}
          actions={(n) => !n.readAt && (
            <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={async () => { await request('POST', `/api/v1/platform/notifications/${n.id}:read`); void refresh(); }}>Mark read</button>
          )} />
      </div>
    </div>
  );
}
