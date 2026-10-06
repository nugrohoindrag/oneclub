/** Public Order of Merit of the website (PRD P5 EP-23 §7.4: series leaderboard with consent —
 * /public/tournament-series, /public/tournament-series/{id}/order-of-merit). Players without public
 * consent arrive masked (initials) from the API. */
import { getProperty, pub } from '../../../lib-p2';

export interface PublicSeries { id: string; code: string; name: string; season: number; status: string; events: number; countedEvents: number; champion?: string | null }
export interface SeriesEventPoints { seriesEventId: string; positionLabel: string; points: string }
export interface SeriesStanding { rank?: number | null; positionLabel: string; playerName: string; points: string; events: number; wins: number; eventPoints: SeriesEventPoints[] }
export interface SeriesEvent { id: string; sequence: number; tournamentName: string; startDate: string; weight: string; isFinal: boolean; status: string }
export interface OrderOfMerit {
  seriesId: string; code: string; name: string; season: number; status: string; final: boolean; bestOf?: number | null; minEvents: number;
  champion?: string | null; events: SeriesEvent[]; standings: SeriesStanding[]; updatedAt: string;
}

export async function getSeriesList(): Promise<PublicSeries[]> {
  const p = await getProperty();
  if (!p) return [];
  const r = await pub<{ items: PublicSeries[] }>(`/api/v1/public/tournament-series?propertyId=${p.id}`);
  return r?.items ?? [];
}

export async function getOrderOfMerit(id: string): Promise<OrderOfMerit | null> {
  const p = await getProperty();
  if (!p) return null;
  return pub<OrderOfMerit>(`/api/v1/public/tournament-series/${encodeURIComponent(id)}/order-of-merit?propertyId=${p.id}`);
}
