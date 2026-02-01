import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const hasSprites = Boolean(
  process.env.SPRITES_TOKEN || process.env.SPRITE_TOKEN
) && Boolean(
  process.env.SPRITES_TARBALL_URL || process.env.COOK_SPRITES_TARBALL_URL
);

test.describe("Sprites backend test", () => {
  test.skip(!hasSprites, "SPRITES_TOKEN/SPRITES_TARBALL_URL not set");

  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('can create branch with Sprites backend and see terminal', async ({ page }) => {
    test.setTimeout(420000);
    await page.goto("/login");
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL(/\/(dashboard|repos)/, { timeout: 15000 });

    await page.goto(`/repos/${TEST_PUBKEY}/test`);
    await page.waitForLoadState('networkidle');

    await page.click('button:has-text("New Task")');
    await page.waitForTimeout(500);

    const taskTitle = `sprites-test-${Date.now()}`;
    await page.fill('input[name="title"]', taskTitle);
    await page.fill('textarea[name="body"]', "Test Sprites backend - create hello.txt");
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

    const spritesRadio = startDialog.locator('input[name="backend"][value="sprites"]').first();
    await spritesRadio.check();

    const [startResponse] = await Promise.all([
      page.waitForResponse(
        (response) => {
          return response.request().method() === "POST" && response.url().includes("/start");
        },
        { timeout: 120000 }
      ),
      startDialog.locator('button:has-text("Start with Agent")').click(),
    ]);

    if (startResponse.status() >= 400) {
      const body = await startResponse.text();
      throw new Error(`Start failed (${startResponse.status()}): ${body}`);
    }

    console.log('Waiting for Sprites sandbox to provision...');
    await page.waitForURL("**/branches/**", { timeout: 300000 });

    await expect(page.locator('h1')).toContainText(taskTitle, { timeout: 5000 });

    console.log('Waiting for terminal to connect...');
    await page.waitForSelector('.tab-status.connected', { timeout: 120000 });

    await page.waitForTimeout(3000);

    console.log('SUCCESS: Sprites backend connected!');
  });
});
