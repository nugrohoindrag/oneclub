import React from 'react';
import { useGet, type Schemas } from '@oneclub/api-client';
import { Icon } from '@oneclub/shell';

// Live weather at the course (Open-Meteo, demo feedback 9 Oct 2026): on the
// Course Monitor, Course Status (where it suggests the weather status) and
// the caddy tablet. Refreshed every 10 minutes like the server's cache.

export type Weather = Schemas['Weather'];

const ICON: Record<string, string> = {
  clear: 'sunny', partly_cloudy: 'partly_cloudy_day', cloudy: 'cloud', fog: 'foggy', drizzle: 'rainy_light', rain: 'rainy',
  heavy_rain: 'rainy_heavy', thunderstorm: 'thunderstorm', other: 'cloud',
};

export const weatherIcon = (w?: Weather) => ICON[w?.condition ?? ''] ?? 'partly_cloudy_day';

export function useCourseWeather(courseId?: string | null) {
  return useGet<Weather>(courseId ? `/api/v1/golf/courses/${courseId}/weather` : null, { refetchInterval: 10 * 60_000, staleTime: 5 * 60_000 });
}

/** The live weather line; with onApply, the weather status it suggests. */
export function LiveWeather({ courseId, onApply }: { courseId: string; onApply?: (status: string) => void }) {
  const w = useCourseWeather(courseId).data;
  if (!w) return null;
  if (!w.available) return <span className="oc-small oc-muted"><Icon name="cloud_off" size={16} /> Live weather not available{w.reason ? ` (${w.reason})` : ''}</span>;
  const alert = w.suggestedStatus !== 'normal';
  return (
    <span className={`oc-row-wrap oc-small${alert ? ' oc-weather-alert' : ''}`} style={{ alignItems: 'center', gap: 6 }} title={`Open-Meteo · ${new Date(w.at).toLocaleTimeString('en-GB', { timeStyle: 'short' })}`}>
      <Icon name={weatherIcon(w)} size={18} />
      <strong>{w.summary}</strong>
      <span className="oc-muted">wind {Math.round(w.windKmh)} km/h</span>
      {onApply && alert && (
        <button className="oc-btn oc-btn-outline oc-btn-sm" onClick={() => onApply(w.suggestedStatus)}>Set weather: {w.suggestedStatus.replace(/_/g, ' ')}</button>
      )}
    </span>
  );
}
