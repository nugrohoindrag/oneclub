import type { Lang } from '../../lib';
import { ActivateForm } from './activate';

/** Membership (PRD P1 §6.4) with Member Portal activation (FR-APP-01). */
export default async function Membership({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const id = (lang as Lang) === 'id';
  return (
    <div>
      <h1>Membership</h1>
      <div className="w-grid">
        <div className="w-card">
          <h2>{id ? 'Keanggotaan Golf' : 'Golf Membership'}</h2>
          <p>{id ? 'Individu, keluarga, dan korporat. Member bermain dengan Member Rate dan dapat memesan tee time lebih awal.' : 'Individual, family and corporate memberships. Members play at the Member Rate and book tee times earlier.'}</p>
          <p className="w-muted">{id ? 'Hubungi kantor membership untuk mendaftar.' : 'Contact the membership office to apply.'}</p>
        </div>
        <div className="w-card">
          <h2>{id ? 'Aktivasi Member Portal' : 'Activate the Member Portal'}</h2>
          <p>{id ? 'Sudah menjadi member? Masukkan nomor member, e-mail, dan tanggal lahir Anda.' : 'Already a member? Enter your member number, e-mail and date of birth.'}</p>
          <ActivateForm lang={lang as Lang} />
        </div>
      </div>
    </div>
  );
}
