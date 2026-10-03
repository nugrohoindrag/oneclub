import { copy, getBootstrap, type Lang } from '../lib';

const SECTIONS: [string, string, string | null][] = [
  ['Golf', 'golf', 'golf'],
  ['Sport Club', 'sportclub', null],
  ['Membership', 'membership', 'membership'],
  ['Events', 'banquet', null],
];

/** Home placeholder (PRD EP-12): branding-driven hero, ID/EN. */
export default async function Home({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const t = copy[lang as Lang];
  const b = await getBootstrap();
  const photo = b.branding.loginImageUrl;
  return (
    <>
      <section className="w-hero" data-photo={photo ? 'true' : 'false'}>
        {photo && <img src={photo} alt="" />}
        <h1>
          {t.heroTitle} — {b.branding.appName}
        </h1>
        <p style={{ fontSize: 18, margin: '0 0 24px' }}>{t.heroText}</p>
        <div>
          <a className="w-btn" href={`/${lang}/book-golf`}>{t.bookSoon}</a>
          <a className="w-btn w-btn-ghost" href={`/${lang}/golf`}>Golf Course</a>
        </div>
      </section>
      <div className="w-grid">
        {SECTIONS.filter(([, m]) => b.enabledModules.includes(m)).map(([label, , page]) =>
          page ? (
            <a key={label} className="w-card" href={`/${lang}/${page}`} style={{ textDecoration: 'none', color: 'inherit' }}>
              <h2>{label}</h2>
              <p style={{ margin: 0, opacity: 0.7 }}>{label === 'Golf' ? 'Golf Course · Course Guide · Book Golf' : 'Membership types and activation'}</p>
            </a>
          ) : (
            <div key={label} className="w-card">
              <h2>{label}</h2>
              <p style={{ margin: 0, opacity: 0.7 }}>{t.soon}</p>
            </div>
          ),
        )}
      </div>
    </>
  );
}
