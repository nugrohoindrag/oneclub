import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Link, Navigate, useNavigate } from 'react-router';
import { request, uuidv7, type Schemas } from '@oneclub/api-client';
import { formatDateTime, useTranslation } from '@oneclub/i18n';
import { AuthFrame, ErrorAlert, Icon, SelectField, TextField, currentSurface, surfaceUrl, useBootstrap, useModuleEnabled } from '@oneclub/shell';

/*
 * Attendance Form (/presence): clock in / out from the login page without
 * signing in — employee ID + attendance PIN and the browser's GPS. The
 * location is checked as soon as the form opens; without GPS the notice shows
 * right away and the form cannot be sent. Served on its own presence domain
 * (presence.<club domain>, geolocation allowed there; every path is the form)
 * or at /presence elsewhere. After a clock-in / out the form is ready for the
 * next employee on the presence domain, elsewhere the page returns to the login.
 */

const onPresenceDomain = () => currentSurface() === 'presence';

type Direction = 'in' | 'out';
type Gps = { state: 'checking' } | { state: 'ready'; pos: GeolocationPosition } | { state: 'off' | 'denied' };

const locate = (): Promise<GeolocationPosition> =>
  new Promise((resolve, reject) => {
    if (!('geolocation' in navigator)) return reject({ code: 2 });
    navigator.geolocation.getCurrentPosition(resolve, reject, { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 });
  });

const gpsError = (e: unknown): Gps => ({ state: (e as GeolocationPositionError)?.code === 1 ? 'denied' : 'off' });

/** Login-page link to the Attendance Form (HRIS enabled). */
export function PresenceLink() {
  const { t } = useTranslation();
  const domain = surfaceUrl('presence', '/');
  if (!useModuleEnabled('hris')) return null;
  const content = <><Icon name="how_to_reg" size={20} /> {t('auth.presenceForm')}</>;
  const style = { height: 48, textDecoration: 'none' };
  return (
    <>
      <div className="oc-row oc-small oc-muted" style={{ gap: 12 }}>
        <hr style={{ flex: 1, border: 0, borderTop: '1px solid var(--md-sys-color-outline-variant)' }} />{t('auth.or')}
        <hr style={{ flex: 1, border: 0, borderTop: '1px solid var(--md-sys-color-outline-variant)' }} />
      </div>
      {domain
        ? <a href={domain} className="oc-btn oc-btn-outline oc-btn-block" style={style}>{content}</a>
        : <Link to="/presence" className="oc-btn oc-btn-outline oc-btn-block" style={style}>{content}</Link>}
    </>
  );
}

