import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';

/**
 * Prepares browser-test accounts (e2e/setup.sql) in the target instance.
 * Needs psql with superuser access: PSQL (default `psql` on PATH) and
 * E2E_DATABASE_URL (default: local dev instance mgcc). Staging runs the same
 * SQL on the DB host via deploy/scripts/e2e-setup.sh and sets E2E_SKIP_SETUP.
 */
export default async function globalSetup() {
  if (process.env.E2E_SKIP_SETUP) return;
  const psql = process.env.PSQL ?? 'psql';
  const url = process.env.E2E_DATABASE_URL ?? 'postgresql://postgres:postgres@localhost:5432/oneclub_mgcc';
  const sql = resolve('e2e/setup.sql'); // Playwright runs from web/
  execFileSync(psql, [url, '-v', 'ON_ERROR_STOP=1', '-q', '-f', sql], { stdio: 'inherit' });
}
