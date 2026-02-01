import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

test.describe("Modal backend test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('can create branch with Modal backend and see terminal', async ({ page }) => {
    test.setTimeout(600000);
    // Login
    await page.goto("/login");
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL(/\/(dashboard|repos)/, { timeout: 15000 });
    
    // Go directly to test repo owned by the mocked pubkey.
    await page.goto(`/repos/${TEST_PUBKEY}/test`);
    await page.waitForLoadState('networkidle');

    // Create task
    await page.click('button:has-text("New Task")');
    await page.waitForTimeout(500);
    
    const taskTitle = `modal-test-${Date.now()}`;
    await page.fill('input[name="title"]', taskTitle);
    await page.fill('textarea[name="body"]', "Test Modal backend - create hello.txt");
    await page.click('dialog button[type="submit"]');
    await page.waitForLoadState('networkidle');

    // Click Start on the new task
    const startButton = page
      .locator(`tr:has-text("${taskTitle}")`)
      .locator('button:has-text("Start")')
      .first();
    await startButton.click();
    await page.waitForTimeout(500);

    // Select Modal backend
    const startDialog = page.locator('dialog[open]');
    await expect(startDialog).toBeVisible();

    const modalRadio = startDialog.locator('input[name="backend"][value="modal"]').first();
    await modalRadio.check();

    // Start (without agent for faster test)
    const [startResponse] = await Promise.all([
      page.waitForResponse(
        (response) => {
          return response.request().method() === "POST" && response.url().includes("/start");
        },
        { timeout: 420000 }
      ),
      startDialog.locator('button:has-text("Start with Agent")').click(),
    ]);

    if (startResponse.status() >= 400) {
      const body = await startResponse.text();
      throw new Error(`Start failed (${startResponse.status()}): ${body}`);
    }
    
    // Wait for branch page - Modal sandbox takes ~2 minutes
    console.log('Waiting for Modal sandbox to provision (this takes ~2 minutes)...');
    await page.waitForURL("**/branches/**", { timeout: 240000 });

    // Verify we're on a branch page
    await expect(page.locator('h1')).toContainText(taskTitle, { timeout: 5000 });

    // Wait for terminal to connect via WebSocket
    console.log('Waiting for terminal to connect...');
    await page.waitForSelector('.tab-status.connected', { timeout: 120000 });

    // Give terminal time to initialize
    await page.waitForTimeout(3000);

    console.log('SUCCESS: Modal backend connected!');
  });
});
