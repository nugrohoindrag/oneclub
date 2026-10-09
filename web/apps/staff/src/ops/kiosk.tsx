import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { qs, request, uuidv7, type Page } from '@oneclub/api-client';
import { ErrorAlert, Icon } from '@oneclub/shell';
import { ScanField } from '../p4/inventory';
import { money, today } from './golf';

// Self check-in kiosk (demo feedback 9 Oct 2026): a tablet at the clubhouse
// entrance, signed in by the front desk, where golfers scan the QR of their
// booking. The players are checked in and wait at the front desk, which
// picks the caddy and the golf cart (Reservations › Self check-in).

type R = Record<string, unknown>;

export function KioskCheckInPage() {
  const [done, setDone] = useState<R | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const working = useRef(false); // a stable scan callback keeps the camera running
  const reset = useCallback(() => { setDone(null); setError(null); }, []);
  // back to the scan screen for the next golfer
  useEffect(() => {
    if (!done && !error) return undefined;
    const t = window.setTimeout(reset, done ? 12_000 : 15_000);
    return () => window.clearTimeout(t);
  }, [done, error, reset]);
  const scan = useCallback(async (raw: string) => {
    const value = raw.trim();
    if (!value || working.current) return;
    working.current = true;
    setBusy(true);
    setError(null);
    try {
      const found = (await request<Page<R>>('GET', `/api/v1/golf/check-ins:lookup${qs({ method: 'booking_qr', value })}`)).items;
      const b = found.find((x) => String(x.startAt ?? '').length > 0 && localDate(String(x.startAt)) === today());
      if (!b) throw new Error(found.length ? 'This booking is not for today. Please see the front desk.' : 'No booking found for this QR. Please see the front desk.');
      if (Number(b.paymentDue) > 0) throw new Error(`Please pay ${money(b.paymentDue)} at the front desk first.`);
      await request('POST', '/api/v1/golf/check-ins', { method: 'booking_qr', value, bookingId: b.bookingId, kiosk: true }, { 'Idempotency-Key': uuidv7() });
      setDone(b);
    } catch (e) {
      setError(e);
    } finally {
      working.current = false;
      setBusy(false);
    }
  }, []);
  const onCode = useCallback((c: string) => void scan(c), [scan]);
  return (
    <div className="kiosk">
      <Link className="kiosk-exit" to="/ops/front-desk/check-in" aria-label="Leave the kiosk"><Icon name="close" size={20} /></Link>
      <div className="kiosk-card">
        {done ? (
          <>
            <span className="kiosk-mark"><Icon name="how_to_reg" size={44} /></span>
            <h1>Welcome, {String(done.contactName)}</h1>
            <p className="kiosk-big">Booking {String(done.code)} · Tee time {String(done.localTime)}</p>
            <p>You are checked in. The front desk will call you for your caddy and golf cart.</p>
            <button className="pos-btn pos-pill" onClick={reset}>Done</button>
          </>
        ) : (
          <>
            <span className="kiosk-mark"><Icon name="qr_code_scanner" size={44} /></span>
            <h1>Self check-in</h1>
            <p className="kiosk-big">Scan the QR of your booking (Member App or confirmation e-mail).</p>
            <ScanField onCode={onCode} label="Booking QR" />
            {busy && <p className="pos-muted">Checking in…</p>}
            <ErrorAlert error={error} />
            <p className="pos-muted">No QR? Please see the front desk.</p>
          </>
        )}
      </div>
    </div>
  );
}

function localDate(iso: string) {
  const d = new Date(iso);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}
