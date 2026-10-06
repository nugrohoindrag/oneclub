/** Careers page helpers (PRD P5 EP-03 FR-RCT-05). */
import { getProperty, pub } from '../../lib-p2';

export interface CareerPosition {
  id: string;
  number: string;
  title: string;
  department: string;
  location?: string | null;
  contractType: 'pkwt' | 'pkwtt';
  workerCategory: string;
  openings: number;
  description?: string | null;
  requirements?: string | null;
  publishUntil?: string | null;
  postedAt?: string | null;
}

/** Open public positions of the website property (empty when the careers page is off). */
export async function getPositions(): Promise<{ propertyId: string | null; items: CareerPosition[] }> {
  const p = await getProperty();
  if (!p) return { propertyId: null, items: [] };
  const r = await pub<{ items: CareerPosition[] }>(`/api/v1/public/careers?propertyId=${p.id}`);
  return { propertyId: p.id, items: r?.items ?? [] };
}

/** One open position (null when closed or unknown). */
export async function getPosition(id: string): Promise<{ propertyId: string | null; position: CareerPosition | null }> {
  const p = await getProperty();
  if (!p) return { propertyId: null, position: null };
  return { propertyId: p.id, position: await pub<CareerPosition>(`/api/v1/public/careers/${encodeURIComponent(id)}?propertyId=${p.id}`) };
}

export const contractLabel = (c: string, id: boolean) =>
  c === 'pkwtt' ? (id ? 'Karyawan tetap (PKWTT)' : 'Permanent (PKWTT)') : id ? 'Kontrak (PKWT)' : 'Fixed term (PKWT)';
