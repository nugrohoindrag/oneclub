import type { Lang } from '../../../lib';
import { rp } from '../../../lib-p2';
import { dates, getTournament, label } from '../lib';
import { RegisterTournament } from './register';

/** A public tournament with its packages and the registration form (PRD P3 FR-WEB-P3-03). */
export default async function TournamentPage({ params }: { params: Promise<{ lang: string; id: string }> }) {
  const { lang, id: tid } = await params;
  const id = lang === 'id';
  const { propertyId, t } = await getTournament(tid);
  if (!t) return <p className="w-muted">{id ? 'Turnamen tidak ditemukan.' : 'Tournament not found.'}</p>;
  return (
    <div>
      <h1>{t.name}</h1>
      <div className="w-grid">
        <div className="w-card">
          <p>{dates(t)} · {t.courseName}</p>
          <p>{label(t.format)} ({label(t.scoringBasis)}) · {label(t.startType)} · {t.rounds.map((r) => `${r.playDate} ${r.startTime}`).join(', ')}</p>
          <p>{id ? 'Handicap maksimum' : 'Maximum handicap'}: {t.maxHandicap}</p>
          {t.description && <p>{t.description}</p>}
          {t.sponsors.length > 0 && (
            <p>{id ? 'Didukung oleh' : 'Sponsored by'}: {t.sponsors.map((s) => (s.logoUrl ? <img key={s.name} src={s.logoUrl} alt={s.name} style={{ height: 28, marginRight: 8, verticalAlign: 'middle' }} /> : <span key={s.name}>{s.name} </span>))}</p>
          )}
          {t.leaderboardPublic && (t.status === 'in_progress' || t.status === 'completed') && <a className="w-btn" href={`/${lang}/tournaments/${t.id}/leaderboard`}>Leaderboard</a>}
        </div>
        <div className="w-card">
          <h2>{id ? 'Paket & biaya' : 'Packages & fees'}</h2>
          <table className="w-table"><tbody>
            {t.packages.map((p) => <tr key={p.code}><td>{p.name}{p.description ? <div className="w-muted">{p.description}</div> : null}</td><td style={{ textAlign: 'right' }}>{rp(p.guestTotal)}</td></tr>)}
          </tbody></table>
        </div>
      </div>
      {t.registrationOpen
        ? <RegisterTournament lang={lang as Lang} propertyId={propertyId} t={t} />
        : <p className="w-muted" style={{ marginTop: 16 }}>{id ? 'Pendaftaran online ditutup.' : 'Online registration is closed.'}</p>}
    </div>
  );
}
