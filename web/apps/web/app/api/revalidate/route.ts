import { createHmac, timingSafeEqual } from 'node:crypto';
import { revalidatePath, revalidateTag } from 'next/cache';

/**
 * On-demand revalidation (PRD P4 FR-CMS-10): OneClub's website integration
 * POSTs {revision, paths, tags} after a publication, signed in
 * X-OneClub-Signature "t=<unix>,v1=<hex HMAC-SHA256(secret, t + '.' + body)>"
 * with ONECLUB_REVALIDATE_SECRET; stale (> 5 min) or unsigned requests are
 * refused.
 */
export async function POST(req: Request) {
  const secret = process.env.ONECLUB_REVALIDATE_SECRET ?? '';
  const body = await req.text();
  if (secret.length < 16 || !verify(req.headers.get('x-oneclub-signature') ?? '', secret, body)) {
    return Response.json({ revalidated: false, error: 'invalid signature' }, { status: 401 });
  }
  let payload: { paths?: unknown; tags?: unknown };
  try {
    payload = JSON.parse(body) as { paths?: unknown; tags?: unknown };
  } catch {
    return Response.json({ revalidated: false, error: 'invalid body' }, { status: 400 });
  }
  const paths = Array.isArray(payload.paths)
    ? payload.paths.filter((p): p is string => typeof p === 'string' && /^\/[A-Za-z0-9/_.-]*$/.test(p)).slice(0, 200) : [];
  const tags = Array.isArray(payload.tags) ? payload.tags.filter((t): t is string => typeof t === 'string' && /^[a-z0-9:_-]{1,64}$/.test(t)).slice(0, 20) : [];
  for (const t of tags.length ? tags : ['cms']) revalidateTag(t, 'max');
  for (const p of paths) revalidatePath(p);
  return Response.json({ revalidated: true, paths, tags });
}

/** Verifies the HMAC signature with a 5 minute tolerance (constant-time compare). */
function verify(header: string, secret: string, body: string): boolean {
  const parts: Record<string, string> = {};
  for (const p of header.split(',')) {
    const i = p.indexOf('=');
    if (i > 0) parts[p.slice(0, i).trim()] = p.slice(i + 1).trim();
  }
  const ts = Number(parts.t);
  if (!parts.t || !parts.v1 || !Number.isFinite(ts) || Math.abs(Date.now() / 1000 - ts) > 300) return false;
  const want = createHmac('sha256', secret).update(`${parts.t}.`).update(body).digest();
  const got = Buffer.from(parts.v1, 'hex');
  return got.length === want.length && timingSafeEqual(got, want);
}
