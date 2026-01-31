import { test, expect } from '@playwright/test';
import { setupNostrMock, TEST_PUBKEY } from './lib/nostr-mock';

test.describe("Modal backend test", () => {
  test.beforeEach(async ({ page }) => {
    await setupNostrMock(page);
  });

  test('can create branch with Modal backend and see terminal', async ({ page }) => {
    // Login
    await page.goto("/login");
    await page.click('button:has-text("Login with Nostr")');
    await page.waitForURL(/\/(dashboard|repos)/, { timeout: 15000 });
    
    await page.screenshot({ path: '/tmp/modal-test-1-loggedin.png' });
    
    // Go to repos page
    await page.goto("/repos");
    await page.waitForLoadState('networkidle');
    
    await page.screenshot({ path: '/tmp/modal-test-2-repos.png' });
    
    // Click on first repo (test repo)
    await page.click('a:has-text("test")');
    await page.waitForLoadState('networkidle');
    
    await page.screenshot({ path: '/tmp/modal-test-3-repo-detail.png' });
    
    // Create task
    await page.click('button:has-text("New Task")');
    await page.waitForTimeout(500);
    
    const taskTitle = `modal-test-${Date.now()}`;
    await page.fill('input[name="title"]', taskTitle);
    await page.fill('textarea[name="body"]', "Test Modal backend - create hello.txt");
    await page.click('dialog button[type="submit"]');
    await page.waitForLoadState('networkidle');
    
    await page.screenshot({ path: '/tmp/modal-test-4-task-created.png' });
    
    // Click Start on the new task
    const startButton = page.locator(`tr:has-text("${taskTitle}") button:has-text("Start")`);
    await startButton.click();
    await page.waitForTimeout(500);
    
    await page.screenshot({ path: '/tmp/modal-test-5-start-dialog.png' });
    
    // Select Modal backend
    const modalRadio = page.locator('input[name="backend"][value="modal"]');
    await modalRadio.click();
    
    await page.screenshot({ path: '/tmp/modal-test-6-modal-selected.png' });
    
    // Start (without agent for faster test)
    await page.click('button:has-text("Start without Agent")');
    
    // Wait for branch page - Modal sandbox takes ~2 minutes
    console.log('Waiting for Modal sandbox to provision (this takes ~2 minutes)...');
    await page.waitForURL("**/branches/**", { timeout: 240000 });
    
    await page.screenshot({ path: '/tmp/modal-test-7-branch-page.png' });
    
    // Verify we're on a branch page
    await expect(page.locator('h1')).toContainText(/Branch/i, { timeout: 5000 });
    
    await page.screenshot({ path: '/tmp/modal-test-8-branch-loaded.png' });
    
    // Wait for terminal to connect via WebSocket
    console.log('Waiting for terminal to connect...');
    await page.waitForSelector('.status.connected', { timeout: 120000 });
    
    await page.screenshot({ path: '/tmp/modal-test-9-terminal-connected.png' });
    
    // Give terminal time to initialize
    await page.waitForTimeout(3000);
    await page.screenshot({ path: '/tmp/modal-test-10-terminal-ready.png' });
    
    console.log('Modal test screenshots saved to /tmp/modal-test-*.png');
    console.log('SUCCESS: Modal backend connected!');
  });
});
