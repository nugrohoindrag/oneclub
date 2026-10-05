import { getProperty, rp } from '../../lib-p2';
import { BanquetInquiry } from '../events/forms';
import { dates, getTournaments, label } from './lib';

/** Tournaments open to the public: schedule, fee, places left (PRD P3 FR-WEB-P3-03). */
export default async function Tournaments({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = lang === 'id';
  const [{ items }, p] = await Promise.all([getTournaments(), getProperty()]);
  return (
    <div>
      <h1>{id ? 'Turnamen' : 'Tournaments'}</h1>
      <p>{id ? 'Daftar dan bayar biaya turnamen secara online. Leaderboard tampil langsung selama turnamen.' : 'Register and pay the tournament fee online. The leaderboard is live during play.'}</p>
      {items.length === 0 && <p className="w-muted">{id ? 'Belum ada turnamen terjadwal.' : 'No tournaments scheduled.'}</p>}
      <div className="w-grid">
        {items.map((t) => {
          const pkg = t.packages.find((p) => p.isDefault) ?? t.packages[0];
          return (
            <div key={t.id} className="w-card">
              <h2>{t.name}</h2>
              <p>{dates(t)} · {t.courseName}<br />{label(t.format)} · {label(t.startType)}</p>
              {pkg && <p>{id ? 'Biaya' : 'Fee'}: {rp(pkg.guestTotal)}</p>}
              {t.registrationOpen && <p>{t.placesLeft > 0 ? `${t.placesLeft} ${id ? 'tempat tersisa' : 'places left'}` : id ? 'Penuh — daftar tunggu' : 'Full — waitlist'}</p>}
              <a className="w-btn" href={`/${lang}/tournaments/${t.id}`}>{t.registrationOpen ? (id ? 'Daftar' : 'Register') : id ? 'Detail' : 'Details'}</a>
              {t.leaderboardPublic && (t.status === 'in_progress' || t.status === 'completed') && (
                <a className="w-btn w-btn-ghost" href={`/${lang}/tournaments/${t.id}/leaderboard`}>Leaderboard</a>
              )}
            </div>
          );
        })}
      </div>
      {p && (
        <section style={{ marginTop: 32 }}>
          <h2>{id ? 'Golf korporat & turnamen Anda sendiri' : 'Corporate golf & your own tournament'}</h2>
          <p>{id ? 'Kami siapkan lapangan, start sheet, leaderboard dan jamuan — minta penawaran.' : 'We prepare the course, start sheet, leaderboard and dinner — request a quotation.'}</p>
          <BanquetInquiry propertyId={p.id} lang={lang} initial="corporate_golf" />
        </section>
      )}
    </div>
  );
}
