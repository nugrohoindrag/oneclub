import { notFound } from 'next/navigation';
import { getProperty, pub } from '../../app/lib-p2';
import { ContactForms, WeatherScript } from './client';
import { StaySearchBar } from './stay-search';
import { MgccPage } from './site';

// Route components of the live site's pages (app/[lang]/<path>/page.tsx).

type Props = { params: Promise<{ lang: string }> };

/** "Book …" block in the live site's Book Your Tee Time style, leading to the OneClub booking pages. */
function BookBlock({ title, text, href, label, links = [] }: { title: string; text: string; href: string; label: string; links?: [string, string][] }) {
  return (
    <section className="book">
      <div className="container">
        <div className="columns is-centered">
          <div className="column is-6 has-text-centered">
            <div className="section-title"><h3 className="has-text-white">{title}</h3></div>
            <p className="mb-5">{text}</p>
            {links.length > 0 && (
              <div className="buttons is-centered mb-4">
                {links.map(([l, h]) => <a key={h} className="btn btn-primary" href={h}>{l}</a>)}
              </div>
            )}
            <a className={links.length ? 'btn' : 'btn btn-primary'} href={href}>{label}</a>
          </div>
        </div>
      </div>
    </section>
  );
}

const BOOK: Record<string, (lang: string) => { title: string; text: string; path: string; label: string }> = {
  bungalow: (l) => l === 'id'
    ? { title: 'Pesan Bungalow Anda', text: 'Pilih tanggal menginap dan tipe bungalow, lihat harga, lalu bayar online.', path: '/book/bungalow', label: 'Pesan Sekarang' }
    : { title: 'Book Your Bungalow', text: 'Choose your dates and bungalow type, see the rate and pay online.', path: '/book/bungalow', label: 'Book Now' },
  'vip-suite': (l) => l === 'id'
    ? { title: 'Pesan VIP Suite', text: 'Ajukan pemesanan VIP Suite; tim kami mengonfirmasi ketersediaan.', path: '/book/vip-suite', label: 'Pesan Sekarang' }
    : { title: 'Book the VIP Suite', text: 'Request the VIP Suite; our team confirms the availability.', path: '/book/vip-suite', label: 'Book Now' },
  'mice-and-wedding': (l) => l === 'id'
    ? { title: 'Pesan Venue Acara', text: 'Pesan meeting room untuk rapat dan acara, atau kirim permintaan pernikahan & banquet.', path: '/book/meeting-room', label: 'Pesan Venue' }
    : { title: 'Book Your Venue', text: 'Book a meeting room for your meeting or event, or send a wedding & banquet inquiry.', path: '/book/meeting-room', label: 'Book Venue' },
  'sport-club': (l) => l === 'id'
    ? { title: 'Sewa Lapangan Sport Club', text: 'Pesan lapangan tenis, futsal, basket & voli dan basket indoor: pilih jam yang kosong, lalu bayar online.', path: '/book/sport-club', label: 'Semua cabor & kelas' }
    : { title: 'Book a Sport Club Court', text: 'Book tennis, futsal, basket & volley and indoor basketball courts: pick a free hour and pay online.', path: '/book/sport-club', label: 'All sports & classes' },
};

/** "Pesan" per sport bookable online (requirement-booking-sportclub-mgcc FR-01): the sport pages of the booking. */
async function sportLinks(lang: string): Promise<[string, string][]> {
  const p = await getProperty();
  if (!p) return [];
  const page = await pub<{ facilities: { code: string; name: string; usageMode: string; sortOrder: number; courts: number; content?: { slug?: string; nameEn?: string } }[] }>(
    `/api/v1/public/sport-club?propertyId=${p.id}`);
  return (page?.facilities ?? []).filter((f) => f.usageMode === 'slot_booking' && f.courts > 0).sort((a, b) => a.sortOrder - b.sortOrder)
    .map((f) => [`${lang === 'id' ? 'Pesan' : 'Book'} ${lang === 'en' && f.content?.nameEn ? f.content.nameEn : f.name}`,
      `/${lang}/book/sport-club/${f.content?.slug || f.code.toLowerCase()}`]);
}

/** The bungalow search of the booking engine (docs/requirement-booking-hotel-mgcc.md FR-H07) on the Bungalow page and the Home. */
function StaySearchBlock({ lang }: { lang: string }) {
  const id = lang === 'id';
  return (
    <section className="book oc-stay-search">
      <div className="container">
        <div className="section-title has-text-centered"><h3 className="has-text-white">{id ? 'Pesan Bungalow' : 'Book a Bungalow'}</h3></div>
        <StaySearchBar lang={id ? 'id' : 'en'} />
        <p className="has-text-centered"><a href={`/${lang}/book/bungalow/check`}>{id ? 'Cek Booking' : 'Check Booking'}</a></p>
      </div>
    </section>
  );
}

/** A captured page of the live site as a route component. */
export function livePage(name: string) {
  return async function Page({ params }: Props) {
    const { lang } = await params;
    const book = BOOK[name]?.(lang);
    const links = name === 'sport-club' ? await sportLinks(lang) : [];
    const page = (
      <MgccPage lang={lang} name={name}>
        {name === 'bungalow' || name === 'home' ? <StaySearchBlock lang={lang} />
          : book && <BookBlock title={book.title} text={book.text} href={`/${lang}${book.path}`} label={book.label} links={links} />}
      </MgccPage>
    );
    if (name === 'home') return <>{page}<WeatherScript /></>;
    if (name === 'contact') {
      const property = await getProperty();
      return <>{page}<ContactForms propertyId={property?.id ?? ''} lang={lang} /></>;
    }
    return page;
  };
}

/** What's On detail pages captured from the live site. */
export const EVENT_DETAILS = ['Rate-Golf-2025', 'driving-range-promo-2025', 'wija-soju-buy-2-Only-200K'];

export async function EventDetail({ params }: { params: Promise<{ lang: string; slug: string }> }) {
  const { lang, slug } = await params;
  if (!EVENT_DETAILS.includes(slug)) notFound();
  return <MgccPage lang={lang} name={`event-detail--${slug}`} />;
}
