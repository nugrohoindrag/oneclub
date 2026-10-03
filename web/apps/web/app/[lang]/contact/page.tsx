import { copy, getBootstrap, type Lang } from '../../lib';

export default async function Contact({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const t = copy[lang as Lang];
  const b = await getBootstrap();
  return (
    <div className="w-grid">
      <div className="w-card">
        <h1 style={{ marginTop: 0 }}>{t.contactTitle}</h1>
        <p>{t.contactText}</p>
        <p><strong>{b.branding.appName}</strong></p>
      </div>
    </div>
  );
}
