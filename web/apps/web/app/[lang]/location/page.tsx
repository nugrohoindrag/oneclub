import { copy, getBootstrap, type Lang } from '../../lib';

export default async function Location({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const t = copy[lang as Lang];
  const b = await getBootstrap();
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{t.locationTitle}</h1>
        <p>{t.locationText}</p>
        <p>{b.branding.appName} · {b.timezone}</p>
      </div>
    </div>
  );
}
