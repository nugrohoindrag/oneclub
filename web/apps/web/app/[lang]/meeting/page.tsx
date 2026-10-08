import { redirect } from 'next/navigation';

/** Earlier path of the meeting room page (links in e-mails): now Book Meeting Room. */
export default async function Meeting({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  redirect(`/${lang}/book/meeting-room`);
}
