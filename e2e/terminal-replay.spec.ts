import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const TEST_SSH_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJqroFRwqhT3rl/7MjKi6PiFOcHZYlLMzUndKwp8Lv7m test@example.com";

test.describe("Terminal replay test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('terminal content replays correctly after navigation', async ({ page }) => {
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
    await repoForm.locator('input[name="name"]').fill("replay-test-repo");
    await repoForm.locator('button:has-text("Create Repository")').click();
    await page.waitForURL("**/repos/**");
    
    // Create a branch via modal
    await page.click('button:has-text("+ New Branch")');
    await page.waitForTimeout(300);
    await page.fill('dialog input[name="name"]', "test-branch");
    await page.click('dialog button:has-text("Create Branch")');
    await page.waitForURL("**/branches/**");
    
    // Wait for terminal to connect
    await page.waitForSelector('.tab-status.connected', { timeout: 10000 });
    await page.waitForTimeout(1000);
    
    // Create a second shell tab
    await page.click('.tab-add');
    await page.waitForTimeout(500);
    
    // Click on Shell 2 tab to activate it
    await page.click('.tab:has-text("Shell 2")');
    await page.waitForTimeout(500);
    
    // Wait for Shell 2 to connect
    await page.waitForSelector('.tab.active .tab-status.connected', { timeout: 5000 });
    await page.waitForTimeout(1000);
    
    // Type a command with distinctive output in Shell 2
    // Using echo to create predictable output
    await page.keyboard.type('echo "LINE1-BEFORE"; echo "LINE2-BEFORE"; echo "LINE3-BEFORE"');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(1000);
    
    // Take screenshot before navigation
    await page.screenshot({ path: '/tmp/replay-test-1-before.png' });
    
    // Get terminal content before (look for our marker text)
    const terminalBefore = await page.locator('.terminal-panel.active').textContent();
    console.log('Terminal content before:', terminalBefore?.substring(0, 500));
    
    // Navigate away
    await page.click('a:has-text("replay-test-repo")');
    await page.waitForURL("**/repos/**");
    await page.waitForTimeout(500);
    
    await page.screenshot({ path: '/tmp/replay-test-2-away.png' });
    
    // Navigate back
    await page.click('text=test-branch');
    await page.waitForURL("**/branches/**");
    await page.waitForTimeout(1000);
    
    // Click on Shell 2 tab
    await page.click('.tab:has-text("Shell 2")');
    await page.waitForTimeout(1000);
    
    // Take screenshot after navigation
    await page.screenshot({ path: '/tmp/replay-test-3-after.png' });
    
    // Get terminal content after
    const terminalAfter = await page.locator('.terminal-panel.active').textContent();
    console.log('Terminal content after:', terminalAfter?.substring(0, 500));
    
    // Shell tabs should now replay history correctly (PTY created at correct size)
    // Check that our marker text is present
    expect(terminalAfter).toContain('LINE1-BEFORE');
    expect(terminalAfter).toContain('LINE2-BEFORE');
    expect(terminalAfter).toContain('LINE3-BEFORE');
    
    // The terminal should NOT contain garbled "%" characters
    expect(terminalAfter).not.toContain('%\njustin');
    
    console.log('Terminal replay test PASSED - history replayed correctly!');
  });
});
