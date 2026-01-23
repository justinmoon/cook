import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const TEST_SSH_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJqroFRwqhT3rl/7MjKi6PiFOcHZYlLMzUndKwp8Lv7m test@example.com";

test.describe("Tab persistence test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('tabs persist after navigating away and back', async ({ page }) => {
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
    await repoForm.locator('input[name="name"]').fill("tab-test-repo");
    await repoForm.locator('button:has-text("Create Repository")').click();
    await page.waitForURL("**/repos/**");
    
    // Create a branch via modal
    await page.click('button:has-text("+ New Branch")');
    await page.waitForTimeout(300);
    await page.fill('dialog input[name="name"]', "test-branch");
    await page.click('dialog button:has-text("Create Branch")');
    await page.waitForURL("**/branches/**");
    
    await page.screenshot({ path: '/tmp/tab-test-1-branch.png' });
    
    // Wait for initial tab to load
    await page.waitForSelector('.tab', { timeout: 5000 });
    
    // Count initial tabs (should be 1 - Agent)
    let tabCount = await page.locator('.tab').count();
    console.log(`Initial tab count: ${tabCount}`);
    expect(tabCount).toBe(1);
    
    // Click + button to add shell tabs
    await page.click('.tab-add');
    await page.waitForTimeout(500);
    await page.click('.tab-add');
    await page.waitForTimeout(500);
    await page.click('.tab-add');
    await page.waitForTimeout(500);
    
    await page.screenshot({ path: '/tmp/tab-test-2-tabs-created.png' });
    
    // Verify we have 4 tabs now
    tabCount = await page.locator('.tab').count();
    console.log(`Tab count after creating 3 shells: ${tabCount}`);
    expect(tabCount).toBe(4);
    
    // Get tab names for verification
    const tabNames = await page.locator('.tab .tab-name').allTextContents();
    console.log('Tab names:', tabNames);
    
    // Navigate away (go to repo page)
    await page.click('a:has-text("tab-test-repo")');
    await page.waitForURL("**/repos/**");
    
    await page.screenshot({ path: '/tmp/tab-test-3-navigated-away.png' });
    
    // Navigate back to branch page
    await page.click('text=test-branch');
    await page.waitForURL("**/branches/**");
    
    // Wait for tabs to load
    await page.waitForTimeout(1000);
    
    await page.screenshot({ path: '/tmp/tab-test-4-returned.png' });
    
    // Verify tabs are restored
    tabCount = await page.locator('.tab').count();
    console.log(`Tab count after returning: ${tabCount}`);
    expect(tabCount).toBe(4);
    
    // Verify tab names match
    const restoredTabNames = await page.locator('.tab .tab-name').allTextContents();
    console.log('Restored tab names:', restoredTabNames);
    expect(restoredTabNames).toEqual(tabNames);
    
    console.log('Tab persistence test PASSED!');
  });
});
