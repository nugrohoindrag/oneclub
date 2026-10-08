import React, { useEffect, useState } from 'react';

// Actual play time of a golf round: it starts at the tee-off (Starter
// Dispatch or the caddy's Start Round) and stops at the round finish.
// While the round is on, the count runs live; a rain pause stops it and
// the paused time is left out.

/** Minutes played between the tee-off and the finish (or now), without the
 * paused time (rain). */
export function playMinutes(start?: string | null, end?: string | null, now = Date.now(), pausedSeconds = 0, pausedAt?: string | null): number | null {
  if (!start) return null;
  const from = new Date(start).getTime();
  const to = end ? new Date(end).getTime() : pausedAt ? new Date(pausedAt).getTime() : now;
  return Math.max(0, Math.floor((to - from - pausedSeconds * 1000) / 60_000));
}

/** 1h 05m, 42m. */
export function formatPlayTime(min: number): string {
  if (min < 60) return `${min}m`;
  return `${Math.floor(min / 60)}h ${String(min % 60).padStart(2, '0')}m`;
}

/** The current time, refreshed every interval while live. */
export function useNow(live: boolean, everyMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live) return undefined;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(t);
  }, [live, everyMs]);
  return now;
}

/** Live play time: "Playing 1h 05m" while on course, "Played 4h 12m" once
 * finished, nothing before the tee-off (or the fallback). */
export function PlayTime({ start, end, pausedAt, pausedSeconds = 0, label = true, fallback = null, className }: {
  start?: string | null; end?: string | null; pausedAt?: string | null; pausedSeconds?: number | null; label?: boolean; fallback?: React.ReactNode; className?: string;
}) {
  const paused = !end && !!pausedAt;
  const now = useNow(!!start && !end && !paused);
  const min = playMinutes(start, end, now, pausedSeconds ?? 0, paused ? pausedAt : null);
  if (min == null) return <>{fallback}</>;
  const text = formatPlayTime(min);
  if (paused) {
    return <span className={className ?? 'oc-playtime'} data-paused title="Paused (rain): the play time has stopped">{label ? `Paused ${text}` : `${text} ⏸`}</span>;
  }
  return (
    <span className={className ?? 'oc-playtime'} data-live={end ? undefined : true}
      title={`Tee-off ${new Date(start!).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}${end ? ` · finished ${new Date(end).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}` : ''}`}>
      {label ? (end ? `Played ${text}` : `Playing ${text}`) : text}
    </span>
  );
}
