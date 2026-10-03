import { copy, getBootstrap, type Lang } from '../lib';

const SECTIONS: [string, string][] = [
  ['Golf', 'golf'],
  ['Sport Club', 'sportclub'],
  ['Membership', 'membership'],
  ['Events', 'banquet'],
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
          <span className="w-btn" aria-disabled="true">{t.bookSoon}</span>
        </div>
      </section>
      <div className="w-grid">
        {SECTIONS.filter(([, m]) => b.enabledModules.includes(m)).map(([label]) => (
          <div key={label} className="w-card">
            <h2>{label}</h2>
            <p style={{ margin: 0, opacity: 0.7 }}>{t.soon}</p>
          </div>
        ))}
      </div>
    </>
  );
}
