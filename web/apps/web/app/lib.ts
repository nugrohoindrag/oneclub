/** Server-side helpers for the public website. */
export type Lang = 'id' | 'en';
export const LANGS: Lang[] = ['id', 'en'];

export interface Bootstrap {
  name: string;
  defaultLocale: string;
  currency: string;
  timezone: string;
  branding: {
    appName: string;
    logoUrl?: string | null;
    loginImageUrl?: string | null;
    accent: string;
    primaryColor?: string | null;
    customAccent?: { light: Record<string, string>; dark: Record<string, string> } | null;
  };
  enabledModules: string[];
}

const API = process.env.ONECLUB_API_URL ?? 'http://localhost:8080';

/** Fetches the instance bootstrap (cached 60 s); falls back if the API is down. */
export async function getBootstrap(): Promise<Bootstrap> {
  try {
    const r = await fetch(`${API}/api/v1/public/bootstrap`, { next: { revalidate: 60 } });
    if (r.ok) return (await r.json()) as Bootstrap;
  } catch {
    /* fall through */
  }
  return { name: 'OneClub', defaultLocale: 'id', currency: 'IDR', timezone: 'Asia/Jakarta', branding: { appName: 'OneClub', accent: 'lime' }, enabledModules: [] };
}

/** Website copy (FR-L10N-01). Navigation labels follow Naming Convention §26. */
export const copy = {
  id: {
    nav: { home: 'Home', contact: 'Contact', location: 'Location' },
    heroTitle: 'Selamat datang',
    heroText: 'Lapangan golf, fasilitas olahraga, dan acara terbaik dalam satu club. Pemesanan online hadir segera.',
    bookSoon: 'Book Golf (segera hadir)',
    member: 'Member Portal',
    soon: 'Segera hadir',
    contactTitle: 'Contact',
    contactText: 'Hubungi kami untuk informasi membership, tee time, dan acara.',
    locationTitle: 'Location',
    locationText: 'Kunjungi club kami.',
    footer: 'Didukung oleh OneClub',
  },
  en: {
    nav: { home: 'Home', contact: 'Contact', location: 'Location' },
    heroTitle: 'Welcome',
    heroText: 'Championship golf, sport facilities and memorable events in one club. Online booking is coming soon.',
    bookSoon: 'Book Golf (coming soon)',
    member: 'Member Portal',
    soon: 'Coming soon',
    contactTitle: 'Contact',
    contactText: 'Get in touch about membership, tee times and events.',
    locationTitle: 'Location',
    locationText: 'Visit our club.',
    footer: 'Powered by OneClub',
  },
};
