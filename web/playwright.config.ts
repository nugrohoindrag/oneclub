import { defineConfig } from '@playwright/test';

/**
 * Browser acceptance tests for the application shells (PRD EP-12, Exit
 * Criteria #7). Run against a running instance:
 *   API on :8080, previews on :5173 (backoffice) :5174 (member) :5175 (ops)
 *   :5176 (platform-admin) :3000 (website). Staging uses E2E_BASE_URL.
 */
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  globalSetup: './e2e/global-setup.ts',
  use: {
    channel: process.env.PW_CHANNEL ?? 'chrome',
    headless: true,
    viewport: { width: 1360, height: 860 },
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
});
