import { defineConfig } from '@playwright/test';

/**
 * Browser acceptance tests for the application shells (PRD EP-12, Exit
 * Criteria #7). Run against a running instance:
 *   API on :8080, previews on :5173 (Staff App) :5174 (Member App) :3000
 *   (website). Staging sets E2E_STAFF, E2E_MEMBER and E2E_WEB.
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
