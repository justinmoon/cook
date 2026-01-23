import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

const TEST_SSH_KEY =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJqroFRwqhT3rl/7MjKi6PiFOcHZYlLMzUndKwp8Lv7m test@example.com";

test.describe("Agent terminal test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('agent appears in terminal after starting task', async ({ page }) => {
    // Login
    await page.goto("/login");
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL("**/dashboard", { timeout: 15000 });
    
    await page.screenshot({ path: '/tmp/cook-test-1-dashboard.png' });
    
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
    await repoForm.locator('input[name="name"]').fill("agent-test-repo");
    await repoForm.locator('button:has-text("Create Repository")').click();
    await page.waitForURL("**/repos/**");
    
    await page.screenshot({ path: '/tmp/cook-test-2-repo.png' });
    
    // Create task using modal
    await page.click('button:has-text("New Task")');
    await page.waitForTimeout(300);
    await page.fill('input[name="title"]', "test-agent-task");
    await page.fill('textarea[name="body"]', "Create a hello.txt file with content 'agent test passed'");
    await page.click('dialog button[type="submit"]');
    await page.waitForLoadState('networkidle');
    
    await page.screenshot({ path: '/tmp/cook-test-3-with-task.png' });
    
    // Click Start button to open modal
    await page.click('button:has-text("Start")');
    await page.waitForTimeout(300);
    
    await page.screenshot({ path: '/tmp/cook-test-4-start-modal.png' });
    
    // Claude is already selected by default, just click submit
    await page.click('button:has-text("Start with Agent")');
    await page.waitForURL("**/branches/**");
    
    await page.screenshot({ path: '/tmp/cook-test-5-branch-page.png' });
    
    // Terminal is now embedded in branch page - wait for it to connect
    await page.waitForSelector('.status.connected', { timeout: 10000 });
    
    // Wait for terminal to show content
    await page.waitForTimeout(2000);
    await page.screenshot({ path: '/tmp/cook-test-6-terminal-2s.png' });
    
    // Wait for Claude to start producing output
    await page.waitForTimeout(5000);
    await page.screenshot({ path: '/tmp/cook-test-7-terminal-7s.png' });
    
    // Wait longer
    await page.waitForTimeout(10000);
    await page.screenshot({ path: '/tmp/cook-test-8-terminal-17s.png' });
    
    // Final screenshot
    await page.waitForTimeout(5000);
    await page.screenshot({ path: '/tmp/cook-test-9-terminal-final.png' });
    
    console.log('Screenshots saved to /tmp/cook-test-*.png');
  });
});
