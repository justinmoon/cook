import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const TEST_SSH_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJqroFRwqhT3rl/7MjKi6PiFOcHZYlLMzUndKwp8Lv7m test@example.com";

test.describe("Preview tab test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('can create preview tab with back/forward navigation and persistence', async ({ page }) => {
    // Login
    await page.goto("/login");
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL("**/dashboard", { timeout: 15000 });
    
    // Add SSH key if needed
    const sshKeyForm = page.locator('textarea[name="key"]');
    if (await sshKeyForm.isVisible()) {
      await page.fill('textarea[name="key"]', TEST_SSH_KEY);
      await page.fill('input[name="name"]', "Test Key");
      await page.click('button:has-text("Add SSH Key")');
      await page.waitForURL("**/dashboard");
    }
    
    // Create repo
    const repoForm = page.locator('#repo-form');
    await repoForm.locator('input[name="name"]').fill("preview-test-repo");
    await repoForm.locator('button:has-text("Create Repository")').click();
    await page.waitForURL("**/repos/**");
    
    // Create a branch via modal
    await page.click('button:has-text("+ New Branch")');
    await page.waitForTimeout(300);
    await page.fill('dialog input[name="name"]', "test-branch");
    await page.click('dialog button:has-text("Create Branch")');
    await page.waitForURL("**/branches/**");
    const branchUrl = page.url();
    
    // Wait for initial terminal tab
    await page.waitForSelector('.tab', { timeout: 5000 });
    
    // Click the globe button to add preview tab
    await page.click('#addPreviewBtn');
    await page.waitForTimeout(300);
    
    // Verify preview tab exists with back/forward buttons
    const previewPanel = page.locator('.preview-panel.active');
    await expect(previewPanel).toBeVisible();
    
    const backBtn = previewPanel.locator('.back-btn');
    const forwardBtn = previewPanel.locator('.forward-btn');
    const urlInput = previewPanel.locator('input');
    
    // Both buttons should be disabled initially (no history)
    await expect(backBtn).toBeDisabled();
    await expect(forwardBtn).toBeDisabled();
    
    // Navigate to first URL
    await urlInput.fill('https://example.com');
    await urlInput.press('Enter');
    await page.waitForTimeout(500);
    
    // Back still disabled (only 1 entry), forward still disabled
    await expect(backBtn).toBeDisabled();
    await expect(forwardBtn).toBeDisabled();
    
    // Navigate to second URL
    await urlInput.fill('https://example.org');
    await urlInput.press('Enter');
    await page.waitForTimeout(500);
    
    // Now back should be enabled (2 entries), forward still disabled
    await expect(backBtn).toBeEnabled();
    await expect(forwardBtn).toBeDisabled();
    
    // Click back
    await backBtn.click();
    await page.waitForTimeout(300);
    
    // URL should be first one, forward should now be enabled
    await expect(urlInput).toHaveValue('https://example.com');
    await expect(forwardBtn).toBeEnabled();
    
    // Click forward
    await forwardBtn.click();
    await page.waitForTimeout(300);
    
    // URL should be second one
    await expect(urlInput).toHaveValue('https://example.org');
    
    // Test persistence - reload the page
    await page.reload();
    await page.waitForSelector('.tab', { timeout: 5000 });
    await page.waitForTimeout(1000); // Wait for tabs to load
    
    // Preview tab should be restored
    const restoredPreviewTab = page.locator('.tab:has-text("Preview")');
    await expect(restoredPreviewTab).toBeVisible();
    
    // Click on it to activate
    await restoredPreviewTab.click();
    await page.waitForTimeout(300);
    
    // Check the URL is restored
    const restoredPanel = page.locator('.preview-panel.active');
    const restoredUrlInput = restoredPanel.locator('input');
    await expect(restoredUrlInput).toHaveValue('https://example.org');
    
    // History should be restored - back button should work
    const restoredBackBtn = restoredPanel.locator('.back-btn');
    await expect(restoredBackBtn).toBeEnabled();
    
    await restoredBackBtn.click();
    await page.waitForTimeout(300);
    await expect(restoredUrlInput).toHaveValue('https://example.com');
    
    console.log('Preview tab persistence test PASSED!');
  });
});
