/** Public tournament data of the website (PRD P3 FR-WEB-P3-03/06, K5:
 * /public/tournaments, /public/tournaments/{id}, /public/tournaments/{id}/leaderboard). */
import { getProperty, pub } from '../../lib-p2';

export interface PublicPackage { id?: string | null; code: string; name: string; description?: string | null; playerType: string; isDefault: boolean; memberTotal: string; guestTotal: string }
export interface Sponsor { name: string; level: string; logoUrl?: string | null }
export interface PublicTournament {
  id: string; code: string; name: string; description?: string | null; tournamentType: string; courseName: string; startDate: string; endDate: string;
  format: string; scoringBasis: string; eligibility: string; startType: string; fieldSize: number; placesLeft: number; waitlistEnabled: boolean;
  status: string; registrationOpen: boolean; registrationClosesAt?: string | null; leaderboardPublic: boolean;
  rounds: { roundNo: number; playDate: string; startTime: string }[]; packages: PublicPackage[]; sponsors: Sponsor[]; maxHandicap: string;
}

export async function getTournaments(): Promise<{ propertyId: string; items: PublicTournament[] }> {
  const p = await getProperty();
  if (!p) return { propertyId: '', items: [] };
  const r = await pub<{ items: PublicTournament[] }>(`/api/v1/public/tournaments?propertyId=${p.id}`);
  return { propertyId: p.id, items: r?.items ?? [] };
}

export async function getTournament(id: string): Promise<{ propertyId: string; t: PublicTournament | null }> {
  const p = await getProperty();
  if (!p) return { propertyId: '', t: null };
  return { propertyId: p.id, t: await pub<PublicTournament>(`/api/v1/public/tournaments/${encodeURIComponent(id)}?propertyId=${p.id}`) };
}

export const label = (v: string) => v.replace(/_/g, ' ');
export const dates = (t: PublicTournament) => (t.startDate === t.endDate ? t.startDate : `${t.startDate} – ${t.endDate}`);
