import { notFound } from 'next/navigation';
import { getProperty } from '../../app/lib-p2';
import { ContactForms, WeatherScript } from './client';
import { MgccPage } from './site';

// Route components of the live site's pages (app/[lang]/<path>/page.tsx).

type Props = { params: Promise<{ lang: string }> };

/** "Book …" block in the live site's Book Your Tee Time style, leading to the OneClub booking pages. */
function BookBlock({ title, text, href, label }: { title: string; text: string; href: string; label: string }) {
  return (
    <section className="book">
      <div className="container">
        <div className="columns is-centered">
          <div className="column is-6 has-text-centered">
            <div className="section-title"><h3 className="has-text-white">{title}</h3></div>
            <p className="mb-5">{text}</p>
            <a className="btn btn-primary" href={href}>{label}</a>
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
    ? { title: 'Pesan Fasilitas Sport Club', text: 'Pesan lapangan tenis, squash, badminton dan kelas olahraga.', path: '/book/sport-club', label: 'Pesan Sekarang' }
    : { title: 'Book Sport Club Facilities', text: 'Book tennis, squash and badminton courts and sport classes.', path: '/book/sport-club', label: 'Book Now' },
};

/** A captured page of the live site as a route component. */
export function livePage(name: string) {
  return async function Page({ params }: Props) {
    const { lang } = await params;
    const book = BOOK[name]?.(lang);
    const page = (
      <MgccPage lang={lang} name={name}>
        {book && <BookBlock title={book.title} text={book.text} href={`/${lang}${book.path}`} label={book.label} />}
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
