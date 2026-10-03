import { redirect } from 'next/navigation';
import { getBootstrap } from './lib';

export default async function Root() {
  const b = await getBootstrap();
  redirect(`/${b.defaultLocale === 'en' ? 'en' : 'id'}`);
}