export function PresencePage() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const b = useBootstrap();
  const hris = useModuleEnabled('hris');
  const [direction, setDirection] = useState<Direction>('in');
  const [property, setProperty] = useState(b.properties[0]?.id ?? '');
  const [no, setNo] = useState('');
  const [pin, setPin] = useState('');
  const [gps, setGps] = useState<Gps>({ state: 'checking' });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState<Schemas['AttendanceClockResult'] | null>(null);
  const client = useRef(uuidv7());

  const check = useCallback(() => {
    setGps({ state: 'checking' });
    locate().then((pos) => setGps({ state: 'ready', pos }), (e) => setGps(gpsError(e)));
  }, []);
  useEffect(check, [check, direction]);

  useEffect(() => {
    if (!done) return;
    const id = window.setTimeout(() => {
      if (!onPresenceDomain()) return nav('/login', { replace: true });
      setDone(null);
      setNo('');
      setPin('');
      client.current = uuidv7();
      check();
    }, 4000);
    return () => window.clearTimeout(id);
  }, [done, nav, check]);

  if (!hris) {
    if (!onPresenceDomain()) return <Navigate to="/login" replace />;
    return <AuthFrame><div className="oc-alert oc-alert-info" role="status">{t('auth.presenceOff')}</div></AuthFrame>;
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    let pos: GeolocationPosition;
    try {
      pos = await locate(); // a fresh position at the moment of the clock-in / out
    } catch (err) {
      setGps(gpsError(err));
      setBusy(false);
      return;
    }
    setGps({ state: 'ready', pos });
    try {
      setDone(await request<Schemas['AttendanceClockResult']>('POST', '/api/v1/public/attendance:clock', {
        propertyId: property, employeeNo: no.trim(), pin, direction, clientEventId: client.current,
        latitude: pos.coords.latitude, longitude: pos.coords.longitude, accuracyMeters: Math.round(pos.coords.accuracy),
      }));
    } catch (err) {
      setError(err);
      client.current = uuidv7();
    } finally {
      setBusy(false);
    }
  };

  const ev = done?.event;
  return (
    <AuthFrame>
      <form className="oc-stack" onSubmit={submit} noValidate>
        <div>
          <h1 style={{ fontSize: 32 }}>{t('auth.presenceForm')}</h1>
          <p className="oc-muted" style={{ margin: '8px 0 0' }}>{t('auth.presenceHelp')}</p>
        </div>

        {done && ev ? (
          <div className={`oc-alert ${ev.reviewStatus === 'pending' ? 'oc-alert-info' : 'oc-alert-success'}`} role="status" aria-live="polite">
            <strong>{ev.employeeName}</strong> · {ev.direction === 'in' ? t('auth.clockIn') : t('auth.clockOut')} {formatDateTime(ev.occurredAt)}
            {ev.reviewStatus === 'pending' && <div>{done.message.split(' — ')[1]}</div>}
            <div className="oc-muted" style={{ marginTop: 4 }}>{onPresenceDomain() ? t('auth.presenceNext') : t('auth.presenceBack')}</div>
          </div>
        ) : (
          <>
            <div className="oc-row" role="radiogroup" aria-label={t('auth.presenceForm')} style={{ gap: 12 }}>
              {(['in', 'out'] as const).map((d) => (
                <button key={d} type="button" role="radio" aria-checked={direction === d} onClick={() => setDirection(d)}
                  className={`oc-btn ${direction === d ? 'oc-btn-ink' : 'oc-btn-outline'}`} style={{ flex: 1, height: 52, fontWeight: 600 }}>
                  <Icon name={d === 'in' ? 'input' : 'logout'} size={20} /> {d === 'in' ? t('auth.clockIn') : t('auth.clockOut')}
                </button>
              ))}
            </div>

            {gps.state === 'checking' && <div className="oc-alert oc-alert-info" role="status"><Icon name="my_location" size={16} /> {t('auth.gpsChecking')}</div>}
            {gps.state === 'ready' && (
              <div className="oc-alert oc-alert-success" role="status">
                <Icon name="location_on" size={16} /> {t('auth.gpsReady', { meters: Math.round(gps.pos.coords.accuracy) })}
              </div>
            )}
            {(gps.state === 'off' || gps.state === 'denied') && (
              <div className="oc-alert oc-alert-error oc-row" role="alert" style={{ alignItems: 'flex-start', gap: 10 }}>
                <Icon name="location_off" size={22} />
                <div style={{ flex: 1 }}>
                  <strong>{t('auth.gpsOff')}</strong>
                  <div>{gps.state === 'denied' ? t('auth.gpsDeniedHelp') : t('auth.gpsOffHelp')}</div>
                </div>
                <button type="button" className="oc-btn oc-btn-sm oc-btn-neutral" onClick={check}>{t('auth.gpsRetry')}</button>
              </div>
            )}

            {b.properties.length > 1 && (
              <SelectField label={t('shell.property')} value={property} onChange={setProperty} required
                options={b.properties.map((p) => ({ value: p.id, label: p.name }))} />
            )}
            <TextField label={t('auth.employeeNo')} autoComplete="username" value={no} onChange={setNo} required autoFocus />
            <TextField label={t('auth.attendancePin')} type="password" inputMode="numeric" autoComplete="off" maxLength={6} value={pin}
              onChange={(v) => setPin(v.replace(/\D/g, ''))} required help={t('auth.attendancePinHelp')} />
            <ErrorAlert error={error} />
            <button className="oc-btn oc-btn-ink oc-btn-block" disabled={busy || gps.state !== 'ready' || !property || !no.trim() || pin.length !== 6}>
              {busy ? t('auth.gpsChecking') : t('auth.submit')}
            </button>
          </>
        )}
        {!onPresenceDomain() && <Link to="/login" className="oc-small" style={{ fontWeight: 600 }}>← {t('auth.backToLogin')}</Link>}
      </form>
    </AuthFrame>
  );
}
