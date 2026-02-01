import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const hasFlyToken = Boolean(process.env.FLY_API_TOKEN || process.env.FLY_TOKEN);
const hasFlyApp = Boolean(
  process.env.FLY_MACHINES_APP || process.env.COOK_FLY_MACHINES_APP
);
const hasFlyMachines = hasFlyToken && hasFlyApp;

test.describe('Fly Machines backend test', () => {
  test.skip(!hasFlyMachines, 'FLY_API_TOKEN/FLY_MACHINES_APP not set');

  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('can create branch with Fly Machines backend and see terminal', async ({ page }) => {
    test.setTimeout(420000);
    await page.goto('/login');
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL(/\/(dashboard|repos)/, { timeout: 15000 });

    await page.goto(`/repos/${TEST_PUBKEY}/test`);
    await page.waitForLoadState('networkidle');

    await page.click('button:has-text("New Task")');
    await page.waitForTimeout(500);

    const taskTitle = `fly-test-${Date.now()}`;
    await page.fill('input[name="title"]', taskTitle);
    await page.fill('textarea[name="body"]', 'Test Fly Machines backend - create hello.txt');
    await page.click('dialog button[type="submit"]');
    await page.waitForLoadState('networkidle');

    const startButton = page
      .locator(`tr:has-text("${taskTitle}")`)
      .locator('button:has-text("Start")')
      .first();
    await startButton.click();
    await page.waitForTimeout(500);

    const startDialog = page.locator('dialog[open]');
    await expect(startDialog).toBeVisible();

    const flyRadio = startDialog.locator('input[name="backend"][value="fly-machines"]').first();
    await flyRadio.check();

    const [startResponse] = await Promise.all([
      page.waitForResponse(
        (response) => {
          return response.request().method() === 'POST' && response.url().includes('/start');
        },
        { timeout: 300000 }
      ),
      startDialog.locator('button:has-text("Start with Agent")').click(),
    ]);

    if (startResponse.status() >= 400) {
      const body = await startResponse.text();
      throw new Error(`Start failed (${startResponse.status()}): ${body}`);
    }

    console.log('Waiting for Fly Machines sandbox to provision...');
    await page.waitForURL('**/branches/**', { timeout: 300000 });

    await expect(page.locator('h1')).toContainText(taskTitle, { timeout: 5000 });

    console.log('Waiting for terminal to connect...');
    await page.waitForSelector('.tab-status.connected', { timeout: 120000 });

    await page.waitForTimeout(3000);

    console.log('SUCCESS: Fly Machines backend connected!');
  });
});
